-- 0123_user_names.sql — an optional first and last name on a user.
--
-- The admin console showed members and users only by UUID (and by e-mail in
-- the user directory), so "which of these two owners is Vera" had to be
-- guessed from join times. Both columns are NULLable and never required: an
-- invited user has only an e-mail until somebody fills the name in.
--
-- No permission is seeded here, so the 532 parity guard has nothing to check.

-- +goose Up

ALTER TABLE users ADD COLUMN first_name text;
ALTER TABLE users ADD COLUMN last_name  text;

ALTER TABLE users ADD CONSTRAINT users_first_name_len CHECK (first_name IS NULL OR char_length(first_name) BETWEEN 1 AND 100);
ALTER TABLE users ADD CONSTRAINT users_last_name_len  CHECK (last_name  IS NULL OR char_length(last_name)  BETWEEN 1 AND 100);

COMMENT ON COLUMN users.first_name IS 'Optional given name, 1-100 characters; NULL = not given.';
COMMENT ON COLUMN users.last_name  IS 'Optional family name, 1-100 characters; NULL = not given.';

-- +goose Down

ALTER TABLE users DROP CONSTRAINT IF EXISTS users_last_name_len;
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_first_name_len;
ALTER TABLE users DROP COLUMN IF EXISTS last_name;
ALTER TABLE users DROP COLUMN IF EXISTS first_name;
