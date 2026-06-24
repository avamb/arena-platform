-- 0029_barcode_authorities.sql — barcode authority federation model (feature #142).
--
-- The barcode authority model supports multiple barcode origin systems operating
-- within the same platform. Each barcode belongs to exactly one authority, and
-- duplicate external references within the same authority are rejected by the
-- UNIQUE (authority_id, external_ref) constraint.
--
-- Authorities:
--   platform          — barcodes issued by Arena Platform (linked to tickets via ticket_id)
--   legacy_bil24      — barcodes from the legacy Bil24 system (external_ref required)
--   external_platform — barcodes from third-party ticketing platforms
--   guest_list        — manually added guest-list entries (ticket_id typically nil)
--
-- Scan validation honors the authority context: the authority type is resolved
-- first; unknown authority types are rejected before any barcode lookup occurs.

-- +goose Up

-- ── barcode_authorities ───────────────────────────────────────────────────────
--
-- One row per originating system. The 'type' column drives authority resolution
-- in the scan flow (see barcodes.go handleScan). 'label' is a human-readable
-- display name for operator UIs.

CREATE TABLE barcode_authorities (
    id         uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    type       text        NOT NULL
                           CONSTRAINT barcode_authorities_type_check
                           CHECK (type IN ('platform', 'legacy_bil24', 'external_platform', 'guest_list')),
    label      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- Seed the default 'platform' authority. This row is required before any
-- platform-issued barcodes (e.g. static_qr credentials) can be registered.
INSERT INTO barcode_authorities (type, label)
VALUES ('platform', 'Arena Platform');

-- ── barcodes ──────────────────────────────────────────────────────────────────
--
-- One row per individual barcode value within an authority.
--
-- external_ref  — the barcode string as emitted by the issuing system. May be a
--                 static_qr token, a Bil24 serial, a guest-list name, etc.
-- ticket_id     — nullable. For 'platform' barcodes this is always populated
--                 (links the token to the issued ticket). For 'external_platform'
--                 and 'guest_list' barcodes it may be nil.
-- status        — active (can be scanned), scanned (already admitted),
--                 revoked (cancelled / refunded).
-- UNIQUE (authority_id, external_ref) — duplicate barcode within an authority
--   is rejected at the DB level, enforcing the federation guarantee.

CREATE TABLE barcodes (
    id           uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    authority_id uuid        NOT NULL REFERENCES barcode_authorities(id),
    external_ref text        NOT NULL,
    ticket_id    uuid        REFERENCES tickets(id),
    status       text        NOT NULL DEFAULT 'active'
                             CONSTRAINT barcodes_status_check
                             CHECK (status IN ('active', 'scanned', 'revoked')),
    scanned_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (authority_id, external_ref)
);

-- Index for listing/resolving barcodes by authority.
CREATE INDEX barcodes_authority_id ON barcodes (authority_id);

-- Partial index for ticket → barcode reverse-lookups (omits external barcodes with no ticket).
CREATE INDEX barcodes_ticket_id ON barcodes (ticket_id) WHERE ticket_id IS NOT NULL;

-- ── RBAC seeds ────────────────────────────────────────────────────────────────
--
-- Permissions:
--   barcode.create — register a new barcode in the federation
--   barcode.read   — read barcode details and authority info
--   barcode.scan   — submit a scan event and validate a barcode
--   barcode.revoke — revoke an active barcode
--
-- Role grants:
--   admin     → all four permissions
--   org_admin → all four permissions
--   member    → barcode.read + barcode.scan (ticket holders and scanner operators)

INSERT INTO permissions (name, description) VALUES
    ('barcode.create', 'Register a new barcode in the federation (feature #142)'),
    ('barcode.read',   'Read barcode details and authority info (feature #142)'),
    ('barcode.scan',   'Submit a scan event and validate a barcode (feature #142)'),
    ('barcode.revoke', 'Revoke an active barcode (feature #142)')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('barcode.create', 'barcode.read', 'barcode.scan', 'barcode.revoke')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('barcode.create', 'barcode.read', 'barcode.scan', 'barcode.revoke')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'member'
  AND  p.name IN ('barcode.read', 'barcode.scan')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('barcode.create', 'barcode.read', 'barcode.scan', 'barcode.revoke')
);

DELETE FROM permissions
WHERE name IN ('barcode.create', 'barcode.read', 'barcode.scan', 'barcode.revoke');

DROP TABLE IF EXISTS barcodes;
DROP TABLE IF EXISTS barcode_authorities;
