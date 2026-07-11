-- widget_funnel_events.sql — sqlc query definitions for the widget funnel
-- telemetry sink (feature #322 WID-0e).
-- The table is append-only (no reads from the API layer); rows are consumed
-- by analytics pipelines that read directly from PostgreSQL.

-- name: InsertWidgetFunnelEvent :exec
-- InsertWidgetFunnelEvent appends one funnel telemetry event to the audit table.
-- No PII is stored beyond the opaque checkout_token linkage.
INSERT INTO widget_funnel_events (
    id,
    feed_token,
    event_type,
    checkout_token,
    session_id,
    occurred_at
) VALUES (
    gen_random_uuid(),
    $1,
    $2,
    $3,
    $4,
    $5
);
