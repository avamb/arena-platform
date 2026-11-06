-- 0210_marketing_import_source.sql — spec 62 section 10.1 (P-18).
--
-- The Brevo / Elementor import (cmd/arena-marketing-import) writes its facts to
-- the same marketing_subscriptions / marketing_consents tables as the widget.
-- The consent journal's source list gets 'import': a row of that source is
-- evidence of a consent given elsewhere (date and source copied from the
-- export), or of a suppression (unsubscribed / blacklisted in Brevo).

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE marketing_consents DROP CONSTRAINT marketing_consents_source_check;
ALTER TABLE marketing_consents ADD CONSTRAINT marketing_consents_source_check
    CHECK (source IN ('widget', 'confirm_link', 'withdraw_link', 'organizer', 'erasure', 'import'));

-- +goose Down
DELETE FROM marketing_consents WHERE source = 'import';
ALTER TABLE marketing_consents DROP CONSTRAINT marketing_consents_source_check;
ALTER TABLE marketing_consents ADD CONSTRAINT marketing_consents_source_check
    CHECK (source IN ('widget', 'confirm_link', 'withdraw_link', 'organizer', 'erasure'));
