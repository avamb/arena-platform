//go:build integration

package httpserver

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type gsBody struct {
	GettingStarted struct {
		Complete bool `json:"complete"`
		Steps    []struct {
			Key  string `json:"key"`
			Done bool   `json:"done"`
		} `json:"steps"`
	} `json:"getting_started"`
}

func (e *sec1Env) gettingStarted(t *testing.T, org uuid.UUID, tok string) (keys []string, done map[string]bool, complete bool) {
	t.Helper()
	raw := e.expect(t, http.StatusOK, http.MethodGet, "/v1/organizations/"+org.String()+"/getting-started", tok, "", "")
	var b gsBody
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if b.GettingStarted.Steps == nil {
		t.Fatalf("steps is null: %s", raw)
	}
	done = map[string]bool{}
	keys = []string{}
	for _, s := range b.GettingStarted.Steps {
		keys = append(keys, s.Key)
		done[s.Key] = s.Done
	}
	return keys, done, b.GettingStarted.Complete
}

func gsSalesLinkCode() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz23456789"
	b := make([]byte, 7)
	for i := range b {
		b[i] = alphabet[rand.IntN(len(alphabet))]
	}
	return string(b)
}

// GET /v1/organizations/{org_id}/getting-started on the real router and a
// real database: the owner sees all four steps, the manager only the steps
// their permissions allow, the flags follow the organization's rows, and a
// stranger is refused like on /me.
func TestGettingStarted_RealRouter_StepsPermissionsAndFlags(t *testing.T) {
	e := newSec1Env(t)
	ctx := context.Background()
	org := e.org(t, "GS")
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM sales_links WHERE org_id = $1`,
			`DELETE FROM events WHERE org_id = $1`,
			`DELETE FROM payment_provider_configs WHERE org_id = $1`,
		} {
			if _, err := e.srv.pgxPool.Exec(ctx, stmt, org); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := e.srv.pgxPool.Exec(ctx, stmt, args...); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	owner, manager, stranger := e.user(t, "gs-owner"), e.user(t, "gs-manager"), e.user(t, "gs-stranger")
	e.member(t, owner, org, "org_admin")
	e.member(t, manager, org, "organizer")
	ownerTok, managerTok := e.token(t, owner), e.token(t, manager)

	all := []string{"org_data", "payments", "event", "sales_link"}

	// A fresh organization: the owner sees four steps, none done.
	keys, done, complete := e.gettingStarted(t, org, ownerTok)
	if !reflect.DeepEqual(keys, all) {
		t.Fatalf("owner steps = %v; want %v", keys, all)
	}
	for _, k := range all {
		if done[k] {
			t.Errorf("fresh organization: %s is done", k)
		}
	}
	if complete {
		t.Error("fresh organization is complete")
	}

	// The manager may not change the organization data nor read the payment
	// configs: only event and sales_link.
	keys, _, complete = e.gettingStarted(t, org, managerTok)
	if !reflect.DeepEqual(keys, []string{"event", "sales_link"}) {
		t.Fatalf("manager steps = %v; want [event sales_link]", keys)
	}
	if complete {
		t.Error("manager: complete with nothing done")
	}

	// A stranger gets the refusal of /me.
	out := e.expect(t, http.StatusForbidden, http.MethodGet, "/v1/organizations/"+org.String()+"/getting-started", e.token(t, stranger), "", "")
	if sec1Code(out) != "org.access_denied" {
		t.Errorf("stranger: code %q; want org.access_denied", sec1Code(out))
	}
	e.expect(t, http.StatusUnauthorized, http.MethodGet, "/v1/organizations/"+org.String()+"/getting-started", "", "", "")

	// An inactive or unverified payment config does not count.
	exec(`INSERT INTO payment_provider_configs (org_id, provider, mode, secrets, status, is_active, verification_status)
		VALUES ($1, 'stripe', 'test', '{}'::jsonb, 'configured', true, 'unverified')`, org)
	if _, done, _ = e.gettingStarted(t, org, ownerTok); done["payments"] {
		t.Error("an unverified payment config counts as connected")
	}

	// Fill everything in.
	exec(`UPDATE organizations SET legal_name = 'GS Legal OU', tax_id = 'EE100000001',
		legal_address_line1 = 'Narva mnt 1', legal_address_city = 'Tallinn', legal_address_postal_code = '10117'
		WHERE id = $1`, org)
	exec(`UPDATE payment_provider_configs SET verification_status = 'ok' WHERE org_id = $1`, org)
	exec(`INSERT INTO events (org_id, name, status, visibility) VALUES ($1, 'GS draft', 'draft', 'public')`, org)
	exec(`INSERT INTO sales_links (org_id, code, target_kind, label) VALUES ($1, $2, 'org_page', 'GS link')`, org, gsSalesLinkCode())

	keys, done, complete = e.gettingStarted(t, org, ownerTok)
	if !reflect.DeepEqual(keys, all) {
		t.Fatalf("owner steps after filling = %v", keys)
	}
	for _, k := range all {
		if !done[k] {
			t.Errorf("after filling: %s is not done", k)
		}
	}
	if !complete {
		t.Error("owner: not complete after filling everything")
	}
	if _, done, complete = e.gettingStarted(t, org, managerTok); !complete || !done["event"] || !done["sales_link"] {
		t.Errorf("manager after filling: done %v complete %v", done, complete)
	}
}

// The access gate of GET .../getting-started on the real router, beyond
// owner, manager, stranger and no token: a member of another organization,
// API keys of another and of the same organization, an archived organization
// and a platform superadmin without X-Admin-Reason (same answer as GET .../me).
func TestGettingStarted_RealRouter_AccessGate(t *testing.T) {
	e := newSec1Env(t)
	ctx := context.Background()
	orgA, orgB := e.org(t, "GSA"), e.org(t, "GSB")
	t.Cleanup(func() {
		for _, stmt := range []string{
			`DELETE FROM sales_links WHERE org_id = ANY($1)`,
			`DELETE FROM events WHERE org_id = ANY($1)`,
		} {
			if _, err := e.srv.pgxPool.Exec(ctx, stmt, []uuid.UUID{orgA, orgB}); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	// Organization B has an event and a sales link, A has neither: a fact of B
	// leaking into an answer about A would show as done.
	if _, err := e.srv.pgxPool.Exec(ctx, `INSERT INTO events (org_id, name, status, visibility) VALUES ($1, 'GS B draft', 'draft', 'public')`, orgB); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := e.srv.pgxPool.Exec(ctx, `INSERT INTO sales_links (org_id, code, target_kind, label) VALUES ($1, $2, 'org_page', 'GS B link')`, orgB, gsSalesLinkCode()); err != nil {
		t.Fatalf("insert sales link: %v", err)
	}

	path := func(org uuid.UUID) string { return "/v1/organizations/" + org.String() + "/getting-started" }
	noFacts := func(t *testing.T, who, out string) {
		t.Helper()
		if strings.Contains(out, "getting_started") || strings.Contains(out, `"steps"`) {
			t.Errorf("%s: refusal carries steps: %s", who, out)
		}
	}

	// (a) A member (owner) of B asks about A: 403 org.access_denied.
	ownerB := e.user(t, "gs-owner-b")
	e.member(t, ownerB, orgB, "org_admin")
	tokB := e.token(t, ownerB)
	out := e.expect(t, http.StatusForbidden, http.MethodGet, path(orgA), tokB, "", "")
	if sec1Code(out) != "org.access_denied" {
		t.Errorf("member of another org: code %q; want org.access_denied", sec1Code(out))
	}
	noFacts(t, "member of another org", out)

	// (b) An API key of B asks about A: refused. A key of A asks about A: 200,
	// only its scopes' steps, and none of B's facts.
	scopes := []string{"event.read", "import.bil24_session", "sales_link.manage"}
	keyA, keyB := e.key(t, orgA, scopes...), e.key(t, orgB, scopes...)
	st, out := e.do(t, http.MethodGet, path(orgA), keyB, "", "")
	t.Logf("api key of another org: %d %s", st, out)
	if st != http.StatusForbidden || sec1Code(out) != "org.access_denied" {
		t.Errorf("api key of another org: %d %q; want 403 org.access_denied", st, sec1Code(out))
	}
	noFacts(t, "api key of another org", out)
	keys, done, complete := e.gettingStarted(t, orgA, keyA)
	if !reflect.DeepEqual(keys, []string{"event", "sales_link"}) {
		t.Errorf("api key of the same org: steps %v; want [event sales_link]", keys)
	}
	if done["event"] || done["sales_link"] || complete {
		t.Errorf("api key of A sees B's facts: done %v complete %v", done, complete)
	}
	// Control: B's own key does see B's facts.
	if _, done, _ = e.gettingStarted(t, orgB, keyB); !done["event"] || !done["sales_link"] {
		t.Errorf("api key of B: done %v; want event and sales_link done", done)
	}

	// (d) A platform superadmin without X-Admin-Reason: the same status as GET .../me.
	_, saTok := e.superadmin(t)
	meSt, meOut := e.do(t, http.MethodGet, "/v1/organizations/"+orgA.String()+"/me", saTok, "", "")
	gsSt, gsOut := e.do(t, http.MethodGet, path(orgA), saTok, "", "")
	t.Logf("superadmin without reason: /me %d, getting-started %d %s", meSt, gsSt, gsOut)
	if gsSt != meSt {
		t.Errorf("superadmin without reason: getting-started %d %s; /me %d %s", gsSt, gsOut, meSt, meOut)
	}

	// (c) An archived organization: its member gets 404 org.not_found.
	ownerA := e.user(t, "gs-owner-a")
	e.member(t, ownerA, orgA, "org_admin")
	tokA := e.token(t, ownerA)
	e.gettingStarted(t, orgA, tokA)
	e.expect(t, http.StatusOK, http.MethodPost, "/v1/admin/organizations/"+orgA.String()+"/archive", saTok, "getting-started gate test", "")
	out = e.expect(t, http.StatusNotFound, http.MethodGet, path(orgA), tokA, "", "")
	if sec1Code(out) != "org.not_found" {
		t.Errorf("archived org member: code %q; want org.not_found", sec1Code(out))
	}
	noFacts(t, "archived org member", out)
}
