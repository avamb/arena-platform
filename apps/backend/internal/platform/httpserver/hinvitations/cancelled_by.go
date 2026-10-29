package hinvitations

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// apiKeyName is the name of the organization API key behind a service actor,
// "" when it cannot be read (a missing name never costs the invitation).
func (h *Handler) apiKeyName(ctx context.Context, actor auth.Actor, orgID uuid.UUID) string {
	id, err := uuid.Parse(actor.ID)
	if err != nil || h.queries == nil {
		return ""
	}
	key, err := h.queries.GetAPIKeyByID(ctx, id, orgID)
	if err != nil {
		h.logger.Warn("invitation: api key name lookup failed", slog.String("error", err.Error()))
		return ""
	}
	return strings.TrimSpace(key.Name)
}

// recordCancelledBy writes who cancelled the invitation into
// orders.metadata.invitation.cancelled_by on the cancellation's own
// transaction, so the sales bot message can never read a cancelled ticket
// without it (spec 46 section 3.7). A user is recorded by id, an API key by
// name; any other actor writes nothing.
func (h *Handler) recordCancelledBy(ctx context.Context, tx pgx.Tx, orderID uuid.UUID) error {
	actor, _ := auth.ActorFromContext(ctx)
	var who map[string]string
	switch {
	case actor.Type == auth.ActorTypeService:
		// The key was authenticated by this request, so its row is readable
		// on the transaction itself (never a pool read inside an open tx).
		var name string
		id, ierr := uuid.Parse(actor.ID)
		orgID, oerr := uuid.Parse(actor.OrgID)
		if ierr == nil && oerr == nil {
			if key, kerr := h.queries.WithTx(tx).GetAPIKeyByID(ctx, id, orgID); kerr == nil {
				name = strings.TrimSpace(key.Name)
			}
		}
		who = map[string]string{"api_key": name}
	case actor.Type == auth.ActorTypeUser || actor.Type == auth.ActorTypeStubUser:
		if _, err := uuid.Parse(actor.ID); err != nil {
			return nil
		}
		who = map[string]string{"user_id": actor.ID}
	default:
		return nil
	}
	raw, err := json.Marshal(who)
	if err != nil {
		return err
	}
	return h.queries.WithTx(tx).SetInvitationCancelledBy(ctx, orderID, raw)
}
