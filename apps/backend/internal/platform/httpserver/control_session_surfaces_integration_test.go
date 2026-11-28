//go:build integration

// control_session_surfaces_integration_test.go — MA-40 (migration 0228) through
// the REAL router: the outside world never sees a control_only session, and an
// ordinary session next to it is unchanged.
//
//   - who may create a control session, and that a stranger gets the very
//     answer the neighbouring session route gives (TestControlSurfaces_Authorization);
//   - the database and the status route keep the container event unpublished
//     (TestControlSurfaces_DatabaseInvariant);
//   - the public feed, the widget payload, the hosted pages, the seat schema
//     routes and the sales-link target show the ordinary session and leave the
//     control one out (TestControlSurfaces_OutsideNeverSeesControlSession). The
//     checks run twice: on the natural state (a draft container) and on a
//     FORCED state, where the invariant is broken by a trigger-free write so
//     that only the sales_mode filter of each query stands between the control
//     session and the buyer;
//   - no hold, quote, checkout, invitation or complimentary ticket is made for
//     it (TestControlSurfaces_HoldsAreRefused);
//   - the organizer's own lists and the lock of the sales mode
//     (TestControlSurfaces_OrganizerListsAndModeLock).
//
// Run against a FRESH migrated database (AGENTS.md CI-Integration recipe).
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/apikeys"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const ctlSurfVisitorKey = "integration-control-hold-visitor-key-0123456789"

// ctlWorld is one organization (the w1a6c fixture: a published event with one
// ordinary on-sale GA session, a channel and a feed token), its people and a
// second organization that must stay out of it.
type ctlWorld struct {
	t      *testing.T
	srv    *Server
	pool   *pgxpool.Pool
	q      *gen.Queries
	secret string
	sfx    string
	f      *w1a6cFixture

	orgSlug   string
	eventSlug string // of the ordinary event
	// ordinary session S1 is f.sessionID (GA, one tier); S3 is the seated one.
	seatedSession uuid.UUID
	seatedTier    uuid.UUID
	planVersion   uuid.UUID
	planID        uuid.UUID

	owner, manager, agent, stranger string // JWTs
	key                             string // organization API key with session.create
	keyUser                         uuid.UUID
	users                           []uuid.UUID

	orgB     uuid.UUID
	venueB   uuid.UUID
	eventB   uuid.UUID
	ownerB   string
	keyB     string
	promoter uuid.UUID
	promSlug string

	ctlVenue     uuid.UUID
	ctlVenueName string
	ctlTitle     string
}

func newCtlWorld(t *testing.T, cfgMutate func(*config.Config)) *ctlWorld {
	t.Helper()
	srv, secret := productionIntegrationServerCfg(t, cfgMutate)
	pool := srv.pgxPool
	ctx := context.Background()
	w := &ctlWorld{t: t, srv: srv, pool: pool, q: gen.New(pool), secret: secret}

	w.f = newW1A6cFixture(t, ctx, pool)
	f := w.f
	w.sfx = f.orgID.String()[:8]
	t.Cleanup(f.cleanup)
	t.Cleanup(w.cleanup)

	w.orgSlug = "w1a6c-" + w.sfx
	w.eventSlug = "ctl-ord-" + w.sfx
	w.exec(`UPDATE events SET slug = $2 WHERE id = $1`, f.eventID, w.eventSlug)
	w.exec(`UPDATE sales_channels SET settings = '{"hosted_page":{"enabled":true}}'::jsonb WHERE id = $1`, f.channelID)

	// People of organization A: owner, manager (membership role organizer),
	// agent, and a user who is a member of nothing.
	mk := func(role string) string {
		u, err := w.q.InsertUser(ctx, "ctl-surf-"+role+"-"+uuid.NewString()+"@example.test", "x", "en")
		if err != nil {
			t.Fatalf("InsertUser: %v", err)
		}
		w.users = append(w.users, u.ID)
		if role != "" {
			if _, err := w.q.InsertMembership(ctx, u.ID, f.orgID, role); err != nil {
				t.Fatalf("InsertMembership(%s): %v", role, err)
			}
		}
		return w.jwt(u.ID)
	}
	w.owner, w.manager, w.agent, w.stranger = mk("org_admin"), mk("organizer"), mk("agent"), mk("")

	keyUser, err := w.q.InsertUser(ctx, "ctl-surf-key-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	w.users = append(w.users, keyUser.ID)
	w.keyUser = keyUser.ID
	_, w.key, err = apikeys.Issue(ctx, apikeys.NewStoreFromQueries(w.q), apikeys.IssueInput{
		OrgID: f.orgID, Name: "ctl-surf-" + w.sfx, Scopes: []string{"session.create", "session.read"}, CreatedBy: keyUser.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue: %v", err)
	}

	// The venue of the control session has a name no other row carries, so a
	// leak is found by a plain search of the answer.
	w.ctlVenue = uuid.New()
	w.ctlVenueName = "CTRLVENUE-" + w.sfx
	w.ctlTitle = "CTRLTITLE-" + w.sfx
	w.exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, w.ctlVenue, f.orgID, w.ctlVenueName)

	// Organization B: its own owner, venue, event and API key.
	orgB, err := w.q.InsertOrganization(ctx, "Ctl B "+w.sfx, "ctl-b-"+w.sfx, "EE", "en", 1200)
	if err != nil {
		t.Fatalf("InsertOrganization B: %v", err)
	}
	w.orgB = orgB.ID
	w.venueB = uuid.New()
	w.exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Madrid')`, w.venueB, w.orgB, "Ctl Venue B "+w.sfx)
	evB, err := w.q.InsertEvent(ctx, w.orgB, "Ctl Event B "+w.sfx, nil, "draft", "private", nil, "text")
	if err != nil {
		t.Fatalf("InsertEvent B: %v", err)
	}
	w.eventB = evB.ID
	ownerB, err := w.q.InsertUser(ctx, "ctl-surf-ownerb-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser B: %v", err)
	}
	w.users = append(w.users, ownerB.ID)
	if _, err := w.q.InsertMembership(ctx, ownerB.ID, w.orgB, "org_admin"); err != nil {
		t.Fatalf("InsertMembership B: %v", err)
	}
	w.ownerB = w.jwt(ownerB.ID)
	_, w.keyB, err = apikeys.Issue(ctx, apikeys.NewStoreFromQueries(w.q), apikeys.IssueInput{
		OrgID: w.orgB, Name: "ctl-surf-b-" + w.sfx, Scopes: []string{"session.create", "session.read", "event.read"}, CreatedBy: ownerB.ID,
	})
	if err != nil {
		t.Fatalf("apikeys.Issue B: %v", err)
	}
	return w
}

