-- 0149_email_letter_sources.sql — tie every change, cancellation and refund
-- letter to the notice it answers, and keep the letter's sender.
--
-- The stale-letter sweep (emailstatus.SweepStale) turns a letter left
-- 'sending' into 'sent' when its source says the letter went out. Until now
-- a change or cancellation letter was confirmed by ANY sent notice of its
-- order, and a refund letter by ANY notified refund of its order, so a letter
-- that never went out could be confirmed by another notice of the same order
-- (a second move, a retry, a second refund operation). Each source row now
-- names the letter that carried it, exactly as delivery_jobs.email_letter_id
-- already does for ticket letters: the link is written when the letter is
-- recorded and moves to the newer letter on a retry.
--
-- from_address and reply_to are dispute evidence: who the buyer saw as the
-- sender and where a reply went.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
-- refunds and session_change_notices are written by live traffic: fail fast
-- rather than queue behind a long transaction during a deploy.
SET LOCAL lock_timeout = '5s';

ALTER TABLE session_change_notices
    ADD COLUMN email_letter_id uuid REFERENCES email_letters(id) ON DELETE SET NULL;

ALTER TABLE refunds
    ADD COLUMN buyer_notice_letter_id uuid REFERENCES email_letters(id) ON DELETE SET NULL;

CREATE INDEX session_change_notices_email_letter_idx
    ON session_change_notices (email_letter_id) WHERE email_letter_id IS NOT NULL;

CREATE INDEX refunds_buyer_notice_letter_idx
    ON refunds (buyer_notice_letter_id) WHERE buyer_notice_letter_id IS NOT NULL;

ALTER TABLE email_letters
    ADD COLUMN from_address text,
    ADD COLUMN reply_to     text;

COMMENT ON COLUMN session_change_notices.email_letter_id IS
    'The letter that carried this notice to the buyer (the latest attempt).';
COMMENT ON COLUMN refunds.buyer_notice_letter_id IS
    'The refund letter that carried this refund to the buyer (the latest attempt).';
COMMENT ON COLUMN email_letters.from_address IS
    'The From address of the letter as sent (dispute evidence).';
COMMENT ON COLUMN email_letters.reply_to IS
    'The Reply-To address of the letter as sent, when it had one (dispute evidence).';

-- +goose Down
ALTER TABLE email_letters
    DROP COLUMN IF EXISTS reply_to,
    DROP COLUMN IF EXISTS from_address;
DROP INDEX IF EXISTS refunds_buyer_notice_letter_idx;
DROP INDEX IF EXISTS session_change_notices_email_letter_idx;
ALTER TABLE refunds DROP COLUMN IF EXISTS buyer_notice_letter_id;
ALTER TABLE session_change_notices DROP COLUMN IF EXISTS email_letter_id;
