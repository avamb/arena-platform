-- 0140_bot_dialogs_strangers.sql — bot_dialogs may hold a dialog of a person
-- who is not linked to an organization yet (EC-40).
--
-- The organizer application in the Telegram bot (spec 34 section 10) starts
-- with a stranger: the first four answers (name, phone, e-mail, language) are
-- asked before any application or bot_telegram_links row exists. They lived in
-- process memory, so a restart lost them. bot_dialogs referenced
-- bot_telegram_links, which only a linked person has, so a stranger's dialog
-- could not be stored.
--
-- The reference is dropped. A dialog row is short lived by design (30 minutes,
-- swept after a day), carries no organization data for a stranger (org_id is
-- NULL) and the sweep deletes whatever an unlinked account leaves behind.
--
-- No permission is seeded, so the superadmin parity guard has nothing to check.

-- +goose Up

ALTER TABLE bot_dialogs DROP CONSTRAINT IF EXISTS bot_dialogs_telegram_user_id_fkey;

COMMENT ON COLUMN bot_dialogs.telegram_user_id IS
    'Telegram account the dialog belongs to. Not a foreign key: a stranger applying for an '
    'organization has no bot_telegram_links row yet.';

-- +goose Down

DELETE FROM bot_dialogs WHERE telegram_user_id NOT IN (SELECT telegram_user_id FROM bot_telegram_links);
ALTER TABLE bot_dialogs
    ADD CONSTRAINT bot_dialogs_telegram_user_id_fkey
    FOREIGN KEY (telegram_user_id) REFERENCES bot_telegram_links(telegram_user_id) ON DELETE CASCADE;
