-- event_sales_state.sql — the facts behind `sales_state` on the organization
-- events list (EC-02, spec 35 §5.3). One batched query per list, never one
-- per event; the decision itself (on_sale / upcoming / sold_out / archived)
-- is made in hcatalog from these facts so it stays unit-testable.

-- name: ListEventSalesFacts :many
-- One row per event that has at least one active (non-deleted,
-- non-cancelled) session — the same set the first_session_at trigger and the
-- hosted page's session_count use. An event with no such session is absent
-- and reads as archived.
--
--   session_count    every active session, past ones included
--   future_sessions  sessions that have not ended yet (end_at > now())
--   next_session_at  the earliest start among those
--   selling          a future session whose own sales end has not passed
--                    still has a free place in a category that is open and
--                    inside its sale window — the hold gate's own rule
--                    (hcheckout.CategorySellable + CheckSessionSalesOpen)
--   places_left      a future session still has a free place in ANY live
--                    category, open or not (a closed category or a sale that
--                    has not started is "upcoming", an empty hall "sold out")
SELECT s.event_id,
       count(*)::bigint                                   AS session_count,
       count(*) FILTER (WHERE s.end_at > now())::bigint   AS future_sessions,
       min(s.start_at) FILTER (WHERE s.end_at > now())    AS next_session_at,
       COALESCE(bool_or(
           s.end_at > now() AND s.sales_end_at > now() AND EXISTS (
               SELECT 1
               FROM   session_seats ss
               JOIN   ticket_tiers  tt ON tt.id = ss.tier_id
               WHERE  ss.session_id = s.id
                 AND  ss.status     = 'available'
                 AND  tt.deleted_at IS NULL
                 AND  tt.is_open
                 AND  (tt.sale_window_start IS NULL OR tt.sale_window_start <= now())
                 AND  (tt.sale_window_end   IS NULL OR tt.sale_window_end   >= now()))
       ), false)                                          AS selling,
       COALESCE(bool_or(
           s.end_at > now() AND EXISTS (
               SELECT 1
               FROM   session_seats ss
               JOIN   ticket_tiers  tt ON tt.id = ss.tier_id
               WHERE  ss.session_id = s.id
                 AND  ss.status     = 'available'
                 AND  tt.deleted_at IS NULL)
       ), false)                                          AS places_left
FROM   sessions s
WHERE  s.event_id = ANY($1::uuid[])
  AND  s.deleted_at IS NULL
  AND  s.status <> 'cancelled'
GROUP  BY s.event_id;
