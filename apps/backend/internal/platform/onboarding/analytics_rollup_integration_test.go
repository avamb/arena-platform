//go:build integration

package onboarding

// Stage A5 of spec 50: the whole chain "email click with cid -> consent (aid)
// -> application started with that aid -> approved -> first sale" must come out
// of analytics.rollup as ONE person with ONE profile that carries every
// milestone, a second run must change nothing, a merge must keep the older
// person, and erasure must remove the history. It lives in this package
// because it drives the REAL application lifecycle (Start, Confirm, Submit,
// Approve, Purge) with its helpers.
//
// Run on a FRESH scratch database (the rollup walks every person in it):
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go.exe test -tags integration -p 1 \
//	  ./apps/backend/internal/platform/onboarding/ -run TestAnalyticsRollup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
)

const rollupTestSecret = "rollup-test-secret"

func nonceString() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }

func trackAt(w *analytics.Writer, at time.Time, id, kind, name string, props map[string]any, server bool) {
	w.Track(context.Background(), analytics.Event{
		At: at, Surface: analytics.SurfaceSite, Name: name, IDKind: kind, IDValue: id,
		Props: props, ServerOrigin: server,
	})
}

type profileView struct {
	PersonID   uuid.UUID
	Stage      string
	Visits     int
	Score      int
	ChangedAt  time.Time
	Milestones map[string]json.RawMessage
	Articles   []map[string]any
}

func loadProfile(t *testing.T, pool *pgxpool.Pool, person uuid.UUID) profileView {
	t.Helper()
	var p profileView
	var ms, art []byte
	err := pool.QueryRow(context.Background(), `
SELECT person_id, stage, visits_30d, activity_score, changed_at, milestones, articles
  FROM analytics.profiles WHERE person_id = $1`, person).
		Scan(&p.PersonID, &p.Stage, &p.Visits, &p.Score, &p.ChangedAt, &ms, &art)
	if err != nil {
		t.Fatalf("profile of %s: %v", person, err)
	}
	_ = json.Unmarshal(ms, &p.Milestones)
	_ = json.Unmarshal(art, &p.Articles)
	return p
}

func personOf(t *testing.T, pool *pgxpool.Pool, kind, value string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(),
		`SELECT person_id FROM analytics.identities WHERE kind = $1 AND value = $2`, kind, value).Scan(&id); err != nil {
		t.Fatalf("identity %s:%s: %v", kind, value, err)
	}
	return id
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// seedFirstSale gives the approved organization an event, a session and one
// paid order, plus an active verified payment config.
func seedFirstSale(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	nonce := nonceString()
	venueID, evtID, sessID, resvID, csID, orderID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	var chanID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM sales_channels WHERE org_id = $1 LIMIT 1`, orgID).Scan(&chanID); err != nil {
		t.Fatalf("channel of the new organization: %v", err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		for _, s := range []string{
			`DELETE FROM orders WHERE id = $1`, `DELETE FROM checkout_sessions WHERE id = $1`,
			`DELETE FROM reservations WHERE id = $1`, `DELETE FROM sessions WHERE id = $1`,
			`DELETE FROM events WHERE id = $1`, `DELETE FROM venues WHERE id = $1`,
		} {
			var arg uuid.UUID
			switch {
			case strings.Contains(s, "FROM orders"):
				arg = orderID
			case strings.Contains(s, "checkout_sessions"):
				arg = csID
			case strings.Contains(s, "reservations"):
				arg = resvID
			case strings.Contains(s, "FROM sessions"):
				arg = sessID
			case strings.Contains(s, "FROM events"):
				arg = evtID
			default:
				arg = venueID
			}
			_, _ = pool.Exec(ctx, s, arg)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM payment_provider_configs WHERE org_id = $1`, orgID)
	})
	exec(`INSERT INTO payment_provider_configs (org_id, provider, mode, status, is_active, verification_status, verified_at)
	      VALUES ($1, 'stripe', 'test', 'configured', true, 'ok', now())`, orgID)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Prague')`, venueID, orgID, "Rollup Hall "+nonce)
	exec(`INSERT INTO events (id, org_id, name, status) VALUES ($1, $2, $3, 'published')`, evtID, orgID, "Rollup Event "+nonce)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, currency, currency_source)
	      VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'EUR', 'override')`,
		sessID, evtID, venueID)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now() + interval '1 hour', now())`, resvID, orgID, chanID, sessID)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state)
	      VALUES ($1, $2, $3, $4, 'completed')`, csID, orgID, chanID, resvID)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, buyer_name, buyer_email, paid_at)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'EUR', 5000, 0, 0, 5000, 'x', 'x@example.test', now())`,
		orderID, orgID, chanID, evtID, sessID, csID, resvID)
}

