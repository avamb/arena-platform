package eventbot

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// The operator's own account is a platform superadmin with no memberships:
// /v1/me reports the role, the client then lists EVERY organization as an
// owner membership (sorted by name), and every request carries
// X-Admin-Reason — the header the API's membership guard demands from a
// superadmin before it lets them into an organization they are not a
// member of.
func TestArenaClient_SuperadminSeesEveryOrganization(t *testing.T) {
	orgA, orgB := uuid.New(), uuid.New()
	c := newStubArena(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Admin-Reason") != AdminReason {
			t.Errorf("X-Admin-Reason missing on %s: %q", r.URL.Path, r.Header.Get("X-Admin-Reason"))
		}
		switch r.URL.Path {
		case "/v1/me":
			_, _ = w.Write([]byte(`{"user":{"id":"u"},"roles":["platform_superadmin"],"organization_memberships":[]}`))
		case "/v1/organizations":
			_, _ = w.Write([]byte(`{"organizations":[
				{"id":"` + orgB.String() + `","name":"Zeta Events","slug":"zeta"},
				{"id":"` + orgA.String() + `","name":"alpha Promotions","slug":"alpha"},
				{"id":"not-a-uuid","name":"Broken","slug":"x"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	me, err := c.MeInfo(context.Background(), "user-jwt")
	if err != nil {
		t.Fatalf("MeInfo: %v", err)
	}
	if !me.Superadmin || len(me.Memberships) != 0 {
		t.Fatalf("MeInfo should flag the superadmin with no memberships: %+v", me)
	}
	all, err := c.AllOrganizations(context.Background(), "user-jwt")
	if err != nil {
		t.Fatalf("AllOrganizations: %v", err)
	}
	if len(all) != 2 || all[0].OrgID != orgA || all[1].OrgID != orgB {
		t.Fatalf("expected the two valid organizations sorted by name, got %+v", all)
	}
	for _, m := range all {
		if m.Role != membershipRoleOwner {
			t.Fatalf("a superadmin works in every organization as owner, got %+v", m)
		}
	}

	// An ordinary member: no platform role, memberships as before.
	plain := newStubArena(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"user":{"id":"u"},"roles":["organizer"],"organization_memberships":[
			{"org_id":"` + orgA.String() + `","org_name":"A","role":"organizer","status":"active"}]}`))
	})
	me, err = plain.MeInfo(context.Background(), "user-jwt")
	if err != nil || me.Superadmin || len(me.Memberships) != 1 {
		t.Fatalf("plain member: %+v %v", me, err)
	}
}
