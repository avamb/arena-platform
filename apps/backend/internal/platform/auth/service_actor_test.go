package auth

import (
	"context"
	"testing"
)

const (
	keyOrg   = "11111111-1111-4111-8111-111111111111"
	otherOrg = "22222222-2222-4222-8222-222222222222"
)

// TestServiceActorInOrg pins the single rule all six org-auth guards delegate
// to (spec §13.1): an organization API key reaches exactly api_keys.org_id and
// nothing else, and it is the only actor type this helper answers for.
func TestServiceActorInOrg(t *testing.T) {
	tests := []struct {
		name          string
		ctx           context.Context
		orgID         string
		wantIsService bool
		wantAllowed   bool
	}{
		{
			name:          "service actor reaches its own org",
			ctx:           WithActor(context.Background(), Actor{Type: ActorTypeService, OrgID: keyOrg}),
			orgID:         keyOrg,
			wantIsService: true,
			wantAllowed:   true,
		},
		{
			name:          "service actor is denied another org",
			ctx:           WithActor(context.Background(), Actor{Type: ActorTypeService, OrgID: keyOrg}),
			orgID:         otherOrg,
			wantIsService: true,
			wantAllowed:   false,
		},
		{
			name:          "service actor with no org reaches nothing",
			ctx:           WithActor(context.Background(), Actor{Type: ActorTypeService}),
			orgID:         keyOrg,
			wantIsService: true,
			wantAllowed:   false,
		},
		{
			name:          "user actor is left to the membership query",
			ctx:           WithActor(context.Background(), Actor{Type: ActorTypeUser, ID: "u1"}),
			orgID:         keyOrg,
			wantIsService: false,
			wantAllowed:   false,
		},
		{
			name:          "request with no actor is left to the membership query",
			ctx:           context.Background(),
			orgID:         keyOrg,
			wantIsService: false,
			wantAllowed:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			isService, allowed := ServiceActorInOrg(tc.ctx, tc.orgID)
			if isService != tc.wantIsService {
				t.Fatalf("isService = %v, want %v", isService, tc.wantIsService)
			}
			if allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v, want %v", allowed, tc.wantAllowed)
			}
		})
	}
}

func TestActorHasPermission(t *testing.T) {
	a := Actor{Type: ActorTypeService, Permissions: []string{"event.read", "session.read"}}
	if !a.HasPermission("session.read") {
		t.Fatal("HasPermission(session.read) = false, want true")
	}
	if a.HasPermission("event.write") {
		t.Fatal("HasPermission(event.write) = true, want false")
	}
	if (Actor{Type: ActorTypeUser}).HasPermission("event.read") {
		t.Fatal("actor with no explicit permissions must hold none")
	}
}

func TestActorIsService(t *testing.T) {
	if !(Actor{Type: ActorTypeService}).IsService() {
		t.Fatal("service actor must report IsService")
	}
	for _, tp := range []ActorType{ActorTypeUser, ActorTypeStubUser, ActorTypeAnon} {
		if (Actor{Type: tp}).IsService() {
			t.Fatalf("actor type %q must not report IsService", tp)
		}
	}
}
