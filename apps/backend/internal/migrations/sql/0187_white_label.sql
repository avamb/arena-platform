-- 0187_white_label.sql — white label: selling without naming Arena Sold Out
-- (spec 08_architecture/52, owner decisions 2026-11-02).
--
-- A brand is the promoter of an event, or the organization itself for an
-- event without a promoter. white_label_periods says from which day to which
-- day (both inclusive, UTC calendar days) a brand sells without the platform
-- badge. Only a platform superadmin writes a period; a period is closed by
-- setting ends_on, never deleted, and two periods of one brand never overlap.
--
-- monthly_price_minor is an individual monthly price in the currency of the
-- organization's billing account (NULL = the price list, 0 = free: the
-- existing clients). The price-list number is the 'subscription' rule of the
-- line 'standard' for the event kind 'white_label_month' (EUR 4500, USD 4500
-- below); a number changes by a new price-list version, never here.
--
-- The job billing.white_label writes one billable event 'white_label_month'
-- per period and completed calendar month, prorated by days.

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE TABLE white_label_periods (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    -- NULL = the organization itself (its events without a promoter).
    promoter_id         uuid        NULL,
    starts_on           date        NOT NULL,
    ends_on             date        NULL,
    monthly_price_minor bigint      NULL
                        CONSTRAINT white_label_periods_price_check CHECK (monthly_price_minor IS NULL OR monthly_price_minor >= 0),
    reason              text        NOT NULL CHECK (btrim(reason) <> ''),
    created_by          uuid        NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    ended_by            uuid        NULL,
    ended_at            timestamptz NULL,
    CONSTRAINT white_label_periods_dates_check CHECK (ends_on IS NULL OR ends_on >= starts_on - 1),
    CONSTRAINT white_label_periods_promoter_fk FOREIGN KEY (promoter_id, org_id)
        REFERENCES org_promoters (id, org_id)
);

CREATE INDEX white_label_periods_org_idx ON white_label_periods (org_id, promoter_id, starts_on);

COMMENT ON TABLE white_label_periods IS
    'White label of a brand (an organization or one of its promoters): the platform badge is hidden from starts_on to ends_on (UTC days, inclusive). Written only by a platform superadmin; closed by ends_on, never deleted.';

-- The billing event of one brand-month.
ALTER TABLE billable_events DROP CONSTRAINT billable_events_kind_check;
ALTER TABLE billable_events ADD CONSTRAINT billable_events_kind_check CHECK (kind IN (
    'ticket_sold', 'ticket_refunded', 'ticket_cancelled',
    'session_cancelled', 'event_published',
    'ticket_complimentary', 'generator_run', 'white_label_month'));

-- The price-list number: 45.00 a month in EUR and USD (owner decision
-- 2026-11-01, R-1 of spec 51), added to every version of standard-2026 that
-- has no such rule yet. Other currencies get no rule: a white label month of
-- such an account stays unrated and billing.reconcile reports it.
INSERT INTO price_list_rules (price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order)
SELECT pl.id, 'subscription', 'standard', 'white_label_month', NULL, '{"monthly_minor":4500}'::jsonb, 40
FROM   price_lists pl
WHERE  pl.code = 'standard-2026'
  AND  pl.currency IN ('EUR', 'USD')
  AND  NOT EXISTS (SELECT 1 FROM price_list_rules x
                   WHERE x.price_list_id = pl.id AND x.rule_type = 'subscription'
                     AND x.event_kind = 'white_label_month');

-- +goose Down

DELETE FROM price_list_rules WHERE rule_type = 'subscription' AND event_kind = 'white_label_month';
ALTER TABLE billable_events DROP CONSTRAINT billable_events_kind_check;
ALTER TABLE billable_events ADD CONSTRAINT billable_events_kind_check CHECK (kind IN (
    'ticket_sold', 'ticket_refunded', 'ticket_cancelled',
    'session_cancelled', 'event_published',
    'ticket_complimentary', 'generator_run'));
DROP TABLE IF EXISTS white_label_periods;
