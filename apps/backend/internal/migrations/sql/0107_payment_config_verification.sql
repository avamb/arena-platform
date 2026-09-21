-- 0107_payment_config_verification.sql — did the provider actually accept
-- this credential?
--
-- `status` already answers "are all the required secret FIELDS filled in",
-- which is a shape check and nothing more: a row whose api_key holds any
-- non-empty string at all reads as `configured`. That is exactly how a live
-- organization spent a day selling nothing — the field was filled with a
-- twenty-character string that was not a Stripe key, every surface in the
-- admin showed the config as ready, and the only party ever told otherwise
-- was the buyer, who got a 503 on the pay button (2026-09-21).
--
-- These columns record the answer to the different question: we sent the
-- credential to the provider and it replied.
--
--   verification_status = 'unverified' — never checked, or the credential
--       changed since the last check. The honest default: it says nothing
--       about the key, and must never be rendered as a pass.
--   verification_status = 'ok'         — the provider accepted it, at
--       verified_at.
--   verification_status = 'failed'     — the provider refused it;
--       verification_error carries its own words, so the operator reads
--       "Invalid API Key provided" rather than a code of ours.
--
-- verification_error is provider text, shown to an authenticated operator of
-- the owning org. Providers do not echo a secret back in an error — Stripe
-- masks the key it rejected — but treat this as operator-facing, never
-- public: it is excluded from every unauthenticated surface, as `secrets`
-- itself is.

-- +goose Up

ALTER TABLE payment_provider_configs
    ADD COLUMN verification_status text NOT NULL DEFAULT 'unverified'
        CHECK (verification_status IN ('unverified', 'ok', 'failed')),
    ADD COLUMN verified_at         timestamptz,
    ADD COLUMN verification_error  text;

-- Existing rows keep the default 'unverified'. Deliberately NOT backfilled
-- to 'ok': nothing has been checked against a provider yet, and a green
-- badge nobody earned is worse than no badge at all.

-- +goose Down

ALTER TABLE payment_provider_configs
    DROP COLUMN verification_error,
    DROP COLUMN verified_at,
    DROP COLUMN verification_status;
