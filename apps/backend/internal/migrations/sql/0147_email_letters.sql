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
-- These rows are EVIDENCE for card disputes (a buyer who says "I never got
-- the tickets"): each letter keeps the exact recipient, subject, ticket
-- numbers, a SHA-256 of the rendered body, the hashes of the attached PDFs
-- and the template version, so it can be proven later even after the
-- templates change. email_letter_interactions logs every open, proxy open
-- and click with its time (and, for a click, the link host and path).
-- Retention: at least 24 months
-- (emailstatus.MinRetention); no job deletes them earlier
-- (tests/staticanalysis/email_letters_retention_test.go).
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
    clicked_at          timestamptz,
    deferred_at         timestamptz,
    receipt_missing     boolean     NOT NULL DEFAULT false,
    subject             text,
    ticket_numbers      text[]      NOT NULL DEFAULT '{}',
    body_sha256         text,
    attachments         jsonb       NOT NULL DEFAULT '[]'::jsonb,
    template_version    text,
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT email_letters_kind_check
        CHECK (kind IN ('ticket', 'invitation', 'change', 'cancel', 'refund')),
    CONSTRAINT email_letters_status_check
        CHECK (status IN ('sending', 'sent', 'delayed', 'delivered', 'opened', 'failed'))
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
COMMENT ON COLUMN email_letters.receipt_missing IS
    'The letter went out (its delivery job or notice says so) but the worker could not record the relay receipt: the stale-letter sweep marked it sent without a message id instead of deleting it.';
COMMENT ON COLUMN email_letters.body_sha256 IS
    'SHA-256 of the rendered HTML body, a NUL byte and the text body: proves which text was sent without storing it.';
COMMENT ON COLUMN email_letters.attachments IS
    'One object per attachment: filename, bytes, sha256 (the ticket PDFs as sent).';
COMMENT ON COLUMN email_letters.template_version IS
    '<template file>@<first 12 hex of its SHA-256>, e.g. ticket.en@1a2b3c4d5e6f.';
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

-- The order read asks whether the webhook delivered anything after a letter
-- was sent (a letter stuck in 'sent' only counts when the webhook works).
CREATE INDEX email_letter_events_received_idx ON email_letter_events (received_at);

COMMENT ON TABLE email_letter_events IS
    'Provider webhook events of a letter, one row per event type: first and last time the provider reported it. No recipient address is stored here.';

CREATE TABLE email_letter_interactions (
    id          uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    letter_id   uuid        NOT NULL REFERENCES email_letters(id) ON DELETE CASCADE,
    event       text        NOT NULL,
    occurred_at timestamptz NOT NULL,
    link        text,
    received_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT email_letter_interactions_event_check
        CHECK (event IN ('opened', 'proxy_open', 'click'))
);

CREATE UNIQUE INDEX email_letter_interactions_uq
    ON email_letter_interactions (letter_id, event, occurred_at, COALESCE(link, ''));

COMMENT ON TABLE email_letter_interactions IS
    'Every open, proxy open and click of a letter Brevo reported, for dispute evidence: time, link host and path (long tokens removed). Kept at least 24 months.';

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
DROP TABLE IF EXISTS email_letter_interactions;
DROP TABLE IF EXISTS email_letter_events;
DROP TABLE IF EXISTS email_letters;
