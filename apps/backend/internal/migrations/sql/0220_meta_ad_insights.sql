-- 0220_meta_ad_insights.sql — the organizer's Meta ad spend, read from the
-- Marketing API with the token of the Meta connection (spec
-- 08_architecture/66 section 5, stage M2, package internal/platform/metainsights).
--
-- ad_spend_daily       one row per ad and day (day = Meta date_start, in the AD
--                      ACCOUNT's timezone). Money in the account currency's
--                      minor units. A run replaces the rows of its range for
--                      the same ad account: a day Meta no longer returns is
--                      deleted. Disconnect keeps the history (the organizer's
--                      own report); only Meta's data-deletion callback removes it.
-- ad_campaigns         name, status and objective of every campaign of the
--                      ad account, read once per run.
-- meta_insights_state  one row per organization: which ad account the history
--                      belongs to, the 90-day backfill, the last run and its
--                      short error code (never a token or Meta's message), the
--                      throttle of a manual refresh.
-- meta_graph_calls     Graph calls and failed calls per UTC day, organization
--                      and endpoint (Marketing API Access Tier: 500 calls in
--                      15 days, errors under 15 %).
--
-- sales_notification_subscriptions.kinds gets 'ads' (the daily ad summary):
-- every organization chat that hears 'tickets' hears 'ads' too; operator rows
-- (org_id NULL) get nothing. No new permission.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE TABLE ad_spend_daily (
    org_id                    uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    ad_account_id             text        NOT NULL,
    day                       date        NOT NULL,
    campaign_id               text        NOT NULL,
    campaign_name             text        NOT NULL DEFAULT '',
    adset_id                  text        NOT NULL,
    adset_name                text        NOT NULL DEFAULT '',
    ad_id                     text        NOT NULL,
    ad_name                   text        NOT NULL DEFAULT '',
    currency                  text        NOT NULL,
    spend_minor               bigint      NOT NULL DEFAULT 0,
    impressions               bigint      NOT NULL DEFAULT 0,
    clicks                    bigint      NOT NULL DEFAULT 0,
    link_clicks               bigint      NOT NULL DEFAULT 0,
    meta_purchases            bigint      NOT NULL DEFAULT 0,
    meta_purchase_value_minor bigint      NOT NULL DEFAULT 0,
    fetched_at                timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, ad_id, day)
);
CREATE INDEX ad_spend_daily_campaign_idx ON ad_spend_daily (org_id, campaign_id, day);
CREATE INDEX ad_spend_daily_day_idx ON ad_spend_daily (org_id, day);

COMMENT ON TABLE ad_spend_daily IS
    'Meta ad spend per ad and day (spec 66 M2). day is in the ad account timezone, money in the account currency minor units.';

CREATE TABLE ad_campaigns (
    org_id           uuid        NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    campaign_id      text        NOT NULL,
    ad_account_id    text        NOT NULL,
    name             text        NOT NULL DEFAULT '',
    effective_status text        NOT NULL DEFAULT '',
    objective        text        NOT NULL DEFAULT '',
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, campaign_id)
);

COMMENT ON TABLE ad_campaigns IS
    'Campaigns of the selected Meta ad account of an organization, read once per insights run (spec 66 M2).';

CREATE TABLE meta_insights_state (
    org_id            uuid        PRIMARY KEY REFERENCES organizations (id) ON DELETE CASCADE,
    ad_account_id     text,
    backfill_done_at  timestamptz,
    last_run_at       timestamptz,
    last_success_at   timestamptz,
    last_error        text,
    manual_not_before timestamptz,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE meta_insights_state IS
    'Insights sync state of an organization (spec 66 M2). last_error is a short code, never a token or Meta''s message.';

CREATE TABLE meta_graph_calls (
    day      date NOT NULL,
    org_id   uuid NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    endpoint text NOT NULL,
    calls    int  NOT NULL DEFAULT 0,
    errors   int  NOT NULL DEFAULT 0,
    PRIMARY KEY (day, org_id, endpoint),
    CONSTRAINT meta_graph_calls_endpoint_chk CHECK (endpoint IN ('insights', 'campaigns'))
);

COMMENT ON TABLE meta_graph_calls IS
    'Marketing API calls and failed calls per UTC day, organization and endpoint (spec 66 M2, Access Tier volume).';

ALTER TABLE sales_notification_subscriptions
    DROP CONSTRAINT sales_notification_subscriptions_kinds_check;

UPDATE sales_notification_subscriptions
SET    kinds = array_append(kinds, 'ads')
WHERE  org_id IS NOT NULL
  AND  'tickets' = ANY (kinds)
  AND  NOT ('ads' = ANY (kinds));

ALTER TABLE sales_notification_subscriptions
    ADD CONSTRAINT sales_notification_subscriptions_kinds_check
    CHECK (cardinality(kinds) > 0 AND kinds <@ ARRAY['tickets', 'shop_sale', 'shop_delivery', 'ads']::text[]);

ALTER TABLE sales_chat_connect_codes
    DROP CONSTRAINT sales_chat_connect_codes_kinds_check;
ALTER TABLE sales_chat_connect_codes
    ADD CONSTRAINT sales_chat_connect_codes_kinds_check
    CHECK (cardinality(kinds) > 0 AND kinds <@ ARRAY['tickets', 'shop_sale', 'shop_delivery', 'ads']::text[]);

COMMENT ON COLUMN sales_notification_subscriptions.kinds IS
    'Which announcements the chat receives: tickets (sales and refunds of tickets), shop_sale (the fact of a shop order: number, amount, items, no buyer data), shop_delivery (the full shop order card for a delivery group), ads (the daily Meta ad summary).';

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- A chat that hears only 'ads' (made after Up) has no meaning without it: it
-- is deleted, like the shop_sale-only rows of the 0198 Down.
ALTER TABLE sales_notification_subscriptions
    DROP CONSTRAINT IF EXISTS sales_notification_subscriptions_kinds_check;

UPDATE sales_notification_subscriptions
SET    kinds = array_remove(kinds, 'ads')
WHERE  'ads' = ANY (kinds);

DELETE FROM sales_notification_subscriptions WHERE cardinality(kinds) = 0;

ALTER TABLE sales_notification_subscriptions
    ADD CONSTRAINT sales_notification_subscriptions_kinds_check
    CHECK (cardinality(kinds) > 0 AND kinds <@ ARRAY['tickets', 'shop_sale', 'shop_delivery']::text[]);

COMMENT ON COLUMN sales_notification_subscriptions.kinds IS
    'Which announcements the chat receives: tickets (sales and refunds of tickets), shop_sale (the fact of a shop order: number, amount, items, no buyer data), shop_delivery (the full shop order card for a delivery group).';

ALTER TABLE sales_chat_connect_codes
    DROP CONSTRAINT IF EXISTS sales_chat_connect_codes_kinds_check;

UPDATE sales_chat_connect_codes
SET    kinds = array_remove(kinds, 'ads')
WHERE  'ads' = ANY (kinds);

DELETE FROM sales_chat_connect_codes WHERE cardinality(kinds) = 0;

ALTER TABLE sales_chat_connect_codes
    ADD CONSTRAINT sales_chat_connect_codes_kinds_check
    CHECK (cardinality(kinds) > 0 AND kinds <@ ARRAY['tickets', 'shop_sale', 'shop_delivery']::text[]);

DROP TABLE meta_graph_calls;
DROP TABLE meta_insights_state;
DROP TABLE ad_campaigns;
DROP TABLE ad_spend_daily;
