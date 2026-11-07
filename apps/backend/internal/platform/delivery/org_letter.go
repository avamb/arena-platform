package delivery

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

// orgLetterStyle is what a letter the organization sends to a buyer outside
// an order (the marketing confirmation, the cart reminder, the late-payment
// letter) takes from the organization: the sender, the name, the accent and
// the logo — the same four things the ticket letters and the contact-form
// letter already take, so every letter of one organization looks and
// arrives alike (owner decision 2026-11-07: all buyer letters leave from the
// organization's own domain).
type orgLetterStyle struct {
	Name         string
	SenderEmail  string
	SenderStatus string
	Accent       string
	LogoID       *uuid.UUID
}

// orgStyleDB is the one read loadOrgLetterStyle needs.
type orgStyleDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadOrgLetterStyle reads the organization's letter style. pgx.ErrNoRows is
// returned as is for an organization that is gone.
func loadOrgLetterStyle(ctx context.Context, db orgStyleDB, orgID uuid.UUID) (orgLetterStyle, error) {
	var s orgLetterStyle
	err := db.QueryRow(ctx, `
		SELECT name, COALESCE(sender_email, ''), COALESCE(sender_verification_status, ''),
		       COALESCE(branding->>'primary', ''), logo_media_id
		FROM   organizations
		WHERE  id = $1`, orgID).Scan(&s.Name, &s.SenderEmail, &s.SenderStatus, &s.Accent, &s.LogoID)
	return s, err
}

// branding is the letter's header: the organization's name, logo and
// accent, resolved like the ticket letter's. Without a logo (or a resolver)
// the header carries the name only.
func (s orgLetterStyle) branding(ctx context.Context, m MediaResolver, logger *slog.Logger) templates.Branding {
	return myTicketsBranding(ctx, m, s.Name, s.Accent, s.LogoID, logger)
}

// applySender makes the letter leave from the organization's own sender
// when it is verified (like the ticket letters; an unverified or missing
// sender leaves the platform default to the adapter) and names the
// organization as the sender when nothing named it yet. A white-label brand
// applied afterwards still wins the name.
func (s orgLetterStyle) applySender(msg *email.Message) {
	if s.SenderStatus == "verified" {
		if from := strings.TrimSpace(s.SenderEmail); from != "" {
			msg.From = from
		}
	}
	if strings.TrimSpace(msg.FromName) == "" {
		msg.FromName = strings.TrimSpace(s.Name)
	}
}