func (w *ctlWorld) jwt(user uuid.UUID) string {
	w.t.Helper()
	tok, _, err := auth.IssueJWT(w.secret, user, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		w.t.Fatalf("IssueJWT: %v", err)
	}
	return tok
}

func (w *ctlWorld) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.pool.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("exec (%.80s): %v", sql, err)
	}
}

func (w *ctlWorld) cleanup() {
	ctx := context.Background()
	orgs := []uuid.UUID{w.f.orgID, w.orgB}
	// Everything of both organizations except the organization A row, its
	// channel and feed token: the w1a6c fixture's own cleanup (it runs next)
	// removes those, and every statement of it is a no-op on rows gone.
	inOrgSessions := `(SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = ANY($1))`
	for _, sql := range []string{
		`DELETE FROM sales_link_hits WHERE sales_link_id IN (SELECT id FROM sales_links WHERE org_id = ANY($1))`,
		`DELETE FROM sales_links WHERE org_id = ANY($1)`,
		`DELETE FROM event_promoters WHERE org_id = ANY($1)`,
		`DELETE FROM org_promoters WHERE org_id = ANY($1)`,
		`DELETE FROM api_keys WHERE org_id = ANY($1)`,
		`DELETE FROM reservation_ga_items WHERE reservation_id IN (SELECT id FROM reservations WHERE org_id = ANY($1))`,
		`DELETE FROM reservation_seats WHERE reservation_id IN (SELECT id FROM reservations WHERE org_id = ANY($1))`,
		`UPDATE session_seats SET reservation_id = NULL WHERE session_id IN ` + inOrgSessions,
		`UPDATE reservations SET rehold_of = NULL WHERE org_id = ANY($1)`,
		`DELETE FROM reservations WHERE org_id = ANY($1)`,
		`DELETE FROM checkout_sessions WHERE org_id = ANY($1)`,
		`DELETE FROM session_seats WHERE session_id IN ` + inOrgSessions,
		`DELETE FROM inventory_ledger WHERE session_id IN ` + inOrgSessions,
		`DELETE FROM ticket_tiers WHERE session_id IN ` + inOrgSessions,
		`DELETE FROM event_publications WHERE event_id IN (SELECT id FROM events WHERE org_id = ANY($1))`,
		`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = ANY($1))`,
		`DELETE FROM events WHERE org_id = ANY($1)`,
		`DELETE FROM seating_plan_versions WHERE seating_plan_id IN (SELECT id FROM seating_plans WHERE owner_org_id = ANY($1))`,
		`DELETE FROM seating_plans WHERE owner_org_id = ANY($1)`,
		`DELETE FROM venues WHERE org_id = ANY($1)`,
		`DELETE FROM memberships WHERE org_id = ANY($1)`,
	} {
		if _, err := w.pool.Exec(ctx, sql, orgs); err != nil {
			w.t.Logf("control surfaces cleanup (%.70s): %v", sql, err)
		}
	}
	if _, err := w.pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, w.orgB); err != nil {
		w.t.Logf("control surfaces cleanup org B: %v", err)
	}
	if _, err := w.pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, append(append([]uuid.UUID{}, w.users...), w.keyUser)); err != nil {
		w.t.Logf("control surfaces cleanup users: %v", err)
	}
}

