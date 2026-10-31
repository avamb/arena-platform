package htickets

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ErrInvitationSeatOriginMissing aborts the cancellation of a staff
// invitation's ticket whose order line carries no usable seat_origin. An
// invitation may have taken the place from the organizer's reserve, so
// guessing 'available' could put a reserve seat on sale (spec 46, R-2).
var ErrInvitationSeatOriginMissing = errors.New("htickets: staff invitation ticket has no seat origin")

// seatOriginReader is the one query SeatReleaseTargetTx needs; *gen.Queries
// implements it.
type seatOriginReader interface {
	GetTicketSeatOrigin(ctx context.Context, ticketID uuid.UUID) (gen.TicketSeatOrigin, error)
}

// SeatReleaseTargetTx answers the status a cancelled ticket's place
// returns to (spec 46, owner decision R-2): 'unavailable' when a staff
// invitation took it from the organizer's blocked places
// (order_items.seat_origin), 'available' for a purchase (NULL origin), an
// invitation from free stock and a legacy ticket with no order line. A
// staff invitation's ticket without a valid origin is an error, never
// 'available'. Run it on the cancellation's own transaction.
func SeatReleaseTargetTx(ctx context.Context, q seatOriginReader, ticketID uuid.UUID) (string, error) {
	row, err := q.GetTicketSeatOrigin(ctx, ticketID)
	return seatReleaseTarget(row, err, ticketID)
}

// seatReleaseTarget maps the origin lookup to a release target. A missing
// ticket row (pgx.ErrNoRows) is the legacy shape and means 'available'; any
// other error is returned so the caller aborts its transaction rather than
// guess.
func seatReleaseTarget(row gen.TicketSeatOrigin, err error, ticketID uuid.UUID) (string, error) {
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.SeatReleaseAvailable, nil
		}
		return "", err
	}
	origin := ""
	if row.SeatOrigin != nil {
		origin = *row.SeatOrigin
	}
	if row.StaffInvitation {
		if origin != gen.SeatReleaseAvailable && origin != gen.SeatReleaseUnavailable {
			return "", fmt.Errorf("%w: ticket %s", ErrInvitationSeatOriginMissing, ticketID)
		}
		return origin, nil
	}
	if origin == gen.SeatReleaseUnavailable {
		return gen.SeatReleaseUnavailable, nil
	}
	return gen.SeatReleaseAvailable, nil
}
