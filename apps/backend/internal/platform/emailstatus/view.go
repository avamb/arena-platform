package emailstatus

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FailureSendFailed is the failure_event of a ticket letter the relay never
// accepted (the delivery job ended failed): the provider has no event for it.
const FailureSendFailed = "send_failed"

// StuckAfter is how long a letter may stay "sent" or "delayed" before the
// order read flags it as stuck: Brevo reports a delivery within seconds, so
// six hours without one means the buyer most likely has no letter. The bot's
// texts name this number of hours; eventbot's
// TestOrderCard_StuckTextsNameTheServerThreshold keeps them in step.
const StuckAfter = 6 * time.Hour

// stuckAfterSQL is StuckAfter as a Postgres interval.
const stuckAfterSQL = "6 hours"

// LetterView is one letter of an order as the order card shows it. Times are
// RFC 3339 UTC; a step that has not happened is null.
type LetterView struct {
	// ID is the email_letters id; null for a letter the relay has not
	// accepted (queued, send_failed) or one sent before letters were
	// recorded.
	ID        *string  `json:"id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Recipient string   `json:"recipient"`
	TicketIDs []string `json:"ticket_ids"`
	QueuedAt  *string  `json:"queued_at"`
	SentAt    *string  `json:"sent_at"`
	// DeliveredAt is the provider's "delivered"; when only a proxy open
	// proves delivery it is null and ProxyOpenedAt is set.
	DeliveredAt   *string `json:"delivered_at"`
	ProxyOpenedAt *string `json:"proxy_opened_at"`
	OpenedAt      *string `json:"opened_at"`
	// ClickedAt is the first click on a link of the letter (the ticket
	// download, for instance); a sign of delivery, not proof of reading.
	ClickedAt *string `json:"clicked_at"`
	// DeferredAt is the first time Brevo reported the letter deferred (the
	// recipient's server asked it to retry later).
	DeferredAt    *string `json:"deferred_at"`
	FailedAt      *string `json:"failed_at"`
	FailureEvent  *string `json:"failure_event"`
	FailureReason *string `json:"failure_reason"`
	// Stuck: the letter has been "sent" or "delayed" for more than
	// StuckAfter with no delivery, while the webhook kept reporting on later
	// letters (so silence is not just a webhook that is switched off).
	Stuck bool `json:"stuck"`
}

// stuckExpr is the SQL form of LetterView.Stuck for a letter row "l".
const stuckExpr = `(l.status IN ('sent', 'delayed') AND l.sent_at < now() - '` + stuckAfterSQL + `'::interval
	AND (l.status = 'delayed' OR EXISTS (SELECT 1 FROM email_letter_events e WHERE e.received_at > l.sent_at)))`

// orderTicketLettersSQL lists each issued ticket of the order with its
// delivery job and the letter that job was last sent in.
const orderTicketLettersSQL = `
SELECT t.id, dj.status, COALESCE(dj.recipient_email, t.holder_email, ''), dj.queued_at, dj.sent_at, dj.last_error,
       l.id, l.kind, l.recipient, l.status, l.sent_at, l.delivered_at, l.proxy_opened_at, l.opened_at,
       l.failed_at, l.failure_event, l.failure_reason, l.clicked_at, l.deferred_at, ` + stuckExpr + `
FROM   order_items oi
JOIN   tickets t        ON t.id = oi.ticket_id
JOIN   delivery_jobs dj ON dj.ticket_id = t.id
LEFT JOIN email_letters l ON l.id = dj.email_letter_id
WHERE  oi.order_id = $1
ORDER  BY oi.ordinal`

// orderOtherLettersSQL lists the order's change, cancellation and refund
// letters, oldest first.
const orderOtherLettersSQL = `
SELECT l.id, l.kind, l.recipient, l.status, l.sent_at, l.delivered_at, l.proxy_opened_at, l.opened_at,
       l.failed_at, l.failure_event, l.failure_reason, l.clicked_at, l.deferred_at, ` + stuckExpr + `
FROM   email_letters l
WHERE  l.order_id = $1 AND l.kind IN ('change', 'cancel', 'refund') AND l.status <> 'sending'
ORDER  BY l.sent_at`

type letterRow struct {
	ID                                   uuid.UUID
	Kind, Recipient, Status              string
	SentAt                               time.Time
	DeliveredAt, ProxyOpenedAt, OpenedAt *time.Time
	FailedAt, ClickedAt, DeferredAt      *time.Time
	FailureEvent, FailureReason          *string
	Stuck                                *bool
}

func (r letterRow) view() LetterView {
	id := r.ID.String()
	v := LetterView{
		ID: &id, Kind: r.Kind, Status: r.Status, Recipient: r.Recipient, TicketIDs: []string{},
		SentAt: ts(&r.SentAt), DeliveredAt: ts(r.DeliveredAt), ProxyOpenedAt: ts(r.ProxyOpenedAt),
		OpenedAt: ts(r.OpenedAt), ClickedAt: ts(r.ClickedAt), DeferredAt: ts(r.DeferredAt), FailedAt: ts(r.FailedAt),
		FailureEvent: r.FailureEvent, FailureReason: r.FailureReason,
		Stuck: r.Stuck != nil && *r.Stuck,
	}
	if v.Status == StatusSending {
		// The event outran Accepted for a moment; the provider has the letter.
		v.Status = StatusSent
	}
	return v
}

// NeedsAttention is true when the buyer most likely has no letter: it
// failed, or it is stuck. This is the order list's letter_failed flag.
func (v LetterView) NeedsAttention() bool {
	return v.Status == StatusFailed || v.Stuck
}

// ticketLetterGroups collects the ticket letters of ONE order or ONE
// invitation batch: one entry per sent letter, one for the tickets still
// queued, one for those the relay refused, one for tickets sent before
// letters were recorded. kind is the kind of the entries that have no
// email_letters row (ticket for an order, invitation for an issuance).
type ticketLetterGroups struct {
	kind   string
	out    []LetterView
	groups map[string]int
}

func newTicketLetterGroups(kind string) *ticketLetterGroups {
	return &ticketLetterGroups{kind: kind, out: []LetterView{}, groups: map[string]int{}}
}

func (g *ticketLetterGroups) add(key string, v LetterView, ticketID uuid.UUID) *LetterView {
	i, ok := g.groups[key]
	if !ok {
		g.out = append(g.out, v)
		i = len(g.out) - 1
		g.groups[key] = i
	}
	g.out[i].TicketIDs = append(g.out[i].TicketIDs, ticketID.String())
	return &g.out[i]
}

// ticketLetterRow is one row of orderTicketLettersSQL.
type ticketLetterRow struct {
	ticketID       uuid.UUID
	jobStatus, rcp string
	queuedAt       time.Time
	jobSentAt      *time.Time
	lastError      *string
	lid            *uuid.UUID
	lKind, lRcp    *string
	lStatus        *string
	lSent          *time.Time
	lr             letterRow
}

// scanTicketLetterRow scans the columns orderTicketLettersSQL selects, after
// any leading columns the caller names in lead.
func scanTicketLetterRow(rows pgx.Rows, lead ...any) (ticketLetterRow, error) {
	var r ticketLetterRow
	dest := append(lead, &r.ticketID, &r.jobStatus, &r.rcp, &r.queuedAt, &r.jobSentAt, &r.lastError,
		&r.lid, &r.lKind, &r.lRcp, &r.lStatus, &r.lSent, &r.lr.DeliveredAt, &r.lr.ProxyOpenedAt, &r.lr.OpenedAt,
		&r.lr.FailedAt, &r.lr.FailureEvent, &r.lr.FailureReason, &r.lr.ClickedAt, &r.lr.DeferredAt, &r.lr.Stuck)
	return r, rows.Scan(dest...)
}

func (g *ticketLetterGroups) addRow(r ticketLetterRow) {
	ticketID, jobStatus, rcp, queuedAt, jobSentAt, lastError := r.ticketID, r.jobStatus, r.rcp, r.queuedAt, r.jobSentAt, r.lastError
	lid, lKind, lRcp, lStatus, lSent, lr := r.lid, r.lKind, r.lRcp, r.lStatus, r.lSent, r.lr
	switch jobStatus {
	case "pending", "processing":
		v := g.add("queued|"+rcp, LetterView{Kind: g.kind, Status: StatusQueued, Recipient: rcp, TicketIDs: []string{}}, ticketID)
		// RFC 3339 UTC strings of one layout compare in time order.
		if q := ts(&queuedAt); v.QueuedAt == nil || (q != nil && *q < *v.QueuedAt) {
			v.QueuedAt = q
		}
	case "failed":
		ev := FailureSendFailed
		v := LetterView{Kind: g.kind, Status: StatusFailed, Recipient: rcp, TicketIDs: []string{},
			QueuedAt: ts(&queuedAt), FailureEvent: &ev}
		if lastError != nil && *lastError != "" {
			r := ScrubReason(*lastError)
			v.FailureReason = &r
		}
		g.add("failed|"+rcp, v, ticketID)
	case "sent":
		if lid == nil {
			g.add("legacy|"+rcp, LetterView{Kind: g.kind, Status: StatusSent, Recipient: rcp,
				TicketIDs: []string{}, SentAt: ts(jobSentAt)}, ticketID)
			return
		}
		lr.ID, lr.Kind, lr.Recipient, lr.Status = *lid, deref(lKind), deref(lRcp), deref(lStatus)
		if lSent != nil {
			lr.SentAt = *lSent
		}
		g.add("letter|"+lid.String(), lr.view(), ticketID)
	}
	// skipped / disabled jobs never became a letter: nothing to show.
}

// OrderLetters assembles the order's letters: its ticket letters (grouped
// the way they went out: one entry per sent letter, one for the tickets still
// queued, one for those the relay refused, one for tickets sent before
// letters were recorded), then its change, cancellation and refund letters.
//
// A ticket letter replaced by a resend stays in email_letters as evidence
// but is not listed: the card shows the letter each ticket was last sent in.
// Invitation letters have no order and are never listed here; see
// InvitationLetters.
func OrderLetters(ctx context.Context, db DB, orderID uuid.UUID) ([]LetterView, error) {
	rows, err := db.Query(ctx, orderTicketLettersSQL, orderID)
	if err != nil {
		return nil, err
	}
	g := newTicketLetterGroups(KindTicket)
	for rows.Next() {
		r, err := scanTicketLetterRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		g.addRow(r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := g.out

	rows, err = db.Query(ctx, orderOtherLettersSQL, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r letterRow
		if err := rows.Scan(&r.ID, &r.Kind, &r.Recipient, &r.Status, &r.SentAt, &r.DeliveredAt,
			&r.ProxyOpenedAt, &r.OpenedAt, &r.FailedAt, &r.FailureEvent, &r.FailureReason, &r.ClickedAt,
			&r.DeferredAt, &r.Stuck); err != nil {
			return nil, err
		}
		out = append(out, r.view())
	}
	return out, rows.Err()
}

// failedLetterOrdersSQL picks, among the given orders, those whose buyer
// most likely has no letter: a ticket whose job failed or whose current
// letter failed or is stuck, or a change / cancellation / refund letter that
// failed or is stuck and was not followed by a newer letter of the same kind.
// "Newer" is compared to the second, exactly as the order card compares the
// RFC 3339 times it shows (horders.supersededLetter), so card and list agree.
const failedLetterOrdersSQL = `
SELECT o.id
FROM   unnest($1::uuid[]) AS o(id)
WHERE  EXISTS (
         SELECT 1
         FROM   order_items oi
         JOIN   delivery_jobs dj ON dj.ticket_id = oi.ticket_id
         LEFT JOIN email_letters l ON l.id = dj.email_letter_id
         WHERE  oi.order_id = o.id
           AND  (dj.status = 'failed' OR (dj.status = 'sent' AND (l.status = 'failed' OR ` + stuckExpr + `))))
   OR  EXISTS (
         SELECT 1
         FROM   email_letters l
         WHERE  l.order_id = o.id AND l.kind IN ('change', 'cancel', 'refund')
           AND  (l.status = 'failed' OR ` + stuckExpr + `)
           AND  NOT EXISTS (
                  SELECT 1 FROM email_letters n
                  WHERE  n.order_id = o.id AND n.kind = l.kind AND n.status <> 'sending'
                    AND  date_trunc('second', n.sent_at) > date_trunc('second', l.sent_at)))`

// FailedLetterOrders reports which of orderIDs have a letter that failed or
// is stuck (letter_failed on the orders list).
func FailedLetterOrders(ctx context.Context, db DB, orderIDs []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if len(orderIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, failedLetterOrdersSQL, orderIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func ts(t *time.Time) *string {
	if t == nil || t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// invitationLettersSQL lists the tickets of the given complimentary
// issuances with their delivery job and the letter that job was last sent
// in. Same columns as orderTicketLettersSQL, after the issuance id.
const invitationLettersSQL = `
SELECT t.complimentary_issuance_id, t.id, dj.status, COALESCE(dj.recipient_email, t.holder_email, ''), dj.queued_at, dj.sent_at, dj.last_error,
       l.id, l.kind, l.recipient, l.status, l.sent_at, l.delivered_at, l.proxy_opened_at, l.opened_at,
       l.failed_at, l.failure_event, l.failure_reason, l.clicked_at, l.deferred_at, ` + stuckExpr + `
FROM   tickets t
JOIN   delivery_jobs dj ON dj.ticket_id = t.id
LEFT JOIN email_letters l ON l.id = dj.email_letter_id
WHERE  t.complimentary_issuance_id = ANY($1::uuid[])
ORDER  BY t.complimentary_issuance_id, t.system_ticket_id`

// InvitationLetters reads, for each of the given complimentary issuances,
// the delivery status of the letters its tickets were sent in. An invitation
// has no order: the letter is found through the ticket's delivery job
// (delivery_jobs.ticket_id -> email_letter_id), the same link the order card
// follows. Entries are grouped like OrderLetters' ticket letters; the kind of
// a letter that has not been accepted yet is "invitation". An issuance with
// no delivery job (or only skipped ones) maps to an empty list.
func InvitationLetters(ctx context.Context, db DB, issuanceIDs []uuid.UUID) (map[uuid.UUID][]LetterView, error) {
	out := map[uuid.UUID][]LetterView{}
	if len(issuanceIDs) == 0 {
		return out, nil
	}
	rows, err := db.Query(ctx, invitationLettersSQL, issuanceIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[uuid.UUID]*ticketLetterGroups{}
	for rows.Next() {
		var issuanceID uuid.UUID
		r, err := scanTicketLetterRow(rows, &issuanceID)
		if err != nil {
			return nil, err
		}
		g, ok := groups[issuanceID]
		if !ok {
			g = newTicketLetterGroups(KindInvitation)
			groups[issuanceID] = g
		}
		g.addRow(r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for id, g := range groups {
		out[id] = g.out
	}
	return out, nil
}