// call sends one request through the real router. token is a JWT or an API
// key; "" is an anonymous (public) caller.
func (w *ctlWorld) call(token, method, path, body string) (int, map[string]any, string) {
	w.t.Helper()
	var rd *bytes.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	w.srv.router.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func (w *ctlWorld) orgBase() string { return "/v1/organizations/" + w.f.orgID.String() }

func (w *ctlWorld) controlBody(title string) string {
	start := time.Now().UTC().Add(45 * 24 * time.Hour).Truncate(time.Minute)
	return `{"title":"` + title + `","venue_id":"` + w.ctlVenue.String() + `","start_at":"` + start.Format(time.RFC3339) + `","capacity":120,"currency":"EUR"}`
}

// createControl creates a control session as the given caller and returns the
// ids; it fails the test unless the answer is 201.
func (w *ctlWorld) createControl(token, title string) (eventID, sessionID uuid.UUID) {
	w.t.Helper()
	st, out, raw := w.call(token, http.MethodPost, w.orgBase()+"/control-sessions", w.controlBody(title))
	if st != http.StatusCreated {
		w.t.Fatalf("create control session: %d %s", st, raw)
	}
	ev, _ := out["event"].(map[string]any)
	se, _ := out["session"].(map[string]any)
	eid, err1 := uuid.Parse(asString(ev["id"]))
	sid, err2 := uuid.Parse(asString(se["id"]))
	if err1 != nil || err2 != nil {
		w.t.Fatalf("create control session: bad ids in %s", raw)
	}
	return eid, sid
}

func asString(v any) string { s, _ := v.(string); return s }

func ctlErrCode(m map[string]any) string { return scErrCode(m) }

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// addSeatedSession gives the organization a SEATED ordinary session S3 (plan,
// one category, three places) in the ordinary event, so the seat schema routes
// have an ordinary session to answer 200 for.
func (w *ctlWorld) addSeatedSession() {
	w.t.Helper()
	f := w.f
	g := planExtraGeometry()
	raw, err := json.Marshal(g)
	if err != nil {
		w.t.Fatal(err)
	}
	sum, err := seating.Checksum(g)
	if err != nil {
		w.t.Fatal(err)
	}
	w.planID, w.planVersion = uuid.New(), uuid.New()
	w.seatedSession, w.seatedTier = uuid.New(), uuid.New()
	w.exec(`INSERT INTO seating_plans (id, venue_id, owner_org_id, name, plan_type, status) VALUES ($1, $2, $3, $4, 'assigned_seats', 'active')`,
		w.planID, f.venueID, f.orgID, "Ctl Plan "+w.sfx)
	w.exec(`INSERT INTO seating_plan_versions (id, seating_plan_id, version_number, geometry, geometry_checksum, capacity_seated)
	        VALUES ($1, $2, 1, $3::jsonb, $4, 3)`, w.planVersion, w.planID, string(raw), sum)
	w.exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
	            admission_mode, currency, currency_source, seating_plan_version_id)
	        VALUES ($1, $2, $3, now() + interval '31 days', now() + interval '31 days 2 hours', 3, 'scheduled',
	            'assigned_seats', 'EUR', 'override', $4)`, w.seatedSession, f.eventID, f.venueID, w.planVersion)
	w.exec(`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order)
	        VALUES ($1, $2, 'Ctl Seated Tier', 'fixed', 2500, 'EUR', 0)`, w.seatedTier, w.seatedSession)
	w.exec(`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 3)`, w.seatedSession)
	for _, n := range []string{"1", "2", "3"} {
		w.exec(`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status)
		        VALUES ($1, $2, 'Hall', 'A', $3, $4, 'available')`, w.seatedSession, "A-0"+n, n, w.seatedTier)
	}
}

// forcePublish breaks the invariant of a control event on purpose: it writes
// 'published' with a connection that does not fire triggers (the database
// guard would refuse it), gives the event a slug and publishes it into the
// ordinary channel, and gives the control session a seated shape (plan,
// assigned_seats). From then on only the sales_mode filter of each public
// query hides the control session. The connection is closed, never returned
// to the pool, so the replica role cannot leak into another test.
func (w *ctlWorld) forcePublish(eventID, sessionID uuid.UUID, slug string) {
	w.t.Helper()
	ctx := context.Background()
	pc, err := w.pool.Acquire(ctx)
	if err != nil {
		w.t.Fatalf("acquire: %v", err)
	}
	conn := pc.Hijack()
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		w.t.Fatalf("cannot switch triggers off for the forced state (needs a superuser): %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE events SET status = 'published', visibility = 'public', slug = $2 WHERE id = $1`, eventID, slug); err != nil {
		w.t.Fatalf("force publish: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE sessions SET admission_mode = 'assigned_seats', seating_plan_version_id = $2 WHERE id = $1`, sessionID, w.planVersion); err != nil {
		w.t.Fatalf("force seated shape: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO event_publications (event_id, feed_token_id) VALUES ($1, $2)`, eventID, w.f.tokenID); err != nil {
		w.t.Fatalf("force publication: %v", err)
	}
	var status, mode string
	if err := conn.QueryRow(ctx, `SELECT e.status, s.sales_mode FROM events e JOIN sessions s ON s.event_id = e.id WHERE s.id = $1`, sessionID).Scan(&status, &mode); err != nil {
		w.t.Fatal(err)
	}
	if status != "published" || mode != "control_only" {
		w.t.Fatalf("forced state is %q / %q, want published / control_only", status, mode)
	}
}

// setSalesMode writes sessions.sales_mode with a connection that fires no
// trigger (the guard of migration 0228 would refuse the change on a published
// container). The connection is closed afterwards.
func (w *ctlWorld) setSalesMode(sessionID uuid.UUID, mode string) {
	w.t.Helper()
	ctx := context.Background()
	pc, err := w.pool.Acquire(ctx)
	if err != nil {
		w.t.Fatalf("acquire: %v", err)
	}
	conn := pc.Hijack()
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		w.t.Fatalf("cannot switch triggers off: %v", err)
	}
	if _, err := conn.Exec(ctx, `UPDATE sessions SET sales_mode = $2 WHERE id = $1`, sessionID, mode); err != nil {
		w.t.Fatalf("set sales_mode %s: %v", mode, err)
	}
}

// refusedPublic reports whether a public hold/quote/checkout answer is one of
// the documented refusals of a session that is not on sale here.
func refusedPublic(st int, code string) bool {
	switch code {
	case "feed.session_not_on_feed":
		return st == http.StatusForbidden || st == http.StatusNotFound
	case "session.sold_externally":
		return st == http.StatusConflict
	}
	return false
}

// ── (1) who may create, and the stranger's answer ────────────────────────────

