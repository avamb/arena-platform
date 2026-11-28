package httputil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// TestServiceActorDecision_DeviceRefused pins that a door-scanner device is
// "not a user" for every legacy org guard and is refused with 403 for every
// organization, its own included (spec 69, MA-10).
func TestServiceActorDecision_DeviceRefused(t *testing.T) {
	org := uuid.New()
	ctx := auth.WithActor(context.Background(), auth.NewDeviceActor(uuid.NewString(), org.String()))
	for _, target := range []uuid.UUID{org, uuid.New()} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/v1/organizations/x", nil).WithContext(ctx)
		notUser, allowed := ServiceActorDecision(w, r, target)
		if !notUser || allowed {
			t.Fatalf("(notUser, allowed) = (%v, %v), want (true, false)", notUser, allowed)
		}
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "org.access_denied") {
			t.Fatalf("code=%d body=%s, want 403 org.access_denied", w.Code, w.Body.String())
		}
	}
}

func TestServiceActorDecision_UserWritesNothing(t *testing.T) {
	ctx := auth.WithActor(context.Background(), auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/organizations/x", nil).WithContext(ctx)
	notUser, allowed := ServiceActorDecision(w, r, uuid.New())
	if notUser || allowed || w.Body.Len() != 0 {
		t.Fatalf("user: (%v, %v) body=%q, want (false, false) and nothing written", notUser, allowed, w.Body.String())
	}
}
