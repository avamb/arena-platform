//go:build integration

package httpserver

// org_isolation_sweep_integration_test.go — SEC-2: the class "a route with
// {org_id} plus a child id must verify the child's organization".
//
// The attacker is organization B, authenticated three ways: an owner (JWT with
// an EMPTY roles claim, so the only authority is the org_admin membership row),
// a manager (organizer membership, same JWT shape) and an organization API key.
// Every request goes through the REAL router to /v1/organizations/<B>/... but
// names ORGANIZATION A's ids (event, session, channel, feed token, allocation,
// issuance, plan version). The route must answer its own 404 — or 403 when the
// caller's role lacks the route's permission altogether, which proves nothing
// about the child check — and organization A's rows must be byte-for-byte what
// they were: inventory ledger counters, feed tokens, session status and
// deleted_at, seats, tiers, allocations, complimentary issuances and tickets,
// and every letter job.
//
// A fresh world for organization A is built per case, so one destructive call
// (a revoked token, a cancelled session) cannot hide the next. The table lives
// in org_isolation_sweep_table_test.go together with the chi.Walk guard.
//
// Run against a migrated database (AGENTS.md CI-Integration recipe).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// ─── environment: organization B, its callers, the server ───────────────────

type sweepEnv struct {
	ts     *httptest.Server
	pool   *pgxpool.Pool
	q      *gen.Queries
	secret string

	orgB, orgC     uuid.UUID
	venueB, eventB uuid.UUID

	owner, manager, key string
	managerPerms        map[string]bool
}

type sweepCaller struct {
	name  string
	token string
	// lacks reports whether the caller's role does not hold perm at all.
	lacks func(perm string) bool
}

func (e *sweepEnv) callers() []sweepCaller {
	return []sweepCaller{
		{"b-owner-jwt", e.owner, func(string) bool { return false }},
		{"b-manager-jwt", e.manager, func(p string) bool { return !e.managerPerms[p] }},
		{"b-api-key", e.key, func(string) bool { return false }},
	}
}

func newSweepEnv(t *testing.T) *sweepEnv {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	pool := srv.pgxPool
	q := gen.New(pool)
	ctx := context.Background()
	e := &sweepEnv{ts: ts, pool: pool, q: q, secret: secret, managerPerms: map[string]bool{}}

	e.orgB = e.newOrg(t, "B")
	e.orgC = e.newOrg(t, "C")
	e.venueB, e.eventB = uuid.New(), uuid.New()
	e.exec(t, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'Sweep B Hall', 'Europe/Prague')`, e.venueB, e.orgB)
	e.exec(t, `INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'Sweep B Show', 'published', 'public')`, e.eventB, e.orgB)

	mkUser := func(role string) (uuid.UUID, string) {
		u, err := q.InsertUser(ctx, "sweep-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		if _, err := q.InsertMembership(ctx, u.ID, e.orgB, role); err != nil {
			t.Fatalf("InsertMembership(%s): %v", role, err)
		}
		tok, _, err := auth.IssueJWT(secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return u.ID, tok
	}
	ownerID, ownerTok := mkUser("org_admin")
	_, mgrTok := mkUser("organizer")
	e.owner, e.manager = ownerTok, mgrTok

	scopeSet := map[string]bool{}
	for _, r := range sweepRows() {
		scopeSet[r.perm] = true
	}
	var scopes []string
	for s := range scopeSet {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	_, raw, err := apikeys.Issue(ctx, apikeys.NewStoreFromQueries(q), apikeys.IssueInput{
		OrgID: e.orgB, Name: "sweep-" + uuid.NewString()[:6], Scopes: scopes, CreatedBy: ownerID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}
	e.key = raw

	rows, err := pool.Query(ctx, `
		SELECT p.name FROM role_permissions rp
		JOIN roles r ON r.id = rp.role_id AND r.name = 'organizer' AND r.org_id IS NULL
		JOIN permissions p ON p.id = rp.permission_id`)
	if err != nil {
		t.Fatalf("read organizer permissions: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		e.managerPerms[name] = true
	}

	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM events WHERE org_id = $1`, e.orgB)
		_, _ = pool.Exec(c, `DELETE FROM venues WHERE org_id = $1`, e.orgB)
		_, _ = pool.Exec(c, `DELETE FROM api_keys WHERE org_id = $1`, e.orgB)
		_, _ = pool.Exec(c, `DELETE FROM memberships WHERE org_id = $1`, e.orgB)
		for _, org := range []uuid.UUID{e.orgB, e.orgC} {
			_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, org)
		}
	})
	return e
}

