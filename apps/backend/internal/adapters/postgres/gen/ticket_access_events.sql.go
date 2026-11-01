package gen

// ticket_access_events.sql.go — hand-written wrappers for
// queries/ticket_access_events.sql (migration 0184): buyer downloads and views
// of their tickets, the access log of a chargeback answer.

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const insertTicketPDFDownload = `-- name: InsertTicketPDFDownload :exec
INSERT INTO ticket_access_events (checkout_session_id, ticket_id, org_id, source, action, ip, user_agent)
VALUES ($1, $2, $3, $4, 'pdf_download', NULLIF($5::text, '')::inet, NULLIF($6::text, ''))`

// InsertTicketPDFDownloadParams is one PDF request. IP is the client address
// as text and UserAgent the request's header; "" stores NULL for either.
type InsertTicketPDFDownloadParams struct {
	CheckoutSessionID uuid.UUID
	TicketID          uuid.UUID
	OrgID             uuid.UUID
	Source            string
	IP                string
	UserAgent         string
}

// InsertTicketPDFDownload records one PDF request.
func (q *Queries) InsertTicketPDFDownload(ctx context.Context, arg InsertTicketPDFDownloadParams) error {
	_, err := q.db.Exec(ctx, insertTicketPDFDownload,
		arg.CheckoutSessionID, arg.TicketID, arg.OrgID, arg.Source, arg.IP, arg.UserAgent)
	return err
}

const insertTicketsView = `-- name: InsertTicketsView :execrows
INSERT INTO ticket_access_events (checkout_session_id, ticket_id, org_id, source, action, ip, user_agent)
SELECT $1, NULL, $2, $3, 'tickets_view', NULLIF($4::text, '')::inet, NULLIF($5::text, '')
WHERE NOT EXISTS (
    SELECT 1
    FROM   ticket_access_events e
    WHERE  e.checkout_session_id = $1
      AND  e.action = 'tickets_view'
      AND  e.ip IS NOT DISTINCT FROM NULLIF($4::text, '')::inet
      AND  e.occurred_at > now() - interval '10 minutes'
)`

// InsertTicketsViewParams is one order-status answer that listed the paid
// tickets.
type InsertTicketsViewParams struct {
	CheckoutSessionID uuid.UUID
	OrgID             uuid.UUID
	Source            string
	IP                string
	UserAgent         string
}

// InsertTicketsView records that the buyer was shown the order's tickets and
// reports whether a row was written: a repeat from the same address within
// ten minutes (the widget polls) writes nothing.
func (q *Queries) InsertTicketsView(ctx context.Context, arg InsertTicketsViewParams) (bool, error) {
	tag, err := q.db.Exec(ctx, insertTicketsView,
		arg.CheckoutSessionID, arg.OrgID, arg.Source, arg.IP, arg.UserAgent)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

const listTicketAccessEventsByCheckout = `-- name: ListTicketAccessEventsByCheckout :many
SELECT id, checkout_session_id, ticket_id, org_id, source, action,
       host(ip) AS ip, user_agent, occurred_at
FROM   ticket_access_events
WHERE  checkout_session_id = $1
ORDER  BY occurred_at, id`

// TicketAccessEventRow is one ticket_access_events row; IP is the address
// without a prefix length, nil when it was unknown.
type TicketAccessEventRow struct {
	ID                uuid.UUID  `json:"id"`
	CheckoutSessionID uuid.UUID  `json:"checkout_session_id"`
	TicketID          *uuid.UUID `json:"ticket_id"`
	OrgID             uuid.UUID  `json:"org_id"`
	Source            string     `json:"source"`
	Action            string     `json:"action"`
	IP                *string    `json:"ip"`
	UserAgent         *string    `json:"user_agent"`
	OccurredAt        time.Time  `json:"occurred_at"`
}

// ListTicketAccessEventsByCheckout returns everything recorded for one
// purchase, oldest first.
func (q *Queries) ListTicketAccessEventsByCheckout(ctx context.Context, checkoutSessionID uuid.UUID) ([]TicketAccessEventRow, error) {
	rows, err := q.db.Query(ctx, listTicketAccessEventsByCheckout, checkoutSessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TicketAccessEventRow
	for rows.Next() {
		var r TicketAccessEventRow
		if err := rows.Scan(&r.ID, &r.CheckoutSessionID, &r.TicketID, &r.OrgID, &r.Source,
			&r.Action, &r.IP, &r.UserAgent, &r.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listCheckoutTokensForTickets = `-- name: ListCheckoutTokensForTickets :many
SELECT t.id, cs.checkout_token
FROM   tickets t
JOIN   checkout_sessions cs ON cs.id = t.checkout_session_id
WHERE  t.id = ANY($1::uuid[])`

// ListCheckoutTokensForTickets maps each given ticket to the public checkout
// token of its purchase, for the download links of the ticket e-mail. A
// ticket that does not exist is simply absent from the map.
func (q *Queries) ListCheckoutTokensForTickets(ctx context.Context, ticketIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ticketIDs))
	if len(ticketIDs) == 0 {
		return out, nil
	}
	rows, err := q.db.Query(ctx, listCheckoutTokensForTickets, ticketIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var token string
		if err := rows.Scan(&id, &token); err != nil {
			return nil, err
		}
		out[id] = token
	}
	return out, rows.Err()
}
