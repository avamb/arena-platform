-- +goose Up
-- User lifecycle: retain blocked identities for audit while denying new sessions.
ALTER TABLE users ADD COLUMN IF NOT EXISTS deactivated_at timestamptz;
CREATE INDEX IF NOT EXISTS users_deactivated_at_idx ON users (deactivated_at) WHERE deactivated_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS users_deactivated_at_idx;
ALTER TABLE users DROP COLUMN IF EXISTS deactivated_at;
