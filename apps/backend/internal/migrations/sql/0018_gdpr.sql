-- +goose Up
-- =====================================================================
-- arena_new — GDPR data workflows (Wave 17, feature #164)
--
-- Step 1: data_subject_requests table
--   Tracks GDPR export and deletion requests from users.
--   The background worker polls pending requests and processes them:
--     - export → generates a JSON dump of all user data
--     - delete → anonymizes PII (email, password_hash) while retaining
--                financial records per accounting law
--
-- Step 4: Consent tracking columns on users
--   consent_given_at     — timestamp of consent acceptance (at registration)
--   marketing_consent    — whether the user opted into marketing emails
--   anonymized_at        — timestamp of PII anonymization (delete workflow)
--
-- Per 10_compliance_security_privacy_ru.md §Privacy and GDPR Article 17.
-- =====================================================================

-- ── Consent tracking additions to users table ─────────────────────────────
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS consent_given_at  timestamptz,
    ADD COLUMN IF NOT EXISTS marketing_consent boolean NOT NULL DEFAULT false,
    ADD COLUMN IF NOT EXISTS anonymized_at     timestamptz;

COMMENT ON COLUMN users.consent_given_at IS
    'Timestamp when the user accepted the terms of service and privacy policy. '
    'NULL for users registered before consent tracking was introduced. '
    'Feature #164 GDPR compliance.';

COMMENT ON COLUMN users.marketing_consent IS
    'Whether the user opted into marketing email communications. '
    'Defaults to false (opt-in model). Feature #164 GDPR compliance.';

COMMENT ON COLUMN users.anonymized_at IS
    'Timestamp when the user''s PII was anonymized per a GDPR deletion request. '
    'When set, email and password_hash are replaced with placeholder values. '
    'Financial records linked to this user_id are retained per accounting law. '
    'Feature #164 GDPR compliance.';

-- ── data_subject_requests ─────────────────────────────────────────────────
-- Tracks GDPR data subject requests (export or deletion).
-- The worker polls rows WHERE status = 'pending' using FOR UPDATE SKIP LOCKED.
CREATE TABLE data_subject_requests (
    id           uuid        PRIMARY KEY DEFAULT uuidv7(),
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    request_type text        NOT NULL CHECK (request_type IN ('export', 'delete')),
    status       text        NOT NULL DEFAULT 'pending'
                             CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    -- payload_url: for export requests, the URL / file path of the generated JSON dump.
    -- NULL until the worker completes the export job.
    payload_url  text,
    -- error_msg: populated when status = 'failed', empty otherwise.
    error_msg    text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz
);

COMMENT ON TABLE data_subject_requests IS
    'GDPR data subject requests (export / delete). Workers poll pending rows '
    'using FOR UPDATE SKIP LOCKED to process them atomically. '
    'Retention policy: export rows are kept indefinitely (audit trail). '
    'Delete rows are kept with anonymized_at on the user row. Feature #164.';

-- Index: worker polling — pending requests ordered by creation time.
-- Partial index on the subset actually polled so the planner skips
-- completed/failed rows without a full table scan.
CREATE INDEX data_subject_requests_pending_idx
    ON data_subject_requests (created_at)
    WHERE status = 'pending';

-- Index: user-facing list endpoint — list a user's own requests by date.
CREATE INDEX data_subject_requests_user_id_idx
    ON data_subject_requests (user_id, created_at DESC);

-- ── RBAC permission seeds ─────────────────────────────────────────────────
-- gdpr.request: allows a user to submit their own export/delete requests.
-- Granted to the built-in 'admin' role by default.
-- In a later milestone, every authenticated user will hold this permission
-- automatically via the 'user' base role.
INSERT INTO permissions (name, description)
VALUES (
    'gdpr.request',
    'Submit GDPR data export or deletion request for the authenticated user''s own account'
)
ON CONFLICT DO NOTHING;

-- Grant gdpr.request to admin (who gets all permissions).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r, permissions p
WHERE  r.name = 'admin'
  AND  p.name = 'gdpr.request'
ON CONFLICT DO NOTHING;

-- +goose Down
ALTER TABLE users
    DROP COLUMN IF EXISTS consent_given_at,
    DROP COLUMN IF EXISTS marketing_consent,
    DROP COLUMN IF EXISTS anonymized_at;
DROP TABLE IF EXISTS data_subject_requests;
DELETE FROM role_permissions
WHERE permission_id IN (SELECT id FROM permissions WHERE name = 'gdpr.request');
DELETE FROM permissions WHERE name = 'gdpr.request';
