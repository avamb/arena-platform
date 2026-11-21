// my_tickets_link.go - the letter of "my tickets by link" (package mytickets,
// spec 62 §7). The token is minted HERE, at send time, and only its hash is
// stored, so the link never sits in worker_jobs. A retry mints a new token.
//
// The link points at the organizer's own site page the buyer asked on
// (my_tickets_link_requests.page_url, already checked against the allowed
// origins when the request was taken), else at /my-tickets on the tickets
// site. The page is expected to show a button and only the button press
// exchanges the token, so a mail scanner opening the link spends nothing.
// The token rides in the fragment (#t=), so the page's analytics never see it.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mytickets"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/worker"
)

// MyTicketsLinkOptions are the dependencies of the letter.
type MyTicketsLinkOptions struct {
	Sender    email.Sender
	Templates *templates.Renderer
	DB        ScannedAlertDB
	// Media resolves the organization's logo for the header. Nil keeps the
	// organization's name as the header.
	Media MediaResolver
	// TicketsBaseURL is the fallback site when the request named no page.
	TicketsBaseURL string
	Logger         *slog.Logger
}

// MyTicketsLinkTarget is where the link points: the page the buyer asked on,
// else /my-tickets on the tickets site, "" when neither exists.
func MyTicketsLinkTarget(page, ticketsBase string) string {
	if p := strings.TrimSpace(page); p != "" {
		return p
	}
	base := strings.TrimRight(strings.TrimSpace(ticketsBase), "/")
	if base == "" {
		return ""
	}
	return base + "/my-tickets"
}

// MyTicketsLinkURL puts the token on the target in the URL FRAGMENT as t
// ("#t=<token>"), never in the query: the organizer's page runs analytics
// (GA page_location, Meta pixel, the Referer of every embedded request) that
// would carry a query token off the site, while a fragment never leaves the
// browser. The page reads the fragment first and, for the transition, falls
// back to a query t. A stray t in the target's own query is dropped.
func MyTicketsLinkURL(target, token string) (string, error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	if q := u.Query(); q.Has("t") {
		q.Del("t")
		u.RawQuery = q.Encode()
	}
	u.Fragment, u.RawFragment = "", ""
	return u.String() + "#" + url.Values{"t": {token}}.Encode(), nil
}

// NewMyTicketsLinkHandler builds the handler of mytickets.JobTypeLinkEmail.
func NewMyTicketsLinkHandler(opts MyTicketsLinkOptions) worker.HandlerFunc {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, payload []byte) error {
		var p mytickets.JobPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil
		}
		id, err := uuid.Parse(p.RequestID)
		if err != nil {
			return nil
		}
		if opts.Sender == nil || email.IsDevOnly(opts.Sender) {
			logger.Info("delivery: my-tickets link skipped, no production e-mail sender configured")
			return nil
		}
		var (
			addr, locale, page, orgName, orgLocale string
			senderEmail, senderStatus, accent      string
			logoID                                 *uuid.UUID
			orgID                                  uuid.UUID
			used                                   bool
		)
		err = opts.DB.QueryRow(ctx, `
			SELECT r.email_normalized, COALESCE(r.locale, ''), COALESCE(r.page_url, ''),
			       o.name, COALESCE(o.default_locale, ''), r.used_at IS NOT NULL,
			       COALESCE(o.sender_email, ''), COALESCE(o.sender_verification_status, ''),
			       COALESCE(o.branding->>'primary', ''), o.logo_media_id, r.org_id
			FROM   my_tickets_link_requests r
			JOIN   organizations o ON o.id = r.org_id
			WHERE  r.id = $1`, id).Scan(&addr, &locale, &page, &orgName, &orgLocale, &used,
			&senderEmail, &senderStatus, &accent, &logoID, &orgID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if used {
			return nil
		}
		target := MyTicketsLinkTarget(page, opts.TicketsBaseURL)
		if target == "" {
			logger.Error("delivery: my-tickets link has no page to point at", slog.String("request_id", id.String()))
			return nil
		}
		r := opts.Templates
		if r == nil {
			if r, err = templates.New(); err != nil {
				return err
			}
		}
		token, err := mytickets.NewToken()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		tag, err := opts.DB.Exec(ctx, `
			UPDATE my_tickets_link_requests
			SET    token_hash = $2, token_expires_at = $3, sent_at = $4
			WHERE  id = $1 AND used_at IS NULL`, id, mytickets.HashToken(token), now.Add(mytickets.LinkTTL), now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		link, err := MyTicketsLinkURL(target, token)
		if err != nil {
			logger.Error("delivery: my-tickets link page is not a URL", slog.String("request_id", id.String()))
			return nil
		}
		loc := locale
		if loc == "" {
			loc = orgLocale
		}
		if loc == "" {
			loc = "en"
		}
		branding := myTicketsBranding(ctx, opts.Media, orgName, accent, logoID, logger)
		contact := buyerContactEmail(ctx, opts.DB, uuid.Nil, orgID, logger)
		out, err := r.Render(templates.TemplateKindMyTicketsLink, loc, templates.Data{
			RecipientEmail: addr,
			Branding:       branding,
			MyTickets:      templates.MyTicketsLinkData{URL: link},
			Contact:        templates.BuyerContact{Email: contact},
		})
		if err != nil {
			return err
		}
		msg := myTicketsLinkMessage(addr, orgName, senderEmail, senderStatus, out)
		withReplyTo(&msg, contact)
		return opts.Sender.Send(ctx, msg)
	}
}

// myTicketsBranding is the header of the letter: the organization's name,
// logo and accent, resolved like the ticket letter's (resolveBranding). Without
// a logo (or a resolver) the header carries the name only.
func myTicketsBranding(ctx context.Context, m MediaResolver, orgName, accent string, logoID *uuid.UUID, logger *slog.Logger) templates.Branding {
	p := Payload{OrgName: orgName, OrgAccentColor: accent}
	if logoID != nil {
		p.OrgLogoMediaID = logoID.String()
	}
	b := resolveBranding(ctx, m, p, logger).Branding
	b.LogoAlt = orgName
	return b
}

// myTicketsLinkMessage leaves from the organization's own sender only when it
// is verified, like the ticket letters, and shows the organization's name as
// the sender name. An empty From leaves the platform default to the adapter.
func myTicketsLinkMessage(to, orgName, senderEmail, senderStatus string, out templates.Rendered) email.Message {
	from := ""
	if senderStatus == "verified" {
		from = strings.TrimSpace(senderEmail)
	}
	return email.Message{
		To: to, From: from, FromName: strings.TrimSpace(orgName),
		Subject: out.Subject, HTMLBody: out.HTMLBody, TextBody: out.TextBody,
	}
}
