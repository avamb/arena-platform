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
-- Existing rows are 'web'. Adding a column with a constant default does not
-- rewrite the table. No permission is seeded, so the superadmin parity guard
-- has nothing to check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE web_login_codes
    ADD COLUMN purpose text NOT NULL DEFAULT 'web',
    ADD CONSTRAINT web_login_codes_purpose_check CHECK (purpose IN ('web', 'miniapp'));

-- +goose Down
ALTER TABLE web_login_codes
    DROP CONSTRAINT IF EXISTS web_login_codes_purpose_check,
    DROP COLUMN IF EXISTS purpose;
