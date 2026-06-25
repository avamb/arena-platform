package httpserver

import (
	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// mountAuthRoutes mounts the public auth + JWT-protected logout endpoints.
//
//	POST /v1/auth/register, GET /v1/auth/verify, POST /v1/auth/login,
//	POST /v1/auth/refresh, POST /v1/auth/password-reset/{request,confirm},
//	POST /v1/auth/logout.
func (s *Server) mountAuthRoutes(r chi.Router) {
	if s.pool != nil {
		r.Post("/auth/register", s.handleAuthRegister)
		r.Get("/auth/verify", s.handleAuthVerifyEmail)
		r.Post("/auth/login", s.handleAuthLogin)
		r.Post("/auth/refresh", s.handleAuthRefresh)
		r.Post("/auth/password-reset/request", s.handleAuthPasswordResetRequest)
		r.Post("/auth/password-reset/confirm", s.handleAuthPasswordResetConfirm)
	}
	if s.stub != nil && s.stub.Enabled() && s.pool != nil {
		r.Group(func(pr chi.Router) {
			pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
			pr.Post("/auth/logout", s.handleAuthLogout)
		})
	}
}
