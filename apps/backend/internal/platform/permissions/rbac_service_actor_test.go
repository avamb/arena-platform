package permissions

import (
	"context"
	"errors"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// TestDBChecker_ServiceActorUsesScopes pins spec §13.1: an organization API key
// carries no roles at all, so the checker must decide purely from the scope set
// the applyAuth middleware resolved into Actor.Permissions. The checker is
// constructed with a nil RBACQuerier on purpose — any fall-through to the
// roles/permissions tables would panic instead of quietly denying.
func TestDBChecker_ServiceActorUsesScopes(t *testing.T) {
	checker := NewDBChecker(nil)

	serviceCtx := func(scopes ...string) context.Context {
		return auth.WithActor(context.Background(), auth.Actor{
			ID:          "9f1d6a4e-0000-4000-8000-000000000001",
			Type:        auth.ActorTypeService,
			Permissions: scopes,
			OrgID:       "11111111-1111-4111-8111-111111111111",
		})
	}

	tests := []struct {
		name      string
		ctx       context.Context
		action    string
		wantAllow bool
	}{
		{
			name:      "scope held is allowed",
			ctx:       serviceCtx("event.read", "session.read"),
			action:    "event.read",
			wantAllow: true,
		},
		{
			name:      "scope not held is denied",
			ctx:       serviceCtx("event.read"),
			action:    "event.write",
			wantAllow: false,
		},
		{
			name:      "empty scope set reaches nothing",
			ctx:       serviceCtx(),
			action:    "event.read",
			wantAllow: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checker.Check(tc.ctx, tc.action, "events")
			if tc.wantAllow {
				if err != nil {
					t.Fatalf("Check() = %v, want nil", err)
				}
				return
			}
			var denied *PermissionDeniedError
			if !errors.As(err, &denied) {
				t.Fatalf("Check() = %v, want *PermissionDeniedError", err)
			}
			if denied.Action != tc.action {
				t.Fatalf("denied action = %q, want %q", denied.Action, tc.action)
			}
		})
	}
}
