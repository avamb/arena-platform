-- 0141_refund_buyer_notice.sql — the letter to a buyer whose money went back
-- (PAY-07, spec 08_architecture/36 section 7 step 5).
--
-- One letter covers the refunds of an operation (a refund batch) that were
-- accepted together. Three columns on refunds keep it from being sent twice and
-- from being lost:
--   buyer_notice_claimed_at  set by the worker job that is about to send the
--                            letter, so two jobs never send the same refund
--                            (a claim older than ten minutes is taken over);
--   buyer_notified_at        set once the mail provider accepted the letter;
--   buyer_notice_given_up_at set when the letter kept failing and the
--                            operator was told instead, so the alert goes
--                            out once and the sweep stops queueing it.
--
-- Refunds that exist now are marked notified: no buyer gets a letter about a
-- refund made before this change.
--
-- No permission is seeded, so the superadmin parity guard has nothing to check.

-- +goose Up

ALTER TABLE refunds
    ADD COLUMN buyer_notice_claimed_at  timestamptz NULL,
    ADD COLUMN buyer_notified_at        timestamptz NULL,
    ADD COLUMN buyer_notice_given_up_at timestamptz NULL;

UPDATE refunds SET buyer_notified_at = now() WHERE batch_id IS NOT NULL;

COMMENT ON COLUMN refunds.buyer_notified_at IS
    'When the letter about this refund reached the mail provider; NULL until then. Only refunds of a refund_batches row with notify_buyer are mailed.';

-- +goose Down

ALTER TABLE refunds
    DROP COLUMN IF EXISTS buyer_notice_given_up_at,
    DROP COLUMN IF EXISTS buyer_notified_at,
    DROP COLUMN IF EXISTS buyer_notice_claimed_at;