func (e *sweepEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("fixture %q: %v", sql, err)
	}
}

func (e *sweepEnv) newOrg(t *testing.T, label string) uuid.UUID {
	t.Helper()
	suffix := uuid.NewString()[:8]
	org, err := e.q.InsertOrganization(context.Background(), "Sweep "+label+" "+suffix, "sweep-"+strings.ToLower(label)+"-"+suffix, "CZ", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization: %v", err)
	}
	// A contact e-mail, so that a cancellation that reaches the session-change
	// journal queues its letters instead of refusing with organization.contact_missing.
	e.exec(t, `UPDATE organizations SET contact_email = $2 WHERE id = $1`, org.ID, "sweep-contact-"+suffix+"@example.test")
	return org.ID
}

func (e *sweepEnv) do(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	resp := integDoRequest(t, e.ts.Client(), method, e.ts.URL+path, token, body)
	raw := integReadBody(t, resp)
	out := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return resp.StatusCode, out
}

// ─── a world: one organization's rows ───────────────────────────────────────

type sweepWorld struct {
	org, partner                                  uuid.UUID
	venue, event, channel, feedToken, tier        uuid.UUID
	s1, s2, s3, s4                                uuid.UUID
	alloc, issuance, planID, planVersion, orderID uuid.UUID
}

func sweepGAGeometry() seating.Geometry {
	return seating.Canonicalize(seating.Geometry{
		SchemaVersion: seating.SchemaVersion,
		Canvas:        seating.Canvas{Width: 400, Height: 300},
		Categories: []seating.Category{
			{Index: 1, Name: "Floor", Kind: seating.KindGeneralAdmission, Capacity: 30},
			{Index: 2, Name: "Balcony", Kind: seating.KindGeneralAdmission, Capacity: 20},
		},
		Sections: []seating.Section{},
		Tables:   []seating.Table{},
	})
}

