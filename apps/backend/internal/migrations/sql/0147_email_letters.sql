-- 0147_email_letters.sql — the delivery status of every buyer e-mail arena
-- sends (ticket letter, invitation, session change or cancellation, refund).
--
-- One email_letters row per message the mail provider ACCEPTED: written by
-- the worker right after the SMTP relay answered 250, with the message id the
-- relay named ("queued as <…>") and a tracking id arena put into the
-- X-Mailin-custom header. Brevo's transactional webhook then reports what
-- happened to the message; each event lands in email_letter_events (one row
-- per letter and event type, first and last time seen) and the letter's
-- status is recomputed from ALL of its event rows, so duplicates and events
-- that arrive out of order give the same answer
-- (internal/platform/emailstatus).
--
-- delivery_jobs keeps one row per ticket; every ticket of one order letter
-- points at the same email_letters row.
--
-- The event rows keep no buyer address: only the event, the provider's time,
-- a scrubbed reason and a few non-personal payload fields.
--
-- Numbered 0147, not 0146: another branch already uses 0146.
-- No permission is seeded, so the superadmin parity guard has nothing to check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE email_letters (
    id                  uuid        PRIMARY KEY,
    org_id              uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    order_id            uuid        REFERENCES orders(id) ON DELETE SET NULL,
    kind                text        NOT NULL,
    recipient           text        NOT NULL,
    provider            text        NOT NULL DEFAULT 'brevo',
    provider_message_id text,
    status              text        NOT NULL DEFAULT 'sending',
    sent_at             timestamptz NOT NULL DEFAULT now(),
    delivered_at        timestamptz,
    proxy_opened_at     timestamptz,
    opened_at           timestamptz,
    failed_at           timestamptz,
    failure_event       text,
    failure_reason      text,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund')),
    CONSTRAINT email_letters_status_check
        CHECK (status IN ('sending', 'sent', 'delivered', 'opened', 'failed'))
);

CREATE UNIQUE INDEX email_letters_provider_message_id_uq
    ON email_letters (provider, provider_message_id)
    WHERE provider_message_id IS NOT NULL;
CREATE INDEX email_letters_order_idx
    ON email_letters (order_id, sent_at DESC)
    WHERE order_id IS NOT NULL;
-- A worker that died while handing a letter to the relay leaves a 'sending'
-- row; emailstatus.Begin deletes such rows after a day.
CREATE INDEX email_letters_sending_idx
    ON email_letters (sent_at)
    WHERE status = 'sending';

COMMENT ON TABLE email_letters IS
    'One row per buyer e-mail the mail provider accepted, with the provider message id and the delivery status derived from email_letter_events.';
COMMENT ON COLUMN email_letters.proxy_opened_at IS
    'A mail proxy (Apple Mail Privacy Protection, Gmail image proxy) fetched the images: proof of delivery, not of reading.';

CREATE TABLE email_letter_events (
    letter_id   uuid        NOT NULL REFERENCES email_letters(id) ON DELETE CASCADE,
    event       text        NOT NULL,
    first_at    timestamptz NOT NULL,
    last_at     timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    seen_count  integer     NOT NULL DEFAULT 1,
    reason      text,
    payload     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (letter_id, event)
);

COMMENT ON TABLE email_letter_events IS
    'Provider webhook events of a letter, one row per event type: first and last time the provider reported it. No recipient address is stored here.';

ALTER TABLE delivery_jobs
    ADD COLUMN email_letter_id     uuid REFERENCES email_letters(id) ON DELETE SET NULL,
    ADD COLUMN provider_message_id text;

CREATE INDEX delivery_jobs_email_letter_idx
    ON delivery_jobs (email_letter_id)
    WHERE email_letter_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS delivery_jobs_email_letter_idx;
ALTER TABLE delivery_jobs
    DROP COLUMN IF EXISTS provider_message_id,
    DROP COLUMN IF EXISTS email_letter_id;
DROP TABLE IF EXISTS email_letter_events;
DROP TABLE IF EXISTS email_letters;
