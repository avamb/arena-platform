-- +goose Up
-- =====================================================================
-- arena_new — Media objects table (Wave G — Media Storage, feature #285 / G-1)
--
-- Centralised registry of binary media (logos, posters, photos) tracked by
-- the platform. The actual bytes live in one of two pluggable backends:
--
--   * `s3` — any S3-compatible object store (AWS S3, Cloudflare R2, MinIO).
--   * `local` — filesystem under $MEDIA_LOCAL_ROOT (development & tests).
--
-- The selected backend is chosen at process start via the MEDIA_BACKEND env
-- var and resolved by apps/backend/internal/adapters/storage. Each row
-- records which backend was used at upload time, so a future migration that
-- mixes backends (e.g. legacy local files + new R2 uploads) can still serve
-- every object by routing on `storage_backend`.
--
-- Design decisions:
--   * uuidv7 primary key — matches the platform-wide ID strategy and gives
--     time-ordered keys for cheap pagination.
--   * `org_id` is NULL for platform-owned assets (default branding, system
--     icons). When non-NULL it is a soft FK to the organizations table; the
--     constraint is deferred so this migration can be applied in isolation
--     without requiring a forward dependency on the organizations table at
--     this exact ordinal. Application code enforces tenant isolation.
--   * `owner_type` is a small enum-like text column constrained via CHECK.
--     Initial set: org_logo, event_poster, artist_photo. New values are
--     added by future migrations as new owner kinds appear.
--   * `owner_id` is a free UUID (not FK-constrained) because the referenced
--     row lives in a different table per owner_type — a polymorphic
--     association. The owner table's delete/soft-delete flow is responsible
--     for marking the media row deleted (sets deleted_at).
--   * `storage_backend` is constrained to ('s3', 'local'); extending it
--     (e.g. 'gcs') requires a follow-up migration to relax the CHECK.
--   * `storage_key` is the backend-specific locator. For s3 it's the object
--     key; for local it's the relative path under MEDIA_LOCAL_ROOT.
--   * `checksum_sha256` is stored as the hex-encoded SHA-256 (64 chars) —
--     deduplication and end-to-end integrity verification both rely on it.
--   * `byte_size` is BIGINT — even though current limits are tens of MB,
--     using INT would prevent future use for larger artefacts (PDF tickets,
--     scanner manifest archives).
--   * `width` / `height` are NULL for non-image media (e.g. a future PDF
--     attachment kind). They are populated by the upload pipeline when the
--     content_type indicates a raster image.
--   * `deleted_at` enables soft-delete; the storage adapter does NOT
--     immediately remove bytes — a background sweeper handles physical
--     deletion after a retention window.
-- =====================================================================

CREATE TABLE media_objects (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id           uuid        NULL,
    owner_type       text        NOT NULL,
    owner_id         uuid        NULL,
    storage_backend  text        NOT NULL,
    storage_key      text        NOT NULL,
    content_type     text        NOT NULL,
    byte_size        bigint      NOT NULL,
    checksum_sha256  text        NOT NULL,
    width            integer     NULL,
    height           integer     NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz                              -- NULL = active
);

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN ('org_logo', 'event_poster', 'artist_photo'));

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_storage_backend_check
        CHECK (storage_backend IN ('s3', 'local'));

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_byte_size_positive_check
        CHECK (byte_size > 0);

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_checksum_sha256_format_check
        CHECK (checksum_sha256 ~ '^[0-9a-f]{64}$');

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_dimensions_check
        CHECK (
            (width IS NULL AND height IS NULL)
            OR (width > 0 AND height > 0)
        );

-- Partial unique index: prevent duplicate keys within a backend for active rows.
-- (Soft-deleted rows can coexist with a fresh upload reusing the same key.)
CREATE UNIQUE INDEX media_objects_backend_key_active
    ON media_objects (storage_backend, storage_key)
    WHERE deleted_at IS NULL;

-- Index: list a single owner's media (e.g. all posters for an event).
CREATE INDEX media_objects_owner_active
    ON media_objects (owner_type, owner_id)
    WHERE deleted_at IS NULL;

-- Index: list media for an org (admin UI media library view).
CREATE INDEX media_objects_org_active
    ON media_objects (org_id, created_at DESC)
    WHERE deleted_at IS NULL;

-- Index: dedup lookup by content hash (future "is this already uploaded?" path).
CREATE INDEX media_objects_checksum_active
    ON media_objects (checksum_sha256)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE media_objects IS
    'Registry of binary media (org logos, event posters, artist photos). '
    'The actual bytes live in the backend named by storage_backend; this '
    'table holds metadata, integrity checksums, and ownership. '
    'Feature #285 / G-1.';
COMMENT ON COLUMN media_objects.org_id IS
    'Owning organization. NULL means a platform-owned asset (e.g. default '
    'branding). Tenant isolation is enforced application-side; the FK to '
    'organizations is deferred to a follow-up migration.';
COMMENT ON COLUMN media_objects.owner_type IS
    'Polymorphic owner kind: org_logo | event_poster | artist_photo. '
    'New kinds are added by future migrations relaxing the CHECK.';
COMMENT ON COLUMN media_objects.owner_id IS
    'UUID of the owning row in the table implied by owner_type. Not '
    'FK-constrained because the target table varies per owner_type.';
COMMENT ON COLUMN media_objects.storage_backend IS
    'Which storage adapter holds the bytes: s3 (any S3-compatible store: '
    'AWS S3, Cloudflare R2, MinIO) or local (filesystem under '
    'MEDIA_LOCAL_ROOT, dev & test only).';
COMMENT ON COLUMN media_objects.storage_key IS
    'Backend-specific locator. For s3 it is the object key within the '
    'configured bucket; for local it is the path relative to '
    'MEDIA_LOCAL_ROOT.';
COMMENT ON COLUMN media_objects.content_type IS
    'MIME type captured at upload time (e.g. image/png, image/jpeg).';
COMMENT ON COLUMN media_objects.byte_size IS
    'Size of the stored object in bytes. BIGINT to leave room for future '
    'larger artefacts (PDF tickets, scanner manifests).';
COMMENT ON COLUMN media_objects.checksum_sha256 IS
    'Hex-encoded SHA-256 of the stored bytes (64 hex chars). Used for '
    'end-to-end integrity verification and dedup lookups.';
COMMENT ON COLUMN media_objects.width IS
    'Image width in pixels (NULL for non-image media). Populated by the '
    'upload pipeline when content_type is a raster image MIME type.';
COMMENT ON COLUMN media_objects.height IS
    'Image height in pixels (NULL for non-image media).';
COMMENT ON COLUMN media_objects.deleted_at IS
    'Soft-delete marker. The storage adapter retains the physical bytes '
    'until a background sweeper removes them after a retention window.';

-- +goose Down
DROP INDEX IF EXISTS media_objects_checksum_active;
DROP INDEX IF EXISTS media_objects_org_active;
DROP INDEX IF EXISTS media_objects_owner_active;
DROP INDEX IF EXISTS media_objects_backend_key_active;

DROP TABLE IF EXISTS media_objects;