func TestAnalyticsRollup_ChainClickToFirstSale(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := nonceString()
	cid := "t" + nonce[len(nonce)-9:]
	aid := "a_rollup_" + nonce[len(nonce)-12:]
	twenty := "twenty-" + nonce
	email := fmt.Sprintf("rollup-chain-%s@example.com", nonce)

	if _, err := pool.Exec(ctx, `INSERT INTO analytics.contact_codes (code, twenty_person_id) VALUES ($1, $2)`, cid, twenty); err != nil {
		t.Fatalf("contact code: %v", err)
	}
	cleanupApplication(t, pool, email)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.events WHERE id_value IN ($1, $2)`, cid, aid)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.people WHERE twenty_person_id = $1`, twenty)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.contact_codes WHERE code = $1`, cid)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.profiles WHERE person_id NOT IN (SELECT id FROM analytics.people)`)
	})

	// 1. The raw events, through the real writer: a click without consent,
	// then the same visitor after "Accept" under an aid, reading an article.
	w := analytics.NewWriter(analytics.Options{DB: pool})
	base := time.Now().UTC().Add(-3 * time.Minute)
	trackAt(w, base, cid, analytics.IDKindCID, "email_click",
		map[string]any{"path": "/ru/start", "utm_campaign": "oct_wave"}, true)
	trackAt(w, base.Add(20*time.Second), aid, analytics.IDKindAID, "page_view",
		map[string]any{"path": "/ru/blog/seat-maps", "ref_host": "google.com"}, false)
	trackAt(w, base.Add(30*time.Second), aid, analytics.IDKindAID, "scroll",
		map[string]any{"path": "/ru/blog/seat-maps", "depth": float64(75)}, false)
	trackAt(w, base.Add(60*time.Second), aid, analytics.IDKindAID, "engaged",
		map[string]any{"path": "/ru/blog/seat-maps", "seconds": float64(45)}, false)
	trackAt(w, base.Add(90*time.Second), aid, analytics.IDKindAID, "page_view",
		map[string]any{"path": "/pricing"}, false)
	// The server sees both codes on one visit and stamps the aid on the click.
	trackAt(w, base.Add(20*time.Second), cid, analytics.IDKindCID, "email_click",
		map[string]any{"path": "/ru/start", "utm_campaign": "oct_wave", "aid": aid}, true)
	flushCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.Close(flushCtx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// 2. The real application lifecycle, started with the aid.
	svc := New(Options{Pool: pool, Notifier: &captureNotifier{}, AdminURL: "https://admin.example.test"})
	started, err := svc.Start(ctx, StartInput{FirstName: "Ana", LastName: "Chain", Email: email,
		Phone: "+34 600 111 222", Locale: "es", AID: aid})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var storedAID *string
	if err := pool.QueryRow(ctx, `SELECT aid FROM onboarding_applications WHERE id = $1`, started.App.ID).Scan(&storedAID); err != nil || storedAID == nil || *storedAID != aid {
		t.Fatalf("stored aid = %v (%v), want %s", storedAID, err, aid)
	}
	var link string
	if err := pool.QueryRow(ctx, `SELECT payload->>'token' FROM worker_jobs WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 ORDER BY created_at LIMIT 1`, email).Scan(&link); err != nil {
		t.Fatalf("link: %v", err)
	}
	confirmed, err := svc.Confirm(ctx, link)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	answers := fullAnswers("ES")
	answers["org_name"] = "Rollup Chain " + nonce
	if _, err := svc.SaveAnswers(ctx, started.App.ID, confirmed.AccessToken, answers); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := svc.Submit(ctx, started.App.ID, confirmed.AccessToken); err != nil {
		t.Fatalf("submit: %v", err)
	}
	reviewer := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, email, password_hash) VALUES ($1, $2, 'x')`, reviewer, "reviewer-"+email); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, reviewer) })
	res, err := svc.Approve(ctx, started.App.ID, operatorActor(reviewer), reviewer)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	seedFirstSale(t, pool, res.OrgID)

	// 3. The rollup.
	roll := analytics.NewRollup(analytics.RollupOptions{Pool: pool, TGHashSecret: rollupTestSecret})
	if _, err := roll.Run(ctx); err != nil {
		t.Fatalf("rollup: %v", err)
	}

	person := personOf(t, pool, analytics.IDKindCID, cid)
	for _, id := range [][2]string{
		{analytics.IDKindAID, aid},
		{analytics.IdentityEmailSHA256, analytics.EmailSHA256(email)},
		{analytics.IDKindUser, res.OwnerID.String()},
	} {
		if got := personOf(t, pool, id[0], id[1]); got != person {
			t.Fatalf("identity %s is person %s, want the one of the cid (%s)", id[0], got, person)
		}
	}
	var gotTwenty *string
	var gotUser *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT twenty_person_id, user_id FROM analytics.people WHERE id = $1`, person).Scan(&gotTwenty, &gotUser); err != nil {
		t.Fatal(err)
	}
	if gotTwenty == nil || *gotTwenty != twenty || gotUser == nil || *gotUser != res.OwnerID {
		t.Fatalf("person attributes: twenty %v user %v", gotTwenty, gotUser)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.identities WHERE person_id = $1`, person); n != 4 {
		t.Fatalf("identities of the person = %d, want 4 (cid, aid, e-mail hash, user)", n)
	}

	prof := loadProfile(t, pool, person)
	if prof.Stage != analytics.StageSelling {
		t.Errorf("stage = %q, want selling", prof.Stage)
	}
	for _, m := range []string{
		analytics.MsApplicationStarted, analytics.MsApplicationSubmitted, analytics.MsApplicationApproved,
		analytics.MsOrgCreated, analytics.MsPaymentConnected, analytics.MsFirstEventCreated,
		analytics.MsFirstEventPublished, analytics.MsFirstSale, analytics.MsGMV30d,
	} {
		if _, ok := prof.Milestones[m]; !ok {
			t.Errorf("milestone %s missing in %v", m, prof.Milestones)
		}
	}
	for _, m := range []string{analytics.MsApplicationRejected, analytics.MsSales100} {
		if _, ok := prof.Milestones[m]; ok {
			t.Errorf("milestone %s must not be set", m)
		}
	}
	var gmv map[string]int64
	if err := json.Unmarshal(prof.Milestones[analytics.MsGMV30d], &gmv); err != nil || len(gmv) != 1 || gmv["EUR"] != 5000 {
		t.Errorf("gmv_30d = %s (%v)", prof.Milestones[analytics.MsGMV30d], err)
	}
	if len(prof.Articles) != 1 || prof.Articles[0]["path"] != "/blog/seat-maps" {
		t.Errorf("articles = %v, want the one read post", prof.Articles)
	}
	if prof.Visits != 1 || prof.Score < 10+20+3+5+2 {
		t.Errorf("visits %d score %d", prof.Visits, prof.Score)
	}
	var touchKind, touchCampaign string
	if err := pool.QueryRow(ctx, `SELECT first_touch->>'kind', first_touch->>'campaign' FROM analytics.people WHERE id = $1`, person).Scan(&touchKind, &touchCampaign); err != nil {
		t.Fatal(err)
	}
	if touchKind != "email_click" || touchCampaign != "oct_wave" {
		t.Errorf("first touch = %s/%s", touchKind, touchCampaign)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.profiles WHERE person_id IN (SELECT person_id FROM analytics.identities WHERE value = ANY($1))`, []string{cid, aid, analytics.EmailSHA256(email)}); n != 1 {
		t.Fatalf("profiles for the chain = %d, want exactly 1", n)
	}

	// 4. A second run over the same window changes nothing.
	sessionsBefore := countRows(t, pool, `SELECT count(*) FROM analytics.sessions WHERE person_id = $1`, person)
	res2, err := roll.Run(ctx)
	if err != nil {
		t.Fatalf("second rollup: %v", err)
	}
	if res2.ProfilesChanged != 0 || res2.SessionsWritten != 0 || res2.PeopleCreated != 0 || res2.PeopleMerged != 0 {
		t.Errorf("second run changed things: %+v", res2)
	}
	prof2 := loadProfile(t, pool, person)
	if !prof2.ChangedAt.Equal(prof.ChangedAt) || prof2.Score != prof.Score {
		t.Errorf("changed_at moved: %v -> %v", prof.ChangedAt, prof2.ChangedAt)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.sessions WHERE person_id = $1`, person); n != sessionsBefore || n != 1 {
		t.Errorf("sessions %d -> %d, want 1 both times", sessionsBefore, n)
	}

	// 5. Erasure: events, sessions, identities, profile and the person go.
	er, err := analytics.ErasePerson(ctx, pool, person)
	if err != nil {
		t.Fatalf("erase: %v", err)
	}
	if er.Events < 6 || er.Identities != 4 || er.Applications != 1 {
		t.Errorf("erasure = %+v", er)
	}
	for _, q := range []string{
		`SELECT count(*) FROM analytics.events WHERE id_value IN ('` + cid + `', '` + aid + `')`,
		`SELECT count(*) FROM analytics.people WHERE id = '` + person.String() + `'`,
		`SELECT count(*) FROM analytics.profiles WHERE person_id = '` + person.String() + `'`,
		`SELECT count(*) FROM analytics.sessions WHERE person_id = '` + person.String() + `'`,
		`SELECT count(*) FROM analytics.identities WHERE person_id = '` + person.String() + `'`,
	} {
		if n := countRows(t, pool, q); n != 0 {
			t.Errorf("%s = %d after erasure", q, n)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT aid FROM onboarding_applications WHERE id = $1`, started.App.ID).Scan(&storedAID); err != nil || storedAID != nil {
		t.Errorf("application aid after erasure = %v (%v)", storedAID, err)
	}
	if _, err := roll.Run(ctx); err != nil {
		t.Fatalf("rollup after erasure: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.events WHERE id_value IN ($1, $2)`, cid, aid); n != 0 {
		t.Errorf("events came back after erasure: %d", n)
	}
}

