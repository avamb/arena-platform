//go:build integration

package httpserver

// analytics_admin_integration_test.go — stage A6 of spec 50: the two
// superadmin routes that tie the analytics to the CRM, through the REAL router
// and a migrated database (run on a scratch one, never the shared stand):
//
//	DATABASE_URL=... go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ -run TestAnalyticsAdmin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

const (
	adminTestSecret = "integration-test-secret-32-bytes!!"
	adminTestBase   = "https://admin.example.test"
	syncTestToken   = "sync-token-for-integration-0123456789abcdef"
)

type analyticsAdminFixture struct {
	*analyticsFixture
	superToken, organizerToken string
}

func newAnalyticsAdminFixture(t *testing.T) *analyticsAdminFixture {
	t.Helper()
	return newAnalyticsAdminFixtureCfg(t, syncTestToken)
}

// newAnalyticsAdminFixtureCfg builds the server with the given
// ANALYTICS_SYNC_TOKEN ("" = not configured).
func newAnalyticsAdminFixtureCfg(t *testing.T, syncToken string) *analyticsAdminFixture {
	t.Helper()
	srv, _ := productionIntegrationServerCfg(t, func(c *config.Config) {
		c.AppPublicURL = adminTestBase
		c.AnalyticsSyncToken = syncToken
	})
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	f := &analyticsAdminFixture{analyticsFixture: &analyticsFixture{srv: srv, ts: ts}}

	ctx := context.Background()
	pool := srv.pgxPool
	q := gen.New(pool)
	run := strings.ToLower(uuid.NewString()[:8])
	super, err := q.InsertUser(ctx, "an-admin-super-"+run+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := q.InsertUser(ctx, "an-admin-mgr-"+run+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	org, err := q.InsertOrganization(ctx, "An Admin Org "+run, "an-admin-org-"+run, "ES", "en", 1200)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.InsertMembership(ctx, mgr.ID, org.ID, "organizer"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id, org_id)
		SELECT $1, id, NULL FROM roles WHERE name = 'platform_superadmin' AND org_id IS NULL`, super.ID); err != nil {
		t.Fatalf("grant platform_superadmin: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM user_roles WHERE user_id = $1`, super.ID)
		_, _ = pool.Exec(bg, `DELETE FROM memberships WHERE org_id = $1`, org.ID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, org.ID)
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = ANY($1)`, []uuid.UUID{super.ID, mgr.ID})
	})
	issue := func(id uuid.UUID) string {
		tok, _, err := auth.IssueJWT(adminTestSecret, id, nil, nil, "arena-api", "arena-api", time.Hour)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		return tok
	}
	f.superToken, f.organizerToken = issue(super.ID), issue(mgr.ID)
	return f
}

func (f *analyticsAdminFixture) call(t *testing.T, method, path, token, reason, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if reason != "" {
		req.Header.Set("X-Admin-Reason", reason)
	}
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

type adminCodes struct {
	Codes []struct {
		TwentyPersonID string `json:"twenty_person_id"`
		Code           string `json:"code"`
	} `json:"codes"`
}

type adminProfile struct {
	PersonID            string            `json:"person_id"`
	TwentyPersonID      *string           `json:"twenty_person_id"`
	Email               *string           `json:"email"`
	MayCreateContact    bool              `json:"may_create_contact"`
	Stage               string            `json:"stage"`
	FirstSource         string            `json:"first_source"`
	LastSource          string            `json:"last_source"`
	LastSeenAt          *string           `json:"last_seen_at"`
	Visits30d           int               `json:"visits_30d"`
	Milestones          map[string]string `json:"milestones"`
	ActivityScore       int               `json:"activity_score"`
	ActivityExplanation string            `json:"activity_explanation"`
	TimelineURL         *string           `json:"timeline_url"`
	ChangedAt           string            `json:"changed_at"`
}

type adminProfiles struct {
	Profiles   []adminProfile `json:"profiles"`
	NextCursor string         `json:"next_cursor"`
}

// setRollupThrough pretends that the last finished rollup run started at t
// (analytics.state), and puts the previous value back afterwards. Seeded
// profiles are only served up to it.
func setRollupThrough(t *testing.T, f *analyticsAdminFixture, through time.Time) {
	t.Helper()
	ctx := context.Background()
	pool := f.srv.pgxPool
	var old []byte
	hadOld := pool.QueryRow(ctx, `SELECT value FROM analytics.state WHERE key = 'rollup'`).Scan(&old) == nil
	raw, _ := json.Marshal(map[string]any{"through": through.UTC().Format(time.RFC3339Nano)})
	if _, err := pool.Exec(ctx, `INSERT INTO analytics.state (key, value, updated_at) VALUES ('rollup', $1, now())
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, raw); err != nil {
		t.Fatalf("set rollup state: %v", err)
	}
	t.Cleanup(func() {
		if hadOld {
			_, _ = pool.Exec(context.Background(), `UPDATE analytics.state SET value = $1 WHERE key = 'rollup'`, old)
		} else {
			_, _ = pool.Exec(context.Background(), `DELETE FROM analytics.state WHERE key = 'rollup'`)
		}
	})
}

