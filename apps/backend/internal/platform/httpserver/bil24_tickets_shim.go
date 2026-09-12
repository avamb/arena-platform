// bil24_tickets_shim.go carries the ticket-delivery slice of the *Server →
// hbil24 wiring: GET_TICKETS_BY_ORDER and SEND_TICKETS_TO_EMAIL (feature #495,
// W1-B2b, spec §7.10/§7.11).
//
// It lives beside bil24_shims.go rather than inside it purely to respect the
// feature #175 per-file budget for internal/platform/httpserver/ (≤400 lines);
// the split is along a natural seam — every dependency here is
// ticket-delivery-specific and nothing else in bil24_shims.go references it.
package httpserver

import (
	"context"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hbil24"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/orderexport"
)

// withBil24Tickets attaches the GET_TICKETS_BY_ORDER / SEND_TICKETS_TO_EMAIL
// dependencies to h, returning h unchanged when the server has no database.
//
// Q rides the PoolDB interface (order/ticket lookups and the delivery_jobs
// enqueue); the ticket rows themselves come from the same neutral projection
// GET_ORDER_INFO uses, which speaks raw pgx and so needs pgxPool. With either
// handle missing, both commands self-gate with resultCode -5 rather than
// panicking on a nil dependency.
func (s *Server) withBil24Tickets(h *hbil24.Handler) *hbil24.Handler {
	if s.pool == nil || s.pgxPool == nil {
		return h
	}
	pool := s.pgxPool
	return h.WithTicketsByOrder(hbil24.TicketsDeps{
		Q: gen.New(s.pool),
		Project: func(ctx context.Context, csID uuid.UUID) (*orderexport.Order, error) {
			return orderexport.QueryCheckoutSession(ctx, pool, csID)
		},
		// The spec's PUBLIC_BASE_URL — the API origin (API_PUBLIC_URL, feature
		// #535), NOT the SPA origin: the buyer opens these PDF links against
		// the API host. config.Validate makes it mandatory in production
		// whenever BIL24_COMPAT_ENABLED is on, so an empty value here can only
		// be a dev deployment.
		PublicBaseURL: s.apiPublicURL(),
	})
}
