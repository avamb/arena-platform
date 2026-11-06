-- 0207_site_forms.sql — the forms of a static site (Vino&Co spec 62 §10.2,
-- П-17 and П-10).
--
-- organizer_contact_messages: the "organizer-contact" form is also reached by
-- feed token now (POST /v1/public/feeds/{token}/organizer-contact). A message
-- carries a topic (contact / private_event), a last name and a phone. The
-- topic picks the recipients (sales_channels.settings.contact_form.recipients),
-- so the letter may go to several addresses: email_done_to lists the ones
-- already mailed, so a retried job never mails the same address twice.
-- email_last_error keeps the last failure (addresses scrubbed, 200 chars) for
-- the operator. The message is optional on the "contact" topic, so the length
-- CHECK now starts at 0 (the old route still demands 10 in its handler).
--
-- marketing_subscriptions: phone of the club form. marketing_consents.source
-- gains 'site' (a consent given on a static site's form).
--
-- No permission is seeded, so the superadmin parity guard has nothing to check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE organizer_contact_messages
    ADD COLUMN topic            text   NOT NULL DEFAULT 'contact'
                                CHECK (topic IN ('contact', 'private_event')),
    ADD COLUMN last_name        text   NOT NULL DEFAULT '' CHECK (char_length(last_name) <= 100),
    ADD COLUMN phone            text   NOT NULL DEFAULT '' CHECK (char_length(phone) <= 30),
    ADD COLUMN email_done_to    text[] NOT NULL DEFAULT '{}',
    ADD COLUMN email_last_error text   NOT NULL DEFAULT '' CHECK (char_length(email_last_error) <= 300);

ALTER TABLE organizer_contact_messages
    DROP CONSTRAINT organizer_contact_messages_message_check,
    ADD CONSTRAINT organizer_contact_messages_message_check CHECK (char_length(message) BETWEEN 0 AND 2000);

ALTER TABLE marketing_subscriptions
    ADD COLUMN phone text NOT NULL DEFAULT '' CHECK (char_length(phone) <= 30);

ALTER TABLE marketing_consents
    DROP CONSTRAINT marketing_consents_source_check,
    ADD CONSTRAINT marketing_consents_source_check CHECK (source IN
        ('widget', 'site', 'confirm_link', 'withdraw_link', 'organizer', 'erasure'));

-- +goose Down
UPDATE marketing_consents SET source = 'widget' WHERE source = 'site';
ALTER TABLE marketing_consents
    DROP CONSTRAINT marketing_consents_source_check,
    ADD CONSTRAINT marketing_consents_source_check CHECK (source IN
        ('widget', 'confirm_link', 'withdraw_link', 'organizer', 'erasure'));
ALTER TABLE marketing_subscriptions DROP COLUMN IF EXISTS phone;
UPDATE organizer_contact_messages SET message = rpad(message, 10, '.') WHERE char_length(message) < 10;
ALTER TABLE organizer_contact_messages
    DROP CONSTRAINT organizer_contact_messages_message_check,
    ADD CONSTRAINT organizer_contact_messages_message_check CHECK (char_length(message) BETWEEN 10 AND 2000);
ALTER TABLE organizer_contact_messages
    DROP COLUMN IF EXISTS email_last_error,
    DROP COLUMN IF EXISTS email_done_to,
    DROP COLUMN IF EXISTS phone,
    DROP COLUMN IF EXISTS last_name,
    DROP COLUMN IF EXISTS topic;