func (f *analyticsAdminFixture) profiles(t *testing.T, query string) adminProfiles {
	t.Helper()
	code, raw := f.call(t, http.MethodGet, "/v1/admin/analytics/profiles?"+query, f.superToken, "", "")
	if code != http.StatusOK {
		t.Fatalf("GET profiles?%s = %d %s", query, code, raw)
	}
	var out adminProfiles
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	return out
}

func TestAnalyticsAdmin_OnlyASuperadminGetsThrough(t *testing.T) {
	f := newAnalyticsAdminFixture(t)
	const body = `{"twenty_person_ids":["x-403-check"]}`
	for _, tc := range []struct {
		name, method, path, token, reason, body string
		want                                    int
	}{
		{"codes without a token", http.MethodPost, "/v1/admin/analytics/contact-codes", "", "r", body, http.StatusUnauthorized},
		{"codes as a manager", http.MethodPost, "/v1/admin/analytics/contact-codes", f.organizerToken, "r", body, http.StatusForbidden},
		{"profiles without a token", http.MethodGet, "/v1/admin/analytics/profiles", "", "", "", http.StatusUnauthorized},
		{"profiles as a manager", http.MethodGet, "/v1/admin/analytics/profiles", f.organizerToken, "r", "", http.StatusForbidden},
		{"codes without a reason", http.MethodPost, "/v1/admin/analytics/contact-codes", f.superToken, "", body, http.StatusBadRequest},
	} {
		code, raw := f.call(t, tc.method, tc.path, tc.token, tc.reason, tc.body)
		if code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, code, raw, tc.want)
		}
	}
	var n int
	if err := f.srv.pgxPool.QueryRow(context.Background(),
		`SELECT count(*) FROM analytics.contact_codes WHERE twenty_person_id = 'x-403-check'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("a refused call left %d codes (%v)", n, err)
	}
}

func TestAnalyticsAdmin_ContactCodesAreStablePerPersonAndAudited(t *testing.T) {
	f := newAnalyticsAdminFixture(t)
	ctx := context.Background()
	run := strings.ToLower(uuid.NewString()[:8])
	ids := []string{"tw-" + run + "-a", "tw-" + run + "-b", "tw-" + run + "-c"}
	t.Cleanup(func() {
		_, _ = f.srv.pgxPool.Exec(ctx, `DELETE FROM analytics.contact_codes WHERE twenty_person_id = ANY($1)`, ids)
	})

	reason := "integration: contact codes " + run
	post := func(list string) adminCodes {
		code, raw := f.call(t, http.MethodPost, "/v1/admin/analytics/contact-codes", f.superToken, reason,
			`{"twenty_person_ids":`+list+`}`)
		if code != http.StatusOK {
			t.Fatalf("POST contact-codes = %d %s", code, raw)
		}
		var out adminCodes
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	first := post(`["` + ids[0] + `","` + ids[1] + `","` + ids[0] + `"]`) // a repeated id is answered once
	if len(first.Codes) != 2 || first.Codes[0].TwentyPersonID != ids[0] || first.Codes[1].TwentyPersonID != ids[1] {
		t.Fatalf("first answer = %+v", first.Codes)
	}
	for _, c := range first.Codes {
		if !analytics.IsContactCode(c.Code) {
			t.Errorf("code %q has the wrong shape", c.Code)
		}
	}
	if first.Codes[0].Code == first.Codes[1].Code {
		t.Error("two people share a code")
	}

	// The same people again, plus a new one, in another order: same codes.
	second := post(`["` + ids[1] + `","` + ids[2] + `","` + ids[0] + `"]`)
	byID := map[string]string{}
	for _, c := range second.Codes {
		byID[c.TwentyPersonID] = c.Code
	}
	if byID[ids[0]] != first.Codes[0].Code || byID[ids[1]] != first.Codes[1].Code {
		t.Fatalf("codes changed between calls: first %+v, second %+v", first.Codes, second.Codes)
	}
	if second.Codes[0].TwentyPersonID != ids[1] || second.Codes[2].TwentyPersonID != ids[0] {
		t.Errorf("answer is not in the order sent: %+v", second.Codes)
	}
	var stored int
	if err := f.srv.pgxPool.QueryRow(ctx,
		`SELECT count(*) FROM analytics.contact_codes WHERE twenty_person_id = ANY($1)`, ids).Scan(&stored); err != nil || stored != 3 {
		t.Fatalf("stored %d codes for 3 people (%v)", stored, err)
	}

	// Invalid input is refused whole and issues nothing.
	for _, bad := range []string{`[]`, `["a b"]`, `["ok-` + run + `","a@b.c"]`} {
		code, raw := f.call(t, http.MethodPost, "/v1/admin/analytics/contact-codes", f.superToken, reason,
			`{"twenty_person_ids":`+bad+`}`)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("%s -> %d %s, want 422", bad, code, raw)
		}
	}
	var leaked int
	if err := f.srv.pgxPool.QueryRow(ctx,
		`SELECT count(*) FROM analytics.contact_codes WHERE twenty_person_id = 'ok-'||$1`, run).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("a refused list issued %d codes (%v)", leaked, err)
	}

	// Both successful calls are in the audit log with the reason.
	var audited int
	if err := f.srv.pgxPool.QueryRow(ctx, `
SELECT count(*) FROM audit_events
 WHERE action = 'v1.admin.analytics.contact_codes.issue' AND metadata->>'reason' = $1`, reason).Scan(&audited); err != nil || audited != 2 {
		t.Errorf("audit rows = %d, want 2 (%v)", audited, err)
	}
}

// seedProfile writes one person with a profile directly (what the rollup
// would produce) and returns the person id.
func seedProfile(t *testing.T, f *analyticsAdminFixture, twenty string, userID *uuid.UUID, changed time.Time, extra func(person uuid.UUID)) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	pool := f.srv.pgxPool
	id := uuid.New()
	var tw any
	if twenty != "" {
		tw = twenty
	}
	if _, err := pool.Exec(ctx, `INSERT INTO analytics.people (id, twenty_person_id, user_id) VALUES ($1, $2, $3)`, id, tw, userID); err != nil {
		t.Fatalf("insert person: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO analytics.profiles (person_id, stage, visits_30d, engaged_minutes_30d, articles, milestones, activity_score, changed_at)
VALUES ($1, 'lead', 3, 5, '[{"path":"/blog/a","at":"2026-10-01T10:00:00Z","seconds":75}]',
        '{"application_started":"2026-10-02T08:00:00Z","gmv_30d":{"EUR":4200}}', 7, $2)`, id, changed); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	if extra != nil {
		extra(id)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM analytics.people WHERE id = $1`, id) })
	return id
}

func TestAnalyticsAdmin_ProfilesPaginateFilterAndResolveEmail(t *testing.T) {
	f := newAnalyticsAdminFixture(t)
	ctx := context.Background()
	pool := f.srv.pgxPool
	q := gen.New(pool)
	run := strings.ToLower(uuid.NewString()[:8])

	// Far in the future: nothing else in the scratch database is this new.
	base := time.Date(2037, 3, 1, 12, 0, 0, 0, time.UTC)
	since := base.Format(time.RFC3339)

	user, err := q.InsertUser(ctx, "an-prof-user-"+run+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	appMail := "an-prof-app-" + run + "@example.test"
	purgedMail := "an-prof-purged-" + run + "@example.test"
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM onboarding_applications WHERE applicant_email = ANY($1)`, []string{appMail, purgedMail})
		_, _ = pool.Exec(bg, `DELETE FROM users WHERE id = $1`, user.ID)
	})
	for _, mail := range []string{appMail, purgedMail} {
		if _, err := pool.Exec(ctx, `INSERT INTO onboarding_applications (applicant_email, expires_at) VALUES ($1, now() + interval '30 days')`, mail); err != nil {
			t.Fatalf("insert application: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE onboarding_applications SET purged_at = now() WHERE applicant_email = $1`, purgedMail); err != nil {
		t.Fatal(err)
	}
	hashIdentity := func(mail string) func(uuid.UUID) {
		return func(p uuid.UUID) {
			if _, err := pool.Exec(ctx, `INSERT INTO analytics.identities (kind, value, person_id, seen_at) VALUES ('email_sha256', $1, $2, now())`,
				analytics.EmailSHA256(mail), p); err != nil {
				t.Fatalf("insert identity: %v", err)
			}
		}
	}

	// An account-only user (an invited manager): e-mail yes, may_create_contact no.
	mgr, err := q.InsertUser(ctx, "an-prof-mgr-"+run+"@example.test", "x", "en")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, mgr.ID) })
	setRollupThrough(t, f, base.Add(5*time.Minute))

	// Order: changed_at, then person_id. p2 and p3 share a timestamp.
	pUser := seedProfile(t, f, "tw-user-"+run, &user.ID, base, nil)
	pApp := seedProfile(t, f, "", nil, base.Add(1*time.Minute), hashIdentity(appMail))
	pBare := seedProfile(t, f, "", nil, base.Add(2*time.Minute), nil) // no e-mail, no CRM id: still listed
	pPurged := seedProfile(t, f, "tw-purged-"+run, nil, base.Add(2*time.Minute), hashIdentity(purgedMail))
	pLate := seedProfile(t, f, "tw-late-"+run, nil, base.Add(3*time.Minute), nil)
	pMgr := seedProfile(t, f, "", &mgr.ID, base.Add(4*time.Minute), nil)
	// Changed after the last finished run: not served yet.
	pAhead := seedProfile(t, f, "tw-ahead-"+run, nil, base.Add(6*time.Minute), nil)
	want := map[uuid.UUID]bool{pUser: true, pApp: true, pBare: true, pPurged: true, pLate: true, pMgr: true}

	// Page through with limit 2 on the cursor alone: every profile exactly
	// once, ordered; the cursor is always there, an empty page keeps it.
	var all []adminProfile
	cursor := ""
	for page := 0; page < 10; page++ {
		qs := "limit=2&changed_since=" + since
		if cursor != "" {
			qs = "limit=2&after=" + cursor
		}
		p := f.profiles(t, qs)
		if len(p.Profiles) > 2 {
			t.Fatalf("page of %d with limit 2", len(p.Profiles))
		}
		if p.NextCursor == "" {
			t.Fatalf("page %d has no next_cursor", page)
		}
		if len(p.Profiles) == 0 {
			if p.NextCursor != cursor {
				t.Errorf("an empty page changed the cursor: %q -> %q", cursor, p.NextCursor)
			}
			break
		}
		all = append(all, p.Profiles...)
		cursor = p.NextCursor
	}
	for _, p := range all {
		if p.PersonID == pAhead.String() {
			t.Errorf("a profile changed after the last finished rollup run was served")
		}
	}
	if len(all) != len(want) {
		t.Fatalf("paged %d profiles, want %d", len(all), len(want))
	}
	seen := map[string]bool{}
	var prev string
	for _, p := range all {
		id := uuid.MustParse(p.PersonID)
		if !want[id] || seen[p.PersonID] {
			t.Fatalf("unexpected or repeated profile %s", p.PersonID)
		}
		seen[p.PersonID] = true
		key := p.ChangedAt + "|" + p.PersonID
		if prev != "" && key < prev {
			t.Errorf("not ordered: %s after %s", key, prev)
		}
		prev = key
	}

	byID := map[string]adminProfile{}
	for _, p := range all {
		byID[p.PersonID] = p
	}
	if p := byID[pUser.String()]; p.Email == nil || *p.Email != user.Email || p.TwentyPersonID == nil || *p.TwentyPersonID != "tw-user-"+run {
		t.Errorf("user profile = %+v", p)
	}
	if p := byID[pApp.String()]; p.Email == nil || *p.Email != appMail || p.TwentyPersonID != nil {
		t.Errorf("application profile = %+v (email resolved from the application)", p)
	}
	if p := byID[pBare.String()]; p.Email != nil || p.TwentyPersonID != nil || p.MayCreateContact {
		t.Errorf("bare profile = %+v", p)
	}
	if p := byID[pMgr.String()]; p.Email == nil || *p.Email != mgr.Email || p.MayCreateContact {
		t.Errorf("an account-only user must show the e-mail but never may_create_contact: %+v", p)
	}
	if p := byID[pApp.String()]; !p.MayCreateContact {
		t.Errorf("a person with an organizer application may get a contact: %+v", p)
	}
	if p := byID[pUser.String()]; !p.MayCreateContact {
		t.Errorf("a person tied to a CRM contact keeps may_create_contact: %+v", p)
	}
	if p := byID[pPurged.String()]; p.Email != nil {
		t.Errorf("a purged application's address came back: %+v", p)
	}
	p := byID[pUser.String()]
	if p.Stage != "lead" || p.Visits30d != 3 || p.ActivityScore != 7 || p.Milestones["application_started"] != "2026-10-02T08:00:00Z" {
		t.Errorf("stored values lost: %+v", p)
	}
	if p.TimelineURL == nil || *p.TimelineURL != adminTestBase+"/analytics/people/"+pUser.String() {
		t.Errorf("timeline_url = %v", p.TimelineURL)
	}
	// No events, so only the stored application milestone counts.
	if !strings.HasPrefix(p.ActivityExplanation, "application_started x1 = ") || strings.Contains(p.ActivityExplanation, "visit") {
		t.Errorf("explanation of a person without events = %q", p.ActivityExplanation)
	}
	if p := byID[pBare.String()]; !strings.HasPrefix(p.ActivityExplanation, "application_started x1") {
		t.Errorf("bare explanation = %q", p.ActivityExplanation)
	}

	// changed_since is an inclusive lower bound: from +2 minutes four remain (+2, +2, +3, +4).
	got := f.profiles(t, "changed_since="+base.Add(2*time.Minute).Format(time.RFC3339))
	if len(got.Profiles) != 4 || got.NextCursor == "" {
		t.Errorf("since +2m: %d profiles, cursor %q", len(got.Profiles), got.NextCursor)
	}
	// Past the last change: nothing.
	if got := f.profiles(t, "changed_since="+base.Add(4*time.Minute+time.Second).Format(time.RFC3339)); len(got.Profiles) != 0 || got.NextCursor == "" {
		t.Errorf("since +4m: %+v", got)
	}

	// A bad cursor and bad numbers are 400.
	for _, qs := range []string{"after=%21", "limit=500", "changed_since=soon"} {
		if code, raw := f.call(t, http.MethodGet, "/v1/admin/analytics/profiles?"+qs, f.superToken, "", ""); code != http.StatusBadRequest {
			t.Errorf("%s -> %d %s, want 400", qs, code, raw)
		}
	}
}

