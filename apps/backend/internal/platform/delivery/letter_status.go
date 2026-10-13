// letter_status.go — every buyer letter the delivery jobs send is recorded
// in email_letters (internal/platform/emailstatus) so the organizer can see
// whether it was delivered, opened or bounced.
//
// The letter row is written BEFORE the relay sees the message (its id goes
// into the X-Mailin-custom header, which Brevo echoes on every webhook
// event), marked sent with the relay's message id once accepted, and deleted
// when the relay refuses. Recording is best effort: a database hiccup never
// stops a letter from going out, it only leaves that letter without a status.
package delivery

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/emailstatus"
)

// letterRecordTimeout bounds each bookkeeping statement around a send.
const letterRecordTimeout = 5 * time.Second

// sendRecorded sends msg through sender and records it as letter l. db may
// be nil (no recording). jobIDs are the delivery_jobs rows the letter
// carries; they are linked to it once the relay accepted it.
func sendRecorded(ctx context.Context, sender email.Sender, db emailstatus.DB, logger *slog.Logger,
	l emailstatus.NewLetter, msg email.Message, jobIDs []uuid.UUID) error {
	var letterID uuid.UUID
	if db != nil && l.OrgID != uuid.Nil && l.Recipient != "" {
		bctx, cancel := context.WithTimeout(ctx, letterRecordTimeout)
		id, err := emailstatus.Begin(bctx, db, l)
		cancel()
		if err != nil {
			logger.Warn("delivery: could not record the letter; sending without a delivery status",
				slog.String("kind", l.Kind), slog.String("error", err.Error()))
		} else {
			letterID = id
			msg.TrackingID = emailstatus.TrackingID(id)
		}
	}

	receipt, err := email.SendWithReceipt(ctx, sender, msg)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), letterRecordTimeout)
	defer cancel()
	if err != nil {
		if letterID != uuid.Nil {
			if aerr := emailstatus.Abandon(rctx, db, letterID); aerr != nil {
				logger.Warn("delivery: could not drop the unsent letter", slog.String("error", aerr.Error()))
			}
		}
		return err
	}
	if letterID != uuid.Nil {
		if aerr := emailstatus.Accepted(rctx, db, letterID, receipt.MessageID, jobIDs); aerr != nil {
			// The letter is out; only its status bookkeeping is lost.
			logger.Warn("delivery: could not mark the letter sent",
				slog.String("letter_id", letterID.String()), slog.String("error", aerr.Error()))
		}
	}
	return nil
}

// ticketLetterContext finds the organization and the order of a ticket for
// its letter. An invitation has no order (nil).
func ticketLetterContext(ctx context.Context, db emailstatus.DB, ticketID uuid.UUID) (uuid.UUID, *uuid.UUID, error) {
	var orgID, orderID *uuid.UUID
	err := db.QueryRow(ctx, `SELECT COALESCE(cs.org_id, ci.org_id, e.org_id),
	       (SELECT oi.order_id FROM order_items oi WHERE oi.ticket_id = t.id LIMIT 1)
	FROM   tickets t
	LEFT JOIN checkout_sessions cs       ON cs.id = t.checkout_session_id
	LEFT JOIN complimentary_issuances ci ON ci.id = t.complimentary_issuance_id
	LEFT JOIN sessions s                 ON s.id = t.session_id
	LEFT JOIN events e                   ON e.id = s.event_id
	WHERE  t.id = $1`, ticketID).Scan(&orgID, &orderID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && orgID == nil) {
		return uuid.Nil, nil, nil
	}
	if err != nil {
		return uuid.Nil, nil, err
	}
	return *orgID, orderID, nil
}
