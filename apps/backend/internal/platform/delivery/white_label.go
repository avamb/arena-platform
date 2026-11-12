package delivery

// white_label.go - buyer letters of a brand with white label (spec
// 08_architecture/53): no "ArenaSoldOut.com" line (templates.Data.WhiteLabel)
// and the brand's name on the sender instead of the platform's. Every lookup
// is best effort: a failure logs and the letter goes out naming the
// platform, never fails.

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/whitelabel"
)

// whiteLabelResolver builds a resolver over db; nil when there is no db.
func whiteLabelResolver(db gen.DBTX) *whitelabel.Resolver {
	if db == nil {
		return nil
	}
	return whitelabel.New(gen.New(db))
}

// whiteLabelQueries is whiteLabelResolver for a *gen.Queries that may be nil.
func whiteLabelQueries(q *gen.Queries) *whitelabel.Resolver {
	if q == nil {
		return nil
	}
	return whitelabel.New(q)
}

// whiteLabelForTicket reports whether the brand of the ticket's event has
// white label today.
func whiteLabelForTicket(ctx context.Context, r *whitelabel.Resolver, logger *slog.Logger, ticketID string) bool {
	id, err := uuid.Parse(strings.TrimSpace(ticketID))
	if err != nil {
		return false
	}
	on, err := r.Ticket(ctx, id)
	return whiteLabelResult(logger, "ticket_id", ticketID, on, err)
}

// whiteLabelForEvent reports whether the event's brand has white label today.
func whiteLabelForEvent(ctx context.Context, r *whitelabel.Resolver, logger *slog.Logger, eventID uuid.UUID) bool {
	on, err := r.Event(ctx, eventID)
	return whiteLabelResult(logger, "event_id", eventID.String(), on, err)
}

// whiteLabelForOrg reports whether the organization itself has white label
// today (its shop letters).
func whiteLabelForOrg(ctx context.Context, r *whitelabel.Resolver, logger *slog.Logger, orgID uuid.UUID) bool {
	on, err := r.Org(ctx, orgID)
	return whiteLabelResult(logger, "org_id", orgID.String(), on, err)
}

func whiteLabelResult(logger *slog.Logger, key, id string, on bool, err error) bool {
	if err != nil {
		if logger != nil {
			logger.Warn("delivery: white label lookup failed; the letter names the platform",
				slog.String(key, id), slog.String("error", err.Error()))
		}
		return false
	}
	return on
}

// brandSenderName is the name a white label letter is sent under: the
// event's promoter, else the organization.
func brandSenderName(p Payload) string {
	if n := strings.TrimSpace(p.PromoterName); n != "" {
		return n
	}
	return strings.TrimSpace(p.OrgName)
}

// applyWhiteLabelSender names the sender of msg after the brand when the
// brand has white label; otherwise msg is left as it is.
func applyWhiteLabelSender(msg *email.Message, whiteLabel bool, name string) {
	if whiteLabel && strings.TrimSpace(name) != "" {
		msg.FromName = name
	}
}

// whiteLabelForSession reports whether the brand of the session's event has
// white label today, and the brand's name (the promoter, else the
// organization) for the sender.
func whiteLabelForSession(ctx context.Context, db gen.DBTX, logger *slog.Logger, sessionID uuid.UUID) (bool, string) {
	if db == nil {
		return false, ""
	}
	var eventID uuid.UUID
	var brand string
	err := db.QueryRow(ctx, `
		SELECT e.id, COALESCE(p.name, o.name)
		FROM   sessions s
		JOIN   events e        ON e.id = s.event_id
		JOIN   organizations o ON o.id = e.org_id
		LEFT   JOIN event_promoters ep ON ep.event_id = e.id
		LEFT   JOIN org_promoters   p  ON p.id = ep.promoter_id
		WHERE  s.id = $1`, sessionID).Scan(&eventID, &brand)
	if err != nil {
		return whiteLabelResult(logger, "session_id", sessionID.String(), false, err), ""
	}
	return whiteLabelForEvent(ctx, whiteLabelResolver(db), logger, eventID), brand
}
