package permissions_test

import (
	"context"
	"errors"
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

// unionOnly resolves roles across all memberships and cannot resolve them
// inside one organization.
type unionOnly struct{}

func (unionOnly) GetActiveRolesForUser(context.Context, uuid.UUID) ([]string, error) {
	return []string{"organizer"}, nil
}

// A request that names an organization is never decided from the union: a
// checker that cannot read roles inside one organization fails loudly.
func TestOrgScope_NoSilentFallbackToTheUnion(t *testing.T) {
	t.Parallel()
	checker := permissions.NewDBChecker(&fakeRBACQuerier{rolePerms: map[string][]string{
		"organizer": {"refund.create"},
	}}).WithMembershipQuerier(unionOnly{})
	if checker.DecidesPerOrganization() {
		t.Fatal("a union-only querier reported as deciding per organization")
	}
	ctx := auth.WithActor(context.Background(), auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
	err := checker.Check(permissions.WithOrgScope(ctx, uuid.New()), "refund.create", "refunds")
	if !errors.Is(err, permissions.ErrNoOrgMembershipQuerier) {
		t.Fatalf("scoped check with a union-only querier = %v, want ErrNoOrgMembershipQuerier", err)
	}
}

// AllowedInOrg decides the gate's permission inside the named organization.
func TestOrgScope_AllowedInOrgUsesTheGatePermission(t *testing.T) {
	t.Parallel()
	m := orgMemberships{a: uuid.New(), b: uuid.New()}
	checker := permissions.NewDBChecker(&fakeRBACQuerier{rolePerms: map[string][]string{
		"agent": {"ticket.read"}, "organizer": {"refund.create"},
	}}).WithMembershipQuerier(m)
	ctx := auth.WithActor(context.Background(), auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeUser})
	if ok, err := permissions.AllowedInOrg(ctx, m.a); err != nil || !ok {
		t.Fatalf("no gate on the request: AllowedInOrg = %v, %v; want allowed", ok, err)
	}
	gated := permissions.WithOrgChecker(permissions.WithRequiredPermission(ctx, "refund.create"), checker)
	if ok, _ := permissions.AllowedInOrg(gated, m.a); ok {
		t.Error("refund.create allowed in A, where the caller is only an agent")
	}
	if ok, err := permissions.AllowedInOrg(gated, m.b); err != nil || !ok {
		t.Errorf("refund.create in B = %v, %v; want allowed", ok, err)
	}
	if _, err := permissions.AllowedInOrg(permissions.WithRequiredPermission(ctx, "refund.create"), m.a); err == nil {
		t.Error("a gated request without a checker was decided instead of failing")
	}
}
