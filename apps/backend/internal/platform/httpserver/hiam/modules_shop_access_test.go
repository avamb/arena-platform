package hiam

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// shopGrantFake answers GetPermissionsForRoles from a fixed table, like the
// role grants of migration 0172: org_admin, organizer and platform_superadmin
// hold shop.read, agent does not. It counts the lookups.
type shopGrantFake struct {
	MeQuerier
	calls int
}

func (f *shopGrantFake) GetPermissionsForRoles(_ context.Context, roles []string) ([]string, error) {
	f.calls++
	var out []string
	for _, r := range roles {
		switch r {
		case "org_admin", "organizer", "platform_superadmin":
			out = append(out, "shop.read", "shop.manage")
		case "agent":
			out = append(out, "events.read")
		}
	}
	return out, nil
}

func shopMode(s string) *string { return &s }

func TestWithholdShop_AgentDoesNotSeeTheShop(t *testing.T) {
	orgA, orgB, orgC := uuid.New(), uuid.New(), uuid.New()
	rows := []gen.MembershipRow{
		{OrgID: orgA, Role: "org_admin"},
		{OrgID: orgB, Role: "agent"},
		{OrgID: orgC, Role: "agent"},
	}
	dtos := []meMembershipDTO{
		{OrgID: orgA.String(), Modules: []string{"shop"}, ShopMode: shopMode("full")},
		{OrgID: orgB.String(), Modules: []string{"shop"}, ShopMode: shopMode("orders_only")},
		{OrgID: orgC.String(), Modules: []string{}},
	}
	fake := &shopGrantFake{}
	h := &Handler{meQueries: fake}
	if err := h.withholdShopFromMembersWithoutAccess(context.Background(), rows, dtos, nil); err != nil {
		t.Fatal(err)
	}
	if dtos[0].ShopMode == nil || *dtos[0].ShopMode != "full" || !reflect.DeepEqual(dtos[0].Modules, []string{"shop"}) {
		t.Fatalf("the owner must keep the shop: %+v", dtos[0])
	}
	if dtos[1].ShopMode != nil || len(dtos[1].Modules) != 0 || dtos[1].Modules == nil {
		t.Fatalf("an agent must not see the shop (modules stays a non-null list): %+v", dtos[1])
	}
	if dtos[2].ShopMode != nil || len(dtos[2].Modules) != 0 {
		t.Fatalf("an organization without the module stays empty: %+v", dtos[2])
	}
	// One lookup per distinct role that has something to hide: org_admin and agent.
	if fake.calls != 2 {
		t.Fatalf("lookups = %d, want 2", fake.calls)
	}
}

func TestWithholdShop_SuperadminKeepsTheShopWhateverTheMembershipRole(t *testing.T) {
	org := uuid.New()
	rows := []gen.MembershipRow{{OrgID: org, Role: "agent"}}
	dtos := []meMembershipDTO{{OrgID: org.String(), Modules: []string{"shop"}, ShopMode: shopMode("full")}}
	h := &Handler{meQueries: &shopGrantFake{}}
	if err := h.withholdShopFromMembersWithoutAccess(context.Background(), rows, dtos, []string{"platform_superadmin", "somerole"}); err != nil {
		t.Fatal(err)
	}
	if dtos[0].ShopMode == nil || len(dtos[0].Modules) != 1 {
		t.Fatalf("a platform superadmin keeps the shop: %+v", dtos[0])
	}
}
