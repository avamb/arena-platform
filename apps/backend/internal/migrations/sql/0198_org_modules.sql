-- 0198_org_modules.sql — paid modules of an organization, the first is the shop
-- (spec 08_architecture/61, owner decisions 2026-11-05).
--
-- org_module_periods says from which day to which day (both inclusive, UTC
-- calendar days) an organization has a module. Same shape and rules as
-- white_label_periods (0188): only a platform superadmin writes a period; a
-- period is closed by setting ends_on, never deleted; two periods of one
-- (organization, module) never share a day (checked in Go under an advisory
-- lock, see platform/orgmodules). The module list is a CHECK; a new module is
-- one more value here.
--
-- monthly_price_minor is an individual monthly price (NULL = the price list,
-- 0 = free) in `currency`. The price-list number is the 'subscription' rule of
-- the line 'standard' for the event kind 'shop_module_month'; it is 0 in every
-- currency of standard-2026 for now (the module is free for everybody, V-9 of
-- the spec), and a number changes by a new price-list version, never here.
--
-- Existing shops: every organization that already has a shop_settings row gets
-- a 'shop' period from the day its shop first shows up (V-10), at an
-- individual price of 0, so the section does not vanish at the release.
--
-- sales_notification_subscriptions.kinds (0173) trades 'shop' for two values:
-- shop_sale (a chat that hears the FACT of a shop sale) and shop_delivery (a
-- delivery group that gets the full card). Every old 'shop' becomes
-- shop_delivery, so existing logistics groups work as before; every row that
-- hears 'tickets' (organization rows and the operator row) also gets
-- shop_sale.

-- +goose Up

SET LOCAL lock_timeout = '5s';

CREATE TABLE org_module_periods (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    module              text        NOT NULL
                        CONSTRAINT org_module_periods_module_check CHECK (module IN ('shop')),
    starts_on           date        NOT NULL,
    ends_on             date        NULL,
    monthly_price_minor bigint      NULL
                        CONSTRAINT org_module_periods_price_check CHECK (monthly_price_minor IS NULL OR monthly_price_minor >= 0),
    currency            text        NULL
                        CONSTRAINT org_module_periods_currency_check CHECK (currency IS NULL OR currency ~ '^[A-Z]{3}$'),
    reason              text        NOT NULL CHECK (btrim(reason) <> ''),
    created_by          uuid        NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    ended_by            uuid        NULL,
    ended_at            timestamptz NULL,
    CONSTRAINT org_module_periods_price_currency_check CHECK ((monthly_price_minor IS NULL) = (currency IS NULL)),
    CONSTRAINT org_module_periods_dates_check CHECK (ends_on IS NULL OR ends_on >= starts_on - 1)
);

CREATE INDEX org_module_periods_org_idx ON org_module_periods (org_id, module, starts_on);

COMMENT ON TABLE org_module_periods IS
    'A paid module of an organization (shop): connected from starts_on to ends_on (UTC days, inclusive). NULL price = the price list, 0 = free. Written only by a platform superadmin; closed by ends_on, never deleted.';

