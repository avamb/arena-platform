-- 0189_smart_price_rules.sql — the "smart price" presets (spec
-- 08_architecture/51_4_2_smart_price_ru.md, owner decisions 2026-11-02).
--
-- A preset is NOT a price engine: enabling it writes ordinary categories,
-- chain links with sell limits (migration 0114) or price windows through the
-- event-bundle import, and those rows stay the only truth about a price. This
-- table only remembers that a category follows a preset and with which
-- parameters, so the event center can say "smart price is on", show the next
-- step, stop the rise and the buyer page can show one card per chain.
--
-- tier_id is the category the preset was applied to (the chain head for
-- by_sales). step_tier_ids lists the categories the preset created after it.
-- status: active, stopped (the organizer froze the price), overridden (a
-- price of a step was edited by hand, owner decision В-5: the preset no
-- longer drives the future steps).

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE TABLE smart_price_rules (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id         uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    session_id     uuid        NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    tier_id        uuid        NOT NULL REFERENCES ticket_tiers(id) ON DELETE CASCADE,
    kind           text        NOT NULL CHECK (kind IN ('by_sales', 'by_days', 'last_days')),
    params         jsonb       NOT NULL,
    base_price     bigint      NOT NULL CHECK (base_price > 0),
    step_tier_ids  uuid[]      NOT NULL DEFAULT '{}',
    status         text        NOT NULL DEFAULT 'active'
                               CHECK (status IN ('active', 'stopped', 'overridden')),
    created_by     uuid        NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    stopped_at     timestamptz NULL
);

-- One live preset per category: a second one would fight the first.
CREATE UNIQUE INDEX smart_price_rules_active_tier_uq
    ON smart_price_rules (tier_id) WHERE status = 'active';

CREATE INDEX smart_price_rules_session_idx ON smart_price_rules (session_id);

-- +goose Down

DROP TABLE IF EXISTS smart_price_rules;
