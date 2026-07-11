-- 0062_widget_funnel_events.sql
-- Audit-grade telemetry sink for embedded widget funnel events (feature #322 WID-0e).
-- No PII stored beyond the opaque checkout_token linkage.  Feed token is the
-- credential accepted in the POST body so no auth-token join is needed at
-- write time (the handler does its own rate limiting).

-- +goose Up

CREATE TABLE widget_funnel_events (
    id             UUID          NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
    feed_token     TEXT          NOT NULL,
    event_type     TEXT          NOT NULL,
    checkout_token TEXT          NULL,
    session_id     UUID          NULL,
    occurred_at    TIMESTAMPTZ   NOT NULL,
    received_at    TIMESTAMPTZ   NOT NULL DEFAULT now()
);

-- Partial index per feed token + time for analytics queries.
CREATE INDEX widget_funnel_events_feed_token_received_at_idx
    ON widget_funnel_events (feed_token, received_at DESC);

-- +goose Down

DROP INDEX IF EXISTS widget_funnel_events_feed_token_received_at_idx;
DROP TABLE IF EXISTS widget_funnel_events;
