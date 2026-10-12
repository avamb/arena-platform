package refunds

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
	// CreatorKind is "user", "api_key" or "provider_dashboard"; CreatorLabel
	// names the person (shown only for a member of the organization) or the
	// API key. Both nil when the creator cannot be told.
	CreatorKind  *string
	CreatorLabel *string
	// CreatorLabelIsEmail: the label is the member's e-mail (they have no
	// name). The API hides it from callers that act with an API key.
	CreatorLabelIsEmail bool
	// Replaced: a retry of this refund was already started (its
	// "retry-<id>" operation exists), so retrying it again is not offered.
	Replaced bool
}

// replacedColumn tells whether a retry operation of the refund exists.
const replacedColumn = `EXISTS (SELECT 1 FROM refund_batches rb WHERE rb.org_id = r.org_id AND rb.idempotency_key = 'retry-' || r.id::text)`

// creatorJoins and creatorColumns resolve who asked for a refund from
// requested_by: an API key of THIS organization (its name), or a member of it
// (their name, else e-mail). A user outside the organization is never named.
const (
	creatorJoins = `
			LEFT JOIN api_keys ak ON ak.id::text = r.requested_by AND ak.org_id = r.org_id
			LEFT JOIN users cu ON cu.id::text = r.requested_by
			     AND EXISTS (SELECT 1 FROM memberships m WHERE m.user_id = cu.id AND m.org_id = r.org_id)`
	creatorColumns = `CASE WHEN ak.id IS NOT NULL THEN 'api_key' WHEN cu.id IS NOT NULL THEN 'user'
			            WHEN r.requested_by = 'provider_dashboard' THEN 'provider_dashboard' END,
			       COALESCE(ak.name, NULLIF(btrim(concat_ws(' ', cu.first_name, cu.last_name)), ''), cu.email),
			       (ak.id IS NULL AND cu.id IS NOT NULL AND NULLIF(btrim(concat_ws(' ', cu.first_name, cu.last_name)), '') IS NULL)`
)

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
		from := ` FROM refunds r
			LEFT JOIN orders o ON o.id = r.order_id AND o.org_id = r.org_id
			LEFT JOIN events ev ON ev.id = o.event_id
			LEFT JOIN tickets t ON t.id = COALESCE(r.ticket_id, r.cancelled_ticket_id)`
		where := `
			WHERE r.org_id = $1
			  AND ($2::text = '' OR r.state = $2)
			  AND ($3::uuid IS NULL OR o.event_id = $3)`
		if err := tx.QueryRow(ctx, `SELECT count(*)`+from+where, orgID, f.State, f.EventID).Scan(&page.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT o.system_id, ev.name, t.system_ticket_id, `+creatorColumns+`, `+replacedColumn+`, `+prefixedColumns("r")+from+creatorJoins+where+`
			ORDER BY r.created_at DESC, r.id DESC LIMIT $4 OFFSET $5`,
			orgID, f.State, f.EventID, f.Limit, f.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it ListItem
			r, err := scanRefundWith(rows, &it.OrderSystemID, &it.EventName, &it.TicketNumber, &it.CreatorKind, &it.CreatorLabel, &it.CreatorLabelIsEmail, &it.Replaced)
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

// prefixedColumns is refundColumns with every column qualified by alias,
// derived from refundColumns itself so the two never drift apart.
func prefixedColumns(alias string) string {
	cols := strings.Split(refundColumns, ",")
	for i, c := range cols {
		cols[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
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
	// History is the LATEST steps (at most historyLimit), oldest first;
	// HistoryTotal counts all of them.
	History      []HistoryEntry
	HistoryTotal int
}

// historyLimit bounds the steps a detail answer carries.
const historyLimit = 100

// Get returns one refund of the organization with its audit history. A refund
// of another organization is CodeRefundNotFound.
func (e *Engine) Get(ctx context.Context, orgID, refundID uuid.UUID) (Detail, error) {
	var d Detail
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT o.system_id, ev.name, t.system_ticket_id, `+creatorColumns+`, `+replacedColumn+`, `+prefixedColumns("r")+`
			FROM refunds r
			LEFT JOIN orders o ON o.id = r.order_id AND o.org_id = r.org_id
			LEFT JOIN events ev ON ev.id = o.event_id
			LEFT JOIN tickets t ON t.id = COALESCE(r.ticket_id, r.cancelled_ticket_id)`+creatorJoins+`
			WHERE r.id = $1 AND r.org_id = $2`, refundID, orgID)
		r, err := scanRefundWith(row, &d.OrderSystemID, &d.EventName, &d.TicketNumber, &d.CreatorKind, &d.CreatorLabel, &d.CreatorLabelIsEmail, &d.Replaced)
		if errors.Is(err, pgx.ErrNoRows) {
			return refusal(http.StatusNotFound, CodeRefundNotFound, "refund not found", nil)
		}
		if err != nil {
			return err
		}
		d.Refund = r
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit_events
			WHERE resource_type = 'refund' AND resource_id = $1`, refundID.String()).Scan(&d.HistoryTotal); err != nil {
			return err
		}
		// The LATEST steps: a long-lived refund keeps the end of its story,
		// which is what an operator needs, not the first hundred.
		rows, err := tx.Query(ctx, `SELECT action, occurred_at, metadata FROM (
				SELECT action, occurred_at, metadata, id FROM audit_events
				WHERE resource_type = 'refund' AND resource_id = $1 ORDER BY occurred_at DESC, id DESC LIMIT $2) x
			ORDER BY occurred_at, id`, refundID.String(), historyLimit)
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
