-- +goose Up
SET LOCAL lock_timeout = '5s';

-- =====================================================================
-- arena_new — billing foundation (billing wave B-A, task B1-1).
--
-- Spec: 08_architecture/41_billing_credits_architecture_ru.md (sections
-- 5.1-5.7, 6.1, 6.3, 6.9, 8.1) and 42_billing_execution_spec_ru.md (card
-- B1-1). Numbers: docs/pricing/pricing_policy_2026-10-16_ru.md.
--
-- What this migration adds, and what it does NOT do:
--
--   * billing_issuers        — our legal entities that issue invoices.
--   * billing_country_routes — country of the venue -> account currency and
--                              issuer (41 section 5.7).
--   * price_lists / price_list_rules / price_list_packs — versioned price
--                              lists. A rate is a ROW, never a code constant:
--                              changing a number means a new version.
--   * billing_accounts       — one account per (organization, currency,
--                              kind). An organization selling in two
--                              currencies has two accounts and two balances.
--   * billing_account_terms  — dated history of an account's conditions.
--                              The terms of a sale are the last row with
--                              effective_from <= occurred_at.
--   * org_rate_overrides     — individual rates of an organization (owner
--                              decision B-2): they replace the price-list
--                              rate everywhere, by invoice and from credits.
--   * permissions billing.purchase and billing.payer_manage.
--
-- Nothing here charges, invoices or moves money. Rating is task B1-2,
-- collection B2. The legacy table tariffs stays readable and is marked so.
--
-- Money is bigint MINOR units with the currency next to it. numeric is used
-- only for a share (price_lists.bonus_share_cap). Rule params (jsonb) carry
-- amounts in minor units and percentages in basis points (pct_bp).
--
-- Account and override rows reference organizations WITHOUT cascade on
-- purpose: billing history must not vanish with a hard delete. A test
-- fixture that hard-deletes an organization deletes its billing_accounts
-- (terms cascade) and org_rate_overrides first.
-- =====================================================================

