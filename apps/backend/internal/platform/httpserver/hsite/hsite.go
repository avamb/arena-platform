// Package hsite serves the public forms of a static site that sells through a
// sales channel's feed token (Vino&Co, spec 62 section 10):
//
//	POST /v1/public/feeds/{feed_token}/organizer-contact      (П-17)
//	POST /v1/public/feeds/{feed_token}/marketing-subscriptions (П-10)
//
// The site is addressed by its feed token, not by an organization slug: such a
// channel has no hosted page. Every form is protected by Cloudflare Turnstile,
// and the server-side check is scoped to the form: the widget action must be
// the form's own (club, contact, private_event; my_tickets for the "my
// tickets" link request) and the host the token was solved on must be one of
// the channel's site origins (sales_channels.settings.site.origins). A channel
// without origins has no site forms (404), and a request without a valid token
// creates nothing (403).
//
// Channel settings read here (all plain JSON in sales_channels.settings):
//
//	site.origins                       ["https://vinoandco.events", "https://new.vinoandco.events"]
//	contact_form.recipients.<topic>    ["owner@example.com", ...]  topic: contact | private_event
//
// Guard is the shared piece: ChannelByFeedToken resolves the channel and
// Guard.Captcha runs the scoped Turnstile check, so another site route (the
// "my tickets" link request) reuses both with action "my_tickets".
package hsite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// Turnstile actions of the site forms.
const (
	ActionClub         = "club"
	ActionContact      = "contact"
	ActionPrivateEvent = "private_event"
	ActionMyTickets    = "my_tickets"
)

// Channel is the sales channel a feed token belongs to, as the site forms
// need it.
type Channel struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	OrgName   string
	OrgLocale string
	// Origins are the site's absolute origins (settings.site.origins).
	Origins []string
	// Recipients is the raw settings.contact_form.recipients object.
	Recipients []byte
	// HasFallbackAddress: the channel's public e-mail, the organization's
	// contact e-mail or its owner's account e-mail exists, so a topic without
	// its own recipients still reaches somebody.
	HasFallbackAddress bool
}

// ErrNoChannel: unknown or inactive feed token, or a channel without site
// origins. Callers answer one and the same 404.
var ErrNoChannel = errors.New("hsite: no site channel for this token")

const channelSQL = `
SELECT sc.id, sc.org_id, o.name, COALESCE(o.default_locale, ''),
       COALESCE(sc.settings #> '{site,origins}', '[]'::jsonb),
       sc.settings #> '{contact_form,recipients}',
       COALESCE(btrim(sc.settings #>> '{hosted_page,public_contact,email}'), '') <> ''
         OR COALESCE(btrim(o.contact_email), '') <> ''
         OR COALESCE(` + gen.OwnerRecipientSQL + `, '') <> ''
FROM agent_feed_tokens ft
JOIN sales_channels sc ON sc.id = ft.sales_channel_id
JOIN organizations o ON o.id = sc.org_id
WHERE ft.token = $1 AND ft.is_active = true
  AND sc.deleted_at IS NULL AND o.deleted_at IS NULL`

// ChannelByFeedToken resolves the channel of a feed token. ErrNoChannel when
// the token is unknown or inactive, the channel is deleted, or it lists no
// valid site origin.
func ChannelByFeedToken(ctx context.Context, pool *pgxpool.Pool, token string) (Channel, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 200 {
		return Channel{}, ErrNoChannel
	}
	var (
		c       Channel
		origins []byte
	)
	err := pool.QueryRow(ctx, channelSQL, token).Scan(&c.ID, &c.OrgID, &c.OrgName, &c.OrgLocale, &origins, &c.Recipients, &c.HasFallbackAddress)
	if errors.Is(err, pgx.ErrNoRows) {
		return Channel{}, ErrNoChannel
	}
	if err != nil {
		return Channel{}, err
	}
	var list []string
	if json.Unmarshal(origins, &list) == nil {
		for _, o := range list {
			if n := OriginOf(o); n != "" {
				c.Origins = append(c.Origins, n)
			}
		}
	}
	if len(c.Origins) == 0 {
		return Channel{}, ErrNoChannel
	}
	return c, nil
}

