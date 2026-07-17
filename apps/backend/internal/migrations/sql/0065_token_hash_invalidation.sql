-- +goose Up
-- Security migration (feature #359 / PR2-03): all tokens stored as plaintext
-- in previous schema versions are invalidated. From this point forward the
-- application layer stores only SHA-256 hashes of refresh tokens, email
-- verification tokens, and password-reset tokens. Existing plaintext rows
-- cannot be matched by hash, so they are revoked/consumed here to prevent
-- confusion and to force re-authentication on the next login attempt.
--
-- Effect on users:
--   - All active login sessions are terminated (next refresh → 401; next
--     password-reset link → 404; next verification link → 404).
--   - Users must log in again with their existing password to obtain a new
--     hashed refresh token.

UPDATE refresh_tokens
SET    revoked_at = NOW()
WHERE  revoked_at IS NULL;

UPDATE email_verification_tokens
SET    used_at = NOW()
WHERE  used_at IS NULL;

UPDATE password_reset_tokens
SET    used_at = NOW()
WHERE  used_at IS NULL;

-- +goose Down
-- Reverting this migration cannot restore revoked/consumed tokens.
-- This is intentional: token values cannot be recovered from hashes.
-- Rolling back the application code without this data is safe because
-- the old application would re-issue fresh tokens on the next login.