-- ── billing_issuers ─────────────────────────────────────────────────────
CREATE TABLE billing_issuers (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    code            text        NOT NULL UNIQUE,
    name            text        NOT NULL,
    country         char(2)     NOT NULL
                    CONSTRAINT billing_issuers_country_check CHECK (country ~ '^[A-Z]{2}$'),
    tax_id          text        NULL,
    address         jsonb       NULL,
    invoice_series  text        NULL,
    -- Month and day boundaries of billing are counted in the ISSUER's zone,
    -- never UTC and never the venue's (41 section 6.2).
    timezone        text        NOT NULL,
    vat_texts       jsonb       NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE billing_issuers IS
    'Platform legal entities that issue billing invoices. Timezone defines billing month/day boundaries (41 section 6.2).';

-- One issuer: the Estonian entity (owner decision B-14). Only the name and
-- the country for now, the legal details (tax id, address, invoice series,
-- VAT texts) come later with invoicing (block B4). The name is read from the
-- ABH TEAM organization profile when it exists, else the plain name.
INSERT INTO billing_issuers (code, name, country, timezone)
VALUES (
    'abh-team-ee',
    COALESCE(
        (SELECT COALESCE(NULLIF(btrim(legal_name), ''), name)
         FROM   organizations
         WHERE  name ILIKE 'ABH TEAM%' AND deleted_at IS NULL
         ORDER BY created_at
         LIMIT  1),
        'ABH TEAM'),
    'EE',
    'Europe/Tallinn'
)
ON CONFLICT (code) DO NOTHING;

-- ── billing_country_routes ──────────────────────────────────────────────
CREATE TABLE billing_country_routes (
    country     char(2) PRIMARY KEY
                CONSTRAINT billing_country_routes_country_check CHECK (country ~ '^[A-Z]{2}$'),
    currency    char(3) NOT NULL
                CONSTRAINT billing_country_routes_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    issuer_id   uuid    NOT NULL REFERENCES billing_issuers(id)
);

COMMENT ON TABLE billing_country_routes IS
    'Venue country -> billing account currency and issuer (41 section 5.7). A country without a row bills in EUR (billing.RouteCurrency).';

-- Policy section 9: EUR for the euro area, the EU and Czechia (until a CZK
-- price list exists), Israel in EUR until shekels come. USD for the United
-- States and Latin America. Run decision R-B2, owner question V-B1: every
-- other country bills in EUR by default (no row needed).
-- Numbers and routes wait for the confirmation of policy section 12.
INSERT INTO billing_country_routes (country, currency, issuer_id)
SELECT c.country, c.currency, i.id
FROM   billing_issuers i
CROSS JOIN (VALUES
    -- EU 27
    ('AT', 'EUR'), ('BE', 'EUR'), ('BG', 'EUR'), ('HR', 'EUR'), ('CY', 'EUR'),
    ('CZ', 'EUR'), ('DK', 'EUR'), ('EE', 'EUR'), ('FI', 'EUR'), ('FR', 'EUR'),
    ('DE', 'EUR'), ('GR', 'EUR'), ('HU', 'EUR'), ('IE', 'EUR'), ('IT', 'EUR'),
    ('LV', 'EUR'), ('LT', 'EUR'), ('LU', 'EUR'), ('MT', 'EUR'), ('NL', 'EUR'),
    ('PL', 'EUR'), ('PT', 'EUR'), ('RO', 'EUR'), ('SK', 'EUR'), ('SI', 'EUR'),
    ('ES', 'EUR'), ('SE', 'EUR'),
    -- EEA outside the EU
    ('IS', 'EUR'), ('LI', 'EUR'), ('NO', 'EUR'),
    -- Israel: shekels later, euros until then
    ('IL', 'EUR'),
    -- United States and Latin America
    ('US', 'USD'), ('MX', 'USD'), ('GT', 'USD'), ('HN', 'USD'), ('SV', 'USD'),
    ('NI', 'USD'), ('CR', 'USD'), ('PA', 'USD'), ('CU', 'USD'), ('DO', 'USD'),
    ('PR', 'USD'), ('CO', 'USD'), ('VE', 'USD'), ('EC', 'USD'), ('PE', 'USD'),
    ('BO', 'USD'), ('BR', 'USD'), ('PY', 'USD'), ('UY', 'USD'), ('AR', 'USD'),
    ('CL', 'USD')
) AS c(country, currency)
WHERE  i.code = 'abh-team-ee'
ON CONFLICT (country) DO NOTHING;

-- ── price_lists ─────────────────────────────────────────────────────────
CREATE TABLE price_lists (
    id                          uuid         PRIMARY KEY DEFAULT uuidv7(),
    code                        text         NOT NULL,
    version                     int          NOT NULL
                                CONSTRAINT price_lists_version_check CHECK (version > 0),
    currency                    char(3)      NOT NULL
                                CONSTRAINT price_lists_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    effective_from              timestamptz  NOT NULL,
    revoked_at                  timestamptz  NULL,
    locked                      boolean      NOT NULL DEFAULT false,
    -- Parameters the policy names without a number are NULL until a
    -- superadmin sets them (stop condition ST-2): the yearly limit of free
    -- tickets (B-16) and the floor of platform revenue per ticket (B-20).
    free_tickets_annual_limit   int          NULL
                                CONSTRAINT price_lists_free_limit_check CHECK (free_tickets_annual_limit IS NULL OR free_tickets_annual_limit >= 0),
    floor_per_ticket_minor      bigint       NULL
                                CONSTRAINT price_lists_floor_check CHECK (floor_per_ticket_minor IS NULL OR floor_per_ticket_minor >= 0),
    bonus_share_cap             numeric(4,3) NOT NULL DEFAULT 0.300
                                CONSTRAINT price_lists_bonus_share_cap_check CHECK (bonus_share_cap >= 0 AND bonus_share_cap <= 1),
    bonus_covers_surcharge      boolean      NOT NULL DEFAULT false,
    created_by                  text         NULL,
    reason                      text         NULL,
    created_at                  timestamptz  NOT NULL DEFAULT now(),
    CONSTRAINT price_lists_code_currency_version_uq UNIQUE (code, currency, version)
);

CREATE INDEX price_lists_code_currency_effective_idx
    ON price_lists (code, currency, effective_from DESC);

COMMENT ON TABLE price_lists IS
    'Versioned platform price lists. A rate change is a NEW version with its own effective_from, never an edit. Sales are rated by the version in force at occurred_at.';

-- ── price_list_rules ────────────────────────────────────────────────────
CREATE TABLE price_list_rules (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    price_list_id   uuid        NOT NULL REFERENCES price_lists(id) ON DELETE CASCADE,
    rule_type       text        NOT NULL
                    CONSTRAINT price_list_rules_type_check CHECK (rule_type IN (
                        'fixed_per_ticket', 'percent_of_price_with_cap', 'low_price_formula',
                        'volume_above_baseline', 'fixed_per_event', 'subscription',
                        'credit_pack', 'generator_run')),
    line            text        NOT NULL
                    CONSTRAINT price_list_rules_line_check CHECK (line IN ('standard', 'venue')),
    event_kind      text        NOT NULL,
    -- A threshold in the currency of the SALE (low_price_formula): NULL for
    -- rules that do not depend on the sale currency.
    sale_currency   char(3)     NULL
                    CONSTRAINT price_list_rules_sale_currency_check CHECK (sale_currency IS NULL OR sale_currency ~ '^[A-Z]{3}$'),
    params          jsonb       NOT NULL,
    sort_order      int         NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX price_list_rules_price_list_idx ON price_list_rules (price_list_id, sort_order);

-- ── price_list_packs ────────────────────────────────────────────────────
CREATE TABLE price_list_packs (
    id                      uuid     PRIMARY KEY DEFAULT uuidv7(),
    price_list_id           uuid     NOT NULL REFERENCES price_lists(id) ON DELETE CASCADE,
    tier                    smallint NOT NULL
                            CONSTRAINT price_list_packs_tier_check CHECK (tier > 0),
    face_minor              bigint   NOT NULL
                            CONSTRAINT price_list_packs_face_check CHECK (face_minor > 0),
    unseated_rate_minor     bigint   NOT NULL
                            CONSTRAINT price_list_packs_unseated_check CHECK (unseated_rate_minor >= 0),
    seated_surcharge_minor  bigint   NOT NULL
                            CONSTRAINT price_list_packs_surcharge_check CHECK (seated_surcharge_minor >= 0),
    active                  boolean  NOT NULL DEFAULT true,
    sort_order              int      NOT NULL DEFAULT 0,
    CONSTRAINT price_list_packs_list_tier_uq UNIQUE (price_list_id, tier)
);

-- ── billing_accounts ────────────────────────────────────────────────────
CREATE TABLE billing_accounts (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id              uuid        NOT NULL REFERENCES organizations(id),
    currency            char(3)     NOT NULL
                        CONSTRAINT billing_accounts_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    issuer_id           uuid        NOT NULL REFERENCES billing_issuers(id),
    account_kind        text        NOT NULL DEFAULT 'organizer'
                        CONSTRAINT billing_accounts_kind_check CHECK (account_kind IN ('organizer', 'tools')),
    payer               jsonb       NULL,
    tax_status          text        NULL
                        CONSTRAINT billing_accounts_tax_status_check CHECK (tax_status IS NULL OR tax_status IN (
                            'b2b_domestic', 'b2b_eu_reverse_charge', 'b2b_non_eu',
                            'b2c_eu', 'b2c_non_eu', 'exempt')),
    tax_id_verified_at  timestamptz NULL,
    auto_topup          jsonb       NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT billing_accounts_org_currency_kind_uq UNIQUE (org_id, currency, account_kind)
);

CREATE INDEX billing_accounts_org_idx ON billing_accounts (org_id);

COMMENT ON TABLE billing_accounts IS
    'Billing account = (organization, currency, kind). Created per currency of the organization''s venues (billing_country_routes).';

-- ── billing_account_terms ───────────────────────────────────────────────
CREATE TABLE billing_account_terms (
    id                  uuid        PRIMARY KEY DEFAULT uuidv7(),
    account_id          uuid        NOT NULL REFERENCES billing_accounts(id) ON DELETE CASCADE,
    effective_from      timestamptz NOT NULL,
    price_list_code     text        NOT NULL,
    plan_kind           text        NOT NULL DEFAULT 'standard'
                        CONSTRAINT billing_account_terms_plan_kind_check CHECK (plan_kind IN ('standard', 'venue')),
    billing_cycle       text        NOT NULL DEFAULT 'monthly'
                        CONSTRAINT billing_account_terms_cycle_check CHECK (billing_cycle IN ('prepaid', 'per_event', 'monthly')),
    shortfall_policy    text        NOT NULL DEFAULT 'invoice_postpaid'
                        CONSTRAINT billing_account_terms_shortfall_check CHECK (shortfall_policy IN ('invoice_postpaid', 'block_publishing')),
    issuer_id           uuid        NOT NULL REFERENCES billing_issuers(id),
    invoice_delay_days  int         NULL
                        CONSTRAINT billing_account_terms_delay_check CHECK (invoice_delay_days IS NULL OR invoice_delay_days >= 0),
    due_days            int         NULL
                        CONSTRAINT billing_account_terms_due_check CHECK (due_days IS NULL OR due_days >= 0),
    locked              boolean     NOT NULL DEFAULT false,
    created_by          text        NULL,
    reason              text        NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT billing_account_terms_account_effective_uq UNIQUE (account_id, effective_from)
);

CREATE INDEX billing_account_terms_account_effective_idx
    ON billing_account_terms (account_id, effective_from DESC);

COMMENT ON TABLE billing_account_terms IS
    'Dated conditions of a billing account. The terms of an event are the last row with effective_from <= occurred_at.';

-- ── org_rate_overrides ──────────────────────────────────────────────────
CREATE TABLE org_rate_overrides (
    id              uuid        PRIMARY KEY DEFAULT uuidv7(),
    org_id          uuid        NOT NULL REFERENCES organizations(id),
    currency        char(3)     NOT NULL
                    CONSTRAINT org_rate_overrides_currency_check CHECK (currency ~ '^[A-Z]{3}$'),
    unseated_minor  bigint      NOT NULL
                    CONSTRAINT org_rate_overrides_unseated_check CHECK (unseated_minor >= 0),
    seated_minor    bigint      NOT NULL
                    CONSTRAINT org_rate_overrides_seated_check CHECK (seated_minor >= 0),
    effective_from  timestamptz NOT NULL,
    effective_to    timestamptz NULL,
    note            text        NULL,
    created_by      text        NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT org_rate_overrides_period_check CHECK (effective_to IS NULL OR effective_to > effective_from),
    CONSTRAINT org_rate_overrides_org_currency_from_uq UNIQUE (org_id, currency, effective_from)
);

CREATE INDEX org_rate_overrides_org_currency_effective_idx
    ON org_rate_overrides (org_id, currency, effective_from DESC);

COMMENT ON TABLE org_rate_overrides IS
    'Individual per-ticket rates of an organization (owner decision B-2). They replace the price-list rate everywhere, by invoice and from credits.';

-- ── tariffs is legacy ───────────────────────────────────────────────────
COMMENT ON TABLE tariffs IS
    'LEGACY since billing wave B-A: read-only compatibility; rates live in price_lists / price_list_rules / org_rate_overrides.';

-- ── seed: price list standard-2026, version 1 ───────────────────────────
-- Numbers wait for the confirmation of policy section 12 (цифры ждут
-- подтверждения §12 политики). Changing one = a new version, not an edit.
-- effective_from is 2026-01-01, earlier than any sale billed by this
-- platform, so a version is always found for a real sale. Dollars carry the
-- same numbers as euros (policy section 9: a decision, not an exchange rate).
INSERT INTO price_lists (code, version, currency, effective_from, created_by, reason)
VALUES
    ('standard-2026', 1, 'EUR', '2026-01-01T00:00:00Z', 'migration',
     'pricing policy 2026-10-16, numbers wait for section 12 confirmation'),
    ('standard-2026', 1, 'USD', '2026-01-01T00:00:00Z', 'migration',
     'pricing policy 2026-10-16, numbers wait for section 12 confirmation')
ON CONFLICT (code, currency, version) DO NOTHING;

-- Rules. Numbers wait for the confirmation of policy section 12.
--   fixed_per_ticket          policy section 2: 0.59 unseated, 1.09 seated
--   low_price_formula         policy section 5 and 12.6: unit_price before
--                             the promo code and without the service charge,
--                             strictly below 10.00 in the sale currency ->
--                             4 %, min 0.10, max 0.35, seated +0.20
--   percent_of_price_with_cap policy section 6: venue line, 2 % of the price
--                             after the promo code without our charge, cap
--                             3.00 per ticket
--   credit_pack               policy section 3 and 12.1: seated surcharge of
--                             a pack ticket 0.35 (the per-tier unseated rates
--                             are in price_list_packs)
INSERT INTO price_list_rules (price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order)
SELECT pl.id, r.rule_type, r.line, 'ticket_sold',
       CASE WHEN r.rule_type = 'low_price_formula' THEN pl.currency END,
       r.params::jsonb, r.sort_order
FROM   price_lists pl
CROSS JOIN (VALUES
    ('low_price_formula',         'standard',
     '{"threshold_minor":1000,"strict_below":true,"pct_bp":400,"min_minor":10,"max_minor":35,"seated_surcharge_minor":20}', 10),
    ('fixed_per_ticket',          'standard',
     '{"unseated_minor":59,"seated_minor":109}', 20),
    ('credit_pack',               'standard',
     '{"seated_surcharge_minor":35}', 30),
    ('percent_of_price_with_cap', 'venue',
     '{"pct_bp":200,"cap_minor":300}', 10)
) AS r(rule_type, line, params, sort_order)
WHERE  pl.code = 'standard-2026' AND pl.version = 1
  AND  NOT EXISTS (SELECT 1 FROM price_list_rules x WHERE x.price_list_id = pl.id);

-- Credit packs, policy section 3 (face / unseated rate / seated surcharge,
-- minor units). Numbers wait for the confirmation of policy section 12.
INSERT INTO price_list_packs (price_list_id, tier, face_minor, unseated_rate_minor, seated_surcharge_minor, sort_order)
SELECT pl.id, p.tier, p.face_minor, p.unseated_rate_minor, p.seated_surcharge_minor, p.tier
FROM   price_lists pl
CROSS JOIN (VALUES
    (1::smallint,   1650::bigint, 55::bigint, 35::bigint),
    (2::smallint,   5200::bigint, 52::bigint, 35::bigint),
    (3::smallint,  14700::bigint, 49::bigint, 35::bigint),
    (4::smallint,  23000::bigint, 46::bigint, 35::bigint),
    (5::smallint,  43000::bigint, 43::bigint, 35::bigint),
    (6::smallint, 100000::bigint, 40::bigint, 35::bigint),
    (7::smallint, 190000::bigint, 38::bigint, 35::bigint)
) AS p(tier, face_minor, unseated_rate_minor, seated_surcharge_minor)
WHERE  pl.code = 'standard-2026' AND pl.version = 1
ON CONFLICT (price_list_id, tier) DO NOTHING;

-- ── seed: individual rates of existing clients ──────────────────────────
-- Policy section 10, numbers wait for the confirmation of policy section
-- 12.7 (the list itself is to be checked). Run decision R-B1: matched by
-- the name of a LIVE organization, zero rows on a dev or CI database is
-- normal. Rates are minor units of EUR.
INSERT INTO org_rate_overrides (org_id, currency, unseated_minor, seated_minor, effective_from, note, created_by)
SELECT o.id, 'EUR', x.unseated_minor, x.seated_minor, '2026-01-01T00:00:00Z',
       'подтвердить (политика §12 п.7)', 'migration'
FROM   organizations o
JOIN   (VALUES
    ('%Lampyris%',        50::bigint, 100::bigint),
    ('Vino%Co%',           50::bigint, 100::bigint),
    ('%Учебный театр%',   100::bigint, 100::bigint),
    ('%Колыбель%',        100::bigint, 100::bigint)
) AS x(pattern, unseated_minor, seated_minor) ON o.name ILIKE x.pattern
WHERE  o.deleted_at IS NULL
ON CONFLICT (org_id, currency, effective_from) DO NOTHING;

-- ── seed: one billing account per currency of each live organization ────
-- The currency of a venue comes from billing_country_routes by the venue's
-- country (venues.country, else the country of its city). A country with no
-- route, or no country at all, bills in EUR. An organization with no live
-- venue gets one account by legal_address_country, else country, else EUR.
-- billing.EnsureAccount applies the same rule to organizations created
-- later.
WITH org_countries AS (
    SELECT v.org_id,
           upper(btrim(COALESCE(NULLIF(btrim(v.country), ''), c.iso2::text, ''))) AS country
    FROM   venues v
    JOIN   organizations o ON o.id = v.org_id AND o.deleted_at IS NULL
    LEFT JOIN cities    ci ON ci.id = v.city_id
    LEFT JOIN countries c  ON c.id  = ci.country_id
    WHERE  v.deleted_at IS NULL
    UNION ALL
    SELECT o.id,
           upper(btrim(COALESCE(NULLIF(btrim(o.legal_address_country), ''),
                                NULLIF(btrim(o.country), ''), '')))
    FROM   organizations o
    WHERE  o.deleted_at IS NULL
      AND  NOT EXISTS (SELECT 1 FROM venues v WHERE v.org_id = o.id AND v.deleted_at IS NULL)
),
routed AS (
    SELECT DISTINCT
           oc.org_id,
           COALESCE(r.currency, 'EUR') AS currency,
           COALESCE(r.issuer_id, (SELECT id FROM billing_issuers WHERE code = 'abh-team-ee')) AS issuer_id
    FROM   org_countries oc
    LEFT JOIN billing_country_routes r ON r.country::text = oc.country
)
INSERT INTO billing_accounts (org_id, currency, issuer_id, account_kind)
SELECT org_id, currency, issuer_id, 'organizer'
FROM   routed
ON CONFLICT (org_id, currency, account_kind) DO NOTHING;

-- First terms of every account: the standard price list, monthly cycle.
INSERT INTO billing_account_terms (
    account_id, effective_from, price_list_code, plan_kind, billing_cycle,
    shortfall_policy, issuer_id, created_by, reason)
SELECT a.id, '2026-01-01T00:00:00Z', 'standard-2026', 'standard', 'monthly',
       'invoice_postpaid', a.issuer_id, 'migration', 'initial terms'
FROM   billing_accounts a
WHERE  NOT EXISTS (SELECT 1 FROM billing_account_terms t WHERE t.account_id = a.id)
ON CONFLICT (account_id, effective_from) DO NOTHING;

-- ── permissions ─────────────────────────────────────────────────────────
-- billing.purchase: buy credit packs, set auto top-up. billing.payer_manage:
-- edit the payer details. Owner (org_admin) only, the manager (organizer)
-- gets nothing (41 section 6.9). Both are refused to organization API keys
-- (apikeys.ForbiddenScopeExact, run decision R-B8).
INSERT INTO permissions (name, description) VALUES
    ('billing.purchase',     'Buy credit packs and set up auto top-up for the organization billing account'),
    ('billing.payer_manage', 'Edit the payer details and tax status of the organization billing account')
ON CONFLICT (name) DO NOTHING;

-- Superadmin parity (TestSuperadminPermissionParity532).
INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('billing.purchase', 'billing.payer_manage')
WHERE  r.name = 'platform_superadmin'
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r
JOIN   permissions p ON p.name IN ('billing.purchase', 'billing.payer_manage')
WHERE  r.name = 'org_admin'
  AND  r.org_id IS NULL
ON CONFLICT DO NOTHING;

-- +goose Down
SET LOCAL lock_timeout = '5s';

DELETE FROM role_permissions
WHERE  permission_id IN (
    SELECT id FROM permissions WHERE name IN ('billing.purchase', 'billing.payer_manage'));

DELETE FROM permissions WHERE name IN ('billing.purchase', 'billing.payer_manage');

COMMENT ON TABLE tariffs IS NULL;

DROP TABLE IF EXISTS org_rate_overrides;
DROP TABLE IF EXISTS billing_account_terms;
DROP TABLE IF EXISTS billing_accounts;
DROP TABLE IF EXISTS price_list_packs;
DROP TABLE IF EXISTS price_list_rules;
DROP TABLE IF EXISTS price_lists;
DROP TABLE IF EXISTS billing_country_routes;
DROP TABLE IF EXISTS billing_issuers;
