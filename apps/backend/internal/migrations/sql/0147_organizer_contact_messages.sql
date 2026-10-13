-- 0147_organizer_contact_messages.sql — the "Write to the organizer" form of
-- the hosted sales page (HP contact form, owner decision 2026-10-13).
--
-- The page no longer publishes the organizer's e-mail: a buyer writes through
-- a form protected by Cloudflare Turnstile. Each message is stored here, then
-- the worker job contact.organizer_message mails it to the channel's public
-- contact e-mail (Reply-To = the buyer) and, when the organization keeps
-- contact_form_to_telegram on, posts it to the organization's own sales
-- Telegram chat. email_sent_at / telegram_sent_at make a retried job send
-- each leg once. Rows older than 90 days are deleted by the same job.
--
-- ip_hash is a salted SHA-256 of the sender's IP, used only for the per-IP
-- rate limit; the IP itself is never stored.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
-- organizations and sales_channels are read on every request: fail fast
-- rather than queue behind a long transaction during a deploy.
SET LOCAL lock_timeout = '5s';

ALTER TABLE organizations
    ADD COLUMN contact_form_to_telegram boolean NOT NULL DEFAULT true;

CREATE TABLE organizer_contact_messages (
    id               uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id           uuid        NOT NULL REFERENCES organizations(id),
    channel_id       uuid        NOT NULL REFERENCES sales_channels(id),
    page_slug        text        NOT NULL CHECK (char_length(page_slug) BETWEEN 1 AND 100),
    sender_name      text        NOT NULL CHECK (char_length(sender_name) BETWEEN 1 AND 100),
    sender_email     text        NOT NULL CHECK (char_length(sender_email) BETWEEN 3 AND 254),
    message          text        NOT NULL CHECK (char_length(message) BETWEEN 10 AND 2000),
    locale           text        NOT NULL DEFAULT 'en' CHECK (char_length(locale) BETWEEN 2 AND 10),
    ip_hash          text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    email_sent_at    timestamptz,
    telegram_sent_at timestamptz
);

CREATE INDEX organizer_contact_messages_ip_idx
    ON organizer_contact_messages (ip_hash, created_at);
CREATE INDEX organizer_contact_messages_channel_idx
    ON organizer_contact_messages (channel_id, created_at);
CREATE INDEX organizer_contact_messages_created_idx
    ON organizer_contact_messages (created_at);

-- +goose Down
DROP TABLE IF EXISTS organizer_contact_messages;
ALTER TABLE organizations DROP COLUMN IF EXISTS contact_form_to_telegram;
