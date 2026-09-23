-- +goose Up
-- =====================================================================
-- arena_new — a promo code can be paused
--
-- The API contract (openapi.yaml, CreatePromoCodeRequest /
-- UpdatePromoCodeRequest) has always offered `status: active | paused`, and
-- both clients use it: the superadmin promo screen's Pause button and the
-- event center's «Промокоды» tab on the WordPress site. The column CHECK
-- from 0022 never allowed 'paused' ('active', 'inactive', 'exhausted',
-- 'expired'), so every pause died on a 23514 and answered a bare
-- 500 promo.update_failed (found on staging 2026-09-23). A paused code is
-- simply not 'active', which is what ValidatePromoForLines already refuses,
-- so no reader needs to change. The old values stay allowed: nothing is
-- rewritten.
-- =====================================================================

ALTER TABLE promo_codes DROP CONSTRAINT promo_codes_status_check;
ALTER TABLE promo_codes ADD CONSTRAINT promo_codes_status_check
    CHECK (status IN ('active', 'paused', 'inactive', 'exhausted', 'expired'));

-- +goose Down
UPDATE promo_codes SET status = 'inactive' WHERE status = 'paused';
ALTER TABLE promo_codes DROP CONSTRAINT promo_codes_status_check;
ALTER TABLE promo_codes ADD CONSTRAINT promo_codes_status_check
    CHECK (status IN ('active', 'inactive', 'exhausted', 'expired'));
