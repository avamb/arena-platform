-- 0169_web_login_code_purpose.sql — a one-time sign-in code knows what it is
-- for (spec 44, task RT-12a).
--
--   purpose  'web'      the "Open in browser" button: a 72-hour web session,
--                       a "signed in" notice in the chat, a new code takes
--                       back the person's earlier unused ones.
--            'miniapp'  the "Edit in the editor" button of the Telegram mini
--                       app: a 30-minute session (channel event_center_tg),
--                       no notice, and it neither takes back nor is taken
--                       back by a code of another purpose.
--
-- Roll back in this order: turn BOT_MINIAPP_EDITOR_ENABLED off, then roll back
-- the API, then this migration (docs/ops/event_center_rollout_ru.md).
--
-- Existing rows are 'web'. Adding a column with a constant default does not
-- rewrite the table. No permission is seeded, so the superadmin parity guard
-- has nothing to check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE web_login_codes
    ADD COLUMN purpose text NOT NULL DEFAULT 'web',
    ADD CONSTRAINT web_login_codes_purpose_check CHECK (purpose IN ('web', 'miniapp'));

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- Without the column the older code reads every row as a 'web' code: an open
-- 'miniapp' code (up to 5 minutes) would open a 72-hour web session, and a
-- used one that was never announced would send a false "signed in" notice.
-- So before the column goes, open 'miniapp' codes are taken back and the
-- notice of every 'miniapp' row is marked as sent.
UPDATE web_login_codes
   SET revoked_at = now()
 WHERE purpose = 'miniapp' AND used_at IS NULL AND revoked_at IS NULL;

UPDATE web_login_codes
   SET notice_claimed_at = now()
 WHERE purpose = 'miniapp' AND notice_claimed_at IS NULL;

ALTER TABLE web_login_codes
    DROP CONSTRAINT IF EXISTS web_login_codes_purpose_check,
    DROP COLUMN IF EXISTS purpose;
