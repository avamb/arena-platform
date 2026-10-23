-- +goose Up
-- =====================================================================
-- arena_new — journal of promo-code checks (promo_code_attempts)
--
-- Why: a client wrote that code RED30 "does not work". The API log held only
-- "command received ADD_PROMO_CODES" lines without the code, the verdict or
-- the reason, and the request comes from the selling site's server, so nobody
-- could tell who tried what. Every check of a promo code now leaves one row
-- here (hcheckout.ValidatePromoForLines + CheckPromoLimits are the single
-- rule, internal/platform/promoattempts is the single writer).
--
-- Privacy: NO e-mail, name or phone of the buyer. The code is stored as the
-- buyer typed it (cut to 64 characters), the client address only as a salted
-- hash. customer_id / gateway_session_id are stored when the surface knows
-- them. Rows older than 90 days are deleted by worker_jobs.cleanup.
--
-- Foreign keys: org_id cascades (a deleted organization takes its journal),
-- promo_code_id is SET NULL (the row outlives a hard-deleted code). channel,
-- customer and gateway session are plain ids on purpose: the journal must
-- never block deleting them.
-- =====================================================================
SET LOCAL lock_timeout = '5s';

CREATE TABLE promo_code_attempts (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    created_at         timestamptz NOT NULL DEFAULT now(),
    org_id             uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    channel_id         uuid,
    surface            text        NOT NULL,
    code_entered       text        NOT NULL,
    promo_code_id      uuid        REFERENCES promo_codes(id) ON DELETE SET NULL,
    session_ids        uuid[]      NOT NULL DEFAULT '{}',
    result             text        NOT NULL,
    reason             text,
    customer_id        uuid,
    gateway_session_id uuid,
    ip_hash            text,
    dedup_key          text        NOT NULL DEFAULT '',
    CONSTRAINT promo_code_attempts_surface_check CHECK (surface IN (
        'gateway_add_promo_codes', 'gateway_check_kdp', 'gateway_create_order',
        'widget_checkout', 'widget_quote', 'org_validate')),
    CONSTRAINT promo_code_attempts_result_check CHECK (result IN ('applied', 'rejected')),
    CONSTRAINT promo_code_attempts_reason_check CHECK (
        (result = 'applied' AND reason IS NULL) OR
        (result = 'rejected' AND reason IN (
            'unknown_code', 'wrong_session', 'wrong_tier', 'outside_window', 'expired',
            'max_uses', 'max_uses_per_customer', 'min_order', 'currency', 'paused',
            'other'))),
    CONSTRAINT promo_code_attempts_code_len_check CHECK (char_length(code_entered) <= 64)
);

CREATE INDEX promo_code_attempts_code_idx ON promo_code_attempts (promo_code_id, created_at DESC)
    WHERE promo_code_id IS NOT NULL;
CREATE INDEX promo_code_attempts_org_idx ON promo_code_attempts (org_id, created_at DESC);
CREATE INDEX promo_code_attempts_dedup_idx ON promo_code_attempts (org_id, dedup_key, created_at DESC);
CREATE INDEX promo_code_attempts_created_idx ON promo_code_attempts (created_at);

-- +goose Down
DROP TABLE IF EXISTS promo_code_attempts;
