package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func TestRequireWebStepUp(t *testing.T) {
	t.Parallel()
	const secret = "guard-test-secret-of-32-bytes-ok!!"
	s := &Server{cfg: &config.Config{JWTSecretStub: secret, JWTIssuer: "arena-api"}}
	user := uuid.New()
	good, _, _ := auth.IssueJWT(secret, user, nil, nil, "arena-api", auth.StepUpAudience, auth.StepUpTTL)
	foreign, _, _ := auth.IssueJWT(secret, uuid.New(), nil, nil, "arena-api", auth.StepUpAudience, auth.StepUpTTL)
	access, _, _ := auth.IssueJWT(secret, user, nil, nil, "arena-api", "arena-api", time.Hour)

	reached := false
	h := s.requireWebStepUp(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	run := func(channel, token string) (int, bool) {
		reached = false
		req := httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader("{}"))
		ctx := auth.WithActor(req.Context(), auth.Actor{ID: user.String(), Type: auth.ActorTypeUser})
		if channel != "" {
			ctx = audit.WithClientChannel(ctx, channel)
		}
		req = req.WithContext(ctx)
		if token != "" {
			req.Header.Set(StepUpHeader, token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, reached
	}
	for name, c := range map[string]struct {
		channel, token string
		pass           bool
	}{
		"bot is never asked":        {"telegram_bot", "", true},
		"admin-web is never asked":  {"admin_web", "", true},
		"no channel is never asked": {"", "", true},
		"web without the token":     {"event_center_web", "", false},
		"web with the token":        {"event_center_web", good, true},
		"web with another's token":  {"event_center_web", foreign, false},
		"web with an access token":  {"event_center_web", access, false},
	} {
		code, ok := run(c.channel, c.token)
		if ok != c.pass || (!c.pass && code != http.StatusForbidden) {
			t.Errorf("%s: reached=%v code=%d", name, ok, code)
		}
	}
}
