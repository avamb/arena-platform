package delivery

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The accent colour of a ticket PDF and letter comes from the selling
// organization's theme (organizations.branding.primary, migration 0203, spec
// 62 P-23). Only a "#rrggbb" value is used: the PDF needs a hex colour, and
// an rgba() value in a letter's band would look different in every mail
// client. Anything else keeps the platform accent.

var accentHex = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// hexAccent answers v lower-cased when it is "#rrggbb", "" otherwise.
func hexAccent(v string) string {
	v = strings.TrimSpace(v)
	if !accentHex.MatchString(v) {
		return ""
	}
	return strings.ToLower(v)
}

// accentQuerier is the optional read resolveAccent needs; *gen.Queries
// satisfies it, the presentation fakes of the unit tests do not (they keep
// the platform accent).
type accentQuerier interface {
	GetOrganizationAccentForTicket(ctx context.Context, ticketID uuid.UUID) (string, error)
}

// resolveAccent fills p.OrgAccentColor from the ticket's organization when
// the enqueuer left it empty. Best effort: a failed read logs and keeps the
// platform accent, it never blocks a ticket.
func resolveAccent(ctx context.Context, q any, ticketID uuid.UUID, p *Payload, logger *slog.Logger) {
	if p == nil || strings.TrimSpace(p.OrgAccentColor) != "" {
		return
	}
	aq, ok := q.(accentQuerier)
	if !ok || aq == nil {
		return
	}
	v, err := aq.GetOrganizationAccentForTicket(ctx, ticketID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) && logger != nil {
			logger.Warn("delivery: organization accent lookup failed; using the platform accent",
				slog.String("ticket_id", ticketID.String()),
				slog.String("error", err.Error()),
			)
		}
		return
	}
	p.OrgAccentColor = hexAccent(v)
}