func TestControlSurfaces_Authorization(t *testing.T) {
	w := newCtlWorld(t, nil)
	ctx := context.Background()
	f := w.f

	// The neighbouring route: POST .../events/{event_id}/sessions of the
	// ordinary event with the same body shape. A control session must be as
	// open (and as closed) as it.
	start := time.Now().UTC().Add(50 * 24 * time.Hour).Truncate(time.Minute)
	ordinary := func(venue uuid.UUID) string {
		return `{"venue_id":"` + venue.String() + `","start_at":"` + start.Format(time.RFC3339) +
			`","end_at":"` + start.Add(2*time.Hour).Format(time.RFC3339) + `","currency":"EUR"}`
	}
	ctlPath := w.orgBase() + "/control-sessions"
	body := w.controlBody(w.ctlTitle)

	countControl := func() int {
		var n int
		if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM sessions s JOIN events e ON e.id = s.event_id
		  WHERE e.org_id = $1 AND s.sales_mode = 'control_only'`, f.orgID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The owner (org_admin) creates one, and so does an organization API key
	// with session.create.
	st, out, raw := w.call(w.owner, http.MethodPost, ctlPath, body)
	if st != http.StatusCreated {
		t.Fatalf("owner create: %d %s", st, raw)
	}
	if se, _ := out["session"].(map[string]any); se["sales_mode"] != "control_only" {
		t.Fatalf("owner create: session = %v", se)
	}
	if st, _, raw = w.call(w.key, http.MethodPost, ctlPath, body); st != http.StatusCreated {
		t.Fatalf("API key (session.create) create: %d %s", st, raw)
	}
	if n := countControl(); n != 2 {
		t.Fatalf("control sessions after two creates = %d, want 2", n)
	}

	// Callers who may not: each gets the answer the neighbouring route gives.
	type caller struct {
		name  string
		token string
		path  string // org path used for both routes
		venue uuid.UUID
	}
	otherBase := "/v1/organizations/" + w.orgB.String()
	callers := []caller{
		// The manager (membership role organizer) holds session.read and
		// session.update but NOT session.create (migrations 0016, 0122): the
		// control route is gated like POST .../sessions, so the manager gets
		// the same 403 on both.
		{"manager (organizer, no session.create)", w.manager, w.orgBase(), f.venueID},
		{"agent (no session.create)", w.agent, w.orgBase(), f.venueID},
		{"user with no membership", w.stranger, w.orgBase(), f.venueID},
		{"owner of ANOTHER organization", w.ownerB, w.orgBase(), f.venueID},
		{"API key of ANOTHER organization", w.keyB, w.orgBase(), f.venueID},
		{"anonymous", "", w.orgBase(), f.venueID},
	}
	for _, c := range callers {
		t.Run(c.name, func(t *testing.T) {
			before := countControl()
			stN, outN, rawN := w.call(c.token, http.MethodPost, c.path+"/events/"+f.eventID.String()+"/sessions", ordinary(c.venue))
			stC, outC, rawC := w.call(c.token, http.MethodPost, c.path+"/control-sessions", body)
			if stC == http.StatusCreated || stC >= http.StatusInternalServerError {
				t.Fatalf("control create answered %d %s", stC, rawC)
			}
			if stC != stN || ctlErrCode(outC) != ctlErrCode(outN) {
				t.Errorf("control route %d %q differs from the neighbouring route %d %q\n control:  %s\n ordinary: %s",
					stC, ctlErrCode(outC), stN, ctlErrCode(outN), rawC, rawN)
			}
			if stC != http.StatusForbidden && stC != http.StatusUnauthorized && stC != http.StatusNotFound {
				t.Errorf("control create answered %d, want 401/403/404", stC)
			}
			if after := countControl(); after != before {
				t.Errorf("a refused create left %d new control sessions", after-before)
			}
		})
	}

	// Another organization's venue: the owner of B uses B's path with the
	// venue of A (and the reverse). The answer is the neighbouring route's
	// one, and nothing is written.
	t.Run("another organization's venue", func(t *testing.T) {
		var eventsB int
		_ = w.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE org_id = $1`, w.orgB).Scan(&eventsB)
		ordB := otherBase + "/events/" + w.eventB.String() + "/sessions"
		for _, tc := range []struct {
			name  string
			token string
		}{{"JWT owner of B", w.ownerB}, {"API key of B", w.keyB}} {
			stN, outN, rawN := w.call(tc.token, http.MethodPost, ordB, ordinary(f.venueID))
			ctlBody := strings.Replace(body, w.ctlVenue.String(), f.venueID.String(), 1)
			stC, outC, rawC := w.call(tc.token, http.MethodPost, otherBase+"/control-sessions", ctlBody)
			if stC == http.StatusCreated {
				t.Fatalf("%s: a control session was created with another organization's venue: %s", tc.name, rawC)
			}
			if stC != stN || ctlErrCode(outC) != ctlErrCode(outN) {
				t.Errorf("%s: control route %d %q differs from the neighbouring route %d %q\n control:  %s\n ordinary: %s",
					tc.name, stC, ctlErrCode(outC), stN, ctlErrCode(outN), rawC, rawN)
			}
		}
		var after int
		_ = w.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE org_id = $1`, w.orgB).Scan(&after)
		if after != eventsB {
			t.Errorf("a refused create left %d events in organization B", after-eventsB)
		}
		// Positive control: B's own venue works for B.
		okBody := strings.Replace(body, w.ctlVenue.String(), w.venueB.String(), 1)
		if st, _, raw := w.call(w.ownerB, http.MethodPost, otherBase+"/control-sessions", okBody); st != http.StatusCreated {
			t.Errorf("owner of B with B's own venue: %d %s", st, raw)
		}
	})
}

// ── (2) the database and the status route keep the container unpublished ─────

func TestControlSurfaces_DatabaseInvariant(t *testing.T) {
	w := newCtlWorld(t, nil)
	ctx := context.Background()
	f := w.f
	eventID, sessionID := w.createControl(w.owner, w.ctlTitle)

	eventStatus := func() string {
		var s string
		if err := w.pool.QueryRow(ctx, `SELECT status FROM events WHERE id = $1`, eventID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The status route: 409 event.control_only, for the owner and the manager.
	for name, tok := range map[string]string{"owner": w.owner, "manager": w.manager} {
		st, out, raw := w.call(tok, http.MethodPost, w.orgBase()+"/events/"+eventID.String()+"/status", `{"status":"published"}`)
		if st != http.StatusConflict || ctlErrCode(out) != "event.control_only" {
			t.Errorf("%s publish: %d %s, want 409 event.control_only", name, st, raw)
		}
	}
	if s := eventStatus(); s != "draft" {
		t.Fatalf("event status %q after refused publishes", s)
	}

	// The database: a direct write to 'published' is refused with 23514.
	if _, err := w.pool.Exec(ctx, `UPDATE events SET status = 'published' WHERE id = $1`, eventID); pgCode(err) != "23514" {
		t.Errorf("direct UPDATE to published: err = %v, want SQLSTATE 23514", err)
	}
	if s := eventStatus(); s != "draft" {
		t.Fatalf("event status %q after a refused direct update", s)
	}

	// A platform session never joins the control event (23514), nor an external one.
	for _, mode := range []string{"platform", "external"} {
		extra := `NULL, NULL`
		if mode == "external" {
			extra = `'https://www.eventim.de/ctl', 'Eventim'`
		}
		_, err := w.pool.Exec(ctx, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
		        admission_mode, currency, currency_source, sales_mode, external_ticket_url, external_seller_name)
		    VALUES ($1, $2, $3, now() + interval '40 days', now() + interval '40 days 2 hours', 5, 'scheduled',
		        'general_admission', 'EUR', 'override', $4, `+extra+`)`, uuid.New(), eventID, w.ctlVenue, mode)
		if pgCode(err) != "23514" {
			t.Errorf("a %s session in the control event: err = %v, want SQLSTATE 23514", mode, err)
		}
	}

	// A control session never joins a published event (the ordinary event is
	// published), and an ordinary session of such an event is never flipped.
	_, err := w.pool.Exec(ctx, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status,
	        admission_mode, currency, currency_source, sales_mode)
	    VALUES ($1, $2, $3, now() + interval '40 days', now() + interval '40 days 2 hours', 5, 'scheduled',
	        'general_admission', 'EUR', 'override', 'control_only')`, uuid.New(), f.eventID, w.ctlVenue)
	if pgCode(err) != "23514" {
		t.Errorf("a control session in the published ordinary event: err = %v, want SQLSTATE 23514", err)
	}
	if _, err := w.pool.Exec(ctx, `UPDATE sessions SET sales_mode = 'control_only' WHERE id = $1`, f.sessionID); pgCode(err) != "23514" {
		t.Errorf("flipping the ordinary session to control_only: err = %v, want SQLSTATE 23514", err)
	}
	var mode string
	_ = w.pool.QueryRow(ctx, `SELECT sales_mode FROM sessions WHERE id = $1`, f.sessionID).Scan(&mode)
	if mode != "platform" {
		t.Errorf("ordinary session sales_mode = %q after the refused flip", mode)
	}
	var live int
	_ = w.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE event_id = $1 AND deleted_at IS NULL`, eventID).Scan(&live)
	if live != 1 {
		t.Errorf("the control event holds %d live sessions after the refused inserts, want 1 (%s)", live, sessionID)
	}
}

