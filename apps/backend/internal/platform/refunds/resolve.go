// resolve.go — an operator closes a refund parked in manual_review after
// checking the provider's dashboard (spec 36 §8). It lives in the engine
// because nothing outside it may set an engine refund to failed or
// succeeded: the operator states what they SAW, and the engine applies the
// matching transition under the payment's lock.
package refunds

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// Resolution outcomes: what the operator found at the provider.
const (
	ResolveRefunded    = "refunded"
	ResolveNotRefunded = "not_refunded"
)

// CodeNotInReview: only a refund in manual_review can be resolved.
const CodeNotInReview = "refund.not_in_review"

// ResolveInput is one manual closure.
type ResolveInput struct {
	OrgID    uuid.UUID
	RefundID uuid.UUID
	Outcome  string
	Note     string
	Actor    Actor
	Via      string
}

const maxResolveNote = 1000

// Resolve closes a manual_review refund. refunded: the money went back at
// the provider, so the refund becomes succeeded and its tickets are cancelled
// as for any accepted refund. not_refunded: no money moved, the refund
// becomes failed and its tickets can be refunded again.
func (e *Engine) Resolve(ctx context.Context, in ResolveInput) (Refund, error) {
	bad := func(msg string) error { return refusal(http.StatusBadRequest, CodeInvalidRequest, msg, nil) }
	note := strings.TrimSpace(in.Note)
	switch {
	case in.Outcome != ResolveRefunded && in.Outcome != ResolveNotRefunded:
		return Refund{}, bad("outcome must be refunded or not_refunded")
	case note == "":
		return Refund{}, bad("a note saying what was checked is required")
	case len([]rune(note)) > maxResolveNote:
		return Refund{}, bad("note is too long")
	}
	var (
		out Refund
		pay Payment
	)
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var piID *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT payment_intent_id FROM refunds WHERE id = $1 AND org_id = $2`,
			in.RefundID, in.OrgID).Scan(&piID)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && piID == nil) {
			return refusal(http.StatusNotFound, CodeRefundNotFound, "refund not found", nil)
		}
		if err != nil {
			return err
		}
		if err := LockPayment(ctx, tx, *piID); err != nil {
			return err
		}
		cur, err := getRefund(ctx, tx, in.RefundID, true)
		if err != nil {
			return err
		}
		if cur.Provider == nil || cur.State != StateManualReview {
			return refusal(http.StatusConflict, CodeNotInReview, "only a refund waiting for manual review can be resolved",
				map[string]any{"state": cur.State})
		}
		if pay, err = getPayment(ctx, tx, *piID); err != nil {
			return err
		}
		state, code := StateFailed, "resolved_not_refunded"
		if in.Outcome == ResolveRefunded {
			state, code = StateSucceeded, "resolved_refunded"
		}
		out, err = scanRefund(tx.QueryRow(ctx, `UPDATE refunds SET state = $2, failure_code = $3, failure_reason = $4,
			alert_due_at = NULL, updated_at = now()
			WHERE id = $1 AND state = 'manual_review' RETURNING `+refundColumns, in.RefundID, state, code, note))
		if err != nil {
			return err
		}
		return e.writeAudit(ctx, tx, audit.Event{ActorType: in.Actor.Type, ActorID: in.Actor.ID,
			Action: "v1.refund.resolve", ResourceType: "refund", ResourceID: out.ID.String(),
			Metadata: map[string]any{"outcome": in.Outcome, "state": state, "via": in.Via}})
	})
	if err != nil {
		return Refund{}, err
	}
	if out.State == StateSucceeded {
		e.settle(ctx, out, pay)
	}
	return out, nil
}
