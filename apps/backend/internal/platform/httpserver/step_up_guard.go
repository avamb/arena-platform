package httpserver

// step_up_guard.go — money routes ask the web event center for a fresh
// password confirmation (owner decision 2026-10-14, spec 08_architecture/37
// section 4.5).
//
// A request is "from the web" when it says so (X-Client-Channel:
// event_center_web) OR when its access token was minted from a web sign-in
// (the "chn" claim, written from the stored session, never from a header).
// The claim is what makes leaving the header out of a request, or out of a
// token refresh, useless: the session itself remembers it is a web one.
//
// Such a request must carry X-Step-Up-Token, the token POST /v1/auth/step-up
// returns. The token is checked against the person, the sign-in ("sid") the
// access token belongs to, and the organization of the action: the path's
// {org_id} here, the loaded row's organization in rowOrgAccess for the flat
// routes. A sign-out or "sign out everywhere" ends the sign-in, and its
// step-up token with it.
//
// The Telegram bot, admin-web and API keys are not asked: the owner decided
// against it for Telegram, and the other two are not the web's
// shared-computer risk. Their tokens carry no "chn".

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/httputil"
)

// StepUpHeader carries the step-up token on a money request.
const StepUpHeader = "X-Step-Up-Token"

// stepUpOrgKey carries the organization a verified step-up token was made
// for, so rowOrgAccess can hold a flat money route to it.
type stepUpOrgKey struct{}

func withStepUpOrg(ctx context.Context, orgID string) context.Context {
	return context.WithValue(ctx, stepUpOrgKey{}, orgID)
}

// stepUpOrgFromContext is the organization of the step-up token the guard
// accepted for this request, if any.
func stepUpOrgFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(stepUpOrgKey{}).(string)
	return v, ok && v != ""
}

var errStepUpNoDatabase = errors.New("step-up: no database to check the sign-in")

// isWebRequest reports whether a request must pass the web's money check.
func isWebRequest(ctx context.Context) bool {
	if channel, _ := audit.ClientChannelFromContext(ctx); channel == audit.ChannelEventCenterWeb {
		return true
	}
	actor, ok := auth.ActorFromContext(ctx)
	return ok && actor.Channel == audit.ChannelEventCenterWeb
}

// writeStepUpRequired is the one refusal of the guard.
func writeStepUpRequired(w http.ResponseWriter, r *http.Request) {
	httputil.WriteJSON(w, http.StatusForbidden, httputil.ErrorEnvelope(
		"auth.step_up_required",
		"confirm your password to continue: this action changes a refund or a payment", r))
}

// stepUpSessionLive reports whether the sign-in sessionID of userID still has
// a live refresh token.
func (s *Server) stepUpSessionLive(ctx context.Context, sessionID, userID uuid.UUID) (bool, error) {
	if s.stepUpSessionCheck != nil {
		return s.stepUpSessionCheck(ctx, sessionID, userID)
	}
	if s.pgxPool == nil {
		return false, errStepUpNoDatabase
	}
	return gen.New(s.pgxPool).RefreshSessionIsLive(ctx, sessionID, userID)
}

// requireWebStepUp is chi middleware for the money routes. It runs after
// the auth middleware, so the actor is already on the context.
func (s *Server) requireWebStepUp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !isWebRequest(ctx) {
			next.ServeHTTP(w, r)
			return
		}
		actor, ok := auth.ActorFromContext(ctx)
		if !ok {
			writeStepUpRequired(w, r)
			return
		}
		userID, errUser := uuid.Parse(actor.ID)
		sessionID, errSession := uuid.Parse(actor.SessionID)
		if errUser != nil || errSession != nil {
			writeStepUpRequired(w, r)
			return
		}
		secret := ""
		if s.cfg != nil {
			secret = s.cfg.JWTSecretStub
		}
		raw := strings.TrimSpace(r.Header.Get(StepUpHeader))
		orgID, err := auth.VerifyStepUp(secret, s.authIssuer(), raw, actor.ID, actor.SessionID)
		if err != nil {
			writeStepUpRequired(w, r)
			return
		}
		if pathOrg := chi.URLParam(r, "org_id"); pathOrg != "" && !strings.EqualFold(pathOrg, orgID) {
			writeStepUpRequired(w, r)
			return
		}
		live, err := s.stepUpSessionLive(ctx, sessionID, userID)
		if err != nil {
			if s.logger != nil {
				s.logger.Error("step-up: sign-in check failed", "error", err.Error())
			}
			httputil.WriteJSON(w, http.StatusServiceUnavailable, httputil.ErrorEnvelope(
				"dependency.database_unavailable", "database is not available", r))
			return
		}
		if !live {
			writeStepUpRequired(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(withStepUpOrg(ctx, orgID)))
	})
}