// ── (3) the outside world ────────────────────────────────────────────────────

// ctlSurfaceChecks runs every public surface and fails when the control
// session shows. The ordinary session must show on each one that has sessions.
func (w *ctlWorld) ctlSurfaceChecks(t *testing.T, ctlEvent, ctlSession uuid.UUID, ctlSlug string) {
	f := w.f
	leaks := []string{ctlSession.String(), w.ctlVenueName}
	noLeak := func(t *testing.T, where, body string) {
		t.Helper()
		for _, needle := range leaks {
			if strings.Contains(body, needle) {
				t.Errorf("%s shows the control session (%q found): %.600s", where, needle, body)
			}
		}
	}
	feed := "/v1/public/feeds/" + f.feedToken

	t.Run("public feed list", func(t *testing.T) {
		st, _, raw := w.call("", http.MethodGet, feed+"/events?per_page=100", "")
		if st != http.StatusOK {
			t.Fatalf("feed list: %d %s", st, raw)
		}
		if !strings.Contains(raw, f.eventID.String()) {
			t.Errorf("the ordinary event is missing from the feed list: %.400s", raw)
		}
		noLeak(t, "feed list", raw)
		t.Logf("control event listed in the feed: %v", strings.Contains(raw, ctlEvent.String()))
	})

	t.Run("public feed detail and widget session payload", func(t *testing.T) {
		st, out, raw := w.call("", http.MethodGet, feed+"/events/"+f.eventID.String(), "")
		if st != http.StatusOK {
			t.Fatalf("feed detail of the ordinary event: %d %s", st, raw)
		}
		if !strings.Contains(raw, f.sessionID.String()) || !strings.Contains(raw, w.seatedSession.String()) {
			t.Errorf("the ordinary sessions are missing from the feed detail: %.600s", raw)
		}
		ev, _ := out["event"].(map[string]any)
		if sessions, _ := ev["sessions"].([]any); len(sessions) != 2 {
			t.Errorf("feed detail lists %d sessions, want the 2 ordinary ones", len(sessions))
		}
		noLeak(t, "feed detail", raw)

		// The control event itself: no session of it is ever on the feed.
		st, _, raw = w.call("", http.MethodGet, feed+"/events/"+ctlEvent.String(), "")
		if st != http.StatusOK && st != http.StatusNotFound {
			t.Errorf("feed detail of the control event: %d %s, want 404 or an event with no sessions", st, raw)
		}
		noLeak(t, "feed detail of the control event", raw)
		if st == http.StatusOK {
			var env struct {
				Event struct {
					Sessions []any `json:"sessions"`
				} `json:"event"`
			}
			_ = json.Unmarshal([]byte(raw), &env)
			if len(env.Event.Sessions) != 0 {
				t.Errorf("feed detail of the control event lists %d sessions", len(env.Event.Sessions))
			}
		}
	})

	t.Run("hosted page of the event", func(t *testing.T) {
		st, out, raw := w.call("", http.MethodGet, "/v1/public/pages/"+w.orgSlug+"/"+w.eventSlug, "")
		if st != http.StatusOK {
			t.Fatalf("hosted page: %d %s", st, raw)
		}
		ev, _ := out["event"].(map[string]any)
		if ev["session_count"] != float64(2) {
			t.Errorf("hosted page session_count = %v, want 2", ev["session_count"])
		}
		noLeak(t, "hosted page", raw)
		if ctlSlug != "" {
			st, out, raw = w.call("", http.MethodGet, "/v1/public/pages/"+w.orgSlug+"/"+ctlSlug, "")
			if st == http.StatusOK {
				cev, _ := out["event"].(map[string]any)
				if cev["session_count"] != float64(0) {
					t.Errorf("hosted page of the control event: session_count = %v, want 0", cev["session_count"])
				}
			}
			noLeak(t, "hosted page of the control event", raw)
		}
	})

	t.Run("hosted page of the organization", func(t *testing.T) {
		st, out, raw := w.call("", http.MethodGet, "/v1/public/pages/"+w.orgSlug, "")
		if st != http.StatusOK {
			t.Fatalf("organization page: %d %s", st, raw)
		}
		if !strings.Contains(raw, f.eventID.String()) {
			t.Errorf("the ordinary event is missing from the organization page: %.400s", raw)
		}
		noLeak(t, "organization page", raw)
		events, _ := out["events"].([]any)
		for _, e := range events {
			if m, _ := e.(map[string]any); m["id"] == ctlEvent.String() && m["session_count"] != float64(0) {
				t.Errorf("organization page: control event session_count = %v, want 0", m["session_count"])
			}
		}
	})

	t.Run("hosted page of the promoter", func(t *testing.T) {
		st, out, raw := w.call("", http.MethodGet, "/v1/public/pages/"+w.promSlug, "")
		if st != http.StatusOK {
			t.Fatalf("promoter page: %d %s", st, raw)
		}
		if !strings.Contains(raw, f.eventID.String()) {
			t.Errorf("the ordinary event is missing from the promoter page: %.400s", raw)
		}
		noLeak(t, "promoter page", raw)
		events, _ := out["events"].([]any)
		for _, e := range events {
			if m, _ := e.(map[string]any); m["id"] == ctlEvent.String() && m["session_count"] != float64(0) {
				t.Errorf("promoter page: control event session_count = %v, want 0", m["session_count"])
			}
		}
	})

	t.Run("public seat schema, seat status and layout by session id", func(t *testing.T) {
		for _, tail := range []string{"/schema", "/seat-status", "/layout.svg"} {
			st, _, raw := w.call("", http.MethodGet, "/v1/event-sessions/"+ctlSession.String()+tail, "")
			if st != http.StatusNotFound {
				t.Errorf("control session %s: %d %.300s, want 404", tail, st, raw)
			}
			// Regression: the ordinary seated session answers.
			st, _, raw = w.call("", http.MethodGet, "/v1/event-sessions/"+w.seatedSession.String()+tail, "")
			if st != http.StatusOK {
				t.Errorf("ordinary seated session %s: %d %.300s, want 200", tail, st, raw)
			}
		}
	})

	t.Run("sales link target", func(t *testing.T) {
		links := w.orgBase() + "/sales-links"
		st, out, raw := w.call(w.owner, http.MethodPost, links,
			`{"target":{"kind":"session","session_id":"`+ctlSession.String()+`"},"label":"Control date","placement_kind":"flyer"}`)
		if st != http.StatusUnprocessableEntity || ctlErrCode(out) != "sales_link.invalid_target" {
			t.Errorf("sales link to the control session: %d %s, want 422 sales_link.invalid_target", st, raw)
		}
		st, _, raw = w.call(w.owner, http.MethodPost, links,
			`{"target":{"kind":"session","session_id":"`+f.sessionID.String()+`"},"label":"Ordinary date","placement_kind":"flyer"}`)
		if st != http.StatusCreated {
			t.Errorf("sales link to the ordinary session: %d %s, want 201", st, raw)
		}
	})
}

