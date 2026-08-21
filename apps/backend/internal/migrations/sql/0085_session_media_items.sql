-- +goose Up
-- AB-47b: per-session media gallery (up to 5 posters + video links).
-- The single cover (sessions.poster_media_id / events.poster_media_id) from
-- 0082_session_poster stays untouched; this table is additive.
-- Poster cap (5) lives in the handler, NOT in a DB CHECK, so raising it later
-- does not require a migration. Gallery poster rows reuse the existing
-- media_objects.owner_type = 'session_poster' allowlist entry (no widening).

CREATE TABLE session_media_items (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    kind        text NOT NULL CHECK (kind IN ('poster','video')),
    media_id    uuid NULL REFERENCES media_objects(id),
    video_url   text NULL,
    position    smallint NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT session_media_items_position_unique
        UNIQUE (session_id, position),
    CONSTRAINT session_media_items_kind_payload_check
        CHECK (
            (kind = 'poster' AND media_id IS NOT NULL AND video_url IS NULL)
            OR
            (kind = 'video'  AND video_url IS NOT NULL AND media_id IS NULL)
        )
);

CREATE INDEX session_media_items_by_session_position
    ON session_media_items (session_id, position);

COMMENT ON TABLE session_media_items IS
    'AB-47b: ordered per-session gallery (posters + video links). '
    'Additive to sessions.poster_media_id / events.poster_media_id (cover). '
    'Poster cap (5) enforced in the handler, not here.';

-- +goose Down
DROP TABLE IF EXISTS session_media_items;