func TestAnalyticsRollup_MergeKeepsTheOlderPerson(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := nonceString()
	aidB := "a_merge_" + nonce[len(nonce)-12:]
	cid2 := "m" + nonce[len(nonce)-9:]
	twenty := "twenty-merge-" + nonce
	if _, err := pool.Exec(ctx, `INSERT INTO analytics.contact_codes (code, twenty_person_id) VALUES ($1, $2)`, cid2, twenty); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.events WHERE id_value IN ($1, $2)`, cid2, aidB)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.people WHERE twenty_person_id = $1 OR id IN (SELECT person_id FROM analytics.identities WHERE value IN ($2, $3))`, twenty, cid2, aidB)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.contact_codes WHERE code = $1`, cid2)
	})
	roll := analytics.NewRollup(analytics.RollupOptions{Pool: pool})

	w := analytics.NewWriter(analytics.Options{DB: pool})
	now := time.Now().UTC()
	trackAt(w, now.Add(-5*time.Minute), aidB, analytics.IDKindAID, "page_view", map[string]any{"path": "/"}, false)
	trackAt(w, now.Add(-4*time.Minute), cid2, analytics.IDKindCID, "email_click", map[string]any{"path": "/"}, true)
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.Close(fctx); err != nil {
		t.Fatal(err)
	}
	if _, err := roll.Run(ctx); err != nil {
		t.Fatalf("first run: %v", err)
	}
	older, younger := personOf(t, pool, analytics.IDKindAID, aidB), personOf(t, pool, analytics.IDKindCID, cid2)
	if older == younger {
		t.Fatal("two unrelated identifiers started as one person")
	}

	// The visit that carried both codes arrives.
	w2 := analytics.NewWriter(analytics.Options{DB: pool})
	trackAt(w2, now.Add(-time.Minute), cid2, analytics.IDKindCID, "email_click", map[string]any{"path": "/", "aid": aidB}, true)
	if err := w2.Close(fctx); err != nil {
		t.Fatal(err)
	}
	res, err := roll.Run(ctx)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.PeopleMerged != 1 {
		t.Errorf("merged = %d, want 1", res.PeopleMerged)
	}
	if got := personOf(t, pool, analytics.IDKindCID, cid2); got != older {
		t.Errorf("survivor = %s, want the older person %s", got, older)
	}
	if got := personOf(t, pool, analytics.IDKindAID, aidB); got != older {
		t.Errorf("aid moved to %s", got)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.people WHERE id = $1`, younger); n != 0 {
		t.Error("the younger person was not deleted")
	}
	var tw *string
	if err := pool.QueryRow(ctx, `SELECT twenty_person_id FROM analytics.people WHERE id = $1`, older).Scan(&tw); err != nil || tw == nil || *tw != twenty {
		t.Errorf("the CRM contact did not move to the survivor: %v %v", tw, err)
	}
	// Both events now belong to the survivor's single session.
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.sessions WHERE person_id = $1`, older); n != 1 {
		t.Errorf("survivor sessions = %d, want 1", n)
	}
}

func TestAnalyticsRollup_TelegramJoinAndPurge(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := nonceString()
	aid := "a_purge_" + nonce[len(nonce)-12:]
	email := fmt.Sprintf("rollup-purge-%s@example.com", nonce)
	tgID := int64(900_000_000 + time.Now().UnixNano()%1_000_000)
	cleanupApplication(t, pool, email)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.people WHERE id IN (SELECT person_id FROM analytics.identities WHERE value = $1)`, aid)
	})

	svc := New(Options{Pool: pool, Notifier: &captureNotifier{}})
	started, err := svc.Start(ctx, StartInput{FirstName: "Pu", LastName: "Rge", Email: email, Phone: "+34 600 111 222", AID: aid})
	if err != nil {
		t.Fatal(err)
	}
	// What the bot's code confirmation leaves behind.
	if _, err := pool.Exec(ctx, `UPDATE onboarding_applications SET telegram_user_id = $2, email_confirmed_at = now() WHERE id = $1`, started.App.ID, tgID); err != nil {
		t.Fatal(err)
	}
	roll := analytics.NewRollup(analytics.RollupOptions{Pool: pool, TGHashSecret: rollupTestSecret})
	if _, err := roll.Run(ctx); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	hash := analytics.EmailSHA256(email)
	person := personOf(t, pool, analytics.IDKindAID, aid)
	if got := personOf(t, pool, analytics.IdentityEmailSHA256, hash); got != person {
		t.Fatal("aid and e-mail hash are different people")
	}
	if got := personOf(t, pool, analytics.IDKindTG, analytics.NewTGHasher(rollupTestSecret).Hash(tgID)); got != person {
		t.Fatal("the Telegram hash is a different person")
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.identities WHERE value = $1`, fmt.Sprint(tgID)); n != 0 {
		t.Fatal("the raw Telegram id was stored")
	}
	if prof := loadProfile(t, pool, person); prof.Stage != analytics.StageApplicant {
		t.Errorf("stage = %q, want applicant", prof.Stage)
	}

	// onboarding.Purge cuts the link to the address and clears the aid.
	reviewer := uuid.New()
	if err := svc.Purge(ctx, started.App.ID, reviewer); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.identities WHERE kind = 'email_sha256' AND value = $1`, hash); n != 0 {
		t.Error("the e-mail identity survived the purge")
	}
	var storedAID *string
	if err := pool.QueryRow(ctx, `SELECT aid FROM onboarding_applications WHERE id = $1`, started.App.ID).Scan(&storedAID); err != nil || storedAID != nil {
		t.Errorf("aid after purge = %v (%v)", storedAID, err)
	}
	// The next rollup must not rebuild the link from the purged row.
	if _, err := roll.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM analytics.identities WHERE kind = 'email_sha256' AND value = $1`, hash); n != 0 {
		t.Error("the rollup rebuilt the e-mail identity of a purged application")
	}
}

func TestAnalyticsRollup_FlowDailyIsRebuiltNotDoubled(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := nonceString()
	flow := "ft" + nonce[len(nonce)-10:]
	user := uuid.NewString()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.events WHERE id_value = $1`, user)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.people WHERE id IN (SELECT person_id FROM analytics.identities WHERE value = $1)`, user)
		_, _ = pool.Exec(ctx, `DELETE FROM analytics.flow_daily WHERE flow = $1`, flow)
	})
	w := analytics.NewWriter(analytics.Options{DB: pool})
	base := time.Now().UTC().Add(-2 * time.Minute)
	step := func(off time.Duration, name string, props map[string]any) {
		w.Track(ctx, analytics.Event{At: base.Add(off), Surface: analytics.SurfaceEventCenter, Name: name,
			IDKind: analytics.IDKindUser, IDValue: user, Props: props})
	}
	step(0, "flow_step", map[string]any{"flow": flow, "step": "title"})
	step(20*time.Second, "flow_step", map[string]any{"flow": flow, "step": "venue"})
	step(25*time.Second, "ui_error", map[string]any{"screen": "/events/new", "code": "venue.invalid"})
	fctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := w.Close(fctx); err != nil {
		t.Fatal(err)
	}
	roll := analytics.NewRollup(analytics.RollupOptions{Pool: pool})
	for run := 1; run <= 2; run++ {
		if _, err := roll.Run(ctx); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		var entered, left, errs int
		var median *int
		if err := pool.QueryRow(ctx, `SELECT entered, left_here, errors, median_seconds FROM analytics.flow_daily WHERE flow = $1 AND step = 'venue'`, flow).
			Scan(&entered, &left, &errs, &median); err != nil {
			t.Fatalf("run %d venue row: %v", run, err)
		}
		if entered != 1 || left != 1 || errs != 1 {
			t.Errorf("run %d: venue entered %d left %d errors %d, want 1/1/1 (a re-run must not double)", run, entered, left, errs)
		}
		if err := pool.QueryRow(ctx, `SELECT entered, left_here, errors, median_seconds FROM analytics.flow_daily WHERE flow = $1 AND step = 'title'`, flow).
			Scan(&entered, &left, &errs, &median); err != nil || entered != 1 || left != 0 || median == nil || *median != 20 {
			t.Errorf("run %d: title row %d/%d/%v (%v)", run, entered, left, median, err)
		}
	}
}
