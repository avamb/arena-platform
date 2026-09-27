-- 0112_ticket_tier_chain.sql — a category hands its free places to the next
-- category when its own sale window closes.
--
-- Organizers sell one hall through a chain of price categories: "Early bird"
-- until 15.09, "Friends" until 01.10, "Last minute" after that. Only one of
-- them sells at a time and they share the hall, so when a category's
-- sale_window_end passes, its FREE places must move to the next category
-- and that category must open. Without this the next category opens with
-- whatever it was created with (often a single place, as the Bil24 import
-- left the Ashdod session of 29.10).
--
-- A row here says "when tier_id's sale window has closed, its free GA places
-- belong to next_tier_id". The tier.chain_sweep worker job
-- (internal/platform/tierchain) does the move under the same session lock
-- and invariants as a quota change (gaquota.HandOver): the session's place
-- count, capacity_total and the session ledger row never change; places held
-- by a live cart and sold places stay where they are. It keeps sweeping, so
-- a place that returns to the pool later (an expired hold) follows the chain
-- too. handed_over_at records the first hand-over, which is also when
-- next_tier_id was opened; later sweeps never reopen a category an operator
-- closed by hand.
--
-- A separate table rather than a ticket_tiers column: every query feeding
-- the shared ticket-tier scanner would have to select a new column, and the
-- chain is read only by the sweep and the tier endpoints.
--
-- Seated places never move (a seat keeps its category); a chain on a seated
-- category only closes it. Price increases on seats use the price schedule.
--
-- No permissions: set through the existing tier.create / tier.update routes.

-- +goose Up

CREATE TABLE ticket_tier_chain (
    tier_id        uuid        PRIMARY KEY REFERENCES ticket_tiers(id) ON DELETE CASCADE,
    next_tier_id   uuid        NOT NULL    REFERENCES ticket_tiers(id) ON DELETE CASCADE,
    handed_over_at timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ticket_tier_chain_not_self CHECK (tier_id <> next_tier_id)
);

CREATE INDEX ticket_tier_chain_next_idx ON ticket_tier_chain (next_tier_id);

-- +goose Down

DROP TABLE IF EXISTS ticket_tier_chain;
