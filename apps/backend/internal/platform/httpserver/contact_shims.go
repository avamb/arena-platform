package httpserver

// contact_shims.go wires the hosted page's "Write to the organizer" form
// (HP contact form): the public send route (hcontact) and the switch the
// page response carries (hfeed.WithContactForm).

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcontact"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/honboarding"
)

// contactFormSiteKey is the public Turnstile site key handed to the page.
func (s *Server) contactFormSiteKey() string {
	if s.cfg == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.ContactFormTurnstileSiteKey)
}

// contactFormReady reports whether the page may offer the form
// (config.ContactFormReady, shared with arena-worker's letters) — a form the
// server would refuse with 503 is never shown.
func (s *Server) contactFormReady() bool {
	return s.cfg.ContactFormReady()
}

func (s *Server) contactHandler() *hcontact.Handler {
	secret, salt := "", ""
	trusted := 0
	production := false
	if s.cfg != nil {
		secret = s.cfg.ContactFormSecret()
		salt = s.cfg.JWTSecretStub
		trusted = s.cfg.TrustedProxyCount
		production = s.cfg.IsProduction()
	}
	verifier := &honboarding.TurnstileVerifier{
		Secret:    secret,
		Required:  production,
		Action:    hcontact.TurnstileAction,
		Hostnames: s.contactFormHostnames(),
	}
	return hcontact.New(s.pgxPool, verifier, salt, trusted, s.logger)
}

func (s *Server) handleOrganizerContact(w http.ResponseWriter, r *http.Request) {
	s.contactHandler().HandleSend(w, r)
}

// contactFormPrivacyURL is the privacy policy the form's consent links to:
// ONBOARDING_PRIVACY_URL, else <ONBOARDING_SITE_URL>/legal/privacy (the same
// page the organizer application links to).
func (s *Server) contactFormPrivacyURL() string {
	if s.cfg == nil {
		return ""
	}
	if u := strings.TrimSpace(s.cfg.OnboardingPrivacyURL); u != "" {
		return u
	}
	if site := strings.TrimRight(strings.TrimSpace(s.cfg.OnboardingSiteURL), "/"); site != "" {
		return site + "/legal/privacy"
	}
	return ""
}

// contactFormHostnames is the host the hosted pages are served from
// (PUBLIC_TICKETS_BASE_URL): a token solved anywhere else is refused. Empty
// when the base URL is not set (local development), which skips the check.
func (s *Server) contactFormHostnames() []string {
	if s.cfg == nil || s.cfg.PublicTicketsBaseURL == "" {
		return nil
	}
	u, err := url.Parse(s.cfg.PublicTicketsBaseURL)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	return []string{u.Hostname()}
}