-- "Does this organization have an open shop order?" is asked on every /v1/me
-- (the shop's orders-only mode, spec 61 section 4.1). The predicate below is
-- THE definition of "open" and must stay identical to openShopOrderPredicate
-- in gen/org_modules.sql.go, or the planner cannot use the index. Open:
-- awaiting payment or in manual review, or paid and not yet shipped (a
-- delivery_kind of 'none' - digital goods, services - has nothing to ship).
CREATE INDEX shop_orders_open_idx ON shop_orders (org_id)
    WHERE status IN ('pending_payment', 'manual_review')
       OR (status IN ('paid', 'partially_refunded') AND shipped_at IS NULL AND delivery_kind <> 'none');

-- "Did this organization ever sell something in the shop?" decides whether its
-- orders stay reachable for refunds and chargebacks after the module ended
-- (orders-only mode has no end date, spec 61 section 4.1). Asked on every
-- /v1/me together with the open predicate above; paid_at is stamped when the
-- money was taken and never cleared. Must match the paid_at IS NOT NULL test in
-- OrgModuleStates (gen/org_modules.sql.go).
CREATE INDEX shop_orders_paid_idx ON shop_orders (org_id)
    WHERE paid_at IS NOT NULL;

-- The billing event of one module-month.
ALTER TABLE billable_events DROP CONSTRAINT billable_events_kind_check;
-- The drop takes ACCESS EXCLUSIVE on billable_events until the migration
-- commits (one transaction, lock_timeout 5s); the re-check of its rows is a
-- short scan. Only the billing jobs write this table, never a sale.
ALTER TABLE billable_events ADD CONSTRAINT billable_events_kind_check CHECK (kind IN (
    'ticket_sold', 'ticket_refunded', 'ticket_cancelled',
    'session_cancelled', 'event_published',
    'ticket_complimentary', 'generator_run', 'white_label_month',
    'shop_module_month'));

-- The price-list number: the module is free for everybody for now. One rule
-- per price list (every currency), added to versions already in force as an
-- exception to "a number changes only by a new version": no shop_module_month
-- event exists before this migration, so no charge already made changes.
INSERT INTO price_list_rules (price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order)
SELECT pl.id, 'subscription', 'standard', 'shop_module_month', NULL, '{"monthly_minor":0}'::jsonb, 41
FROM   price_lists pl
WHERE  pl.code = 'standard-2026'
  AND  NOT EXISTS (SELECT 1 FROM price_list_rules x
                   WHERE x.price_list_id = pl.id AND x.rule_type = 'subscription'
                     AND x.event_kind = 'shop_module_month');

-- Existing shops keep their section. shop_settings has no creation date, so
-- the period starts on the earliest sign of the shop: its settings row, its
-- first product or its first order (UTC day). The currency is the one of the
-- organization's oldest organizer billing account, else the shop's own.
INSERT INTO org_module_periods (org_id, module, starts_on, ends_on, monthly_price_minor, currency, reason, created_by)
SELECT s.org_id, 'shop',
       (LEAST(s.updated_at,
              COALESCE((SELECT min(p.created_at) FROM shop_products p WHERE p.org_id = s.org_id), s.updated_at),
              COALESCE((SELECT min(o.created_at) FROM shop_orders o WHERE o.org_id = s.org_id), s.updated_at))
        AT TIME ZONE 'UTC')::date,
       NULL, 0,
       COALESCE((SELECT ba.currency::text FROM billing_accounts ba
                 WHERE ba.org_id = s.org_id AND ba.account_kind = 'organizer'
                 ORDER BY ba.created_at, ba.currency LIMIT 1),
                s.currency::text),
       'migration 0198: shop existed before modules', NULL
FROM   shop_settings s
WHERE  NOT EXISTS (SELECT 1 FROM org_module_periods m WHERE m.org_id = s.org_id AND m.module = 'shop');

-- Subscriptions: shop -> shop_delivery, tickets -> also shop_sale.
ALTER TABLE sales_notification_subscriptions
    DROP CONSTRAINT sales_notification_subscriptions_kinds_check;

UPDATE sales_notification_subscriptions
SET    kinds = (SELECT array_agg(k ORDER BY first_pos)
                FROM   (SELECT x.k, min(x.pos) AS first_pos
                        FROM   unnest(array_replace(kinds, 'shop', 'shop_delivery')) WITH ORDINALITY AS x(k, pos)
                        GROUP  BY x.k) d)
WHERE  'shop' = ANY (kinds);

UPDATE sales_notification_subscriptions
SET    kinds = array_append(kinds, 'shop_sale')
WHERE  'tickets' = ANY (kinds)
  AND  NOT ('shop_sale' = ANY (kinds));

ALTER TABLE sales_notification_subscriptions
    ADD CONSTRAINT sales_notification_subscriptions_kinds_check
    CHECK (cardinality(kinds) > 0 AND kinds <@ ARRAY['tickets', 'shop_sale', 'shop_delivery']::text[]);

COMMENT ON COLUMN sales_notification_subscriptions.kinds IS
    'Which announcements the chat receives: tickets (sales and refunds of tickets), shop_sale (the fact of a shop order: number, amount, items, no buyer data), shop_delivery (the full shop order card for a delivery group).';

-- +goose Down

-- Down refuses (the CHECK below fails) once a shop_module_month event exists:
-- charged module-months are billing facts and are never deleted. It also
-- removes subscription rules an operator added later for the shop module.
-- A subscription that only hears shop_sale (made after Up) has no meaning in
-- the old world, where an empty kinds is refused and 'tickets' would announce
-- ticket sales the chat never asked for: such a row is deleted.

ALTER TABLE sales_notification_subscriptions
    DROP CONSTRAINT IF EXISTS sales_notification_subscriptions_kinds_check;

UPDATE sales_notification_subscriptions
SET    kinds = array_remove(kinds, 'shop_sale')
WHERE  'shop_sale' = ANY (kinds);

DELETE FROM sales_notification_subscriptions WHERE cardinality(kinds) = 0;

UPDATE sales_notification_subscriptions
SET    kinds = (SELECT array_agg(k ORDER BY first_pos)
                FROM   (SELECT x.k, min(x.pos) AS first_pos
                        FROM   unnest(array_replace(kinds, 'shop_delivery', 'shop')) WITH ORDINALITY AS x(k, pos)
                        GROUP  BY x.k) d)
WHERE  'shop_delivery' = ANY (kinds);

ALTER TABLE sales_notification_subscriptions
    ADD CONSTRAINT sales_notification_subscriptions_kinds_check
    CHECK (cardinality(kinds) > 0 AND kinds <@ ARRAY['tickets', 'shop']::text[]);

COMMENT ON COLUMN sales_notification_subscriptions.kinds IS
    'Which announcements the chat receives: tickets (sales and refunds of tickets), shop (merchandise shop orders).';

DELETE FROM price_list_rules WHERE rule_type = 'subscription' AND event_kind = 'shop_module_month';
ALTER TABLE billable_events DROP CONSTRAINT billable_events_kind_check;
ALTER TABLE billable_events ADD CONSTRAINT billable_events_kind_check CHECK (kind IN (
    'ticket_sold', 'ticket_refunded', 'ticket_cancelled',
    'session_cancelled', 'event_published',
    'ticket_complimentary', 'generator_run', 'white_label_month'));
DROP INDEX IF EXISTS shop_orders_paid_idx;
DROP INDEX IF EXISTS shop_orders_open_idx;
DROP TABLE IF EXISTS org_module_periods;
