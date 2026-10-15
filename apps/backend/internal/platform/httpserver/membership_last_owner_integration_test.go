//go:build integration

// membership_last_owner_integration_test.go — U-4: an organization is never
// left without an owner.
//
//	DELETE /v1/organizations/{org_id}/members/{user_id}   (body {"role":"org_admin"})
//	PATCH  /v1/admin/organizations/{org_id}/members/{membership_id}
//	DELETE /v1/admin/organizations/{org_id}/members/{membership_id}
//
// Until U-4 every one of these removed the last org_admin row, the caller's
// own included; only the bot-invitation revoke had a guard. The fixtures are
// the SEC-1 ones (real router, JWT whose only authority is a membership).
//
//	DATABASE_URL=... go test -tags integration -count=1 -v \
//	    ./apps/backend/internal/platform/httpserver/ -run TestU4_
package httpserver

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"
)

const u4OwnerBody = `{"role":"org_admin"}`

func (e *sec1Env) memberPath(org uuid.UUID, user uuid.UUID) string {
	return "/v1/organizations/" + org.String() + "/members/" + user.String()
}

func (e *sec1Env) adminMemberPath(org uuid.UUID, membership uuid.UUID) string {
	return "/v1/admin/organizations/" + org.String() + "/members/" + membership.String()
}

func (e *sec1Env) wantLastOwner(t *testing.T, body string) {
	t.Helper()
	if c := sec1Code(body); c != "membership.last_owner" {
		t.Errorf("error code = %q (%s), want membership.last_owner", c, body)
	}
}

// The sole owner cannot be removed by anyone: not by themselves, not by the
// superadmin, not through the admin routes, not by changing the role.
func TestU4_SoleOwnerStaysOwner(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "Sole")
	owner := e.user(t, "owner")
	mid := e.member(t, owner, org, "org_admin")
	tok := e.token(t, owner)
	_, superTok := e.superadmin(t)

	steps := []struct {
		name                                  string
		method, path, bearer, reason, payload string
	}{
		{"owner revokes self", http.MethodDelete, e.memberPath(org, owner), tok, "", u4OwnerBody},
		{"superadmin revokes the owner", http.MethodDelete, e.memberPath(org, owner), superTok, "u4", u4OwnerBody},
		{"owner deactivates self (admin route)", http.MethodDelete, e.adminMemberPath(org, mid), tok, "u4", ""},
		{"superadmin deactivates the owner (admin route)", http.MethodDelete, e.adminMemberPath(org, mid), superTok, "u4", ""},
		{"owner demotes self", http.MethodPatch, e.adminMemberPath(org, mid), tok, "u4", `{"role":"organizer"}`},
		{"superadmin demotes the owner", http.MethodPatch, e.adminMemberPath(org, mid), superTok, "u4", `{"role":"agent"}`},
	}
	for _, s := range steps {
		out := e.expect(t, http.StatusConflict, s.method, s.path, s.bearer, s.reason, s.payload)
		t.Logf("%s: %s", s.name, out)
		e.wantLastOwner(t, out)
		if !e.hasActive(t, owner, org, "org_admin") {
			t.Fatalf("%s: the owner row is gone", s.name)
		}
	}

	// Re-assigning the same role is not a removal.
	e.expect(t, http.StatusOK, http.MethodPatch, e.adminMemberPath(org, mid), superTok, "u4", `{"role":"org_admin"}`)
}

// With another owner the removal goes through, including the caller's own.
func TestU4_TwoOwnersOneMayLeave(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "Two")
	o1, o2 := e.user(t, "o1"), e.user(t, "o2")
	e.member(t, o1, org, "org_admin")
	mid2 := e.member(t, o2, org, "org_admin")
	tok1, tok2 := e.token(t, o1), e.token(t, o2)

	e.expect(t, http.StatusOK, http.MethodDelete, e.memberPath(org, o1), tok1, "", u4OwnerBody)
	if e.hasActive(t, o1, org, "org_admin") {
		t.Fatal("o1 should have left")
	}
	// o2 is the last one now.
	out := e.expect(t, http.StatusConflict, http.MethodDelete, e.memberPath(org, o2), tok2, "", u4OwnerBody)
	e.wantLastOwner(t, out)
	out = e.expect(t, http.StatusConflict, http.MethodPatch, e.adminMemberPath(org, mid2), tok2, "u4", `{"role":"organizer"}`)
	e.wantLastOwner(t, out)

	// A second owner again: demoting the first is allowed and a PATCH works.
	o3 := e.user(t, "o3")
	e.member(t, o3, org, "org_admin")
	e.expect(t, http.StatusOK, http.MethodPatch, e.adminMemberPath(org, mid2), tok2, "u4", `{"role":"organizer"}`)
	if !e.hasActive(t, o2, org, "organizer") || e.hasActive(t, o2, org, "org_admin") {
		t.Error("o2 should be an organizer now")
	}
}

// A deactivated account is no owner: it neither counts as the one who stays
// nor is its own removal refused.
func TestU4_DeactivatedOwnerDoesNotCount(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "Deact")
	live, dead := e.user(t, "live"), e.user(t, "dead")
	liveMID := e.member(t, live, org, "org_admin")
	e.member(t, dead, org, "org_admin")
	if _, err := e.srv.pgxPool.Exec(context.Background(), `UPDATE users SET deactivated_at = now() WHERE id = $1`, dead); err != nil {
		t.Fatalf("deactivate user: %v", err)
	}
	_, superTok := e.superadmin(t)

	out := e.expect(t, http.StatusConflict, http.MethodDelete, e.adminMemberPath(org, liveMID), superTok, "u4", "")
	e.wantLastOwner(t, out)
	// Removing the deactivated one leaves the live owner: fine.
	e.expect(t, http.StatusOK, http.MethodDelete, e.memberPath(org, dead), superTok, "u4", u4OwnerBody)
}

