-- 0196_referral_payout_after_event.sql — "Refer a friend": the cashback is
-- paid after the friend's event, not at once (owner decision 03.11.2026,
-- spec 08_architecture/52_refer_a_friend_ru.md R-RF2 and section 5.1).
--
-- The reward is still written in the friend's payment transaction ('due'),
-- but referral.payout waits until the friend's session has ended (plus a
-- day): a friend who takes the money back before that cancels it, and the
-- organizer loses nothing. Before the refund is created the organizer's
-- Stripe balance is read; with too little on it the reward stays 'due'
-- (wait_reason waiting_balance) and the organizer is told once. Waiting longer
-- than 30 days ends it not_possible (reason balance_timeout).
--
-- wait_reason         why a 'due' reward is not paid yet: waiting_event (the
--                     friend's session has not ended), session_cancelled (the
--                     session was cancelled, the friend not refunded yet),
--                     waiting_balance (too little on the organizer's Stripe
--                     balance), balance_unknown (the balance could not be
--                     read). Kept apart from reason, which a paid reward
--                     keeps (cap_reduced).
-- balance_wait_since  when the payout first found too little (or could not
--                     read) the organizer's balance; the 30 days count from it.
-- balance_notice_at   the organizer was told "N cashbacks need X on Stripe".
-- pending_notice_at   the organizer was told the refund waits at Stripe
--                     (Stripe accepted it as pending, usually for lack of
--                     balance).
-- unpaid_notice_at    the organizer was told the cashback was not paid and
--                     they settle with the referrer themselves.
--
-- Numbered 0196: 0195 is taken by the cashback reports (PR #370).

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE referral_rewards
    ADD COLUMN wait_reason        text CHECK (wait_reason IN
        ('waiting_event', 'session_cancelled', 'waiting_balance', 'balance_unknown')),
    ADD COLUMN balance_wait_since timestamptz,
    ADD COLUMN balance_notice_at  timestamptz,
    ADD COLUMN pending_notice_at  timestamptz,
    ADD COLUMN unpaid_notice_at   timestamptz;

-- +goose Down

ALTER TABLE referral_rewards
    DROP COLUMN IF EXISTS unpaid_notice_at,
    DROP COLUMN IF EXISTS pending_notice_at,
    DROP COLUMN IF EXISTS balance_notice_at,
    DROP COLUMN IF EXISTS balance_wait_since,
    DROP COLUMN IF EXISTS wait_reason;
