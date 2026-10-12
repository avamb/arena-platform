// scan_exception.go - a platform superadmin's one-time permission to refund
// a ticket that was already scanned at the gate (owner decision 2026-10-13):
// a show cancelled half-way, a scan made by mistake. One per ticket, valid
// for at most 24 hours, spent by the first refund that uses it. Granting it
// is audited, the organization's owners get an e-mail and the sales bot
// tells the organization's group and the operator.
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
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
)

// Codes of the grant route.
const (
	CodeTicketNotScanned       = "refund.ticket_not_scanned"
	CodeScanExceptionExists    = "refund.scan_exception_exists"
	CodeScanExceptionTicketNot = "ticket.not_found"
)

// EventScanExceptionGranted is the outbox event the sales bot announces.
const EventScanExceptionGranted = "v1.refund.scan_exception_granted"

// MaxScanExceptionHours bounds an exception's life (the CHECK of migration
// 0145 says the same).
const MaxScanExceptionHours = 24

// ScanExceptionInput is one grant.
type ScanExceptionInput struct {
	OrgID    uuid.UUID
	TicketID uuid.UUID
	Actor    Actor
	// Reason is the X-Admin-Reason of the request; Note an optional comment.
	Reason, Note string
	// Hours the exception lives, 1..24 (0 = 24).
	Hours int
	Via   string
}

// ScanException is a granted exception.
type ScanException struct {
	ID        uuid.UUID `json:"id"`
	TicketID  uuid.UUID `json:"ticket_id"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GrantScanException records the exception for a scanned ticket of the
// organization and, in the same transaction, the audit row, the owners'
// e-mail job and the sales-bot event. A ticket of another organization is
// the route's 404; a ticket that was not scanned, or one that already had an
// exception, is 409.
func (e *Engine) GrantScanException(ctx context.Context, in ScanExceptionInput) (ScanException, error) {
	in.Reason = strings.TrimSpace(in.Reason)
	in.Note = strings.TrimSpace(in.Note)
	if in.Hours == 0 {
		in.Hours = MaxScanExceptionHours
	}
	if in.Hours < 1 || in.Hours > MaxScanExceptionHours || in.Reason == "" || len([]rune(in.Note)) > 1000 {
		return ScanException{}, refusal(http.StatusBadRequest, CodeInvalidRequest,
			"hours must be 1 to 24 and a reason is required; a note is at most 1000 characters", nil)
	}
	var out ScanException
	err := e.inTx(ctx, func(tx pgx.Tx) error {
		var scanned bool
		err := tx.QueryRow(ctx, `SELECT `+ticketScannedSQL+` FROM tickets t
			JOIN sessions s ON s.id = t.session_id
			JOIN events ev ON ev.id = s.event_id
			WHERE t.id = $1 AND ev.org_id = $2`, in.TicketID, in.OrgID).Scan(&scanned)
		if errors.Is(err, pgx.ErrNoRows) {
			return refusal(http.StatusNotFound, CodeScanExceptionTicketNot, "ticket not found", nil)
		}
		if err != nil {
			return err
		}
		if !scanned {
			return refusal(http.StatusConflict, CodeTicketNotScanned, "the ticket was not scanned at the gate; it needs no exception", nil)
		}
		err = tx.QueryRow(ctx, `INSERT INTO refund_scan_exceptions (org_id, ticket_id, granted_by, reason, note, expires_at)
			VALUES ($1, $2, $3, $4, NULLIF($5, ''), now() + make_interval(hours => $6))
			RETURNING id, ticket_id, created_at, expires_at`,
			in.OrgID, in.TicketID, actorKey(in.Actor), in.Reason, in.Note, in.Hours).
			Scan(&out.ID, &out.TicketID, &out.CreatedAt, &out.ExpiresAt)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return refusal(http.StatusConflict, CodeScanExceptionExists, "this ticket already had its one exception", nil)
		}
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(ScannedAlertPayload{OrgID: in.OrgID.String(), TicketID: in.TicketID.String(),
			NoticeID: uuid.NewString(), Kind: ScannedAlertKindException,
			Reason: in.Reason, Until: out.ExpiresAt.UTC().Format(time.RFC3339)})
		if _, err := tx.Exec(ctx, `INSERT INTO worker_jobs (job_type, payload, max_attempts, status, scheduled_at)
			VALUES ($1, $2::jsonb, 5, 'pending', now())`, JobTypeScannedAlertEmail, payload); err != nil {
			return err
		}
		evPayload, _ := json.Marshal(map[string]any{"ticket_id": in.TicketID.String(), "org_id": in.OrgID.String(),
			"reason": in.Reason, "expires_at": out.ExpiresAt.UTC().Format(time.RFC3339)})
		if _, err := tx.Exec(ctx, `INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload)
			VALUES ('ticket', $1, $2, $3::jsonb)`, in.TicketID.String(), EventScanExceptionGranted, evPayload); err != nil {
			return err
		}
		return e.writeAudit(ctx, tx, audit.Event{ActorType: in.Actor.Type, ActorID: in.Actor.ID,
			Action: "v1.refund.scan_exception_grant", ResourceType: "ticket", ResourceID: in.TicketID.String(),
			Metadata: map[string]any{"org_id": in.OrgID.String(), "exception_id": out.ID.String(), "reason": in.Reason,
				"note": in.Note, "expires_at": out.ExpiresAt.UTC().Format(time.RFC3339), "via": in.Via}})
	})
	var refErr *Error
	if errors.As(err, &refErr) {
		return ScanException{}, refErr
	}
	return out, err
}
