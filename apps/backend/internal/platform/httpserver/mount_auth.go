package httpserver

import (
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hauth"
)

// mountAuthRoutes mounts the public auth + JWT-protected logout endpoints.
//
//	POST /v1/auth/register, GET /v1/auth/verify, POST /v1/auth/login,
//	POST /v1/auth/refresh, POST /v1/auth/password-reset/{request,confirm},
//	POST /v1/auth/logout.
func (s *Server) mountAuthRoutes(r chi.Router) {
	if s.pool == nil {
		return
	}

	issuer := s.authIssuer()
	audience := s.authAudience()
	h := hauth.New(s.pool, s.audit, s.sessionStore, s.cfg.JWTSecretStub, issuer, audience, s.maxConcurrentSessions, s.cfg.TrustedProxyCount)
	// A web event center session lives while it is used (spec 37 §4.4).
	h.SetWebSessionIdle(time.Duration(s.cfg.WebSessionIdleHours) * time.Hour)

	r.Post("/auth/register", h.Register)
	r.Get("/auth/verify", h.VerifyEmail)
	r.Post("/auth/login", h.Login)
	r.Post("/auth/refresh", h.Refresh)
	r.Post("/auth/password-reset/request", h.PasswordResetRequest)
	r.Post("/auth/password-reset/confirm", h.PasswordResetConfirm)
	// A one-time code from the bot's "Open in browser" (spec 37 §4.3).
	r.Post("/auth/web/link", h.WebLink)

	if s.authEnabled() {
		r.Group(func(pr chi.Router) {
			pr.Use(auth.Middleware(s.authProvider(), auth.MiddlewareOptions{Logger: s.logger}))
			pr.Post("/auth/logout", h.Logout)
			// The password again, before a money action in the web (spec 37 §4.5).
			pr.Post("/auth/step-up", h.StepUp)
			// "Sign out everywhere" of the bot ends every web session (§4.3).
			pr.Post("/auth/web-sessions/revoke", h.RevokeWebSessions)
		})
	}
}
