-- 0027_ticket_credentials.sql — ticket credentials table (feature #140).
--
-- A credential is a bearer artifact that proves ticket ownership:
--
--   static_qr — opaque cryptographically-random 64-char hex token bound to a
--                ticket UUIDv7. The scanner resolves the token server-side:
--                GET /v1/scan/{token} → ticket status. The token is NOT the
--                ticket UUID itself — this separation prevents enumeration.
--
--   pdf       — server-rendered single-page PDF containing:
--                  • visible ticket ID
--                  • embedded QR token (first 32 chars for display)
--                  • issue timestamp and status
--                Payload is the PDF bytes encoded as standard base64.
--
-- Wallet/NFC/rotating-QR credentials are deferred (out of scope for this milestone).
--
-- Revocation: revoked_at is set when the parent ticket is refunded, cancelled,
-- or transferred. Revoked credentials remain in the table for audit purposes.
--
-- Uniqueness: one credential per (ticket_id, type) pair; ON CONFLICT DO UPDATE
-- allows regeneration without orphaning old rows.

-- +goose Up

CREATE TABLE ticket_credentials (
    id         uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    -- The ticket this credential grants access to.
    ticket_id  uuid        NOT NULL REFERENCES tickets(id) ON DELETE CASCADE,
    -- Credential type: static_qr or pdf.
    type       text        NOT NULL,
    CONSTRAINT ticket_credentials_type_check CHECK (type IN ('static_qr', 'pdf')),
    -- Opaque payload:
    --   static_qr → 64-char lowercase hex string (32 random bytes)
    --   pdf       → standard base64-encoded PDF bytes
    payload    text        NOT NULL,
    -- When the credential was issued.
    issued_at  timestamptz NOT NULL DEFAULT now(),
    -- Set on refund/cancellation. NULL = credential is active.
    revoked_at timestamptz,
    -- One credential per (ticket, type) pair. ON CONFLICT used for idempotent upsert.
    CONSTRAINT ticket_credentials_ticket_type_unique UNIQUE (ticket_id, type)
);

-- Fast lookup of credentials for a ticket (primary read path).
CREATE INDEX ticket_credentials_ticket_id ON ticket_credentials (ticket_id);

COMMENT ON TABLE ticket_credentials IS
    'Bearer credentials issued per ticket. One row per (ticket_id, type) pair. '
    'static_qr holds an opaque hex token; pdf holds base64-encoded rendered PDF bytes. '
    'revoked_at is set on refund/cancellation to invalidate the credential at scan time.';

COMMENT ON COLUMN ticket_credentials.payload IS
    'static_qr: 64-char lowercase hex (32 random bytes, crypto/rand). '
    'pdf: standard base64-encoded PDF document bytes rendered server-side.';

COMMENT ON COLUMN ticket_credentials.revoked_at IS
    'NULL means credential is active. Set to now() on ticket refund, cancellation, or transfer.';

-- ── RBAC permission seeds ────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('credential.read',   'Read credentials for an owned ticket (feature #140)'),
    ('credential.revoke', 'Revoke a ticket credential on refund (feature #140)')
ON CONFLICT (name) DO NOTHING;

-- Grant all credential permissions to admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('credential.read', 'credential.revoke')
ON CONFLICT DO NOTHING;

-- Grant all credential permissions to org_admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('credential.read', 'credential.revoke')
ON CONFLICT DO NOTHING;

-- Grant read to member (buyers can view their own ticket credentials).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'member'
  AND  p.name IN ('credential.read')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('credential.read', 'credential.revoke')
);

DELETE FROM permissions
WHERE name IN ('credential.read', 'credential.revoke');

DROP TABLE IF EXISTS ticket_credentials;
