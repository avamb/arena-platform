package htickets

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// seatOriginReader is the one query SeatReleaseTargetTx needs; *gen.Queries
// implements it.
type seatOriginReader interface {
	GetTicketSeatOrigin(ctx context.Context, ticketID uuid.UUID) (*string, error)
}

// SeatReleaseTargetTx answers the status a cancelled ticket's place
// returns to (spec 46, owner decision R-2): 'unavailable' when a staff
// invitation took it from the organizer's blocked places
// (order_items.seat_origin), 'available' for everything else — a purchase
// (NULL origin), an invitation from free stock and a legacy ticket with no
// order line. Run it on the cancellation's own transaction.
func SeatReleaseTargetTx(ctx context.Context, q seatOriginReader, ticketID uuid.UUID) (string, error) {
	origin, err := q.GetTicketSeatOrigin(ctx, ticketID)
	return seatReleaseTarget(origin, err)
}

// seatReleaseTarget maps the origin lookup to a release target. A missing
// order line (pgx.ErrNoRows) is the legacy shape and means 'available'; any
// other error is returned so the caller aborts its transaction rather than
// guess.
func seatReleaseTarget(origin *string, err error) (string, error) {
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.SeatReleaseAvailable, nil
		}
		return "", err
	}
	if origin != nil && *origin == gen.SeatReleaseUnavailable {
		return gen.SeatReleaseUnavailable, nil
	}
	return gen.SeatReleaseAvailable, nil
}
