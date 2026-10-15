//go:build integration

package himports

// event_bundle_dry_run_integration_test.go — task 3-1 against a live
// database: a dry run checks the bundle with the import's own code, reports
// every error with its field, and writes nothing — no catalog row, no
// poster download or media object, no audit event, no publication, feed
// token or storefront switch, no session-change journal or letter job, no
// catalog webhook. Each test then imports the same bundle for real to prove
// the counters and row counts would have moved.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

// pngBytes is the smallest body http.DetectContentType calls image/png.
var pngBytes = []byte("\x89PNG\r\n\x1a\n0000000000000000")

// countingDoer serves a poster and counts the downloads.
type countingDoer struct{ calls atomic.Int32 }

func (d *countingDoer) Do(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"image/png"}},
		Body:       io.NopCloser(bytes.NewReader(pngBytes)),
	}, nil
}

// countingAudit counts audit events instead of storing them.
type countingAudit struct{ calls atomic.Int32 }

func (a *countingAudit) Write(context.Context, audit.Event) error { a.calls.Add(1); return nil }
func (a *countingAudit) WriteTx(context.Context, pgx.Tx, audit.Event) error {
	a.calls.Add(1)
	return nil
}

// dryRunRig is the handler with every outside effect counted.
type dryRunRig struct {
	h         *Handler
	http      *countingDoer
	audit     *countingAudit
	published atomic.Int32
}

func newDryRunRig(t *testing.T, pool *pgxpool.Pool) *dryRunRig {
	t.Helper()
	rig := &dryRunRig{http: &countingDoer{}, audit: &countingAudit{}}
	rig.h = New(gen.New(pool), pool, rig.audit, nil).
		WithMembershipQueries(gen.New(pool)).
		WithMedia(newBundle525Media(t, pool)).
		WithHTTPClient(rig.http).
		WithCatalogEventPublisher(func(context.Context, string, string, string, []string) { rig.published.Add(1) })
	return rig
}

// dryRun posts the bundle with ?dry_run=true as the fixture's member user.
func dryRun(t *testing.T, f *bundle525Fixture, h *Handler, body bil24compat.ImportSessionRequest) ImportDryRunResponse {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+f.orgID.String()+"/imports/event-bundle?dry_run=true", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", f.orgID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: f.userID.String(), Type: auth.ActorTypeUser})
	rec := httptest.NewRecorder()
	h.HandleEventBundle(rec, req.WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run answered %d: %s", rec.Code, rec.Body.String())
	}
	var out ImportDryRunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode dry run: %v; body=%s", err, rec.Body.String())
	}
	if !out.DryRun {
		t.Fatalf("dry_run flag missing: %s", rec.Body.String())
	}
	return out
}

// orgFootprint counts every row a bundle import of this organization can
// write, directly or through the session-change journal.
func orgFootprint(ctx context.Context, t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) map[string]int {
	t.Helper()
	queries := map[string]string{
		"events":   `SELECT count(*) FROM events WHERE org_id = $1`,
		"sessions": `SELECT count(*) FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		"venues":   `SELECT count(*) FROM venues WHERE org_id = $1`,
		"tiers": `SELECT count(*) FROM ticket_tiers t JOIN sessions s ON s.id = t.session_id
		          JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		"places": `SELECT count(*) FROM session_seats ss JOIN sessions s ON s.id = ss.session_id
		           JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		"ledger": `SELECT count(*) FROM inventory_ledger l JOIN sessions s ON s.id = l.session_id
		           JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		"external_refs": `SELECT count(*) FROM session_external_refs WHERE org_id = $1`,
		"compat_ids": `SELECT count(*) FROM compatibility_id_map WHERE platform_id IN (
		                 SELECT id FROM events WHERE org_id = $1 UNION SELECT id FROM venues WHERE org_id = $1
		                 UNION SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`,
		"media": `SELECT count(*) FROM media_objects WHERE org_id = $1`,
		"publications": `SELECT count(*) FROM event_publications p JOIN events e ON e.id = p.event_id
		                 WHERE e.org_id = $1`,
		"feed_tokens": `SELECT count(*) FROM agent_feed_tokens ft JOIN sales_channels c ON c.id = ft.sales_channel_id
		                WHERE c.org_id = $1`,
		"hosted_pages": `SELECT count(*) FROM sales_channels WHERE org_id = $1
		                 AND settings #>> '{hosted_page,enabled}' = 'true'`,
		"session_changes": `SELECT count(*) FROM session_changes WHERE org_id = $1`,
		"letter_jobs": `SELECT count(*) FROM worker_jobs WHERE job_type = 'session.change_email'
		                AND payload->>'order_id' IN (SELECT id::text FROM orders WHERE org_id = $1)`,
		"audit_rows": `SELECT count(*) FROM audit_events WHERE metadata->>'org_id' = $1::text`,
	}
	out := make(map[string]int, len(queries))
	for name, sql := range queries {
		var n int
		if err := pool.QueryRow(ctx, sql, orgID).Scan(&n); err != nil {
			t.Fatalf("footprint %s: %v", name, err)
		}
		out[name] = n
	}
	return out
}

