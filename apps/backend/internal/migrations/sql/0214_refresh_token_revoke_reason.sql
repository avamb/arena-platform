-- 0214_refresh_token_revoke_reason.sql — why a refresh token was revoked.
--
-- POST /v1/auth/refresh reads it to tell a token replaced by rotation (the
-- account's sessions are ended) from one ended by a sign-out or a sweep
-- (a plain 401). Rotation only changes a live token, so a refresh that loses
-- a race with another refresh, a sign-out or a sweep gets a plain 401 and
-- never overwrites the reason written first.
--
--   revoke_reason  'rotated'      replaced by a newer token on refresh;
--                  'signed_out'   revoked by a sign-out or session eviction;
--                  'revoked_all'  revoked by a sweep over all tokens of the
--                                 person (password reset, the compromise
--                                 sweep, deactivation, admin lifecycle).
--
-- NULL on a revoked row written before this migration means "unknown" and
-- is handled like 'rotated'.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE refresh_tokens ADD COLUMN revoke_reason text;

-- The CHECK is added NOT VALID and validated in a separate statement: the
-- ADD takes the strong lock only briefly, and VALIDATE scans the existing
-- rows under a lock that lets reads and writes go on.
ALTER TABLE refresh_tokens ADD CONSTRAINT refresh_tokens_revoke_reason_check
    CHECK (revoke_reason IN ('rotated', 'signed_out', 'revoked_all')) NOT VALID;

ALTER TABLE refresh_tokens VALIDATE CONSTRAINT refresh_tokens_revoke_reason_check;

COMMENT ON COLUMN refresh_tokens.revoke_reason IS
    'Why the token was revoked: rotated, signed_out or revoked_all. NULL on a revoked row means unknown and is handled like rotated.';

-- +goose Down
ALTER TABLE refresh_tokens DROP CONSTRAINT IF EXISTS refresh_tokens_revoke_reason_check;
ALTER TABLE refresh_tokens DROP COLUMN IF EXISTS revoke_reason;