// OriginOf reduces an absolute http(s) URL to scheme://host[:port], lower
// case; "" for anything else.
func OriginOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// AllowsPage reports whether an absolute page URL lies on one of the
// channel's site origins.
func (c Channel) AllowsPage(raw string) bool {
	o := OriginOf(raw)
	if o == "" {
		return false
	}
	for _, a := range c.Origins {
		if a == o {
			return true
		}
	}
	return false
}

// Guard runs the scoped Turnstile check shared by every site form.
type Guard struct {
	Verifier honboarding.Verifier
	Logger   *slog.Logger
}

// CaptchaOutcome is the verdict of Guard.Captcha.
type CaptchaOutcome int

const (
	CaptchaOK            CaptchaOutcome = iota
	CaptchaNotConfigured                // production without a secret: 503
	CaptchaUnavailable                  // Cloudflare did not answer: 503
	CaptchaFailed                       // wrong token, action or host: 403
)

// Captcha checks token for the form's action and the channel's hosts. A nil
// verifier disables the check (tests only, like the contact form's).
func (g Guard) Captcha(ctx context.Context, ch Channel, token, ip, action string) CaptchaOutcome {
	if g.Verifier == nil {
		return CaptchaOK
	}
	ok, err := honboarding.VerifyFor(ctx, g.Verifier, token, ip, honboarding.Expect{
		Action:    action,
		Hostnames: honboarding.HostnamesOfOrigins(ch.Origins),
	})
	switch {
	case errors.Is(err, honboarding.ErrCaptchaNotConfigured):
		return CaptchaNotConfigured
	case err != nil:
		if g.Logger != nil {
			g.Logger.Warn("site form: captcha check failed", slog.String("action", action), slog.String("error", err.Error()))
		}
		return CaptchaUnavailable
	case !ok:
		return CaptchaFailed
	}
	return CaptchaOK
}

// WriteCaptchaOutcome answers a refused outcome with the form's error codes
// (<prefix>.captcha_not_configured / captcha_unavailable / captcha_failed) and
// reports whether the request may go on. 503 and 403, never 502.
func WriteCaptchaOutcome(w http.ResponseWriter, r *http.Request, prefix string, o CaptchaOutcome) bool {
	switch o {
	case CaptchaNotConfigured:
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			prefix+".captcha_not_configured", "This form is not available yet. Try again later.", r))
	case CaptchaUnavailable:
		httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
			prefix+".captcha_unavailable", "We could not check the anti-robot test. Try again in a minute.", r))
	case CaptchaFailed:
		httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
			prefix+".captcha_failed", "The anti-robot test did not pass. Try it again, then send the form.", r))
	default:
		return true
	}
	return false
}

// HashIP is the salted hash of a client address kept for the per-IP limits.
// The contact form uses the same recipe as hcontact, so a site and a hosted
// page share one budget per address.
func HashIP(salt, ip string) string {
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(salt + "|contact|" + ip))
	return hex.EncodeToString(sum[:])
}

// ChannelLookup resolves the site channel of a feed token; ChannelByFeedToken
// bound to a pool in production, a fake in tests.
type ChannelLookup func(ctx context.Context, feedToken string) (Channel, error)

// CaptchaForFeed is the Turnstile gate of a route that is addressed by a
// feed token but existed before the site forms (the "my tickets" link
// request, action my_tickets). A channel that lists site origins
// (settings.site.origins) must send a token solved on one of them; a channel
// without origins, or an unknown token, is not gated here at all, so the
// route keeps working as before for a hosted page or a WordPress site (its
// own limits still apply, and an unknown token still answers its 404). A
// failed channel lookup reports CaptchaUnavailable (503), never a silent pass.
func (g Guard) CaptchaForFeed(ctx context.Context, lookup ChannelLookup, feedToken, token, ip, action string) CaptchaOutcome {
	if lookup == nil {
		return CaptchaOK
	}
	ch, err := lookup(ctx, feedToken)
	if errors.Is(err, ErrNoChannel) {
		return CaptchaOK
	}
	if err != nil {
		if g.Logger != nil {
			g.Logger.Warn("site form: channel lookup failed", slog.String("action", action), slog.String("error", err.Error()))
		}
		return CaptchaUnavailable
	}
	return g.Captcha(ctx, ch, token, ip, action)
}
