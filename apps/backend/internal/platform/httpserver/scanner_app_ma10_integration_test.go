//go:build integration

// scanner_app_ma10_integration_test.go — spec 69, MA-10: a scanner device
// token works on GET /v1/scanner-app/me and nowhere else.
//
//	(a) /me of an organizer device and of a pass device of organization A;
//	(b) every refusal of the device chain is 401;
//	(c) THE SWEEP: every other mounted route answers a VALID device token
//	    404 http.not_found, also for a device of a platform superadmin;
//	(d) last_seen_at is written by a successful request only.
//
// Run against a migrated database:
//
//	DATABASE_URL=... go test -tags integration -count=1 -p 1 \
//	    ./apps/backend/internal/platform/httpserver/ -run TestScannerAppMA10_
package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/scannerapp"
)

type ma10Env struct {
	*sec1Env
}

// ma10Fixture is an organization's event with two dates, one category each.
type ma10Fixture struct {
	org, venue, event uuid.UUID
	session, session2 uuid.UUID
	tier, tier2       uuid.UUID
}

func newMA10Env(t *testing.T) *ma10Env {
	t.Helper()
	e := &ma10Env{newSec1Env(t)}
	// Registered after newSec1Env, so it runs BEFORE its organization and
	// user cleanup (t.Cleanup is LIFO).
	t.Cleanup(func() {
		ctx := context.Background()
		for _, stmt := range []string{
			`DELETE FROM scanner_devices WHERE org_id = ANY($1)`,
			`DELETE FROM scanner_pairing_codes WHERE org_id = ANY($1)`,
			`DELETE FROM scanner_pass_tiers WHERE pass_id IN (SELECT id FROM scanner_passes WHERE org_id = ANY($1))`,
			`DELETE FROM scanner_pass_sessions WHERE pass_id IN (SELECT id FROM scanner_passes WHERE org_id = ANY($1))`,
			`DELETE FROM scanner_passes WHERE org_id = ANY($1)`,
			`DELETE FROM ticket_tiers WHERE session_id IN (SELECT s.id FROM sessions s JOIN events ev ON ev.id = s.event_id WHERE ev.org_id = ANY($1))`,
			`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = ANY($1))`,
			`DELETE FROM events WHERE org_id = ANY($1)`,
			`DELETE FROM venues WHERE org_id = ANY($1)`,
		} {
			if _, err := e.srv.pgxPool.Exec(ctx, stmt, e.orgs); err != nil {
				t.Logf("cleanup %q: %v", stmt, err)
			}
		}
	})
	return e
}

func (e *ma10Env) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.srv.pgxPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func (e *ma10Env) fixture(t *testing.T, label string) ma10Fixture {
	t.Helper()
	f := ma10Fixture{
		org: e.org(t, label), venue: uuid.New(), event: uuid.New(),
		session: uuid.New(), session2: uuid.New(), tier: uuid.New(), tier2: uuid.New(),
	}
	e.exec(t, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, f.venue, f.org, "MA10 Hall "+label)
	e.exec(t, `INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, f.event, f.org, "MA10 Event "+label)
	for i, ids := range [][2]uuid.UUID{{f.session, f.tier}, {f.session2, f.tier2}} {
		e.exec(t, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
		      VALUES ($1, $2, $3, now() + make_interval(days => 10 + $4::int), now() + make_interval(days => 10 + $4::int, hours => 2), 100, 'EUR', 'override')`,
			ids[0], f.event, f.venue, i)
		e.exec(t, `INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, currency) VALUES ($1, $2, 'General', 'fixed', 'EUR')`,
			ids[1], ids[0])
	}
	return f
}

