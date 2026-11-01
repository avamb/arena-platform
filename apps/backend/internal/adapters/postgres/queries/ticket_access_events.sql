-- ticket_access_events.sql — buyer downloads and views of their tickets
-- (migration 0184, spec 51 decision R-13): the access log a chargeback answer
-- quotes as Stripe's access_activity_log.

-- name: InsertTicketPDFDownload :exec
-- One row per PDF request. ip is the client address as text ('' = unknown).
INSERT INTO ticket_access_events (checkout_session_id, ticket_id, org_id, source, action, ip, user_agent)
VALUES ($1, $2, $3, $4, 'pdf_download', NULLIF($5::text, '')::inet, NULLIF($6::text, ''));

-- name: InsertTicketsView :execrows
-- The order-status answer listed the paid tickets. The widget polls that
-- answer, so a view from the same address within ten minutes is not recorded
-- again (0 rows).
INSERT INTO ticket_access_events (checkout_session_id, ticket_id, org_id, source, action, ip, user_agent)
SELECT $1, NULL, $2, $3, 'tickets_view', NULLIF($4::text, '')::inet, NULLIF($5::text, '')
WHERE NOT EXISTS (
    SELECT 1
    FROM   ticket_access_events e
    WHERE  e.checkout_session_id = $1
      AND  e.action = 'tickets_view'
      AND  e.ip IS NOT DISTINCT FROM NULLIF($4::text, '')::inet
      AND  e.occurred_at > now() - interval '10 minutes'
);

-- name: ListTicketAccessEventsByCheckout :many
-- Everything recorded for one purchase, oldest first.
SELECT id, checkout_session_id, ticket_id, org_id, source, action,
       host(ip) AS ip, user_agent, occurred_at
FROM   ticket_access_events
WHERE  checkout_session_id = $1
ORDER  BY occurred_at, id;

-- name: ListCheckoutTokensForTickets :many
-- The public checkout token of each ticket, for the download links of the
-- ticket e-mail. A ticket's checkout is NOT NULL (migration 0026).
SELECT t.id, cs.checkout_token
FROM   tickets t
JOIN   checkout_sessions cs ON cs.id = t.checkout_session_id
WHERE  t.id = ANY($1::uuid[]);