// The whole chain through the real router: a code is issued, a visitor opens
// the link, the rollup builds the profile, the export shows it - and a second
// rollup changes nothing.
func TestAnalyticsAdmin_CodeToClickToProfile(t *testing.T) {
	f := newAnalyticsAdminFixture(t)
	ctx := context.Background()
	pool := f.srv.pgxPool
	run := strings.ToLower(uuid.NewString()[:8])
	twenty := "tw-chain-" + run
	aid := "a_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	started := time.Now().UTC().Add(-time.Minute)

	t.Cleanup(func() {
		var pid uuid.UUID
		if err := pool.QueryRow(context.Background(),
			`SELECT person_id FROM analytics.identities WHERE kind = 'aid' AND value = $1`, aid).Scan(&pid); err == nil {
			_, _ = analytics.ErasePerson(context.Background(), pool, pid)
		}
		_, _ = pool.Exec(context.Background(), `DELETE FROM analytics.contact_codes WHERE twenty_person_id = $1`, twenty)
		_, _ = pool.Exec(context.Background(), `DELETE FROM analytics.events WHERE id_kind = 'aid' AND id_value = $1`, aid)
	})

	code, raw := f.call(t, http.MethodPost, "/v1/admin/analytics/contact-codes", f.superToken, "integration: chain",
		`{"twenty_person_ids":["`+twenty+`"]}`)
	if code != http.StatusOK {
		t.Fatalf("codes: %d %s", code, raw)
	}
	var codes adminCodes
	_ = json.Unmarshal(raw, &codes)
	cid := codes.Codes[0].Code

	body := `{"aid":"` + aid + `","cid":"` + cid + `","pv":"p_chain01","events":[
		{"name":"page_view","props":{"path":"/pricing","lang":"en","utm_campaign":"outreach-nov"}},
		{"name":"engaged","props":{"path":"/pricing","seconds":45}}]}`
	if resp := f.post(t, "/v1/t", "", body); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /v1/t = %d", resp.StatusCode)
	}
	f.flush(t)

	roll := analytics.NewRollup(analytics.RollupOptions{Pool: pool})
	if _, err := roll.Run(ctx); err != nil {
		t.Fatalf("rollup: %v", err)
	}

	find := func() adminProfile {
		t.Helper()
		for _, p := range f.profiles(t, "changed_since="+started.Format(time.RFC3339Nano)+"&limit=200").Profiles {
			if p.TwentyPersonID != nil && *p.TwentyPersonID == twenty {
				return p
			}
		}
		t.Fatalf("no profile for %s after the rollup", twenty)
		return adminProfile{}
	}
	p := find()
	if p.Stage != "lead" || p.Email != nil || p.Visits30d != 1 {
		t.Errorf("profile = %+v", p)
	}
	if !strings.HasPrefix(p.FirstSource, "email") || !strings.Contains(p.FirstSource, "outreach-nov") {
		t.Errorf("first_source = %q, want the email campaign", p.FirstSource)
	}
	for _, part := range []string{"email_click x1", "visit x1"} {
		if !strings.Contains(p.ActivityExplanation, part) {
			t.Errorf("explanation %q lacks %q", p.ActivityExplanation, part)
		}
	}
	if p.ActivityScore < 3 {
		t.Errorf("score = %d, want at least visit + email click", p.ActivityScore)
	}
	if p.LastSeenAt == nil || p.TimelineURL == nil || !strings.HasSuffix(*p.TimelineURL, "/analytics/people/"+p.PersonID) {
		t.Errorf("last_seen_at / timeline_url = %v / %v", p.LastSeenAt, p.TimelineURL)
	}

	// A second pass over the same data changes nothing: the CRM sync would
	// see no difference.
	if _, err := roll.Run(ctx); err != nil {
		t.Fatalf("second rollup: %v", err)
	}
	if again := find(); again.ChangedAt != p.ChangedAt {
		t.Errorf("changed_at moved from %s to %s without a change", p.ChangedAt, again.ChangedAt)
	}
}

