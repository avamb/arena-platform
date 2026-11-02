//go:build integration

package httpserver

// analytics_ux_integration_test.go — stage A8 of spec 50: the UX dashboards of
// the admin console (flows, errors, person timeline) through the REAL router,
// and the weekly digest built from the same rows. Run on a scratch database:
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go.exe test -tags integration -p 1 \
//	  ./apps/backend/internal/platform/httpserver/ -run TestAnalyticsUX
//
// The seeded rows sit in the month AFTER the current one, whose events
// partition the migration already created, so the periods never touch real
// data and the tests do not depend on today's date.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
)

type uxFlowsResp struct {
	From         string `json:"from"`
	To           string `json:"to"`
	PreviousFrom string `json:"previous_from"`
	PreviousTo   string `json:"previous_to"`
	Flows        []struct {
		Surface, Flow string
		Steps         []struct {
			Step          string
			Entered       int
			LeftHere      int      `json:"left_here"`
			DropOffPct    *float64 `json:"drop_off_pct"`
			Errors        int
			MedianSeconds *int `json:"median_seconds"`
			Previous      struct {
				Entered  int
				LeftHere int `json:"left_here"`
			}
		}
	}
}

type uxErrorsResp struct {
	Items []struct {
		Kind, Surface, Key, Place string
		Count                     int
		PreviousCount             int      `json:"previous_count"`
		ChangePct                 *float64 `json:"change_pct"`
	}
}

type uxTimelineResp struct {
	PersonID       string  `json:"person_id"`
	TwentyPersonID *string `json:"twenty_person_id"`
	Profile        *struct {
		Stage            string
		ActivityScore    int    `json:"activity_score"`
		ScoreExplanation string `json:"score_explanation"`
	}
	Sessions []struct{ Surface string }
	Events   []struct {
		Name  string
		Props map[string]any
	}
	NextCursor *string `json:"next_cursor"`
}

func uxAnchor() time.Time {
	m := analytics.MonthStart(time.Now().UTC())
	return m.AddDate(0, 1, 0)
}