// Another role of the same person is not an owner row, and a missing row is
// the route's own 404, never a 409.
func TestU4_OtherRolesAndMissingRows(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "Roles")
	owner, mgr := e.user(t, "owner"), e.user(t, "mgr")
	e.member(t, owner, org, "org_admin")
	e.member(t, mgr, org, "organizer")
	tok := e.token(t, owner)

	e.expect(t, http.StatusOK, http.MethodDelete, e.memberPath(org, mgr), tok, "", `{"role":"organizer"}`)
	out := e.expect(t, http.StatusNotFound, http.MethodDelete, e.memberPath(org, mgr), tok, "", u4OwnerBody)
	if c := sec1Code(out); c != "membership.not_found" {
		t.Errorf("code = %q, want membership.not_found", c)
	}
}

// A manager (organizer) and an agent hold no membership.revoke: 403, and the
// owner stays.
func TestU4_ManagerAndAgentAreRefused(t *testing.T) {
	e := newSec1Env(t)
	org := e.org(t, "Perm")
	owner, mgr, agent := e.user(t, "owner"), e.user(t, "mgr"), e.user(t, "agent")
	mid := e.member(t, owner, org, "org_admin")
	e.member(t, mgr, org, "organizer")
	e.member(t, agent, org, "agent")

	for name, tok := range map[string]string{"organizer": e.token(t, mgr), "agent": e.token(t, agent)} {
		e.expect(t, http.StatusForbidden, http.MethodDelete, e.memberPath(org, owner), tok, "", u4OwnerBody)
		e.expect(t, http.StatusForbidden, http.MethodDelete, e.adminMemberPath(org, mid), tok, "u4", "")
		e.expect(t, http.StatusForbidden, http.MethodPatch, e.adminMemberPath(org, mid), tok, "u4", `{"role":"agent"}`)
		if !e.hasActive(t, owner, org, "org_admin") {
			t.Fatalf("%s removed the owner", name)
		}
	}
}

// Two owners removed at the same moment (here by the superadmin, whose
// authority does not depend on the rows being removed): exactly one removal
// succeeds and one owner remains. Without the owner lock both read "the other
// one is still there" and both delete.
func TestU4_ConcurrentRemovalsLeaveOneOwner(t *testing.T) {
	e := newSec1Env(t)
	_, superTok := e.superadmin(t)

	for round := 0; round < 12; round++ {
		org := e.org(t, "Race")
		o1, o2 := e.user(t, "r1"), e.user(t, "r2")
		e.member(t, o1, org, "org_admin")
		e.member(t, o2, org, "org_admin")

		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for i, u := range []uuid.UUID{o1, o2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i], _ = e.do(t, http.MethodDelete, e.memberPath(org, u), superTok, "u4", u4OwnerBody)
			}()
		}
		close(start)
		wg.Wait()

		ok, conflict := 0, 0
		for _, c := range codes {
			switch c {
			case http.StatusOK:
				ok++
			case http.StatusConflict:
				conflict++
			}
		}
		if ok != 1 || conflict != 1 {
			t.Fatalf("round %d: statuses %v, want one 200 and one 409", round, codes)
		}
		var owners int
		if err := e.srv.pgxPool.QueryRow(context.Background(),
			`SELECT count(*) FROM memberships WHERE org_id = $1 AND role = 'org_admin' AND status = 'active'`, org).Scan(&owners); err != nil {
			t.Fatalf("count owners: %v", err)
		}
		if owners != 1 {
			t.Fatalf("round %d: %d owners left, want 1", round, owners)
		}
	}
}

// The same race when the owners remove EACH OTHER with their own tokens: the
// second request may already be refused at the gate (its caller is gone), but
// never are both removals applied.
func TestU4_OwnersRemovingEachOtherLeaveOneOwner(t *testing.T) {
	e := newSec1Env(t)
	for round := 0; round < 8; round++ {
		org := e.org(t, "Dual")
		o1, o2 := e.user(t, "d1"), e.user(t, "d2")
		e.member(t, o1, org, "org_admin")
		e.member(t, o2, org, "org_admin")
		toks := []string{e.token(t, o1), e.token(t, o2)}
		targets := []uuid.UUID{o2, o1}

		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for i := range toks {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[i], _ = e.do(t, http.MethodDelete, e.memberPath(org, targets[i]), toks[i], "", u4OwnerBody)
			}()
		}
		close(start)
		wg.Wait()

		ok := 0
		for _, c := range codes {
			if c == http.StatusOK {
				ok++
			}
		}
		var owners int
		if err := e.srv.pgxPool.QueryRow(context.Background(),
			`SELECT count(*) FROM memberships WHERE org_id = $1 AND role = 'org_admin' AND status = 'active'`, org).Scan(&owners); err != nil {
			t.Fatalf("count owners: %v", err)
		}
		if ok != 1 || owners != 1 {
			t.Fatalf("round %d: statuses %v, %d owners left, want one removal and one owner", round, codes, owners)
		}
	}
}
