// Hand-maintained typed query wrappers for the verification of a seating plan
// by a platform superadmin (spec 08_architecture/64 section 3.4, migration
// 0216): the locked read of a plan the verify, needs-fix and PATCH routes
// decide on, and the venue mark that a verified plan brings with it.
package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// SeatingPlanVerificationLock is the part of a live seating plan the
// verification routes decide on, read under a row lock.
type SeatingPlanVerificationLock struct {
	ID                uuid.UUID
	VenueID           uuid.UUID
	OwnerOrgID        uuid.UUID
	PlanType          string
	Status            string
	Visibility        string
	VerificationState *string
	VerificationNote  *string
	VerifiedAt        *time.Time
	CurrentVersionID  *uuid.UUID
}

const lockSeatingPlanForVerification = `-- name: LockSeatingPlanForVerification :one
SELECT id, venue_id, owner_org_id, plan_type, status, visibility,
       verification_state, verification_note, verified_at, current_version_id
FROM   seating_plans
WHERE  id = $1
  AND  deleted_at IS NULL
FOR UPDATE`

// LockSeatingPlanForVerification reads a live plan and locks its row until the
// end of the transaction. pgx.ErrNoRows when the plan does not exist or is
// deleted.
func (q *Queries) LockSeatingPlanForVerification(ctx context.Context, id uuid.UUID) (SeatingPlanVerificationLock, error) {
	var r SeatingPlanVerificationLock
	err := q.db.QueryRow(ctx, lockSeatingPlanForVerification, id).Scan(
		&r.ID, &r.VenueID, &r.OwnerOrgID, &r.PlanType, &r.Status, &r.Visibility,
		&r.VerificationState, &r.VerificationNote, &r.VerifiedAt, &r.CurrentVersionID,
	)
	return r, err
}

const markVenueVerifiedIfNot = `-- name: MarkVenueVerifiedIfNot :execrows
UPDATE venues
SET    review_status = 'verified',
       verified_at   = now(),
       updated_at    = now()
WHERE  id = $1
  AND  deleted_at IS NULL
  AND  review_status <> 'verified'`

// MarkVenueVerifiedIfNot verifies a live venue that is not verified yet
// (migration 0182) and reports whether it changed (0: already verified,
// deleted or missing). A venue that is already verified keeps its verified_at.
func (q *Queries) MarkVenueVerifiedIfNot(ctx context.Context, venueID uuid.UUID) (int64, error) {
	tag, err := q.db.Exec(ctx, markVenueVerifiedIfNot, venueID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