func TestControlSurfaces_OutsideNeverSeesControlSession(t *testing.T) {
	w := newCtlWorld(t, nil)
	f := w.f
	w.addSeatedSession()

	// Promoter page: the promoter owns the ordinary event (and, below, the
	// control one).
	w.promoter = uuid.New()
	w.promSlug = "ctl-prom-" + w.sfx
	w.exec(`INSERT INTO org_promoters (id, org_id, name, slug) VALUES ($1, $2, $3, $4)`, w.promoter, f.orgID, "Ctl Promoter "+w.sfx, w.promSlug)
	w.exec(`INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, f.eventID, f.orgID, w.promoter)

	ctlEvent, ctlSession := w.createControl(w.owner, w.ctlTitle)

	t.Run("natural state: the container is a draft", func(t *testing.T) {
		w.ctlSurfaceChecks(t, ctlEvent, ctlSession, "")
	})

	// Forced state: the container is published (the invariant is broken with a
	// trigger-free write), listed in the channel and linked to the promoter;
	// the control session even looks seated. Only the sales_mode filters of
	// the queries keep it away from the buyer now.
	ctlSlug := "ctl-box-" + w.sfx
	w.forcePublish(ctlEvent, ctlSession, ctlSlug)
	w.exec(`INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, ctlEvent, f.orgID, w.promoter)
	t.Run("forced state: the container is published", func(t *testing.T) {
		w.ctlSurfaceChecks(t, ctlEvent, ctlSession, ctlSlug)
	})

	// Positive control: the checks above would notice a leak. The very same
	// row, written as a platform session, IS served by the schema routes and
	// IS counted and named on the hosted page. Last, because the schema is
	// cached per session.
	t.Run("positive control: the forced session as a platform session shows", func(t *testing.T) {
		w.setSalesMode(ctlSession, "platform")
		defer w.setSalesMode(ctlSession, "control_only")
		for _, tail := range []string{"/schema", "/seat-status"} {
			if st, _, raw := w.call("", http.MethodGet, "/v1/event-sessions/"+ctlSession.String()+tail, ""); st != http.StatusOK {
				t.Errorf("platform-mode session %s: %d %.300s, want 200", tail, st, raw)
			}
		}
		st, out, raw := w.call("", http.MethodGet, "/v1/public/pages/"+w.orgSlug+"/"+ctlSlug, "")
		if st != http.StatusOK {
			t.Fatalf("hosted page of the forced event: %d %s", st, raw)
		}
		if ev, _ := out["event"].(map[string]any); ev["session_count"] != float64(1) {
			t.Errorf("platform-mode session is not counted on the hosted page: session_count = %v", ev["session_count"])
		}
		if !strings.Contains(raw, w.ctlVenueName) {
			t.Errorf("platform-mode session's venue is not on the hosted page: %.400s", raw)
		}
	})
	var mode string
	if err := w.pool.QueryRow(context.Background(), `SELECT sales_mode FROM sessions WHERE id = $1`, ctlSession).Scan(&mode); err != nil || mode != "control_only" {
		t.Fatalf("the control session is %q (%v) after the positive control, want control_only", mode, err)
	}
}

// ── (4) holds ────────────────────────────────────────────────────────────────

