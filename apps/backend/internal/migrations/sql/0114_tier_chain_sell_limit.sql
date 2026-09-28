-- 0114_tier_chain_sell_limit.sql — a step of a chain of categories can be
-- limited by quantity as well as by date.
--
-- 0112 moves a category's free places to the next one when its sale window
-- closes. Organizers also sell "the first 50 tickets at 199, then 249": the
-- cheap step must not sell the whole hall before its date. sell_limit is how
-- many tickets the source category of a link sells at its price. The step
-- then OWNS exactly that many places (sold, held and free); the rest of the
-- hall waits in the next, still closed category (gaquota.RebalanceChain).
-- When the step has no free place left — everything sold or held by a cart —
-- tier.chain_sweep hands over: the step closes and the next one opens, keeping
-- its own limit and passing the rest on. A date and a limit on one step:
-- whichever comes first.
--
-- NULL keeps the date-only behaviour of 0112. No permissions: set through the
-- event-bundle import like the link itself.

-- +goose Up
ALTER TABLE ticket_tier_chain
    ADD COLUMN sell_limit integer CHECK (sell_limit IS NULL OR sell_limit > 0);

COMMENT ON COLUMN ticket_tier_chain.sell_limit IS
    'How many tickets tier_id sells before its places pass to next_tier_id; NULL = only its sale window decides.';

-- +goose Down
ALTER TABLE ticket_tier_chain DROP COLUMN IF EXISTS sell_limit;
