package venueplan

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Notifier is the seam of the notifications of an intake (spec 64 sections
// 3.2 and 4.4): the platform administrators hear of a new intake, of an
// intake that went to them, and of a plan an organizer approved. The intake
// routes call it INSIDE the transaction that made the change, so an
// implementation enqueues its work (a worker job, a notify-log row) on tx and
// the message exists exactly when the change commits. A returned error fails
// the request and rolls the change back; an implementation that wants a
// best-effort write puts it behind its own SAVEPOINT (tx.Begin).
//
// Wave 1 wires NopNotifier; the notification stage implements it.
type Notifier interface {
	// IntakeCreated: an organization sent a new intake.
	IntakeCreated(ctx context.Context, tx pgx.Tx, intakeID uuid.UUID) error
	// IntakeEscalated: the intake went to an administrator, for reason.
	IntakeEscalated(ctx context.Context, tx pgx.Tx, intakeID uuid.UUID, reason EscalationReason) error
	// PlanApproved: a draft became a seating plan that waits for verification.
	PlanApproved(ctx context.Context, tx pgx.Tx, intakeID uuid.UUID) error
}

// NopNotifier tells nobody.
type NopNotifier struct{}

// IntakeCreated does nothing.
func (NopNotifier) IntakeCreated(context.Context, pgx.Tx, uuid.UUID) error { return nil }

// IntakeEscalated does nothing.
func (NopNotifier) IntakeEscalated(context.Context, pgx.Tx, uuid.UUID, EscalationReason) error {
	return nil
}

// PlanApproved does nothing.
func (NopNotifier) PlanApproved(context.Context, pgx.Tx, uuid.UUID) error { return nil }
