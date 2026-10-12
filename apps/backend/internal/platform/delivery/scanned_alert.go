// scanned_alert.go - the refund.scanned_alert_email worker job: tell an
// organization's owners that somebody tried to refund a ticket that was
// already scanned at the gate (owner decision 2026-10-12). The engine refused
// the refund; nothing about the ticket, the order or the money changed. The
// engine queues at most one job per ticket and actor per day.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/refunds"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// ScannedAlertDB is what the handler reads.
type ScannedAlertDB interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ScannedAlertOptions are the dependencies of the job.
type ScannedAlertOptions struct {
	Sender      email.Sender
	FromAddress string
	Templates   *templates.Renderer
	DB          ScannedAlertDB
	Logger      *slog.Logger
}

// NewScannedAlertHandler builds the worker handler of refunds.JobTypeScannedAlertEmail.
func NewScannedAlertHandler(opts ScannedAlertOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	renderer := opts.Templates
	if renderer == nil {
		r, err := templates.New()
		if err != nil {
			return func(context.Context, []byte) error {
				return fmt.Errorf("delivery: scanned-alert templates unavailable: %w", err)
			}
		}
		renderer = r
	}
	return func(ctx context.Context, payload []byte) error {
		var p refunds.ScannedAlertPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil // a payload nobody can read is never going to work
		}
		orgID, err1 := uuid.Parse(p.OrgID)
		ticketID, err2 := uuid.Parse(p.TicketID)
		if err1 != nil || err2 != nil {
			return nil
		}
		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Info("delivery: refund-blocked alert skipped, no production e-mail sender configured")
			return nil
		}
		var (
			orgName, eventName string
			ticketNumber       *int64
			orderNumber        *int64
		)
		err := opts.DB.QueryRow(ctx, `SELECT org.name, ev.name, t.system_ticket_id, o.system_id
			FROM tickets t
			JOIN sessions s ON s.id = t.session_id
			JOIN events ev ON ev.id = s.event_id AND ev.org_id = $2
			JOIN organizations org ON org.id = ev.org_id
			LEFT JOIN orders o ON o.id = t.order_id
			WHERE t.id = $1`, ticketID, orgID).Scan(&orgName, &eventName, &ticketNumber, &orderNumber)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // the ticket is gone or not this organization's
		}
		if err != nil {
			return err
		}
		rows, err := opts.DB.Query(ctx, `SELECT u.email, COALESCE(u.preferred_locale, 'en')
			FROM memberships m JOIN users u ON u.id = m.user_id
			WHERE m.org_id = $1 AND m.role = 'org_admin' AND m.status = 'active' AND u.email <> ''
			ORDER BY u.email`, orgID)
		if err != nil {
			return err
		}
		type owner struct{ email, locale string }
		var owners []owner
		for rows.Next() {
			var o owner
			if err := rows.Scan(&o.email, &o.locale); err != nil {
				rows.Close()
				return err
			}
			owners = append(owners, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		data := templates.Data{
			EventName: eventName,
			Branding:  templates.Branding{OrgName: orgName},
			Alert:     templates.ScanAlertData{Actor: p.ActorLabel},
		}
		if ticketNumber != nil {
			data.Alert.TicketNumber = strconv.FormatInt(*ticketNumber, 10)
		}
		if orderNumber != nil {
			data.Alert.OrderNumber = strconv.FormatInt(*orderNumber, 10)
		}
		var firstErr error
		for _, o := range owners {
			data.RecipientEmail = o.email
			out, rerr := renderer.Render(templates.TemplateKindRefundBlocked, o.locale, data)
			if rerr != nil {
				return rerr
			}
			if serr := opts.Sender.Send(ctx, email.Message{To: o.email, Subject: out.Subject, HTMLBody: out.HTMLBody, TextBody: out.TextBody}); serr != nil {
				// One failing address must not stop the others; the job
				// retries (a repeat is a harmless duplicate notice).
				logger.Warn("delivery: refund-blocked alert not sent", slog.String("org_id", orgID.String()))
				if firstErr == nil {
					firstErr = serr
				}
			}
		}
		return firstErr
	}
}