// organizerDevice pairs a device of user in org (the user must hold
// scan.perform there).
func (e *ma10Env) organizerDevice(t *testing.T, org, user uuid.UUID) (scannerapp.Device, string) {
	t.Helper()
	d, raw, err := scannerapp.CreateOrganizerDevice(context.Background(), e.srv.pgxPool, scannerapp.DeviceParams{
		OrgID: org, UserID: user, Label: "Door phone", Platform: scannerapp.PlatformIOS,
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("CreateOrganizerDevice: %v", err)
	}
	return d, raw
}

// passDevice creates a standard pass of f (date 1 with category 1, date 2
// with every category) and a device of it.
func (e *ma10Env) passDevice(t *testing.T, f ma10Fixture, label string) (uuid.UUID, scannerapp.Device, string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	from, until := now.Add(-time.Hour), now.Add(5*time.Hour)
	passID, err := scannerapp.CreatePass(ctx, e.srv.pgxPool, scannerapp.CreatePassParams{
		OrgID: f.org, Label: label, Kind: scannerapp.PassKindStandard, ValidFrom: &from, ValidUntil: &until, MaxDevices: 3,
		Sessions: []scannerapp.CreatePassSession{{SessionID: f.session, TierIDs: []uuid.UUID{f.tier}}, {SessionID: f.session2}},
	}, now)
	if err != nil {
		t.Fatalf("CreatePass: %v", err)
	}
	d, raw, err := scannerapp.CreatePassDevice(ctx, e.srv.pgxPool, scannerapp.DeviceParams{
		OrgID: f.org, PassID: passID, Label: "Gate 1", Platform: scannerapp.PlatformAndroid,
	}, now)
	if err != nil {
		t.Fatalf("CreatePassDevice: %v", err)
	}
	return passID, d, raw
}

func (e *ma10Env) lastSeen(t *testing.T, device uuid.UUID) *time.Time {
	t.Helper()
	var at *time.Time
	if err := e.srv.pgxPool.QueryRow(context.Background(), `SELECT last_seen_at FROM scanner_devices WHERE id = $1`, device).Scan(&at); err != nil {
		t.Fatalf("read last_seen_at: %v", err)
	}
	return at
}

type ma10Me struct {
	Device struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		Platform string `json:"platform"`
	} `json:"device"`
	Organization struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"organization"`
	Kind        string   `json:"kind"`
	Permissions []string `json:"permissions"`
	Pass        *struct {
		ID          string     `json:"id"`
		Label       string     `json:"label"`
		Kind        string     `json:"kind"`
		ValidFrom   *time.Time `json:"valid_from"`
		ValidUntil  *time.Time `json:"valid_until"`
		State       string     `json:"state"`
		ClosedAt    *time.Time `json:"closed_at"`
		UploadUntil *time.Time `json:"upload_until"`
		Sessions    []struct {
			SessionID string   `json:"session_id"`
			EventID   string   `json:"event_id"`
			AllTiers  *bool    `json:"all_tiers"`
			TierIDs   []string `json:"tier_ids"`
		} `json:"sessions"`
	} `json:"pass"`
}

func (e *ma10Env) me(t *testing.T, token, reason string) (ma10Me, string) {
	t.Helper()
	st, body := e.do(t, http.MethodGet, "/v1/scanner-app/me", token, reason, "")
	if st != http.StatusOK {
		t.Fatalf("GET /v1/scanner-app/me: %d %s, want 200", st, body)
	}
	var out ma10Me
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out, body
}

func ma10SamePerms(got []string) bool {
	want := auth.DevicePermissions()
	if len(got) != len(want) {
		return false
	}
	g := append([]string(nil), got...)
	sort.Strings(g)
	sort.Strings(want)
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

// (a) An organizer device of org A and a pass device of org A.
func TestScannerAppMA10_Me(t *testing.T) {
	e := newMA10Env(t)
	a, b := e.fixture(t, "A"), e.fixture(t, "B")
	owner := e.user(t, "owner")
	e.member(t, owner, a.org, "org_admin")

	t.Run("organizer device", func(t *testing.T) {
		d, raw := e.organizerDevice(t, a.org, owner)
		got, body := e.me(t, raw, "")
		if got.Device.ID != d.ID.String() || got.Device.Label != "Door phone" || got.Device.Platform != "ios" {
			t.Errorf("device = %+v", got.Device)
		}
		if got.Organization.ID != a.org.String() || got.Organization.Name == "" {
			t.Errorf("organization = %+v", got.Organization)
		}
		if got.Kind != scannerapp.KindOrganizer || got.Pass != nil || strings.Contains(body, `"pass"`) {
			t.Errorf("kind = %q, pass present in %s", got.Kind, body)
		}
		if !ma10SamePerms(got.Permissions) {
			t.Errorf("permissions = %v, want exactly %v", got.Permissions, auth.DevicePermissions())
		}
	})

	t.Run("pass device", func(t *testing.T) {
		passID, d, raw := e.passDevice(t, a, "Main door")
		// A row naming a date of org B, written past the store, never shows.
		e.exec(t, `INSERT INTO scanner_pass_sessions (pass_id, session_id) VALUES ($1, $2)`, passID, b.session)
		e.exec(t, `INSERT INTO scanner_pass_tiers (pass_id, session_id, tier_id) VALUES ($1, $2, $3)`, passID, b.session, b.tier)
		got, body := e.me(t, raw, "")
		t.Logf("pass device /me: %s", body)
		if got.Device.ID != d.ID.String() || got.Kind != scannerapp.KindPass || got.Organization.ID != a.org.String() {
			t.Fatalf("me = %s", body)
		}
		if !ma10SamePerms(got.Permissions) {
			t.Errorf("permissions = %v", got.Permissions)
		}
		p := got.Pass
		if p == nil || p.ID != passID.String() || p.Label != "Main door" || p.Kind != scannerapp.PassKindStandard ||
			p.ValidFrom == nil || p.ValidUntil == nil || !p.ValidUntil.After(*p.ValidFrom) {
			t.Fatalf("pass = %s", body)
		}
		if strings.Contains(body, b.session.String()) || strings.Contains(body, b.tier.String()) || strings.Contains(body, b.org.String()) {
			t.Fatalf("org B leaked into /me: %s", body)
		}
		if len(p.Sessions) != 2 {
			t.Fatalf("sessions = %+v, want 2", p.Sessions)
		}
		bySession := map[string][]string{}
		allTiers := map[string]bool{}
		for _, s := range p.Sessions {
			if s.EventID != a.event.String() {
				t.Errorf("session %s event %s, want %s", s.SessionID, s.EventID, a.event)
			}
			if s.AllTiers == nil {
				t.Fatalf("session %s has no all_tiers: %s", s.SessionID, body)
			}
			bySession[s.SessionID] = s.TierIDs
			allTiers[s.SessionID] = *s.AllTiers
		}
		if tiers := bySession[a.session.String()]; allTiers[a.session.String()] || len(tiers) != 1 || tiers[0] != a.tier.String() {
			t.Errorf("session 1 = all_tiers %v, tiers %v, want false, [%s]", allTiers[a.session.String()], tiers, a.tier)
		}
		if tiers, ok := bySession[a.session2.String()]; !ok || !allTiers[a.session2.String()] || tiers == nil || len(tiers) != 0 {
			t.Errorf("session 2 = all_tiers %v, tiers %v (present %v), want true, []", allTiers[a.session2.String()], tiers, ok)
		}
	})

	t.Run("restricted date after its only category is deleted", func(t *testing.T) {
		c := e.fixture(t, "C")
		_, _, raw := e.passDevice(t, c, "Narrow door")
		e.exec(t, `DELETE FROM ticket_tiers WHERE id = $1`, c.tier)
		got, body := e.me(t, raw, "")
		t.Logf("pass device /me after the category was deleted: %s", body)
		if got.Pass == nil || len(got.Pass.Sessions) != 2 {
			t.Fatalf("me = %s, want 2 sessions", body)
		}
		for _, s := range got.Pass.Sessions {
			if s.SessionID != c.session.String() {
				continue
			}
			if s.AllTiers == nil || *s.AllTiers || s.TierIDs == nil || len(s.TierIDs) != 0 {
				t.Fatalf("restricted date = %s, want all_tiers false, tier_ids []", body)
			}
			return
		}
		t.Fatalf("restricted date missing from %s", body)
	})
}

// (b) Every refusal is 401.
func TestScannerAppMA10_Refusals(t *testing.T) {
	e := newMA10Env(t)
	a := e.fixture(t, "A")
	owner := e.user(t, "owner")
	e.member(t, owner, a.org, "org_admin")

	expect401 := func(t *testing.T, name, token string) {
		t.Helper()
		st, body := e.do(t, http.MethodGet, "/v1/scanner-app/me", token, "", "")
		if st != http.StatusUnauthorized {
			t.Errorf("%s: %d %s, want 401", name, st, body)
			return
		}
		if code := sec1Code(body); code != "auth.invalid_token" && code != "auth.missing_token" {
			t.Errorf("%s: code %q", name, code)
		}
	}

	unknown, _, err := scannerapp.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	expect401(t, "unknown token", unknown)
	expect401(t, "malformed token", scannerapp.TokenWirePrefix+"short")
	expect401(t, "owner JWT", e.token(t, owner))
	// scan.* may not be put on an API key at all; any valid key will do.
	expect401(t, "API key", e.key(t, a.org, "membership.read"))
	expect401(t, "no header", "")

	_, superTok := e.superadmin(t)
	expect401(t, "superadmin JWT", superTok)

	t.Run("revoked device", func(t *testing.T) {
		d, raw := e.organizerDevice(t, a.org, owner)
		e.me(t, raw, "")
		if ok, err := scannerapp.RevokeDevice(context.Background(), e.srv.pgxPool, a.org, d.ID, &owner, "lost", time.Now().UTC()); err != nil || !ok {
			t.Fatalf("RevokeDevice = %v, %v", ok, err)
		}
		expect401(t, "revoked device", raw)
	})
	t.Run("expired access token", func(t *testing.T) {
		d, raw := e.organizerDevice(t, a.org, owner)
		e.exec(t, `UPDATE scanner_devices SET access_expires_at = now() - interval '1 minute' WHERE id = $1`, d.ID)
		expect401(t, "expired access token", raw)
	})
	// A closed pass leaves its devices "upload only" for 24 hours (spec 69
	// B.1): /me answers 200 with pass.state closed, then 401.
	wantClosed := func(t *testing.T, raw string) {
		t.Helper()
		me, body := e.me(t, raw, "")
		if me.Pass == nil || me.Pass.State != "closed" || me.Pass.ClosedAt == nil || me.Pass.UploadUntil == nil ||
			!me.Pass.UploadUntil.Equal(me.Pass.ClosedAt.Add(scannerapp.PassUploadGrace)) {
			t.Fatalf("/me of a closed pass = %s", body)
		}
	}
	t.Run("revoked pass", func(t *testing.T) {
		passID, _, raw := e.passDevice(t, a, "Revoked pass")
		if me, body := e.me(t, raw, ""); me.Pass == nil || me.Pass.State != "active" || me.Pass.ClosedAt != nil || me.Pass.UploadUntil != nil {
			t.Fatalf("/me of an open pass = %s", body)
		}
		if ok, err := scannerapp.RevokePass(context.Background(), e.srv.pgxPool, a.org, passID, &owner, time.Now().UTC()); err != nil || !ok {
			t.Fatalf("RevokePass = %v, %v", ok, err)
		}
		wantClosed(t, raw)
		e.exec(t, `UPDATE scanner_passes SET revoked_at = now() - interval '24 hours 1 minute' WHERE id = $1`, passID)
		expect401(t, "revoked pass after the upload term", raw)
	})
	t.Run("pass window ended", func(t *testing.T) {
		passID, _, raw := e.passDevice(t, a, "Ended pass")
		e.exec(t, `UPDATE scanner_passes SET valid_from = now() - interval '3 hours', valid_until = now() - interval '1 minute' WHERE id = $1`, passID)
		wantClosed(t, raw)
		e.exec(t, `UPDATE scanner_passes SET valid_from = now() - interval '27 hours', valid_until = now() - interval '24 hours 1 minute' WHERE id = $1`, passID)
		expect401(t, "pass window ended after the upload term", raw)
	})
	t.Run("membership removed after pairing", func(t *testing.T) {
		u := e.user(t, "gate")
		e.member(t, u, a.org, "organizer")
		_, raw := e.organizerDevice(t, a.org, u)
		e.me(t, raw, "")
		e.exec(t, `DELETE FROM memberships WHERE user_id = $1 AND org_id = $2`, u, a.org)
		expect401(t, "membership removed", raw)
	})
	t.Run("organization archived", func(t *testing.T) {
		c := e.fixture(t, "C")
		u := e.user(t, "cowner")
		e.member(t, u, c.org, "org_admin")
		_, raw := e.organizerDevice(t, c.org, u)
		e.me(t, raw, "")
		e.exec(t, `UPDATE organizations SET deleted_at = now() WHERE id = $1`, c.org)
		expect401(t, "organization archived", raw)
	})
}

// ma10Param matches a chi route parameter, with an optional regexp.
var ma10Param = regexp.MustCompile(`\{([^}:]+)(:[^}]*)?\}`)

// ma10Fill substitutes the parameters of a chi route: every parameter whose
// name speaks of an organization gets org, every other one a random UUID, a
// trailing wildcard a plain segment.
func ma10Fill(route string, org uuid.UUID) string {
	p := ma10Param.ReplaceAllStringFunc(route, func(m string) string {
		name := strings.ToLower(ma10Param.FindStringSubmatch(m)[1])
		if strings.Contains(name, "org") {
			return org.String()
		}
		return uuid.NewString()
	})
	return strings.ReplaceAll(p, "*", "x")
}

type ma10Route struct{ method, route string }

func (e *ma10Env) routes(t *testing.T) []ma10Route {
	t.Helper()
	var out []ma10Route
	err := chi.Walk(e.srv.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, scannerAppPathPrefix) {
			return nil
		}
		if method == "" {
			method = http.MethodGet
		}
		out = append(out, ma10Route{method, route})
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].route != out[j].route {
			return out[i].route < out[j].route
		}
		return out[i].method < out[j].method
	})
	return out
}

func ma10Body(method string) string {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return "{}"
	}
	return ""
}

// sweep sends every route with token for each org in orgs and returns the
// routes that answered 404 http.not_found every time.
func (e *ma10Env) sweep(t *testing.T, routes []ma10Route, token, reason string, orgs ...uuid.UUID) map[ma10Route]bool {
	t.Helper()
	refused := map[ma10Route]bool{}
	for _, r := range routes {
		if r.method == http.MethodConnect {
			t.Errorf("CONNECT %s cannot be sent by the test client", r.route)
			continue
		}
		ok := true
		for _, org := range orgs {
			path := ma10Fill(r.route, org)
			st, body := e.do(t, r.method, path, token, reason, ma10Body(r.method))
			// A HEAD answer carries no body: its status is all there is.
			if st != http.StatusNotFound || (r.method != http.MethodHead && sec1Code(body) != "http.not_found") {
				t.Errorf("%s %s with a device token: %d %s, want 404 http.not_found", r.method, path, st, body)
				ok = false
			}
		}
		if ok {
			refused[r] = true
		}
	}
	return refused
}

// (c) + (d) THE SWEEP.
func TestScannerAppMA10_DeviceTokenRefusedOnEveryOtherRoute(t *testing.T) {
	e := newMA10Env(t)
	a, b := e.fixture(t, "A"), e.fixture(t, "B")
	owner := e.user(t, "owner")
	e.member(t, owner, a.org, "org_admin")
	device, raw := e.organizerDevice(t, a.org, owner)

	routes := e.routes(t)
	if len(routes) < 300 {
		t.Fatalf("only %d routes walked, want at least 300", len(routes))
	}
	refused := e.sweep(t, routes, raw, "", b.org, a.org)
	t.Logf("swept %d routes (x2 organizations) with an organizer-device token of org A: %d answered 404 http.not_found", len(routes), len(refused))
	if len(refused) != len(routes) {
		t.Errorf("%d of %d routes did not refuse the device token", len(routes)-len(refused), len(routes))
	}

	// No database read happened: the device was never seen.
	if at := e.lastSeen(t, device.ID); at != nil {
		t.Errorf("last_seen_at = %v after the sweep, want NULL (the refusal must not authenticate)", at)
	}

	// Every route that needs a credential (401 without a header) was swept.
	needAuth := 0
	for _, r := range routes {
		if r.method == http.MethodConnect {
			continue
		}
		st, _ := e.do(t, r.method, ma10Fill(r.route, a.org), "", "", "")
		if st != http.StatusUnauthorized {
			continue
		}
		needAuth++
		if !refused[r] {
			t.Errorf("%s %s answers 401 without a header but did not refuse the device token", r.method, r.route)
		}
	}
	t.Logf("%d of the swept routes answer 401 without a header", needAuth)
	if needAuth < 100 {
		t.Errorf("only %d routes answer 401 without a header, the probe is broken", needAuth)
	}

	// (d) The token is valid: /me answers and writes last_seen_at.
	e.me(t, raw, "")
	if at := e.lastSeen(t, device.ID); at == nil {
		t.Error("last_seen_at not written by a successful /me")
	}
}

// The device of a person who is BOTH platform superadmin and owner of org A,
// with X-Admin-Reason: still 404 everywhere, and only the fixed permissions.
func TestScannerAppMA10_SuperadminDeviceRefusedEverywhere(t *testing.T) {
	e := newMA10Env(t)
	a, b := e.fixture(t, "A"), e.fixture(t, "B")
	superID, superTok := e.superadmin(t)
	e.member(t, superID, a.org, "org_admin")
	// The person's own JWT does cross into org B with a reason: the device
	// must not.
	if st, body := e.do(t, http.MethodGet, "/v1/organizations/"+b.org.String()+"/members", superTok, "ma10 control", ""); st != http.StatusOK {
		t.Fatalf("control: the superadmin JWT reads org B members: %d %s", st, body)
	}
	_, raw := e.organizerDevice(t, a.org, superID)

	routes := e.routes(t)
	refused := e.sweep(t, routes, raw, "ma10 sweep", b.org)
	t.Logf("swept %d routes with a superadmin's device token and X-Admin-Reason: %d answered 404 http.not_found", len(routes), len(refused))
	if len(refused) != len(routes) {
		t.Errorf("%d of %d routes did not refuse the superadmin's device token", len(routes)-len(refused), len(routes))
	}

	got, body := e.me(t, raw, "ma10 sweep")
	if !ma10SamePerms(got.Permissions) || got.Organization.ID != a.org.String() || got.Kind != scannerapp.KindOrganizer {
		t.Errorf("superadmin device /me = %s", body)
	}
}
