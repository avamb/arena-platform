// account_limits.go holds the request limits of the public account routes:
// POST /v1/auth/register, POST /v1/auth/password-reset/request,
// POST /v1/auth/password-reset/confirm and GET /v1/auth/verify.
//
// Each limit is a sliding window per client address (httputil.TrustedClientIP)
// and, on the routes that take an e-mail address, a second one per normalized
// address. A refused request answers 429 auth.rate_limited with
// retry_after_seconds and the Retry-After header, as POST /v1/auth/login does.
package hauth

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/ratelimit"
)

// The limits of the public account routes, each within loginRateLimitWindow.
const (
	registerPerIPAttempts        = 10
	registerPerEmailAttempts     = 3
	resetRequestPerIPAttempts    = 10
	resetRequestPerEmailAttempts = 3
	resetConfirmPerIPAttempts    = 10
	verifyPerIPAttempts          = 30
)

// accountLimiters are the limiters of the public account routes. A nil
// limiter allows every request.
type accountLimiters struct {
	registerIP        ratelimit.Limiter
	registerEmail     ratelimit.Limiter
	resetRequestIP    ratelimit.Limiter
	resetRequestEmail ratelimit.Limiter
	resetConfirmIP    ratelimit.Limiter
	verifyIP          ratelimit.Limiter
}

func newAccountWindow(attempts int) ratelimit.Limiter {
	return ratelimit.New(ratelimit.Config{MaxAttempts: attempts, Window: loginRateLimitWindow})
}

// newAccountLimiters builds the default in-memory limiters.
func newAccountLimiters() accountLimiters {
	return accountLimiters{
		registerIP:        newAccountWindow(registerPerIPAttempts),
		registerEmail:     newAccountWindow(registerPerEmailAttempts),
		resetRequestIP:    newAccountWindow(resetRequestPerIPAttempts),
		resetRequestEmail: newAccountWindow(resetRequestPerEmailAttempts),
		resetConfirmIP:    newAccountWindow(resetConfirmPerIPAttempts),
		verifyIP:          newAccountWindow(verifyPerIPAttempts),
	}
}

// allowAccountRequest records the request in the address limiter and, when
// email is not empty, in the e-mail limiter. Both are counted even when one
// refuses. It answers 429 and returns false when either limit is exceeded.
func (h *Handler) allowAccountRequest(w http.ResponseWriter, r *http.Request, byIP, byEmail ratelimit.Limiter, email string) bool {
	ipAllowed := byIP == nil || byIP.Allow("ip:"+httputil.TrustedClientIP(r, h.trustedProxyCount))
	emailAllowed := email == "" || byEmail == nil || byEmail.Allow("email:"+email)
	if ipAllowed && emailAllowed {
		return true
	}
	logging.FromContext(r.Context()).Warn("auth: account route rate limit exceeded", "path", r.URL.Path)
	setRateLimitRetryAfter(w)
	httputil.WriteJSON(w, http.StatusTooManyRequests, httputil.ErrorEnvelopeWithDetails(
		"auth.rate_limited", "too many requests; please wait before trying again", r,
		map[string]any{"retry_after_seconds": int(loginRateLimitWindow.Seconds())},
	))
	return false
}
