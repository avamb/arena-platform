-- 0038_complimentary_revocation.sql — Complimentary revocation flow (feature #150).
--
-- Extends the status domain of complimentary_issuances and tickets to support the
-- revocation lifecycle:
--
--   complimentary_issuances.status: adds 'revoked' and 'manual_review'
--     pending → issued → revoked         (clean revocation; inventory restored)
--     issued  → manual_review            (revocation blocked by scanned ticket)
--
--   tickets.status: adds 'revoked'
--     active → revoked                   (set during complimentary revocation)
--
-- Also seeds the 'complimentary.revoke' RBAC permission and grants it to
-- admin and org_admin roles.

-- +goose Up

-- ── Extend complimentary_issuances status domain ─────────────────────────────

ALTER TABLE complimentary_issuances DROP CONSTRAINT complimentary_status_check;

ALTER TABLE complimentary_issuances
    ADD CONSTRAINT complimentary_status_check
        CHECK (status IN ('pending', 'issued', 'failed', 'revoked', 'manual_review'));

COMMENT ON COLUMN complimentary_issuances.status IS
    'Lifecycle: pending → issued → revoked (clean) | manual_review (blocked by scan). '
    'failed: issuance attempted but failed. '
    'Feature #150 extends the domain with revoked and manual_review.';

-- ── Extend tickets status domain ─────────────────────────────────────────────

ALTER TABLE tickets DROP CONSTRAINT tickets_status_check;

ALTER TABLE tickets
    ADD CONSTRAINT tickets_status_check
        CHECK (status IN ('active', 'cancelled', 'transferred', 'revoked'));

COMMENT ON COLUMN tickets.status IS
    'State machine: active → cancelled | transferred | revoked. '
    'revoked is used when the complimentary issuance that sourced the ticket is revoked. '
    'Feature #150 extends the domain with revoked.';

-- ── RBAC permission seeds ────────────────────────────────────────────────────

INSERT INTO permissions (name, description) VALUES
    ('complimentary.revoke', 'Revoke a complimentary issuance and restore inventory (feature #150)')
ON CONFLICT (name) DO NOTHING;

-- Grant revoke permission to admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name = 'complimentary.revoke'
ON CONFLICT DO NOTHING;

-- Grant revoke permission to org_admin.
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name = 'complimentary.revoke'
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions WHERE name = 'complimentary.revoke'
);
DELETE FROM permissions WHERE name = 'complimentary.revoke';

-- Restore tickets status domain (drop revoked).
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_status_check;
ALTER TABLE tickets
    ADD CONSTRAINT tickets_status_check
        CHECK (status IN ('active', 'cancelled', 'transferred'));

-- Restore complimentary_issuances status domain (drop revoked + manual_review).
ALTER TABLE complimentary_issuances DROP CONSTRAINT IF EXISTS complimentary_status_check;
ALTER TABLE complimentary_issuances
    ADD CONSTRAINT complimentary_status_check
        CHECK (status IN ('pending', 'issued', 'failed'));
