// auth_shims.go bridges the *Server god-object to the hauth sub-package.
// All handler and validation logic lives in hauth/; these thin delegating
// methods preserve the unexported *Server method surface so test files and
// mount files compile unchanged.
package httpserver

import (
	"net/http"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hauth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// ─── const forwarders ────────────────────────────────────────────────────────

// accessTokenTTL and refreshTokenTTL forward the hauth constants so that
// auth_login_test.go (package httpserver) can reference them directly.
const (
	accessTokenTTL          = time.Duration(hauth.AccessTokenTTL)
	refreshTokenTTL         = time.Duration(hauth.RefreshTokenTTL)
	loginRateLimitAttempts  = hauth.LoginRateLimitAttempts
	loginRateLimitWindow    = time.Duration(hauth.LoginRateLimitWindow)
)

// ─── package-level rate limiter ──────────────────────────────────────────────

// loginRateLimiter is the package-level rate limiter used by handleAuthLogin.
// auth_login_test.go replaces this var with a test-controlled limiter in the
// rate-limit sub-tests; the var must therefore live in this (httpserver) package
// rather than in hauth so tests can reach it without an import.
var loginRateLimiter ratelimit.Limiter = ratelimit.New(ratelimit.Config{
	MaxAttempts: loginRateLimitAttempts,
	Window:      loginRateLimitWindow,
})

// ─── function forwarders ──────────────────────────────────────────────────────

// loginRateLimiterKey forwards to hauth.LoginRateLimiterKey so that
// auth_login_test.go (package httpserver) can call loginRateLimiterKey
// without importing hauth directly.
func loginRateLimiterKey(r *http.Request, email string) string {
	return hauth.LoginRateLimiterKey(r, email)
}

// ─── auth handler ────────────────────────────────────────────────────────────

// authHandler constructs an hauth.Handler using the current loginRateLimiter
// so that test code can substitute a controlled limiter via the package-level var.
func (s *Server) authHandler() *hauth.Handler {
	issuer, audience := "arena-api", "arena-api"
	if s.stub != nil {
		issuer = s.stub.Issuer()
		audience = s.stub.Audience()
	}
	jwtSecret := ""
	if s.cfg != nil {
		jwtSecret = s.cfg.JWTSecretStub
	}
	return hauth.NewWithLimiter(
		s.pool,
		s.audit,
		s.sessionStore,
		jwtSecret,
		issuer,
		audience,
		s.maxConcurrentSessions,
		loginRateLimiter,
	)
}

// ─── auth handler shims ───────────────────────────────────────────────────────

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	s.authHandler().Login(w, r)
}

func (s *Server) handleAuthRefresh(w http.ResponseWriter, r *http.Request) {
	s.authHandler().Refresh(w, r)
}

func (s *Server) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	s.authHandler().Register(w, r)
}

func (s *Server) handleAuthVerifyEmail(w http.ResponseWriter, r *http.Request) {
	s.authHandler().VerifyEmail(w, r)
}

func (s *Server) handleAuthPasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	s.authHandler().PasswordResetRequest(w, r)
}

func (s *Server) handleAuthPasswordResetConfirm(w http.ResponseWriter, r *http.Request) {
	s.authHandler().PasswordResetConfirm(w, r)
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	s.authHandler().Logout(w, r)
}
