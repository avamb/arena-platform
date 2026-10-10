-- event_delete_impact.sql — what deleting an event would touch (EC-10).

-- name: EventDeleteImpact :one
-- Live sessions, orders that were EVER paid (paid / partially_refunded /
-- refunded — the same set the summaries call "paid") and every ticket issued
-- for a session of the event. Deleting is refused when either of the last two
-- is above zero: the buyers hold valid tickets, so the only exit is archive.
SELECT
    (SELECT count(*) FROM sessions s
      WHERE s.event_id = $1 AND s.deleted_at IS NULL)::bigint            AS sessions,
    (SELECT count(*) FROM orders o
      WHERE o.event_id = $1
        AND o.status IN ('paid', 'partially_refunded', 'refunded'))::bigint AS paid_orders,
    (SELECT count(*) FROM tickets t
       JOIN sessions s ON s.id = t.session_id
      WHERE s.event_id = $1)::bigint                                       AS tickets;
