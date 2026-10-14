package httpserver

// step_up_guard.go — money routes ask the web event center for a fresh
// password confirmation (owner decision 2026-10-14, spec 08_architecture/37
// section 4.5). A request that says it comes from the web
// (X-Client-Channel: event_center_web) must carry X-Step-Up-Token, the
// token POST /v1/auth/step-up returns, issued to the same person within
// auth.StepUpTTL. The Telegram bot, admin-web and API keys are not asked:
// the owner decided against it for Telegram, and the other two are not the
// web's shared-computer risk.

import (
	"net/http"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// StepUpHeader carries the step-up token on a money request.
const StepUpHeader = "X-Step-Up-Token"

// requireWebStepUp is chi middleware for the money routes. It runs after
// the auth middleware, so the actor is already on the context.
func (s *Server) requireWebStepUp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		channel, _ := audit.ClientChannelFromContext(r.Context())
		if channel != audit.ChannelEventCenterWeb {
			next.ServeHTTP(w, r)
			return
		}
		actor, ok := auth.ActorFromContext(r.Context())
		secret := ""
		if s.cfg != nil {
			secret = s.cfg.JWTSecretStub
		}
		raw := strings.TrimSpace(r.Header.Get(StepUpHeader))
		if !ok || auth.VerifyStepUp(secret, s.authIssuer(), raw, actor.ID) != nil {
			httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
				"auth.step_up_required",
				"confirm your password to continue: this action moves money", r))
			return
		}
		next.ServeHTTP(w, r)
	})
}
