-- 0125_onboarding_applications.sql — the organizer's application form (spec
-- 08_architecture/34_onboarding_applications_ru.md).
--
-- A newcomer fills in a long form on the website (or in the Telegram bot), may
-- leave and come back, and the platform operator approves or rejects the
-- application. NOTHING is created in organizations / users / sales_channels until
-- the operator approves, so a half-filled or abusive form leaves no trace outside
-- these tables.
--
--   onboarding_applications       the application and its draft answers
--   onboarding_application_events the timeline (and a snapshot of every submission)
--   onboarding_application_notes  the operator's private notes
--   onboarding_application_checks automatic checks, recomputed on demand
--   onboarding_documents          KYB documents (phase 2: storage is added later)
--   onboarding_settings           the single settings row (approval mode, retention)
--
-- Tokens are never stored: only their SHA-256 hex (users.TokenHash). The long tail
-- of answers lives in answers jsonb so a new form field needs no migration; the
-- fields the queue filters on are real columns.

-- +goose Up

CREATE TABLE onboarding_applications (
    id                      uuid        PRIMARY KEY DEFAULT uuidv7(),
    status                  text        NOT NULL DEFAULT 'draft'
                            CHECK (status IN ('draft', 'pending_approval', 'info_requested',
                                              'approved', 'rejected', 'expired')),
    source                  text        NOT NULL DEFAULT 'site'
                            CHECK (source IN ('site', 'telegram', 'operator')),
    applicant_email         text        NOT NULL
                            CHECK (applicant_email = lower(applicant_email) AND position('@' IN applicant_email) > 1),
    email_confirmed_at      timestamptz,
    applicant_name          text,
    applicant_phone         text,
    locale                  text        NOT NULL DEFAULT 'en',
    country                 text        CHECK (country IS NULL OR country ~ '^[A-Z]{2}$'),
    org_name                text,
    legal_name              text,
    current_step            text        NOT NULL DEFAULT 'contact',
    progress_pct            smallint    NOT NULL DEFAULT 0 CHECK (progress_pct BETWEEN 0 AND 100),
    answers                 jsonb       NOT NULL DEFAULT '{}'::jsonb,
    requested_fields        text[]      NOT NULL DEFAULT '{}',
    info_request_message    text,
    access_token_hash       text,
    resume_token_hash       text,
    resume_token_expires_at timestamptz,
    telegram_user_id        bigint,
    terms_version           text,
    privacy_version         text,
    consented_at            timestamptz,
    utm                     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    ip_hash                 text,
    reminders_sent          smallint    NOT NULL DEFAULT 0,
    last_reminder_at        timestamptz,
    last_activity_at        timestamptz NOT NULL DEFAULT now(),
    expires_at              timestamptz NOT NULL,
    submitted_at            timestamptz,
    reviewed_by             uuid        REFERENCES users (id) ON DELETE SET NULL,
    reviewed_at             timestamptz,
    decision_reason         text,
    org_id                  uuid        REFERENCES organizations (id) ON DELETE SET NULL,
    purged_at               timestamptz,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE onboarding_applications IS
    'An organizer''s application: draft answers, status and the operator''s decision. No organization exists until it is approved.';
COMMENT ON COLUMN onboarding_applications.answers IS
    'All form answers keyed by the form-schema field key; the columns above only mirror what the queue filters on.';
COMMENT ON COLUMN onboarding_applications.access_token_hash IS
    'SHA-256 hex of the token the browser or the bot keeps to edit this application. NULL once revoked.';
COMMENT ON COLUMN onboarding_applications.resume_token_hash IS
    'SHA-256 hex of the token in the confirm/resume link sent by e-mail.';
COMMENT ON COLUMN onboarding_applications.purged_at IS
    'Set when the personal fields were erased (retention or a deletion request); the row stays for statistics.';

CREATE INDEX onboarding_applications_status_idx
    ON onboarding_applications (status, last_activity_at DESC);
CREATE INDEX onboarding_applications_email_idx
    ON onboarding_applications (applicant_email);
CREATE UNIQUE INDEX onboarding_applications_access_token_uq
    ON onboarding_applications (access_token_hash) WHERE access_token_hash IS NOT NULL;
CREATE UNIQUE INDEX onboarding_applications_resume_token_uq
    ON onboarding_applications (resume_token_hash) WHERE resume_token_hash IS NOT NULL;
CREATE INDEX onboarding_applications_telegram_idx
    ON onboarding_applications (telegram_user_id) WHERE telegram_user_id IS NOT NULL;
CREATE INDEX onboarding_applications_draft_expiry_idx
    ON onboarding_applications (expires_at) WHERE status = 'draft';

CREATE TABLE onboarding_application_events (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    application_id uuid        NOT NULL REFERENCES onboarding_applications (id) ON DELETE CASCADE,
    kind           text        NOT NULL,
    actor_type     text        NOT NULL DEFAULT 'system'
                   CHECK (actor_type IN ('applicant', 'operator', 'system')),
    actor_id       text        NOT NULL DEFAULT '',
    detail         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    snapshot       jsonb,
    created_at     timestamptz NOT NULL DEFAULT now()
);

COMMENT ON COLUMN onboarding_application_events.snapshot IS
    'The answers as they stood at a submission, so a later edit cannot rewrite what the operator saw.';

CREATE INDEX onboarding_application_events_app_idx
    ON onboarding_application_events (application_id, created_at);

CREATE TABLE onboarding_application_notes (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    application_id uuid        NOT NULL REFERENCES onboarding_applications (id) ON DELETE CASCADE,
    author_id      uuid        REFERENCES users (id) ON DELETE SET NULL,
    body           text        NOT NULL CHECK (length(body) BETWEEN 1 AND 4000),
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX onboarding_application_notes_app_idx
    ON onboarding_application_notes (application_id, created_at);

CREATE TABLE onboarding_application_checks (
    application_id uuid        NOT NULL REFERENCES onboarding_applications (id) ON DELETE CASCADE,
    key            text        NOT NULL,
    result         text        NOT NULL CHECK (result IN ('pass', 'warn', 'fail')),
    detail         text        NOT NULL DEFAULT '',
    checked_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (application_id, key)
);

CREATE TABLE onboarding_documents (
    id             uuid        PRIMARY KEY DEFAULT uuidv7(),
    application_id uuid        NOT NULL REFERENCES onboarding_applications (id) ON DELETE CASCADE,
    kind           text        NOT NULL
                   CHECK (kind IN ('registration_extract', 'representative_id', 'bank_proof', 'other')),
    media_id       uuid        REFERENCES media_objects (id) ON DELETE SET NULL,
    status         text        NOT NULL DEFAULT 'uploaded'
                   CHECK (status IN ('uploaded', 'verified', 'rejected')),
    note           text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    reviewed_at    timestamptz
);

COMMENT ON TABLE onboarding_documents IS
    'KYB documents of an application. Upload and private storage arrive in a later phase; the table is here so the form and the queue already have the slots.';

CREATE INDEX onboarding_documents_app_idx ON onboarding_documents (application_id);

CREATE TABLE onboarding_settings (
    id                boolean     PRIMARY KEY DEFAULT true CHECK (id),
    approval_mode     text        NOT NULL DEFAULT 'manual'
                      CHECK (approval_mode IN ('manual', 'auto_when_complete')),
    draft_ttl_days    integer     NOT NULL DEFAULT 180 CHECK (draft_ttl_days BETWEEN 7 AND 1095),
    purge_after_days  integer     NOT NULL DEFAULT 365 CHECK (purge_after_days BETWEEN 30 AND 3650),
    countries         text[]      NOT NULL DEFAULT '{}',
    max_new_per_day   integer     NOT NULL DEFAULT 300 CHECK (max_new_per_day BETWEEN 1 AND 100000),
    terms_version     text        NOT NULL DEFAULT '1',
    privacy_version   text        NOT NULL DEFAULT '1',
    updated_by        uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

COMMENT ON COLUMN onboarding_settings.countries IS
    'ISO-3166 alpha-2 codes accepted for new applications. Empty = every country (owner decision 2026-10-08).';

INSERT INTO onboarding_settings (id) VALUES (true);

INSERT INTO permissions (name, description) VALUES
  ('onboarding.review', 'See and decide organizer applications (approve, reject, ask for details)'),
  ('onboarding.settings', 'Change the onboarding approval mode, retention and accepted countries');

-- Superadmin parity (TestSuperadminPermissionParity532).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('onboarding.review', 'onboarding.settings')
WHERE  r.name = 'platform_superadmin'
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM permissions WHERE name IN ('onboarding.review', 'onboarding.settings');
DROP TABLE IF EXISTS onboarding_settings;
DROP TABLE IF EXISTS onboarding_documents;
DROP TABLE IF EXISTS onboarding_application_checks;
DROP TABLE IF EXISTS onboarding_application_notes;
DROP TABLE IF EXISTS onboarding_application_events;
DROP TABLE IF EXISTS onboarding_applications;