func TestControlSurfaces_HoldsAreRefused(t *testing.T) {
	w := newCtlWorld(t, func(c *config.Config) {
		c.HoldOnClickEnabled = true
		c.HoldVisitorKey = ctlSurfVisitorKey
	})
	ctx := context.Background()
	f := w.f
	w.addSeatedSession()
	ctlEvent, ctlSession := w.createControl(w.owner, w.ctlTitle)
	feed := "/v1/public/feeds/" + f.feedToken

	rows := func(session uuid.UUID) (reservations, seats, ledger, tickets, orders int) {
		t.Helper()
		if err := w.pool.QueryRow(ctx, `SELECT
		    (SELECT count(*) FROM reservations WHERE session_id = $1),
		    (SELECT count(*) FROM session_seats WHERE session_id = $1),
		    (SELECT count(*) FROM inventory_ledger WHERE session_id = $1),
		    (SELECT count(*) FROM tickets WHERE session_id = $1),
		    (SELECT count(*) FROM orders WHERE session_id = $1)`, session).Scan(&reservations, &seats, &ledger, &tickets, &orders); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		return
	}
	checkNoRows := func(t *testing.T) {
		t.Helper()
		if r, s, l, tk, o := rows(ctlSession); r+s+l+tk+o != 0 {
			t.Errorf("rows exist for the control session: reservations %d, seats %d, ledger %d, tickets %d, orders %d", r, s, l, tk, o)
		}
		var checkouts int
		_ = w.pool.QueryRow(ctx, `SELECT count(*) FROM checkout_sessions WHERE org_id = $1`, f.orgID).Scan(&checkouts)
		if checkouts != 0 {
			t.Errorf("%d checkout sessions exist; the control session must not start one", checkouts)
		}
	}

	// Every refusal is checked on the natural state (a draft container, a
	// general-admission session without places) and again on the forced one
	// (published container, seated shape, listed on the channel), where only
	// the sales mode stands in the way.
	refusals := func(t *testing.T) {
		t.Run("widget hold", func(t *testing.T) {
			st, out, raw := w.call("", http.MethodPost, feed+"/holds", `{"session_id":"`+ctlSession.String()+`","seat_keys":["A-01"],"device_id":"ctl-device-1"}`)
			if !refusedPublic(st, ctlErrCode(out)) {
				t.Errorf("hold on the control session: %d %s, want feed.session_not_on_feed (403/404) or 409 session.sold_externally", st, raw)
			}
			checkNoRows(t)
		})

		t.Run("widget quote and checkout start", func(t *testing.T) {
			quote := `{"session_id":"` + ctlSession.String() + `","ga_items":[{"tier_id":"` + uuid.NewString() + `","quantity":1}]}`
			st, out, raw := w.call("", http.MethodPost, feed+"/checkout/quote", quote)
			if !refusedPublic(st, ctlErrCode(out)) {
				t.Errorf("quote on the control session: %d %s", st, raw)
			}
			start := `{"session_id":"` + ctlSession.String() + `","return_url":"` + hostedTicketsBaseURL + `/embed","tier_id":"` + uuid.NewString() +
				`","qty":1,"buyer":{"email":"ctl-buyer-` + w.sfx + `@arena-integration.test","name":"Ctl Buyer","phone":"+36300000001"}}`
			st, out, raw = w.call("", http.MethodPost, feed+"/checkout/start", start)
			if !refusedPublic(st, ctlErrCode(out)) {
				t.Errorf("checkout start on the control session: %d %s", st, raw)
			}
			checkNoRows(t)
		})

		t.Run("REST reservation", func(t *testing.T) {
			for name, body := range map[string]string{
				"seats": `{"session_id":"` + ctlSession.String() + `","channel_id":"` + f.channelID.String() + `","org_id":"` + f.orgID.String() + `","seats":["A-01"]}`,
				"GA":    `{"session_id":"` + ctlSession.String() + `","channel_id":"` + f.channelID.String() + `","org_id":"` + f.orgID.String() + `","tier_id":"` + uuid.NewString() + `","quantity":1}`,
			} {
				st, out, raw := w.call(w.owner, http.MethodPost, "/v1/reservations", body)
				if st < 400 || st >= 500 {
					t.Errorf("REST %s reservation on the control session: %d %s, want a 4xx refusal", name, st, raw)
				}
				t.Logf("REST %s reservation on the control session: %d %s", name, st, ctlErrCode(out))
			}
			checkNoRows(t)
		})

		t.Run("staff invitation", func(t *testing.T) {
			inv := `{"request_id":"` + uuid.NewString() + `","ga":[{"tier_id":"` + uuid.NewString() + `","qty":1}],` +
				`"guest":{"name":"Guest","email":"ctl-guest-` + w.sfx + `@example.test"},"delivery":"guest_email"}`
			for name, tok := range map[string]string{"owner": w.owner, "manager": w.manager} {
				st, out, raw := w.call(tok, http.MethodPost, w.orgBase()+"/sessions/"+ctlSession.String()+"/invitations", inv)
				if st != http.StatusConflict || ctlErrCode(out) != "session.sold_externally" {
					t.Errorf("%s invitation on the control session: %d %s, want 409 session.sold_externally", name, st, raw)
				}
			}
			checkNoRows(t)
		})

		t.Run("complimentary issuance", func(t *testing.T) {
			body := `{"session_id":"` + ctlSession.String() + `","qty":1,"batch_id":"ctl-batch-` + w.sfx + `"}`
			st, out, raw := w.call(w.owner, http.MethodPost, w.orgBase()+"/complimentary", body)
			if st != http.StatusConflict || ctlErrCode(out) != "session.sold_externally" {
				t.Errorf("complimentary on the control session: %d %s, want 409 session.sold_externally", st, raw)
			}
			var issuances int
			_ = w.pool.QueryRow(ctx, `SELECT count(*) FROM complimentary_issuances WHERE org_id = $1`, f.orgID).Scan(&issuances)
			if issuances != 0 {
				t.Errorf("%d complimentary issuances exist", issuances)
			}
			checkNoRows(t)
		})

	}
	t.Run("natural state", refusals)
	w.forcePublish(ctlEvent, ctlSession, "ctl-box-"+w.sfx)
	t.Run("forced state", refusals)

	// Regression: the ordinary seated session of the same channel takes a hold.
	t.Run("the ordinary session still takes a hold", func(t *testing.T) {
		st, out, raw := w.call("", http.MethodPost, feed+"/holds", `{"session_id":"`+w.seatedSession.String()+`","seat_keys":["A-01"],"device_id":"ctl-device-2"}`)
		if st != http.StatusCreated {
			t.Fatalf("hold on the ordinary seated session: %d %s (code %q)", st, raw, ctlErrCode(out))
		}
		if r, _, _, _, _ := rows(w.seatedSession); r != 1 {
			t.Errorf("ordinary hold wrote %d reservations, want 1", r)
		}
		checkNoRows(t)
	})
}

// ── follow-ups: sales state, events list state ──────────────────────────────

func TestControlSurfaces_SalesStateIsLockedAndSilent(t *testing.T) {
	w := newCtlWorld(t, nil)
	ctx := context.Background()
	f := w.f
	_, ctlSession := w.createControl(w.owner, w.ctlTitle)

	counts := func(session uuid.UUID) (states, journal, outbox int) {
		t.Helper()
		if err := w.pool.QueryRow(ctx, `SELECT
		    (SELECT count(*) FROM session_sales_states WHERE session_id = $1),
		    (SELECT count(*) FROM session_sales_state_changes WHERE session_id = $1),
		    (SELECT count(*) FROM outbox_events WHERE payload::text LIKE '%' || $1::text || '%')`, session).Scan(&states, &journal, &outbox); err != nil {
			t.Fatalf("count rows: %v", err)
		}
		return
	}

	for _, state := range []string{"sold_out", "soon"} {
		st, out, raw := w.call(w.owner, http.MethodPut, w.orgBase()+"/sessions/"+ctlSession.String()+"/sales-state", `{"state":"`+state+`"}`)
		if st != http.StatusConflict || ctlErrCode(out) != "session.sales_mode_locked" {
			t.Errorf("PUT sales-state %s on the control session: %d %s, want 409 session.sales_mode_locked", state, st, raw)
		}
	}
	if s, j, o := counts(ctlSession); s+j+o != 0 {
		t.Errorf("control session: %d sales-state rows, %d journal rows, %d outbox rows, want none", s, j, o)
	}

	// Regression: the same call on the ordinary session works and IS journaled.
	st, _, raw := w.call(w.owner, http.MethodPut, w.orgBase()+"/sessions/"+f.sessionID.String()+"/sales-state", `{"state":"sold_out"}`)
	if st != http.StatusOK {
		t.Fatalf("PUT sales-state on the ordinary session: %d %s", st, raw)
	}
	if s, j, _ := counts(f.sessionID); s != 1 || j != 1 {
		t.Errorf("ordinary session: %d sales-state rows, %d journal rows, want 1 and 1", s, j)
	}
}

