// sales_alert.go - the sales_state.alert_email worker job (package
// salesalert, owner decisions 2026-10-31): tell an organization that a session
// sold out (the owners AND the managers) or that the WordPress sites did not
// take a sales state change in time (the owners, only when no sales chat of
// the organization took the message). The watch queues one job per alert.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesalert"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// SalesAlertOptions are the dependencies of the job.
type SalesAlertOptions struct {
	Sender    email.Sender
	Templates *templates.Renderer
	DB        ScannedAlertDB
	Logger    *slog.Logger
}

// NewSalesAlertHandler builds the worker handler of salesalert.JobTypeAlertEmail.
func NewSalesAlertHandler(opts SalesAlertOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: sales alert templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	return func(ctx context.Context, payload []byte) error {
		var p salesalert.AlertEmailPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil // a payload nobody can read is never going to work
		}
		orgID, err1 := uuid.Parse(p.OrgID)
		sessionID, err2 := uuid.Parse(p.SessionID)
		if err1 != nil || err2 != nil || (p.Kind != salesalert.KindSoldOut && p.Kind != salesalert.KindSiteStale) {
			return nil
		}
		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Info("delivery: sales alert skipped, no production e-mail sender configured")
			return nil
		}
		in, err := salesalert.LoadSessionInfo(ctx, opts.DB, orgID, sessionID)
		if errors.Is(err, salesalert.ErrSessionGone) {
			return nil
		}
		if err != nil {
			return err
		}
		recipients, err := salesAlertRecipients(ctx, opts.DB, orgID, p.Kind == salesalert.KindSoldOut)
		if err != nil {
			return err
		}
		if len(recipients) == 0 {
			logger.Error("delivery: sales alert has no recipient in the organization", slog.String("org_id", orgID.String()))
			return nil
		}
		data := templates.Data{
			EventName: in.EventName,
			Branding:  templates.Branding{OrgName: in.OrgName},
			SalesAlert: templates.SalesAlertData{
				Kind: p.Kind, Manual: p.Manual, Actor: p.Actor, When: in.When(), Venue: in.Venue,
				Total: in.TotalPlaces, Free: in.FreePlaces, State: p.State,
			},
		}
		sent := 0
		var firstErr error
		for _, rc := range recipients {
			data.RecipientEmail = rc.email
			out, rerr := renderer.Render(templates.TemplateKindSalesAlert, rc.locale, data)
			if rerr != nil {
				return rerr
			}
			if serr := opts.Sender.Send(ctx, email.Message{To: rc.email, Subject: out.Subject, HTMLBody: out.HTMLBody, TextBody: out.TextBody}); serr != nil {
				logger.Warn("delivery: sales alert not sent", slog.String("org_id", orgID.String()), slog.String("kind", p.Kind))
				if firstErr == nil {
					firstErr = serr
				}
				continue
			}
			sent++
		}
		// A retry would mail again everybody who already has it: retry only
		// when nobody got the alert.
		if sent == 0 {
			return firstErr
		}
		return nil
	}
}

type salesAlertRecipient struct{ email, locale string }

// salesAlertRecipients are the organization's owners, with its managers when
// withManagers; without any member who has an address, its contact address.
func salesAlertRecipients(ctx context.Context, db ScannedAlertDB, orgID uuid.UUID, withManagers bool) ([]salesAlertRecipient, error) {
	roles := []string{"org_admin"}
	if withManagers {
		roles = append(roles, "organizer")
	}
	rows, err := db.Query(ctx, `SELECT DISTINCT u.email, COALESCE(u.preferred_locale, 'en')
		FROM memberships m JOIN users u ON u.id = m.user_id
		WHERE m.org_id = $1 AND m.role = ANY($2::text[]) AND m.status = 'active' AND u.email <> ''
		ORDER BY u.email`, orgID, roles)
	if err != nil {
		return nil, err
	}
	var out []salesAlertRecipient
	for rows.Next() {
		var r salesAlertRecipient
		if err := rows.Scan(&r.email, &r.locale); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		return out, nil
	}
	var contact, locale string
	if err := db.QueryRow(ctx, `SELECT btrim(COALESCE(contact_email, '')), default_locale FROM organizations WHERE id = $1`, orgID).Scan(&contact, &locale); err != nil {
		return nil, nil //nolint:nilerr // no organization row: nobody to tell
	}
	if contact == "" {
		return nil, nil
	}
	return []salesAlertRecipient{{email: contact, locale: locale}}, nil
}
