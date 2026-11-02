package hcheckout

import (
	"context"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ExpireOneReservation runs the TTL sweep's per-reservation step for one
// reservation, so a test on the shared database expires only its own hold
// instead of every expired hold ProcessExpiredReservations would poll.
func (p *ReservationProcessor) ExpireOneReservation(ctx context.Context, r gen.ReservationRow) error {
	return p.expireReservation(ctx, r)
}
