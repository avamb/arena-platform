package refunds

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CodeRefundNotFound is the 404 of a refund that does not exist in the
// organization (a foreign refund reads exactly like a missing one).
const CodeRefundNotFound = "refund.not_found"

// listStates are the values the queue accepts as a state filter.
var listStates = map[string]bool{
	StateRequested: true, StateApproved: true, StateProviderPending: true,
	StateSucceeded: true, StateFailed: true, StateManualReview: true, "rejected": true,
}

// Queue paging bounds.
const (
	DefaultListLimit = 50
	MaxListLimit     = 100
)

// ListFilter selects the organization's refunds for the queue.
type ListFilter struct {
	// State, when set, is one of the refund states.
	State string
	// EventID, when set, keeps refunds of orders of that event.
	EventID *uuid.UUID
	Limit   int
	Offset  int
}

// ListItem is one queue row: the refund plus what a person needs to place it.
type ListItem struct {
	Refund
	OrderSystemID *int64
	EventName     *string
	TicketNumber  *int64
}

// ListPage is one page of the queue.
type ListPage struct {
	Items   []ListItem
	Total   int
	HasMore bool
}

// List pages the refunds of ONE organization, newest first. A refund that
// needs an operator (manual_review) is not special-cased here: the caller
// asks for it with State.
func (e *Engine) List(ctx context.Context, orgID uuid.UUID, f ListFilter) (ListPage, error) {
	if f.State != "" && !listStates[f.State] {
		return ListPage{}, refusal(http.StatusBadRequest, CodeInvalidRequest, "state is not a refund state", nil)
	}
	if f.Limit <= 0 {
		f.Limit = DefaultListLimit
	}
	if f.Limit > MaxListLimit {
		f.Limit = MaxListLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	var page ListPage
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		where := ` FROM refunds r
			LEFT JOIN orders o ON o.id = r.order_id AND o.org_id = r.org_id
			LEFT JOIN events ev ON ev.id = o.event_id
			LEFT JOIN tickets t ON t.id = COALESCE(r.ticket_id, r.cancelled_ticket_id)
			WHERE r.org_id = $1
			  AND ($2::text = '' OR r.state = $2)
			  AND ($3::uuid IS NULL OR o.event_id = $3)`
		if err := tx.QueryRow(ctx, `SELECT count(*)`+where, orgID, f.State, f.EventID).Scan(&page.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT o.system_id, ev.name, t.system_ticket_id, `+prefixedColumns("r")+where+`
			ORDER BY r.created_at DESC, r.id DESC LIMIT $4 OFFSET $5`,
			orgID, f.State, f.EventID, f.Limit, f.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it ListItem
			r, err := scanRefundWith(rows, &it.OrderSystemID, &it.EventName, &it.TicketNumber)
			if err != nil {
				return err
			}
			it.Refund = r
			page.Items = append(page.Items, it)
		}
		return rows.Err()
	})
	if err != nil {
		return ListPage{}, err
	}
	page.HasMore = f.Offset+len(page.Items) < page.Total
	return page, nil
}

// prefixedColumns is refundColumns with every column qualified by alias.
func prefixedColumns(alias string) string {
	cols := []string{"id", "org_id", "payment_intent_id", "order_id", "ticket_id", "batch_id", "amount", "currency",
		"reason", "requested_by", "state", "settlement", "provider", "provider_refund_id", "provider_status",
		"failure_code", "failure_reason", "cancel_ticket", "provider_attempts", "provider_attempted_at",
		"created_at", "updated_at", "cancelled_ticket_id"}
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += alias + "." + c
	}
	return out
}

// HistoryEntry is one step of a refund's life, read from the audit trail.
type HistoryEntry struct {
	Action      string
	At          time.Time
	Outcome     string
	State       string
	FailureCode string
}

// Detail is a refund with its history.
type Detail struct {
	ListItem
	History []HistoryEntry
}

// Get returns one refund of the organization with its audit history. A refund
// of another organization is CodeRefundNotFound.
func (e *Engine) Get(ctx context.Context, orgID, refundID uuid.UUID) (Detail, error) {
	var d Detail
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT o.system_id, ev.name, t.system_ticket_id, `+prefixedColumns("r")+`
			FROM refunds r
			LEFT JOIN orders o ON o.id = r.order_id AND o.org_id = r.org_id
			LEFT JOIN events ev ON ev.id = o.event_id
			LEFT JOIN tickets t ON t.id = COALESCE(r.ticket_id, r.cancelled_ticket_id)
			WHERE r.id = $1 AND r.org_id = $2`, refundID, orgID)
		r, err := scanRefundWith(row, &d.OrderSystemID, &d.EventName, &d.TicketNumber)
		if errors.Is(err, pgx.ErrNoRows) {
			return refusal(http.StatusNotFound, CodeRefundNotFound, "refund not found", nil)
		}
		if err != nil {
			return err
		}
		d.Refund = r
		rows, err := tx.Query(ctx, `SELECT action, occurred_at, metadata FROM audit_events
			WHERE resource_type = 'refund' AND resource_id = $1 ORDER BY occurred_at, id LIMIT 100`, refundID.String())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h HistoryEntry
			var meta []byte
			if err := rows.Scan(&h.Action, &h.At, &meta); err != nil {
				return err
			}
			// Only the fields that explain the step; the rest of the audit
			// metadata (ids, amounts) stays in the audit trail.
			var m struct {
				Outcome     string `json:"outcome"`
				State       string `json:"state"`
				FailureCode string `json:"failure_code"`
			}
			_ = json.Unmarshal(meta, &m)
			h.Outcome, h.State, h.FailureCode = m.Outcome, m.State, m.FailureCode
			d.History = append(d.History, h)
		}
		return rows.Err()
	})
	return d, err
}
