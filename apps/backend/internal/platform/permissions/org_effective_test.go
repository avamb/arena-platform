package permissions_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// EffectiveInOrg lists exactly what the per-organization gate decides from:
// a manager of B who is an agent of A sees the agent's permissions in A and
// the manager's in B, never the union.
func TestEffectiveInOrg_ListsTheRolesOfThatOrganizationOnly(t *testing.T) {
	t.Parallel()
	m := orgMemberships{a: uuid.New(), b: uuid.New()}
	checker := permissions.NewDBChecker(&fakeRBACQuerier{rolePerms: map[string][]string{
		"agent": {"ticket.read"}, "organizer": {"order.read", "refund.create"},
	}}).WithMembershipQuerier(m)
	actor := auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser}
	ctx := auth.WithActor(context.Background(), actor)

	inA, err := checker.EffectiveInOrg(ctx, actor, m.a)
	if err != nil {
		t.Fatalf("EffectiveInOrg(A): %v", err)
	}
	if !reflect.DeepEqual(inA.Roles, []string{"agent"}) || !reflect.DeepEqual(inA.Permissions, []string{"ticket.read"}) {
		t.Errorf("A = %+v; want only the agent role and its permission", inA)
	}
	inB, err := checker.EffectiveInOrg(ctx, actor, m.b)
	if err != nil {
		t.Fatalf("EffectiveInOrg(B): %v", err)
	}
	if !reflect.DeepEqual(inB.Permissions, []string{"order.read", "refund.create"}) {
		t.Errorf("B permissions = %v", inB.Permissions)
	}
	// The list agrees with the gate for every permission either role has.
	for _, p := range []string{"ticket.read", "order.read", "refund.create"} {
		listed := contains(inA.Permissions, p)
		allowed := checker.CheckInOrg(ctx, p, m.a) == nil
		if listed != allowed {
			t.Errorf("%s in A: listed=%v but the gate says allowed=%v", p, listed, allowed)
		}
	}

	none, err := checker.EffectiveInOrg(ctx, actor, uuid.New())
	if err != nil {
		t.Fatalf("EffectiveInOrg(other): %v", err)
	}
	if len(none.Roles) != 0 || len(none.Permissions) != 0 || none.Roles == nil || none.Permissions == nil {
		t.Errorf("a stranger's organization = %+v; want empty, non-nil lists", none)
	}
}

// An API key has no roles: its permissions are its scopes, sorted.
func TestEffectiveInOrg_APIKeyListsItsScopes(t *testing.T) {
	t.Parallel()
	checker := permissions.NewDBChecker(&fakeRBACQuerier{}).WithMembershipQuerier(orgMemberships{})
	actor := auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeService, Permissions: []string{"order.read", "event.read", "order.read"}}
	eff, err := checker.EffectiveInOrg(context.Background(), actor, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(eff.Roles) != 0 || !reflect.DeepEqual(eff.Permissions, []string{"event.read", "order.read"}) {
		t.Errorf("api key = %+v", eff)
	}
}

// A checker that cannot resolve roles inside one organization is an error,
// never the union of every membership.
func TestEffectiveInOrg_RefusesTheUnionWhenItCannotScope(t *testing.T) {
	t.Parallel()
	checker := permissions.NewDBChecker(&fakeRBACQuerier{}).WithMembershipQuerier(unionOnly{})
	actor := auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser}
	_, err := checker.EffectiveInOrg(context.Background(), actor, uuid.New())
	if !errors.Is(err, permissions.ErrNoOrgMembershipQuerier) {
		t.Fatalf("err = %v; want ErrNoOrgMembershipQuerier", err)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