func TestControlSurfaces_EventListStateIsDraft(t *testing.T) {
	w := newCtlWorld(t, nil)
	ctlEvent, ctlSession := w.createControl(w.owner, w.ctlTitle)

	var start time.Time
	if err := w.pool.QueryRow(context.Background(), `SELECT start_at FROM sessions WHERE id = $1`, ctlSession).Scan(&start); err != nil {
		t.Fatal(err)
	}
	if !start.After(time.Now()) {
		t.Fatalf("the control session starts %s, the check needs a future one", start)
	}
	st, out, raw := w.call(w.manager, http.MethodGet, w.orgBase()+"/events", "")
	if st != http.StatusOK {
		t.Fatalf("events list: %d %s", st, raw)
	}
	found := false
	for _, e := range out["events"].([]any) {
		m, _ := e.(map[string]any)
		if m["id"] == ctlEvent.String() {
			found = true
			if m["sales_state"] != "draft" {
				t.Errorf("control event sales_state in the list = %v, want draft (session is in the future)", m["sales_state"])
			}
			if m["control_only"] != true {
				t.Errorf("control event control_only = %v", m["control_only"])
			}
		}
	}
	if !found {
		t.Fatalf("control event missing from the events list: %s", raw)
	}
}

// ── (5), (6) organizer lists and the lock of the sales mode ──────────────────

func TestControlSurfaces_OrganizerListsAndModeLock(t *testing.T) {
	w := newCtlWorld(t, nil)
	f := w.f
	ctlEvent, ctlSession := w.createControl(w.owner, w.ctlTitle)

	// The control event's sessions list shows the session with its mode.
	for name, tok := range map[string]string{"manager": w.manager, "API key": w.key} {
		st, out, raw := w.call(tok, http.MethodGet, w.orgBase()+"/events/"+ctlEvent.String()+"/sessions", "")
		if st != http.StatusOK {
			t.Fatalf("%s: sessions list: %d %s", name, st, raw)
		}
		sessions, _ := out["sessions"].([]any)
		if len(sessions) != 1 {
			t.Fatalf("%s: sessions list has %d rows: %s", name, len(sessions), raw)
		}
		if m, _ := sessions[0].(map[string]any); m["id"] != ctlSession.String() || m["sales_mode"] != "control_only" {
			t.Errorf("%s: control session in the list = %v, want sales_mode control_only", name, m)
		}
	}
	// The ordinary event's sessions say 'platform'.
	st, out, raw := w.call(w.manager, http.MethodGet, w.orgBase()+"/events/"+f.eventID.String()+"/sessions", "")
	if st != http.StatusOK {
		t.Fatalf("ordinary sessions list: %d %s", st, raw)
	}
	for _, s := range out["sessions"].([]any) {
		if m, _ := s.(map[string]any); m["sales_mode"] != "platform" {
			t.Errorf("ordinary session reads sales_mode %v", m["sales_mode"])
		}
	}

	// The events list: control_only true for the container, false for the rest.
	st, out, raw = w.call(w.manager, http.MethodGet, w.orgBase()+"/events", "")
	if st != http.StatusOK {
		t.Fatalf("events list: %d %s", st, raw)
	}
	seen := map[string]any{}
	for _, e := range out["events"].([]any) {
		m, _ := e.(map[string]any)
		seen[asString(m["id"])] = m["control_only"]
	}
	if seen[ctlEvent.String()] != true {
		t.Errorf("events list: control event control_only = %v, want true", seen[ctlEvent.String()])
	}
	if v, ok := seen[f.eventID.String()]; !ok || v != false {
		t.Errorf("events list: ordinary event control_only = %v (listed %v), want false", v, ok)
	}
	// GET one event carries the flag too.
	if st, out, raw = w.call(w.manager, http.MethodGet, "/v1/events/"+ctlEvent.String(), ""); st != http.StatusOK {
		t.Fatalf("GET control event: %d %s", st, raw)
	} else if e, _ := out["event"].(map[string]any); e["control_only"] != true {
		t.Errorf("GET control event: control_only = %v", e["control_only"])
	}

	// The lock: an ordinary session of the PUBLISHED event does not become a
	// control one (409 session.sales_mode_locked), and a control one does not
	// become a platform one.
	ordSession := w.orgBase() + "/events/" + f.eventID.String() + "/sessions/" + f.sessionID.String()
	if st, out, raw = w.call(w.owner, http.MethodPatch, ordSession, `{"sales_mode":"control_only"}`); st != http.StatusConflict || ctlErrCode(out) != "session.sales_mode_locked" {
		t.Errorf("PATCH ordinary to control_only: %d %s, want 409 session.sales_mode_locked", st, raw)
	}
	ctlPath := w.orgBase() + "/events/" + ctlEvent.String() + "/sessions/" + ctlSession.String()
	if st, out, raw = w.call(w.owner, http.MethodPatch, ctlPath, `{"sales_mode":"platform"}`); st != http.StatusConflict || ctlErrCode(out) != "session.sales_mode_locked" {
		t.Errorf("PATCH control to platform: %d %s, want 409 session.sales_mode_locked", st, raw)
	}
	var mode, ctlMode string
	_ = w.pool.QueryRow(context.Background(), `SELECT sales_mode FROM sessions WHERE id = $1`, f.sessionID).Scan(&mode)
	_ = w.pool.QueryRow(context.Background(), `SELECT sales_mode FROM sessions WHERE id = $1`, ctlSession).Scan(&ctlMode)
	if mode != "platform" || ctlMode != "control_only" {
		t.Errorf("modes after the refused PATCHes: ordinary %q, control %q", mode, ctlMode)
	}
}
