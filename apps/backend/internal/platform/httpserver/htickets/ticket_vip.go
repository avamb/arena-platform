package htickets

// ticket_vip.go — helpers for the organizer's VIP mark of a ticket (task V1,
// migration 0164). The mark is private: it never reaches the PDF e-ticket or
// a letter to the buyer, so nothing here touches GetTicketPresentationByID.

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// vipActorID is the principal that makes the change: a user id or an
// organization API key id (both bare UUIDs), nil when the request carries
// none.
func vipActorID(r *http.Request) *uuid.UUID {
	actor, ok := auth.ActorFromContext(r.Context())
	if !ok {
		return nil
	}
	id, err := uuid.Parse(actor.ID)
	if err != nil {
		return nil
	}
	return &id
}

// complimentaryTickets shapes the issued tickets for an answer and adds the
// `vip` flag of each, read in one extra query. A failed lookup logs and
// answers every ticket as not VIP rather than failing a read that has
// already succeeded.
func (h *Handler) complimentaryTickets(ctx context.Context, rows []gen.ComplimentaryTicketRow) []map[string]any {
	out := complimentaryTicketsFromRows(rows)
	if len(rows) == 0 {
		return out
	}
	ids := make([]uuid.UUID, len(rows))
	for i, t := range rows {
		ids[i] = t.ID
	}
	vip, err := h.complimentaryQueries.ListTicketVIPByIDs(ctx, ids)
	if err != nil {
		h.logger.Warn("complimentary: vip lookup failed", "error", err.Error())
		vip = map[uuid.UUID]bool{}
	}
	for i, t := range rows {
		out[i]["vip"] = vip[t.ID]
	}
	return out
}
