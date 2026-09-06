package audit

import (
	"context"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

const testKeyID = "9f1d6a4e-0000-4000-8000-000000000001"

func serviceActorCtx(id string) context.Context {
	return auth.WithActor(context.Background(), auth.Actor{
		ID:    id,
		Type:  auth.ActorTypeService,
		OrgID: "11111111-1111-4111-8111-111111111111",
	})
}

// TestAttributeToServiceActor pins spec §13.1: mutations performed under an
// organization API key must land in audit_events as actor `api_key:<id>`,
// while every other request keeps the attribution its call site supplied.
func TestAttributeToServiceActor(t *testing.T) {
	tests := []struct {
		name          string
		ctx           context.Context
		in            Event
		wantActorType string
		wantActorID   string
	}{
		{
			name:          "service actor is re-attributed to its key",
			ctx:           serviceActorCtx(testKeyID),
			in:            Event{ActorType: "user", ActorID: "someone-else", Action: "event.update"},
			wantActorType: ServiceActorType,
			wantActorID:   ServiceActorPrefix + testKeyID,
		},
		{
			name: "user actor passes through unchanged",
			ctx: auth.WithActor(context.Background(), auth.Actor{
				ID:   "5c2b1a90-0000-4000-8000-0000000000aa",
				Type: auth.ActorTypeUser,
			}),
			in:            Event{ActorType: "user", ActorID: "5c2b1a90-0000-4000-8000-0000000000aa"},
			wantActorType: "user",
			wantActorID:   "5c2b1a90-0000-4000-8000-0000000000aa",
		},
		{
			name:          "request with no actor passes through unchanged",
			ctx:           context.Background(),
			in:            Event{ActorType: "system", ActorID: "dispatcher"},
			wantActorType: "system",
			wantActorID:   "dispatcher",
		},
		{
			name:          "service actor without an id is not re-attributed",
			ctx:           serviceActorCtx(""),
			in:            Event{ActorType: "user", ActorID: "kept"},
			wantActorType: "user",
			wantActorID:   "kept",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := attributeToServiceActor(tc.ctx, tc.in)
			if got.ActorType != tc.wantActorType {
				t.Fatalf("ActorType = %q, want %q", got.ActorType, tc.wantActorType)
			}
			if got.ActorID != tc.wantActorID {
				t.Fatalf("ActorID = %q, want %q", got.ActorID, tc.wantActorID)
			}
			if got.Action != tc.in.Action {
				t.Fatalf("Action = %q, want %q (decorator must not touch it)", got.Action, tc.in.Action)
			}
		})
	}
}

// TestWithServiceActor_NilInner lets wire.go apply the decorator
// unconditionally, even when the server runs without an audit writer.
func TestWithServiceActor_NilInner(t *testing.T) {
	if got := WithServiceActor(nil); got != nil {
		t.Fatalf("WithServiceActor(nil) = %v, want nil", got)
	}
}