// newWorld builds organization org's rows: an event with four sessions, a
// category with ten free places and a session ledger (50 total, 5 held), a
// sales channel with a feed token, an external allocation for partner, one
// complimentary issuance, a GA seating plan version and a paid order with an
// active ticket on session 4. Cleaned up when t ends.
func (e *sweepEnv) newWorld(t *testing.T, org, partner uuid.UUID) *sweepWorld {
	t.Helper()
	w := &sweepWorld{
		org: org, partner: partner,
		venue: uuid.New(), event: uuid.New(), channel: uuid.New(), feedToken: uuid.New(), tier: uuid.New(),
		s1: uuid.New(), s2: uuid.New(), s3: uuid.New(), s4: uuid.New(),
		alloc: uuid.New(), issuance: uuid.New(), planID: uuid.New(), planVersion: uuid.New(), orderID: uuid.New(),
	}
	suffix := uuid.NewString()[:8]
	start := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Hour)
	geometry := sweepGAGeometry()
	geoJSON, err := json.Marshal(geometry)
	if err != nil {
		t.Fatalf("marshal geometry: %v", err)
	}
	checksum, err := seating.Checksum(geometry)
	if err != nil {
		t.Fatalf("checksum: %v", err)
	}

	t.Cleanup(func() { e.cleanupWorld(w) })

	e.exec(t, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, w.venue, org, "Sweep Hall "+suffix)
	e.exec(t, `INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, $3, 'published', 'public')`, w.event, org, "Sweep Show "+suffix)
	e.exec(t, `INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`, w.channel, org, "Sweep Channel "+suffix)
	e.exec(t, `INSERT INTO agent_feed_tokens (id, token, sales_channel_id, label) VALUES ($1, $2, $3, 'sweep')`,
		w.feedToken, "sweep-token-"+uuid.NewString(), w.channel)
	for i, s := range []uuid.UUID{w.s1, w.s2, w.s3, w.s4} {
		e.exec(t, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
		           VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 50, 'scheduled', 'EUR', 'override')`,
			s, w.event, w.venue, start.Add(time.Duration(i)*24*time.Hour))
	}
	e.exec(t, `INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, capacity, unit_seq, is_open)
	           VALUES ($1, $2, 'Standing', 'fixed', 2500, 'EUR', 50, 1, true)`, w.tier, w.s1)
	e.exec(t, `INSERT INTO inventory_ledger (session_id, tier_id, capacity_total, capacity_held, capacity_sold) VALUES ($1, NULL, 50, 5, 0)`, w.s1)
	for i := 1; i <= 10; i++ {
		e.exec(t, `INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, kind)
		           VALUES ($1, $2, 'GA', '-', $3, $4, 'ga_unit')`, w.s1, fmt.Sprintf("ga|t1|%06d", i), fmt.Sprint(i), w.tier)
	}
	for i := 1; i <= 3; i++ {
		e.exec(t, `INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, kind)
		           VALUES ($1, $2, 'S', 'A', $3, 'seat')`, w.s3, fmt.Sprintf("S|A|%d", i), fmt.Sprint(i))
	}
	e.exec(t, `INSERT INTO external_allocations (id, session_id, partner_org_id, tier_id, quota_qty, status) VALUES ($1, $2, $3, $4, 5, 'pending')`,
		w.alloc, w.s1, partner, w.tier)
	e.exec(t, `INSERT INTO complimentary_issuances (id, org_id, session_id, tier_id, qty, recipients, batch_id, status)
	           VALUES ($1, $2, $3, $4, 1, ARRAY['sweep-guest@example.test'], $5, 'issued')`, w.issuance, org, w.s1, w.tier, "sweep-seed-"+suffix)
	e.exec(t, `INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'general_admission', 'active')`,
		w.planID, w.venue, org, "Sweep Plan "+suffix)
	e.exec(t, `INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated, capacity_standing)
	           VALUES ($1, $2, 1, $3::jsonb, $4, 0, 50)`, w.planVersion, w.planID, string(geoJSON), checksum)

	// A paid order with one active ticket on session 4: cancelling or moving
	// that session must queue a letter to this buyer.
	res, cs := uuid.New(), uuid.New()
	e.exec(t, `INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	           VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, res, org, w.channel, w.s4)
	e.exec(t, `INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, cs, org, w.channel, res)
	e.exec(t, `INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                               source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
	           VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 2500, 0, 0, 2500, 'Sweep Buyer', $8, now())`,
		w.orderID, org, w.channel, w.event, w.s4, cs, res, "sweep-buyer-"+suffix+"@example.test")
	e.exec(t, `INSERT INTO tickets (checkout_session_id, session_id, tier_id, holder_email, status, order_id, ordinal)
	           VALUES ($1, $2, $3, $4, 'active', $5, 0)`, cs, w.s4, w.tier, "sweep-buyer-"+suffix+"@example.test", w.orderID)
	return w
}

func (e *sweepEnv) cleanupWorld(w *sweepWorld) {
	c := context.Background()
	sessions := `(SELECT id FROM sessions WHERE event_id = $1)`
	tickets := `(SELECT id FROM tickets WHERE session_id IN ` + sessions + `)`
	for _, st := range []struct {
		sql string
		arg uuid.UUID
	}{
		{`DELETE FROM worker_jobs WHERE payload->>'order_id' IN (SELECT id::text FROM orders WHERE org_id = $1)`, w.org},
		{`DELETE FROM worker_jobs WHERE payload->>'ticket_id' IN (SELECT id::text FROM tickets WHERE session_id IN (SELECT id FROM sessions WHERE event_id = $1))`, w.event},
		{`DELETE FROM session_change_notices WHERE change_id IN (SELECT id FROM session_changes WHERE org_id = $1)`, w.org},
		{`DELETE FROM session_changes WHERE org_id = $1`, w.org},
		{`DELETE FROM delivery_jobs WHERE ticket_id IN ` + tickets, w.event},
		{`DELETE FROM order_items WHERE order_id IN (SELECT id FROM orders WHERE org_id = $1)`, w.org},
		{`DELETE FROM tickets WHERE session_id IN ` + sessions, w.event},
		{`DELETE FROM orders WHERE org_id = $1`, w.org},
		{`DELETE FROM checkout_sessions WHERE org_id = $1`, w.org},
		{`DELETE FROM reservations WHERE org_id = $1`, w.org},
		{`DELETE FROM complimentary_issuances WHERE org_id = $1`, w.org},
		{`DELETE FROM external_allocations WHERE session_id IN ` + sessions, w.event},
		{`DELETE FROM inventory_ledger WHERE session_id IN ` + sessions, w.event},
		{`DELETE FROM session_seats WHERE session_id IN ` + sessions, w.event},
		{`DELETE FROM ticket_tier_prices WHERE tier_id IN (SELECT id FROM ticket_tiers WHERE session_id IN ` + sessions + `)`, w.event},
		{`DELETE FROM ticket_tiers WHERE session_id IN ` + sessions, w.event},
		{`DELETE FROM sessions WHERE event_id = $1`, w.event},
		{`DELETE FROM events WHERE id = $1`, w.event},
		{`DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`, w.channel},
		{`DELETE FROM sales_channels WHERE id = $1`, w.channel},
		{`UPDATE seating_plans SET current_version_id = NULL WHERE id = $1`, w.planID},
		{`DELETE FROM seating_plan_versions WHERE seating_plan_id = $1`, w.planID},
		{`DELETE FROM seating_plans WHERE id = $1`, w.planID},
		{`DELETE FROM venues WHERE id = $1`, w.venue},
	} {
		if _, err := e.pool.Exec(c, st.sql, st.arg); err != nil {
			fmt.Printf("sweep cleanup %q: %v\n", st.sql, err)
		}
	}
	// The organization of an attack world (not B, not C) goes with it.
	if w.org != e.orgB {
		_, _ = e.pool.Exec(c, `DELETE FROM audit_events WHERE metadata->>'org_id' = $1::text`, w.org)
		_, _ = e.pool.Exec(c, `DELETE FROM organizations WHERE id = $1`, w.org)
	}
}

// ids names the world's rows for a caller who stands in organization pathOrg.
func (w *sweepWorld) ids(e *sweepEnv, pathOrg uuid.UUID) sweepIDs {
	return sweepIDs{
		Org: pathOrg, OwnVenue: e.venueB, EventOther: e.eventB,
		Event: w.event, Session1: w.s1, Session2: w.s2, Session3: w.s3, Session4: w.s4,
		Tier: w.tier, Channel: w.channel, FeedToken: w.feedToken, Alloc: w.alloc, Issuance: w.issuance,
		PlanVersion: w.planVersion, PartnerOrg: w.partner,
	}
}

// ─── the state that must not move ───────────────────────────────────────────

func (e *sweepEnv) snapshot(t *testing.T, w *sweepWorld) map[string]string {
	t.Helper()
	ctx := context.Background()
	inSessions := `(SELECT id FROM sessions WHERE event_id = $1)`
	queries := []struct {
		label, sql string
		arg        uuid.UUID
	}{
		{"inventory ledger", `SELECT COALESCE(string_agg(session_id||':'||COALESCE(tier_id::text,'-')||':'||COALESCE(capacity_total,-1)||'/'||capacity_held||'/'||capacity_sold||'/'||version, ',' ORDER BY session_id, tier_id), '')
		   FROM inventory_ledger WHERE session_id IN ` + inSessions, w.event},
		{"sessions", `SELECT COALESCE(string_agg(id||':'||status||':'||(deleted_at IS NOT NULL)||':'||start_at||':'||end_at||':'||seat_status_version||':'||capacity_total, ',' ORDER BY id), '')
		   FROM sessions WHERE event_id = $1`, w.event},
		{"categories", `SELECT COALESCE(string_agg(id||':'||name||':'||COALESCE(capacity,-1)||':'||is_open||':'||price_amount||':'||(deleted_at IS NOT NULL), ',' ORDER BY id), '')
		   FROM ticket_tiers WHERE session_id IN ` + inSessions, w.event},
		{"places", `SELECT COALESCE(string_agg(session_id||':'||kind||':'||status||'='||n||'@'||v, ',' ORDER BY session_id, kind, status), '')
		   FROM (SELECT session_id, kind, status, count(*) n, max(status_version) v FROM session_seats WHERE session_id IN ` + inSessions + ` GROUP BY 1, 2, 3) x`, w.event},
		{"feed tokens", `SELECT COALESCE(string_agg(id||':'||is_active||':'||(revoked_at IS NOT NULL), ',' ORDER BY id), '')
		   FROM agent_feed_tokens WHERE sales_channel_id = $1`, w.channel},
		{"allocations", `SELECT COALESCE(string_agg(id||':'||status||':'||quota_qty||':'||quota_consumed, ',' ORDER BY id), '')
		   FROM external_allocations WHERE session_id IN ` + inSessions, w.event},
		{"complimentary issuances", `SELECT count(*)::text FROM complimentary_issuances WHERE session_id IN ` + inSessions, w.event},
		{"complimentary tickets", `SELECT count(*)::text FROM tickets WHERE complimentary_issuance_id IS NOT NULL AND session_id IN ` + inSessions, w.event},
		{"ticket delivery jobs", `SELECT count(*)::text FROM delivery_jobs WHERE ticket_id IN (SELECT id FROM tickets WHERE session_id IN ` + inSessions + `)`, w.event},
		{"ticket mail jobs", `SELECT count(*)::text FROM worker_jobs WHERE job_type = 'ticket.deliver'
		   AND payload->>'ticket_id' IN (SELECT id::text FROM tickets WHERE session_id IN ` + inSessions + `)`, w.event},
		{"session change journal", `SELECT count(*)::text FROM session_changes WHERE org_id = $1`, w.org},
		{"session change mail jobs", `SELECT count(*)::text FROM worker_jobs WHERE job_type = 'session.change_email'
		   AND payload->>'order_id' IN (SELECT id::text FROM orders WHERE org_id = $1)`, w.org},
	}
	out := map[string]string{}
	for _, q := range queries {
		var s string
		if err := e.pool.QueryRow(ctx, q.sql, q.arg).Scan(&s); err != nil {
			t.Fatalf("snapshot %s: %v", q.label, err)
		}
		out[q.label] = s
	}
	return out
}

func diffSnapshots(before, after map[string]string) []string {
	var diffs []string
	for k, b := range before {
		if a := after[k]; a != b {
			diffs = append(diffs, fmt.Sprintf("%s changed:\n    before: %s\n    after:  %s", k, b, a))
		}
	}
	sort.Strings(diffs)
	return diffs
}

// ─── the sweep ──────────────────────────────────────────────────────────────

func TestOrgIsolationSweep_ForeignChildrenAreNotFoundAndNothingMoves(t *testing.T) {
	e := newSweepEnv(t)
	for _, row := range sweepRows() {
		for _, caller := range e.callers() {
			row, caller := row, caller
			t.Run(row.hole+"/"+row.name+"/"+caller.name, func(t *testing.T) {
				orgA := e.newOrg(t, "A")
				w := e.newWorld(t, orgA, e.orgC)
				ids := w.ids(e, e.orgB)
				before := e.snapshot(t, w)

				body := ""
				if row.body != nil {
					body = row.body(ids)
				}
				status, out := e.do(t, strings.SplitN(row.route, " ", 2)[0], row.path(ids), caller.token, body)

				want := http.StatusNotFound
				if caller.lacks(row.perm) {
					want = http.StatusForbidden
				}
				if status != want {
					t.Errorf("%s %s as %s: status %d, want %d (%v)", row.hole, row.name, caller.name, status, want, out)
				}
				if diffs := diffSnapshots(before, e.snapshot(t, w)); len(diffs) > 0 {
					t.Errorf("%s %s as %s moved organization A's rows:\n  %s", row.hole, row.name, caller.name, strings.Join(diffs, "\n  "))
				}
			})
		}
	}
}

// The same calls inside organization B's OWN world must keep working: the
// guards refuse a foreign child, not a legitimate flow.
func TestOrgIsolationSweep_OwnOrganizationStillWorks(t *testing.T) {
	e := newSweepEnv(t)
	for _, row := range sweepRows() {
		if row.attackOnly {
			continue
		}
		row := row
		t.Run(row.hole+"/"+row.name, func(t *testing.T) {
			w := e.newWorld(t, e.orgB, e.orgB)
			ids := w.ids(e, e.orgB)
			body := ""
			if row.body != nil {
				body = row.body(ids)
			}
			status, out := e.do(t, strings.SplitN(row.route, " ", 2)[0], row.path(ids), e.owner, body)
			if row.controlStatus != 0 {
				if status != row.controlStatus {
					t.Errorf("own %s: status %d, want %d (%v)", row.name, status, row.controlStatus, out)
				}
				return
			}
			if status < 200 || status > 299 {
				t.Errorf("own %s: status %d, want 2xx (%v)", row.name, status, out)
			}
		})
	}
}

// H5 — who is the "partner org" of an external allocation. The session's
// ORGANIZER creates an allocation for a partner (the path organization) and may
// run every step of its life; the PARTNER may read its own allocation and
// report what it consumed or dispute it, but never creates one, never holds or
// settles the organizer's inventory, and never sees an allocation that names
// another partner.
func TestOrgIsolationSweep_ExternalAllocationModel(t *testing.T) {
	e := newSweepEnv(t)
	// Organization B is the PARTNER here; A organizes the session.
	orgA := e.newOrg(t, "A")
	w := e.newWorld(t, orgA, e.orgB)
	organizerTok := func() string {
		u, err := e.q.InsertUser(context.Background(), "sweep-organizer-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.q.InsertMembership(context.Background(), u.ID, orgA, "org_admin"); err != nil {
			t.Fatal(err)
		}
		tok, _, err := auth.IssueJWT(e.secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}()
	base := "/v1/organizations/" + e.orgB.String() + "/external-allocations"
	one := base + "/" + w.alloc.String()

	// The partner reads its own allocation...
	if st, out := e.do(t, http.MethodGet, one, e.owner, ""); st != http.StatusOK {
		t.Fatalf("partner get own allocation: %d %v", st, out)
	}
	// ...but may not create one on the organizer's session (the session is not its own).
	create := fmt.Sprintf(`{"session_id":%q,"quota_qty":2,"status":"pending"}`, w.s1)
	if st, out := e.do(t, http.MethodPost, base, e.owner, create); st != http.StatusNotFound {
		t.Errorf("partner creates an allocation on the organizer's session: %d %v, want 404", st, out)
	}
	// ...nor activate it (that holds the organizer's inventory).
	if st, out := e.do(t, http.MethodPatch, one, e.owner, `{"status":"active"}`); st != http.StatusForbidden {
		t.Errorf("partner activates the allocation: %d %v, want 403", st, out)
	}
	if got := e.snapshot(t, w)["allocations"]; !strings.Contains(got, ":pending:5:0") {
		t.Errorf("allocation moved by the partner: %s", got)
	}
	// An allocation that names another partner is invisible to B.
	other := uuid.New()
	e.exec(t, `INSERT INTO external_allocations (id, session_id, partner_org_id, quota_qty, status) VALUES ($1, $2, $3, 1, 'pending')`, other, w.s1, e.orgC)
	if st, out := e.do(t, http.MethodGet, base+"/"+other.String(), e.owner, ""); st != http.StatusNotFound {
		t.Errorf("partner reads another partner's allocation: %d %v, want 404", st, out)
	}

	// The organizer creates and activates one for the partner B through B's path.
	if st, out := e.do(t, http.MethodPost, base, organizerTok, create); st != http.StatusCreated {
		t.Fatalf("organizer creates an allocation for the partner: %d %v", st, out)
	}
	if st, out := e.do(t, http.MethodPatch, one, organizerTok, `{"status":"active"}`); st != http.StatusOK {
		t.Fatalf("organizer activates the allocation: %d %v", st, out)
	}
	// The organizer reads it too (a member of the session's organization).
	if st, out := e.do(t, http.MethodGet, one, organizerTok, ""); st != http.StatusOK {
		t.Errorf("organizer reads the allocation: %d %v", st, out)
	}
	// The partner may dispute it and report consumption without settling the inventory.
	if st, out := e.do(t, http.MethodPatch, one, e.owner, `{"status":"disputed","quota_consumed":2}`); st != http.StatusOK {
		t.Errorf("partner disputes the allocation: %d %v", st, out)
	}
	// Settling is the organizer's.
	if st, out := e.do(t, http.MethodPatch, one, e.owner, `{"status":"reconciled","quota_consumed":2}`); st != http.StatusForbidden {
		t.Errorf("partner reconciles the allocation: %d %v, want 403", st, out)
	}
	if st, out := e.do(t, http.MethodPatch, one, organizerTok, `{"status":"reconciled","quota_consumed":2}`); st != http.StatusOK {
		t.Errorf("organizer reconciles the allocation: %d %v", st, out)
	}
}
