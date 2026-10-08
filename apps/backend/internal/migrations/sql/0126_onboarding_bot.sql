-- 0126_onboarding_bot.sql — the Telegram channel of the organizer application
-- (08_architecture/34_onboarding_applications_ru.md §10).
--
--   email_code_*          a six-digit code the bot asks for instead of a link:
--                         only its SHA-256 is stored, with an expiry and a
--                         counter of wrong guesses.
--   telegram_notified_status
--                         the status the applicant has already been told about
--                         in Telegram, so the bot's poll announces each
--                         decision (approved, rejected, details requested) once.

-- +goose Up

ALTER TABLE onboarding_applications
    ADD COLUMN email_code_hash          text,
    ADD COLUMN email_code_expires_at    timestamptz,
    ADD COLUMN email_code_attempts      smallint NOT NULL DEFAULT 0,
    ADD COLUMN telegram_notified_status text;

COMMENT ON COLUMN onboarding_applications.email_code_hash IS
    'SHA-256 hex of "<application id>:<code>"; NULL when no code is outstanding.';
COMMENT ON COLUMN onboarding_applications.telegram_notified_status IS
    'The last status the bot announced to the applicant; NULL until the first announcement.';

CREATE INDEX onboarding_applications_tg_notify_idx
    ON onboarding_applications (status)
    WHERE telegram_user_id IS NOT NULL AND purged_at IS NULL;

-- +goose Down

DROP INDEX IF EXISTS onboarding_applications_tg_notify_idx;
ALTER TABLE onboarding_applications
    DROP COLUMN IF EXISTS telegram_notified_status,
    DROP COLUMN IF EXISTS email_code_attempts,
    DROP COLUMN IF EXISTS email_code_expires_at,
    DROP COLUMN IF EXISTS email_code_hash;
