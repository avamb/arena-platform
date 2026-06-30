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

// Exported constants for use by the httpserver shim layer (auth_shims.go).
const (
	LoginRateLimitAttempts = loginRateLimitAttempts
	LoginRateLimitWindow   = loginRateLimitWindow
)

// Exported TTL constants for use by the httpserver shim layer (auth_login_test.go).
// These are defined in login.go but re-exported here so the shim file can forward
// them as package-level constants without importing login.go symbols directly.
const (
	AccessTokenTTL  = accessTokenTTL
	RefreshTokenTTL = refreshTokenTTL
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

// NewWithLimiter is like New but accepts an external ratelimit.Limiter instead of
// constructing the default sliding-window limiter. Used by the httpserver shim layer
// (auth_shims.go) so that auth_login_test.go can substitute a test-controlled limiter
// via the package-level loginRateLimiter variable.
func NewWithLimiter(
	db DB,
	auditW audit.Writer,
	store redissession.Store,
	jwtSecret, issuer, audience string,
	maxSessions int,
	limiter ratelimit.Limiter,
) *Handler {
	return &Handler{
		db:                    db,
		audit:                 auditW,
		sessionStore:          store,
		jwtSecret:             jwtSecret,
		jwtIssuer:             issuer,
		jwtAudience:           audience,
		maxConcurrentSessions: maxSessions,
		rateLimiter:           limiter,
	}
}
