// Package hauth owns the HTTP handlers for the auth domain:
// register, email verify, login, refresh, logout, and password reset.
// It depends only on the httputil sub-package (no import cycle back to httpserver).
package hauth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/redissession"
)

const (
	loginRateLimitAttempts = 5
	loginRateLimitWindow   = 15 * time.Minute
)

// DB is the minimal database interface required by auth handlers.
type DB interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Handler owns all auth domain HTTP handlers and their resolved dependencies.
type Handler struct {
	db                    DB
	audit                 audit.Writer
	sessionStore          redissession.Store
	jwtSecret             string
	jwtIssuer             string
	jwtAudience           string
	maxConcurrentSessions int
	rateLimiter           ratelimit.Limiter
}

// New creates a Handler. jwtSecret, issuer, and audience must be fully resolved
// by the caller (mount_auth.go derives them from the stub provider or hardcoded
// fallbacks before calling New).
func New(
	db DB,
	auditW audit.Writer,
	store redissession.Store,
	jwtSecret, issuer, audience string,
	maxSessions int,
) *Handler {
	return &Handler{
		db:                    db,
		audit:                 auditW,
		sessionStore:          store,
		jwtSecret:             jwtSecret,
		jwtIssuer:             issuer,
		jwtAudience:           audience,
		maxConcurrentSessions: maxSessions,
		rateLimiter: ratelimit.New(ratelimit.Config{
			MaxAttempts: loginRateLimitAttempts,
			Window:      loginRateLimitWindow,
		}),
	}
}
