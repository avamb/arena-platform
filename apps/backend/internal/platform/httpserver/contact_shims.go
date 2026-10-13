package httpserver

// contact_shims.go wires the hosted page's "Write to the organizer" form
// (HP contact form): the public send route (hcontact) and the switch the
// page response carries (hfeed.WithContactForm).

import (
	"net/http"
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

// contactFormReady reports whether the page may offer the form: outside
// production always (the captcha is then optional), in production only with
// both a secret and a site key — a form the server would refuse with 503 is
// never shown.
func (s *Server) contactFormReady() bool {
	if s.cfg == nil || !s.cfg.IsProduction() {
		return true
	}
	return s.cfg.ContactFormSecret() != "" && s.contactFormSiteKey() != ""
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
	verifier := &honboarding.TurnstileVerifier{Secret: secret, Required: production}
	return hcontact.New(s.pgxPool, verifier, salt, trusted, s.logger)
}

func (s *Server) handleOrganizerContact(w http.ResponseWriter, r *http.Request) {
	s.contactHandler().HandleSend(w, r)
}
