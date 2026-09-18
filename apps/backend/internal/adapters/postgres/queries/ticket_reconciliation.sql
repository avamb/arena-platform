-- ticket_reconciliation.sql — admin Tickets console manual reconciliation
-- query (GET /v1/admin/tickets). Kept as its OWN row type/scanner rather
-- than widening the shared TicketRow (see ticket_reconciliation.sql.go).

-- name: ListTicketsForReconciliation :many
-- Returns tickets across organizations (or one organization) with the
-- event/session/tier/order context the reconciliation console needs.
-- Pass NULL for any filter to leave it unconstrained.
SELECT t.id, t.checkout_session_id, t.session_id, s.event_id, t.tier_id,
       tt.name AS tier_name, t.holder_email, t.status, t.issued_at,
       t.created_at, t.updated_at, t.seat_key, t.seat_sector, t.seat_row,
       t.seat_number, t.ordinal, t.cancelled_at, t.cancellation_reason,
       t.refund_mode, t.refund_id, t.refund_date, t.refund_price,
       t.review_hold, t.review_hold_reason,
       t.system_ticket_id, ord.system_id AS order_system_id,
       tc.payload AS barcode_str
FROM   tickets t
JOIN   checkout_sessions cs ON cs.id = t.checkout_session_id
JOIN   sessions s ON s.id = t.session_id
LEFT JOIN ticket_tiers tt ON tt.id = t.tier_id
LEFT JOIN orders ord ON ord.id = t.order_id
LEFT JOIN ticket_credentials tc ON tc.ticket_id = t.id AND tc.type = 'ean13'
WHERE  ($1::uuid IS NULL OR cs.org_id = $1)
  AND  ($2::text  IS NULL OR t.status = $2)
  AND  ($3::uuid IS NULL OR s.event_id = $3)
  AND  ($4::uuid IS NULL OR t.session_id = $4)
ORDER BY t.issued_at DESC, t.id DESC
LIMIT  $5 OFFSET $6;
