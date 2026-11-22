-- 0218_site_tracking_partnership.sql — spec 63, L-10 and L-5.
--
-- L-10, server-side tracking of a static site (POST /v1/public/feeds/{token}/track):
--   * sales_funnel_daily gets the step outbound_click: a click on a partner box
--     office. It is counted by the track route only, never by the public funnel
--     beacon, and it is not one of the six steps the organizer funnel reads.
--   * outbound_clicks_daily: the same click per event, day and box office (the
--     seller name), no visitor data. It feeds the organizer's "Clicks to box
--     offices" block.
--   * conversion_deliveries now also carries events that have no order (a click
--     on a box office, a lead): order_id may be NULL, event_id is the browser's
--     id of the event (the dedup key of Meta and GA4), payload keeps the context
--     the sender needs and is emptied when the row is final. The sweep deletes
--     such rows 30 days after the last change.
--
-- L-5, the contact form topic partnership: company, city and website columns.
--
-- No permission is seeded.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_funnel_daily DROP CONSTRAINT sales_funnel_daily_step_check;
ALTER TABLE sales_funnel_daily ADD CONSTRAINT sales_funnel_daily_step_check
    CHECK (step IN ('view', 'session_open', 'seats_chosen', 'outbound_click'));

CREATE TABLE outbound_clicks_daily (
    org_id   uuid   NOT NULL,
    event_id uuid   NOT NULL,
    day      date   NOT NULL,
    seller   text   NOT NULL DEFAULT '',
    hits     bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (event_id, day, seller),
    CONSTRAINT outbound_clicks_daily_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT outbound_clicks_daily_seller_check CHECK (char_length(seller) <= 64),
    CONSTRAINT outbound_clicks_daily_hits_check CHECK (hits >= 0)
);
CREATE INDEX outbound_clicks_daily_org_day_idx ON outbound_clicks_daily (org_id, day);

ALTER TABLE conversion_deliveries
    ALTER COLUMN order_id DROP NOT NULL,
    ADD COLUMN event_id text,
    ADD COLUMN payload  jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE conversion_deliveries DROP CONSTRAINT conversion_deliveries_kind_chk;
ALTER TABLE conversion_deliveries ADD CONSTRAINT conversion_deliveries_kind_chk
    CHECK (kind IN ('purchase', 'page_view', 'view_content', 'initiate_checkout', 'lead', 'subscribe'));
ALTER TABLE conversion_deliveries ADD CONSTRAINT conversion_deliveries_shape_chk
    CHECK ((order_id IS NOT NULL AND event_id IS NULL)
        OR (order_id IS NULL AND event_id IS NOT NULL AND kind <> 'purchase'));
CREATE UNIQUE INDEX conversion_deliveries_event_uq
    ON conversion_deliveries (org_id, provider, event_id) WHERE event_id IS NOT NULL;
CREATE INDEX conversion_deliveries_event_purge_idx
    ON conversion_deliveries (updated_at) WHERE order_id IS NULL;

ALTER TABLE organizer_contact_messages
    DROP CONSTRAINT organizer_contact_messages_topic_check,
    ADD CONSTRAINT organizer_contact_messages_topic_check
        CHECK (topic IN ('contact', 'private_event', 'partnership')),
    ADD COLUMN company text NOT NULL DEFAULT '' CHECK (char_length(company) <= 100),
    ADD COLUMN city    text NOT NULL DEFAULT '' CHECK (char_length(city) <= 100),
    ADD COLUMN website text NOT NULL DEFAULT '' CHECK (char_length(website) <= 200);

-- +goose Down
ALTER TABLE organizer_contact_messages
    DROP COLUMN website, DROP COLUMN city, DROP COLUMN company;
DELETE FROM organizer_contact_messages WHERE topic = 'partnership';
ALTER TABLE organizer_contact_messages DROP CONSTRAINT organizer_contact_messages_topic_check;
ALTER TABLE organizer_contact_messages ADD CONSTRAINT organizer_contact_messages_topic_check
    CHECK (topic IN ('contact', 'private_event'));

DROP INDEX conversion_deliveries_event_purge_idx;
DROP INDEX conversion_deliveries_event_uq;
DELETE FROM conversion_deliveries WHERE order_id IS NULL;
ALTER TABLE conversion_deliveries DROP CONSTRAINT conversion_deliveries_shape_chk;
ALTER TABLE conversion_deliveries DROP CONSTRAINT conversion_deliveries_kind_chk;
ALTER TABLE conversion_deliveries ADD CONSTRAINT conversion_deliveries_kind_chk CHECK (kind IN ('purchase'));
ALTER TABLE conversion_deliveries DROP COLUMN payload, DROP COLUMN event_id;
ALTER TABLE conversion_deliveries ALTER COLUMN order_id SET NOT NULL;

DROP TABLE outbound_clicks_daily;
DELETE FROM sales_funnel_daily WHERE step = 'outbound_click';
ALTER TABLE sales_funnel_daily DROP CONSTRAINT sales_funnel_daily_step_check;
ALTER TABLE sales_funnel_daily ADD CONSTRAINT sales_funnel_daily_step_check
    CHECK (step IN ('view', 'session_open', 'seats_chosen'));
