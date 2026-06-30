package httpserver

import (
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

	issuer, audience := "arena-api", "arena-api"
	if s.stub != nil {
		issuer = s.stub.Issuer()
		audience = s.stub.Audience()
	}
	h := hauth.New(s.pool, s.audit, s.sessionStore, s.cfg.JWTSecretStub, issuer, audience, s.maxConcurrentSessions)

	r.Post("/auth/register", h.Register)
	r.Get("/auth/verify", h.VerifyEmail)
	r.Post("/auth/login", h.Login)
	r.Post("/auth/refresh", h.Refresh)
	r.Post("/auth/password-reset/request", h.PasswordResetRequest)
	r.Post("/auth/password-reset/confirm", h.PasswordResetConfirm)

	if s.stub != nil && s.stub.Enabled() {
		r.Group(func(pr chi.Router) {
			pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
			pr.Post("/auth/logout", h.Logout)
		})
	}
}