func assertSameFootprint(t *testing.T, label string, before, after map[string]int) {
	t.Helper()
	for name, n := range before {
		if after[name] != n {
			t.Errorf("%s: %s went %d -> %d; a dry run must write nothing", label, name, n, after[name])
		}
	}
}

func (rig *dryRunRig) assertNoOutsideEffects(t *testing.T, label string) {
	t.Helper()
	if n := rig.http.calls.Load(); n != 0 {
		t.Errorf("%s: the poster was downloaded %d times", label, n)
	}
	if n := rig.audit.calls.Load(); n != 0 {
		t.Errorf("%s: %d audit events were written", label, n)
	}
	if n := rig.published.Load(); n != 0 {
		t.Errorf("%s: the catalog webhook fired %d times", label, n)
	}
}

func defaultValue(defs []ImportDefault, name string) (string, bool) {
	for _, d := range defs {
		if d.Name == name {
			return d.Value, true
		}
	}
	return "", false
}

// newDryRunChannel gives the fixture's organization a sales channel without a
// storefront page, and returns its cleanup.
func newDryRunChannel(ctx context.Context, t *testing.T, f *bundle525Fixture) (uuid.UUID, func()) {
	t.Helper()
	id := uuid.New()
	if _, err := f.pool.Exec(ctx, `INSERT INTO sales_channels (id, org_id, name) VALUES ($1, $2, $3)`,
		id, f.orgID, "Dry run channel "+id.String()[:8]); err != nil {
		t.Fatal(err)
	}
	return id, func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM event_publications WHERE feed_token_id IN (
		                           SELECT id FROM agent_feed_tokens WHERE sales_channel_id = $1)`, id)
		_, _ = f.pool.Exec(ctx, `DELETE FROM agent_feed_tokens WHERE sales_channel_id = $1`, id)
		_, _ = f.pool.Exec(ctx, `DELETE FROM sales_channels WHERE id = $1`, id)
	}
}

// TestEventBundleDryRun_NewEventWritesNothing: a valid new-event bundle that
// asks for a poster, publication and a channel is reported valid with the
// defaults the import would apply, and leaves no trace. The same bundle
// imported for real then moves every counter.
func TestEventBundleDryRun_NewEventWritesNothing(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	channelID, dropChannel := newDryRunChannel(ctx, t, f)
	defer dropChannel()
	rig := newDryRunRig(t, pool)

	body := f.payload()
	body.Publish = true
	body.ChannelIDs = []string{channelID.String()}
	body.Action.BigPosterURL = "https://posters.example.test/dry-run.png"
	body.Venue.VenueName = "  " + f.venueName + "  "

	before := orgFootprint(ctx, t, pool, f.orgID)
	out := dryRun(t, f, rig.h, body)
	assertSameFootprint(t, "new event", before, orgFootprint(ctx, t, pool, f.orgID))
	rig.assertNoOutsideEffects(t, "new event")

	if !out.Valid || len(out.Errors) != 0 {
		t.Fatalf("a valid bundle was refused: %+v", out.Errors)
	}
	if out.EventID != nil || out.SessionID != nil {
		t.Fatalf("a new event must report no ids, got event %v session %v", out.EventID, out.SessionID)
	}
	if out.Source != bil24compat.SourceArena || out.Normalized.Venue.VenueName != f.venueName {
		t.Fatalf("normalized bundle = %+v", out.Normalized)
	}
	for name, want := range map[string]string{
		"session_end_time": "2026-04-26T20:00:00+02:00",
		"sales_end_time":   "2026-04-26T17:00:00+02:00",
		"event_slug":       "bundle525-grand-tasting",
		"session_status":   "scheduled",
	} {
		if got, ok := defaultValue(out.Defaults, name); !ok || got != want {
			t.Errorf("default %s = %q (present %v), want %q", name, got, ok, want)
		}
	}

	// The real import of the same bundle does every one of those things.
	rec, created := f.call(rig.h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("real import: %d %s", rec.Code, rec.Body.String())
	}
	after := orgFootprint(ctx, t, pool, f.orgID)
	for _, name := range []string{"events", "sessions", "tiers", "places", "media", "publications", "feed_tokens", "hosted_pages", "compat_ids"} {
		if after[name] <= before[name] {
			t.Errorf("real import did not write %s (%d -> %d): the dry-run assertion proves nothing", name, before[name], after[name])
		}
	}
	if rig.http.calls.Load() != 1 || rig.audit.calls.Load() != 1 || rig.published.Load() != 1 {
		t.Errorf("real import effects: poster %d audit %d webhook %d, want 1 each",
			rig.http.calls.Load(), rig.audit.calls.Load(), rig.published.Load())
	}

	// A dry run of the now existing event names it and still writes nothing.
	again := orgFootprint(ctx, t, pool, f.orgID)
	out = dryRun(t, f, rig.h, body)
	assertSameFootprint(t, "existing event", again, orgFootprint(ctx, t, pool, f.orgID))
	if !out.Valid || out.EventID == nil || *out.EventID != created.EventID ||
		out.SessionID == nil || *out.SessionID != created.SessionID {
		t.Fatalf("dry run of the existing event: %+v", out)
	}
	if rig.http.calls.Load() != 1 || rig.audit.calls.Load() != 1 || rig.published.Load() != 1 {
		t.Error("the second dry run reached outside")
	}
}

// TestEventBundleDryRun_MovingASessionWithBuyersJournalsNothing: the dry run
// answers the session-change refusal through sessionchange.Preview, and an
// allowed move neither moves the session, journals it nor queues a letter.
func TestEventBundleDryRun_MovingASessionWithBuyersJournalsNothing(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	rig := newDryRunRig(t, pool)

	rec, created := f.call(rig.h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	defer f.seedPaidTicket(ctx, t, created.SessionID, created.EventID)()
	rig.audit.calls.Store(0)

	move := f.payload()
	move.ActionEvent.Day = "03.05.2026"
	move.ChangeMessage = "Moved by a week."

	startOf := func() string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT start_at::text FROM sessions WHERE id = $1`, created.SessionID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	start := startOf()

	before := orgFootprint(ctx, t, pool, f.orgID)
	out := dryRun(t, f, rig.h, move)
	if out.Valid || len(out.Errors) != 1 || out.Errors[0].Code != "organization.contact_missing" {
		t.Fatalf("move without a contact: %+v", out.Errors)
	}

	if _, err := pool.Exec(ctx, `UPDATE organizations SET contact_email = 'dry-run@example.com' WHERE id = $1`, f.orgID); err != nil {
		t.Fatal(err)
	}
	out = dryRun(t, f, rig.h, move)
	if !out.Valid || out.SessionID == nil || *out.SessionID != created.SessionID {
		t.Fatalf("allowed move: %+v", out)
	}
	assertSameFootprint(t, "move", before, orgFootprint(ctx, t, pool, f.orgID))
	rig.assertNoOutsideEffects(t, "move")
	if startOf() != start {
		t.Fatal("the dry run moved the session")
	}

	tooLong := move
	tooLong.ChangeMessage = strings.Repeat("x", 1001)
	out = dryRun(t, f, rig.h, tooLong)
	if out.Valid || out.Errors[0].Field != "/changeMessage" {
		t.Fatalf("over-long message: %+v", out.Errors)
	}

	// The real move journals and queues exactly as before.
	if rec, _ := f.call(rig.h, move); rec.Code != http.StatusOK {
		t.Fatalf("real move: %d %s", rec.Code, rec.Body.String())
	}
	after := orgFootprint(ctx, t, pool, f.orgID)
	if after["session_changes"] != before["session_changes"]+1 || after["letter_jobs"] != before["letter_jobs"]+1 {
		t.Fatalf("real move: journal %d -> %d, letters %d -> %d", before["session_changes"], after["session_changes"],
			before["letter_jobs"], after["letter_jobs"])
	}
}