func TestAnalyticsUX_FlowsErrorsTimelineAndDigest(t *testing.T) {
	e := newSec1Env(t)
	ctx := context.Background()
	pool := e.srv.pgxPool
	_, super := e.superadmin(t)
	run := strings.ToLower(uuid.NewString()[:8])
	base := uxAnchor()
	d := func(n int) time.Time { return base.AddDate(0, 0, n) }
	dayStr := func(n int) string { return d(n).Format("2006-01-02") }

	flow := "a8flow" + run
	code := "a8code" + run
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	person := uuid.New()
	aid := "a_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM analytics.flow_daily WHERE flow = $1`, flow)
		_, _ = pool.Exec(bg, `DELETE FROM analytics.events WHERE props->>'code' = $1 OR id_value = $2 OR props->>'reason' = $1`, code, aid)
		_, _ = pool.Exec(bg, `DELETE FROM analytics.people WHERE id = $1`, person)
	})

	// Flow numbers: current days 10-11, previous days 8-9.
	fd := `INSERT INTO analytics.flow_daily (day, surface, flow, step, entered, left_here, errors, median_seconds)
	       VALUES ($1::date, 'bot', $2, $3, $4, $5, $6, $7)`
	exec(fd, dayStr(10), flow, "venue", 10, 4, 1, 30)
	exec(fd, dayStr(11), flow, "venue", 5, 1, 0, 50)
	exec(fd, dayStr(8), flow, "venue", 6, 2, 0, 20)
	exec(fd, dayStr(10), flow, "price", 8, 0, 0, nil)
	exec(fd, dayStr(10), flow+"x", "other", 3, 3, 0, nil) // a second flow, event_center
	exec(`UPDATE analytics.flow_daily SET surface = 'event_center' WHERE flow = $1`, flow+"x")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM analytics.flow_daily WHERE flow = $1`, flow+"x")
	})

	// Errors: 3 now, 1 before, one robot row that must not count.
	ev := `INSERT INTO analytics.events (at, surface, name, id_kind, id_value, props, suspect)
	       VALUES ($1, $2, $3, 'user', $4, $5::jsonb, $6)`
	uid := uuid.NewString()
	for i := 0; i < 3; i++ {
		exec(ev, d(10).Add(time.Duration(i)*time.Hour), "event_center", "ui_error", uid, fmt.Sprintf(`{"code":%q,"screen":"/x"}`, code), false)
	}
	exec(ev, d(9).Add(time.Hour), "event_center", "ui_error", uid, fmt.Sprintf(`{"code":%q,"screen":"/x"}`, code), false)
	exec(ev, d(10).Add(5*time.Hour), "event_center", "ui_error", uid, fmt.Sprintf(`{"code":%q,"screen":"/x"}`, code), true)
	exec(ev, d(10).Add(6*time.Hour), "bot", "wizard_input_error", uid, fmt.Sprintf(`{"step":"venue","reason":%q}`, code), false)

	period := fmt.Sprintf("from=%s&to=%s", dayStr(10), dayStr(11))

	t.Run("flows", func(t *testing.T) {
		body := e.expect(t, 200, "GET", "/v1/admin/analytics/flows?surface=bot&"+period, super, "", "")
		var r uxFlowsResp
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		if r.PreviousFrom != dayStr(8) || r.PreviousTo != dayStr(9) {
			t.Errorf("previous period = %s..%s", r.PreviousFrom, r.PreviousTo)
		}
		var found bool
		for _, f := range r.Flows {
			if f.Flow == flow+"x" {
				t.Errorf("the event_center flow must be filtered out by surface=bot")
			}
			if f.Flow != flow {
				continue
			}
			found = true
			if len(f.Steps) != 2 || f.Steps[0].Step != "venue" || f.Steps[1].Step != "price" {
				t.Fatalf("steps = %+v", f.Steps)
			}
			v := f.Steps[0]
			if v.Entered != 15 || v.LeftHere != 5 || v.Errors != 1 || v.Previous.Entered != 6 || v.Previous.LeftHere != 2 {
				t.Errorf("venue numbers = %+v", v)
			}
			if v.DropOffPct == nil || *v.DropOffPct != 33.3 {
				t.Errorf("drop-off = %v", v.DropOffPct)
			}
			if v.MedianSeconds == nil || *v.MedianSeconds != 40 {
				t.Errorf("median = %v", v.MedianSeconds)
			}
			if p := f.Steps[1]; p.Entered != 8 || p.LeftHere != 0 || p.MedianSeconds != nil {
				t.Errorf("price numbers = %+v", p)
			}
		}
		if !found {
			t.Fatalf("flow %s missing: %s", flow, body)
		}
		// Without a surface filter both flows are listed.
		body = e.expect(t, 200, "GET", "/v1/admin/analytics/flows?"+period, super, "", "")
		if !strings.Contains(body, flow+"x") {
			t.Errorf("event_center flow missing without a surface filter")
		}
	})

	t.Run("errors", func(t *testing.T) {
		body := e.expect(t, 200, "GET", "/v1/admin/analytics/errors?"+period, super, "", "")
		var r uxErrorsResp
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		var ui, wiz bool
		for _, it := range r.Items {
			switch {
			case it.Kind == "ui_error" && it.Key == code:
				ui = true
				if it.Count != 3 || it.PreviousCount != 1 || it.Place != "/x" || it.ChangePct == nil || *it.ChangePct != 200 {
					t.Errorf("ui_error = %+v (the suspect row must not count)", it)
				}
			case it.Kind == "wizard_input_error" && it.Key == code:
				wiz = true
				if it.Count != 1 || it.Place != "venue" || it.ChangePct != nil {
					t.Errorf("wizard_input_error = %+v", it)
				}
			}
		}
		if !ui || !wiz {
			t.Fatalf("rows missing (ui=%v wizard=%v): %s", ui, wiz, body)
		}
		body = e.expect(t, 200, "GET", "/v1/admin/analytics/errors?surface=bot&"+period, super, "", "")
		if strings.Contains(body, `"ui_error"`) {
			t.Errorf("surface=bot must leave the event center error out")
		}
	})

	t.Run("digest", func(t *testing.T) {
		// The digest of the Monday after day 14: the week before is days
		// 7..13 or similar, so build it from the explicit period instead.
		week := analytics.Period{From: d(10), To: d(11)}
		dg, err := analytics.BuildUXDigest(ctx, pool, week)
		if err != nil {
			t.Fatal(err)
		}
		text := analytics.FormatUXDigest(dg)
		if !strings.Contains(text, "bot / "+flow+" / venue: 5 left (33% of 15), was 2, +150%") {
			t.Errorf("digest lacks the venue step:\n%s", text)
		}
		if !strings.Contains(text, "ui_error "+code+" at /x [event_center]: 3, was 1, +200%") {
			t.Errorf("digest lacks the error:\n%s", text)
		}
		if strings.Contains(text, "@") || strings.Contains(text, uid) {
			t.Errorf("digest carries personal data:\n%s", text)
		}
	})

	t.Run("digest handler sends once and queues the next Monday", func(t *testing.T) {
		exec(`DELETE FROM worker_jobs WHERE job_type = $1`, analytics.UXDigestJobType)
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM worker_jobs WHERE job_type = $1`, analytics.UXDigestJobType)
		})
		var sent []string
		h := analytics.NewUXDigestHandler(analytics.UXDigestHandlerOptions{
			DB: pool, Notifier: uxCaptureNotifier{&sent},
			Scheduler: analytics.NewPGUXDigestScheduler(pool),
			// Monday 09:00 Madrid, a fortnight into the seeded month.
			Now: func() time.Time { return uxMondayAfter(d(14)) },
		})
		for i := 0; i < 2; i++ { // a retry must not fork the chain
			if err := h(ctx, nil); err != nil {
				t.Fatal(err)
			}
		}
		if len(sent) != 2 || !strings.Contains(sent[0], "UX digest") {
			t.Fatalf("sent = %v", sent)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND status = 'pending'`, analytics.UXDigestJobType).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("pending digest jobs = %d, want exactly 1", n)
		}
		// The startup scheduler adds nothing while one is pending.
		if err := analytics.ScheduleUXDigestJob(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1`, analytics.UXDigestJobType).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("digest jobs after the startup scheduler = %d, want 1", n)
		}
	})

	t.Run("timeline", func(t *testing.T) {
		exec(`INSERT INTO analytics.people (id, twenty_person_id, first_touch, last_touch)
		      VALUES ($1, $2, '{"source":"newsletter"}', '{"source":"google"}')`, person, "crm-"+run)
		exec(`INSERT INTO analytics.identities (kind, value, person_id, seen_at) VALUES ('aid', $1, $2, now())`, aid, person)
		for i := 0; i < 3; i++ {
			exec(`INSERT INTO analytics.events (at, surface, name, id_kind, id_value, props)
			      VALUES ($1, 'site', 'page_view', 'aid', $2, $3::jsonb)`,
				d(10).Add(time.Duration(i)*time.Minute), aid, fmt.Sprintf(`{"path":"/p%d"}`, i))
		}
		exec(`INSERT INTO analytics.events (at, surface, name, id_kind, id_value, props, suspect)
		      VALUES ($1, 'site', 'page_view', 'aid', $2, '{"path":"/robot"}', true)`, d(10).Add(time.Hour), aid)
		exec(`INSERT INTO analytics.sessions (id, person_id, surface, started_at, ended_at, pages, engaged_seconds, entry)
		      VALUES ($1, $2, 'site', $3, $4, 3, 90, '{"path":"/p0"}')`, uuid.New(), person, d(10), d(10).Add(2*time.Minute))
		exec(`INSERT INTO analytics.profiles (person_id, stage, last_seen_at, visits_30d, engaged_minutes_30d, articles, milestones, activity_score, changed_at)
		      VALUES ($1, 'applicant', $2, 1, 2, '[]', '{"application_started":"2026-10-22T09:00:00Z"}', 11, now())`, person, d(10))

		path := "/v1/admin/analytics/people/" + person.String() + "/timeline"
		body := e.expect(t, 200, "GET", path+"?limit=2", super, "", "")
		var r uxTimelineResp
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		if r.PersonID != person.String() || r.TwentyPersonID == nil || *r.TwentyPersonID != "crm-"+run {
			t.Errorf("person = %+v", r)
		}
		if r.Profile == nil || r.Profile.Stage != "applicant" || r.Profile.ActivityScore != 11 ||
			!strings.Contains(r.Profile.ScoreExplanation, "visit x1") || !strings.Contains(r.Profile.ScoreExplanation, "application_started x1") {
			t.Errorf("profile = %+v", r.Profile)
		}
		if len(r.Sessions) != 1 {
			t.Errorf("sessions = %+v", r.Sessions)
		}
		// Newest first, the robot row left out, two per page.
		if len(r.Events) != 2 || r.Events[0].Props["path"] != "/p2" || r.Events[1].Props["path"] != "/p1" || r.NextCursor == nil {
			t.Fatalf("first page = %+v cursor=%v", r.Events, r.NextCursor)
		}
		body = e.expect(t, 200, "GET", path+"?limit=2&cursor="+*r.NextCursor, super, "", "")
		var r2 uxTimelineResp
		if err := json.Unmarshal([]byte(body), &r2); err != nil {
			t.Fatal(err)
		}
		if len(r2.Events) != 1 || r2.Events[0].Props["path"] != "/p0" || r2.NextCursor != nil {
			t.Errorf("second page = %+v cursor=%v", r2.Events, r2.NextCursor)
		}
		if r2.Profile != nil || len(r2.Sessions) != 0 {
			t.Errorf("a continuation page must carry events only")
		}
		if strings.Contains(body, "robot") {
			t.Errorf("suspect events must not be listed")
		}

		e.expect(t, 404, "GET", "/v1/admin/analytics/people/"+uuid.NewString()+"/timeline", super, "", "")
		e.expect(t, 400, "GET", "/v1/admin/analytics/people/nope/timeline", super, "", "")
		e.expect(t, 400, "GET", path+"?cursor=garbage", super, "", "")
		e.expect(t, 400, "GET", path+"?limit=201", super, "", "")
		e.expect(t, 400, "GET", "/v1/admin/analytics/flows?from=2026-01-01&to=2026-11-01", super, "", "")
	})

	t.Run("only a platform superadmin reads", func(t *testing.T) {
		org, err := e.q.InsertOrganization(ctx, "A8 org "+run, "a8-org-"+run, "ES", "en", 1200)
		if err != nil {
			t.Fatal(err)
		}
		e.orgs = append(e.orgs, org.ID)
		owner := e.user(t, "a8owner")
		e.member(t, owner, org.ID, "org_admin")
		ownerTok := e.token(t, owner)
		key := e.key(t, org.ID, "report.read", "event.read")
		for _, p := range []string{
			"/v1/admin/analytics/flows?" + period,
			"/v1/admin/analytics/errors?" + period,
			"/v1/admin/analytics/people/" + person.String() + "/timeline",
		} {
			e.expect(t, 401, "GET", p, "", "", "")
			e.expect(t, 403, "GET", p, ownerTok, "", "")
			e.expect(t, 403, "GET", p, key, "", "")
		}
	})
}

type uxCaptureNotifier struct{ sent *[]string }

func (c uxCaptureNotifier) Send(_ context.Context, text string) error {
	*c.sent = append(*c.sent, text)
	return nil
}

// uxMondayAfter is 09:00 Madrid time on the first Monday on or after t.
func uxMondayAfter(t time.Time) time.Time {
	return analytics.NextDigestTime(t.Add(-time.Second).Truncate(24 * time.Hour))
}
