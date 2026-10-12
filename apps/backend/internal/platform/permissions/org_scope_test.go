package permissions_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/permissions"
)

// memberships is one user who is an agent of orgA and a manager of orgB.
type orgMemberships struct{ a, b uuid.UUID }

func (m orgMemberships) GetActiveRolesForUser(context.Context, uuid.UUID) ([]string, error) {
	return []string{"agent", "organizer"}, nil
}

func (m orgMemberships) GetActiveRolesForUserInOrg(_ context.Context, _, org uuid.UUID) ([]string, error) {
	if org == m.a {
		return []string{"agent"}, nil
	}
	if org == m.b {
		return []string{"organizer"}, nil
	}
	return nil, nil
}

// A request that names an organization is decided from the roles held IN it.
func TestOrgScope_RolesOfAnotherOrganizationDoNotCount(t *testing.T) {
	t.Parallel()
	m := orgMemberships{a: uuid.New(), b: uuid.New()}
	checker := permissions.NewDBChecker(&fakeRBACQuerier{rolePerms: map[string][]string{
		"agent": {"ticket.read"}, "organizer": {"refund.create"},
	}}).WithMembershipQuerier(m)
	ctx := auth.WithActor(context.Background(), auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})

	// no organization on the request: the historical union (kept for routes that name none)
	if err := checker.Check(ctx, "refund.create", "refunds"); err != nil {
		t.Fatalf("unscoped check = %v; want the union to allow it", err)
	}
	if err := checker.Check(permissions.WithOrgScope(ctx, m.a), "refund.create", "refunds"); err == nil {
		t.Error("an agent of A passed refund.create in A through a manager role held in B")
	}
	if err := checker.Check(permissions.WithOrgScope(ctx, m.b), "refund.create", "refunds"); err != nil {
		t.Errorf("a manager of B was refused in B: %v", err)
	}
	if err := checker.CheckInOrg(ctx, "refund.create", m.a); err == nil {
		t.Error("CheckInOrg(A) allowed the agent")
	}
	if err := checker.CheckInOrg(ctx, "ticket.read", m.a); err != nil {
		t.Errorf("CheckInOrg(A) refused the agent's own permission: %v", err)
	}
}