// The n8n sync does not hold a user's token: it calls the two /v1/service
// routes with ANALYTICS_SYNC_TOKEN. Unset -> 503, wrong -> 401, right -> 200,
// and the token opens nothing else.
func TestAnalyticsAdmin_ServiceTokenRoutes(t *testing.T) {
	const profilesPath = "/v1/service/analytics/profiles"
	const codesPath = "/v1/service/analytics/contact-codes"
	run := strings.ToLower(uuid.NewString()[:8])
	twenty := "tw-svc-" + run

	// Not configured: both routes are closed, even for a request with a token.
	off := newAnalyticsAdminFixtureCfg(t, "")
	for _, path := range []string{profilesPath, codesPath} {
		method := http.MethodGet
		if path == codesPath {
			method = http.MethodPost
		}
		if code, raw := off.call(t, method, path, syncTestToken, "r", `{"twenty_person_ids":["x"]}`); code != http.StatusServiceUnavailable {
			t.Errorf("%s with no ANALYTICS_SYNC_TOKEN configured: %d %s, want 503", path, code, raw)
		}
	}

	f := newAnalyticsAdminFixture(t)
	body := `{"twenty_person_ids":["` + twenty + `"]}`
	t.Cleanup(func() {
		_, _ = f.srv.pgxPool.Exec(context.Background(), `DELETE FROM analytics.contact_codes WHERE twenty_person_id = $1`, twenty)
	})
	for _, tc := range []struct {
		name, method, path, token, reason, body string
		want                                    int
	}{
		{"profiles no token", http.MethodGet, profilesPath, "", "", "", http.StatusUnauthorized},
		{"profiles wrong token", http.MethodGet, profilesPath, syncTestToken + "x", "", "", http.StatusUnauthorized},
		{"profiles a user JWT", http.MethodGet, profilesPath, f.superToken, "", "", http.StatusUnauthorized},
		{"codes wrong token", http.MethodPost, codesPath, "nope", "r", body, http.StatusUnauthorized},
		{"codes a user JWT", http.MethodPost, codesPath, f.superToken, "r", body, http.StatusUnauthorized},
		{"codes right token, no reason", http.MethodPost, codesPath, syncTestToken, "", body, http.StatusBadRequest},
		{"profiles right token", http.MethodGet, profilesPath, syncTestToken, "", "", http.StatusOK},
		{"codes right token", http.MethodPost, codesPath, syncTestToken, "integration: service " + run, body, http.StatusOK},
		// The sync token is no admin credential.
		{"admin route with the sync token", http.MethodGet, "/v1/admin/analytics/profiles", syncTestToken, "", "", http.StatusUnauthorized},
		{"another admin route with the sync token", http.MethodGet, "/v1/admin/users", syncTestToken, "r", "", http.StatusUnauthorized},
	} {
		if code, raw := f.call(t, tc.method, tc.path, tc.token, tc.reason, tc.body); code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, code, raw, tc.want)
		}
	}

	// The audit row names the service, not a user.
	var actorType string
	var actorID *string
	var label, reason string
	err := f.srv.pgxPool.QueryRow(context.Background(), `
SELECT actor_type, actor_id::text, COALESCE(metadata->>'actor_label', ''), COALESCE(metadata->>'reason', '')
  FROM audit_events WHERE action = 'v1.admin.analytics.contact_codes.issue' AND metadata->>'reason' = $1`,
		"integration: service "+run).Scan(&actorType, &actorID, &label, &reason)
	if err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if actorType != "service" || actorID != nil || label != "n8n_crm_sync" {
		t.Errorf("audit row = %q / %v / %q", actorType, actorID, label)
	}
}