// TestEventBundleDryRun_ReportsEveryReferenceAtOnce: unknown event, session,
// venue, promoter, category and channel, plus a field error, come back
// together, each with its JSON pointer — where the import stops at the
// first one.
func TestEventBundleDryRun_ReportsEveryReferenceAtOnce(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	rig := newDryRunRig(t, pool)

	promoter := uuid.NewString()
	body := f.payload()
	body.Action.ArenaEventID = uuid.NewString()
	body.Action.PromoterID = &promoter
	body.Venue.ArenaVenueID = uuid.NewString()
	body.ChannelIDs = []string{uuid.NewString(), "not-a-uuid"}
	body.CategoryList[0].ArenaTierID = uuid.NewString()
	body.CategoryList[1].Price = -1

	before := orgFootprint(ctx, t, pool, f.orgID)
	out := dryRun(t, f, rig.h, body)
	assertSameFootprint(t, "references", before, orgFootprint(ctx, t, pool, f.orgID))
	rig.assertNoOutsideEffects(t, "references")

	got := map[string]string{}
	for _, is := range out.Errors {
		got[is.Field] = is.Code
	}
	want := map[string]string{
		"/action/arenaEventId":        "import.invalid_event",
		"/action/promoterId":          "import.invalid_promoter",
		"/venue/arenaVenueId":         "import.invalid_venue",
		"/channelIds/0":               "import.invalid_channel",
		"/channelIds/1":               "import.invalid_channel",
		"/categoryList/0/arenaTierId": "import.invalid_category",
		"/categoryList/1/price":       "import.invalid_price",
	}
	for field, code := range want {
		if got[field] != code {
			t.Errorf("%s: got %q, want %q (all %v)", field, got[field], code, got)
		}
	}
	if out.Valid {
		t.Fatal("a broken bundle was reported valid")
	}

	// The import itself still answers just the first of them.
	rec, _ := f.call(rig.h, body)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "import.invalid_channel") {
		t.Fatalf("import answered %d %s, want the first error only", rec.Code, rec.Body.String())
	}
}
