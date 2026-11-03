-- 0186_sales_funnel_daily.sql — the buyer funnel counters of an event (spec 50,
-- stage A7, section 6.1).
--
-- The widget, the hosted event page and the promoter page send an anonymous
-- beacon (POST /v1/public/funnel) at three client steps: the event was shown
-- (view), a session was opened (session_open) and seats or tickets were put in
-- the cart (seats_chosen). One row per (event, day, step, surface, session,
-- utm pair) holds the counts. The row carries NO visitor data: no IP, user
-- agent, country, hour or identifier, only the event, the day (in the venue's
-- zone), the step, the surface and the two utm values from the address, cut
-- and restricted to a safe alphabet like event_page_stats (migration 0153).
-- A client that looks like a bot is counted apart in suspect_hits. The numbers
-- are an organizer's guide, not proof for an advertising account.
--
-- The server-side steps (checkout_start, payment_open, paid) are NOT stored
-- here: GET /v1/organizations/{org_id}/events/{event_id}/funnel counts them
-- live from checkout_sessions, payment_intents and orders.
--
-- session_id is NULL for the steps taken before a session is chosen (view).
-- A NULL cannot take part in a PRIMARY KEY, and a unique index over
-- COALESCE(session_id, ...) cannot be the target of ON CONFLICT without
-- repeating the expression everywhere. So session_key is a STORED generated
-- column (the session, or the all-zero uuid) and the primary key uses it:
-- plain columns for the upsert, one row per key, no partial indexes.
--
-- Retention: 24 months, like event_page_stats (no purge job yet, see AGENTS.md).
--
-- No permission is seeded: the read uses the existing report.read.

-- +goose Up
-- The foreign keys below touch busy tables: fail fast rather than queue
-- behind a long transaction during a deploy.
SET LOCAL lock_timeout = '5s';

CREATE TABLE sales_funnel_daily (
    org_id       uuid   NOT NULL,
    event_id     uuid   NOT NULL,
    session_id   uuid   REFERENCES sessions (id) ON DELETE CASCADE,
    session_key  uuid   GENERATED ALWAYS AS
                        (COALESCE(session_id, '00000000-0000-0000-0000-000000000000'::uuid)) STORED,
    day          date   NOT NULL,
    step         text   NOT NULL,
    surface      text   NOT NULL,
    utm_source   text   NOT NULL DEFAULT '',
    utm_campaign text   NOT NULL DEFAULT '',
    hits         bigint NOT NULL DEFAULT 0,
    suspect_hits bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (event_id, day, step, surface, session_key, utm_source, utm_campaign),
    CONSTRAINT sales_funnel_daily_event_fk FOREIGN KEY (event_id, org_id)
        REFERENCES events (id, org_id) ON DELETE CASCADE,
    CONSTRAINT sales_funnel_daily_step_check
        CHECK (step IN ('view', 'session_open', 'seats_chosen')),
    CONSTRAINT sales_funnel_daily_surface_check
        CHECK (surface IN ('widget', 'hosted_page', 'tickets_page')),
    CONSTRAINT sales_funnel_daily_utm_source_check
        CHECK (utm_source ~ '^[A-Za-z0-9._~-]{0,64}$'),
    CONSTRAINT sales_funnel_daily_utm_campaign_check
        CHECK (utm_campaign ~ '^[A-Za-z0-9._~-]{0,64}$'),
    CONSTRAINT sales_funnel_daily_counts_check CHECK (hits >= 0 AND suspect_hits >= 0)
);

-- The organization-wide reads and the cascade from sessions.
CREATE INDEX sales_funnel_daily_org_day_idx ON sales_funnel_daily (org_id, day);
CREATE INDEX sales_funnel_daily_session_idx ON sales_funnel_daily (session_id)
    WHERE session_id IS NOT NULL;

COMMENT ON TABLE sales_funnel_daily IS
    'Spec 50 A7: per-day client funnel counters (view, session_open, seats_chosen) of an event, no visitor data. hits are people, suspect_hits look like bots.';

-- +goose Down
DROP TABLE IF EXISTS sales_funnel_daily;
