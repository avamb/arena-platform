-- +goose Up
-- =====================================================================
-- arena_new — Users & email verification (Wave 1, feature #114)
--
-- Creates the foundation identity tables:
--   * users                      — registered user accounts
--   * email_verification_tokens  — one-time tokens (24h TTL) for verifying
--                                   email addresses after registration
--
-- Business note: this milestone creates the table structure only.
-- Real IdP integration (OAuth, magic link) is deferred to a later wave.
-- The password_hash column stores bcrypt hashes at cost ≥ 12 per
-- 10_compliance_security_privacy_ru.md §Identity.
-- =====================================================================

CREATE TABLE users (
    id                uuid        PRIMARY KEY DEFAULT uuidv7(),
    email             text        NOT NULL UNIQUE,
    password_hash     text        NOT NULL,
    preferred_locale  text        NOT NULL DEFAULT 'en',
    created_at        timestamptz NOT NULL DEFAULT now(),
    email_verified_at timestamptz
);

COMMENT ON TABLE users IS
    'Registered user accounts. Wave 1 — Identity & permissions (feature #114). '
    'Business-domain user attributes (display_name, avatar, org memberships) are '
    'added in subsequent milestones on top of this foundation row.';

COMMENT ON COLUMN users.password_hash IS
    'bcrypt hash at cost ≥ 12 (per compliance spec §Identity). '
    'Stored as Modular Crypt Format string, e.g. "$2a$12$...". '
    'NEVER store or log the plaintext password.';

COMMENT ON COLUMN users.email_verified_at IS
    'NULL until the user clicks the verification link sent after registration. '
    'Set to now() when GET /v1/auth/verify?token=<tok> succeeds.';

CREATE TABLE email_verification_tokens (
    token      text        PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

COMMENT ON TABLE email_verification_tokens IS
    'One-time tokens sent in verification emails after user registration. '
    'TTL = 24 hours from creation. used_at is set when the token is consumed '
    'so subsequent replays return 410 Gone rather than 200 OK.';

CREATE INDEX email_verification_tokens_user_id_idx
    ON email_verification_tokens(user_id);

-- +goose Down
DROP TABLE IF EXISTS email_verification_tokens;
DROP TABLE IF EXISTS users;
