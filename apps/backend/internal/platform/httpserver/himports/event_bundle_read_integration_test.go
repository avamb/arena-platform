//go:build integration

package himports

// event_bundle_read_integration_test.go — task 3-2 against a live database:
// GET .../events/{event_id}/event-bundle answers the event as bundles the
// import accepts. The round trip is the contract: import a bundle, read the
// event back, re-import what was read (the dry run accepts it, the rows do
// not change), and a second read equals the first.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/tierchain"
)

// getEventBundle drives the real read handler as the fixture's member user.
func (f *bundle525Fixture) getEventBundle(h *Handler, orgID, eventID uuid.UUID, userID uuid.UUID) (*httptest.ResponseRecorder, EventBundleReadResponse) {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodGet,
		"/v1/organizations/"+orgID.String()+"/events/"+eventID.String()+"/event-bundle", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID.String())
	rctx.URLParams.Add("event_id", eventID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: userID.String(), Type: auth.ActorTypeUser})
	rec := httptest.NewRecorder()
	h.HandleEventBundleRead(rec, req.WithContext(ctx))
	var out EventBundleReadResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			f.t.Fatalf("decode event bundle: %v; body=%s", err, rec.Body.String())
		}
	}
	return rec, out
}

func (f *bundle525Fixture) readBundles(h *Handler, eventID uuid.UUID) (EventBundleReadResponse, string) {
	f.t.Helper()
	rec, out := f.getEventBundle(h, f.orgID, eventID, f.userID)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("event-bundle read: %d %s", rec.Code, rec.Body.String())
	}
	return out, rec.Body.String()
}

// catalogSnapshot dumps every row an import of this organization's events can
// write as one comparable string. Left out: the updated_at stamps and the two
// optimistic counters an import always bumps (sessions.seat_status_version,
// inventory_ledger.version), and the ids of price-window rows — a category's
// schedule is replaced as a whole, so its windows come back with the same
// content under new ids.
func catalogSnapshot(ctx context.Context, t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID) string {
	t.Helper()
	queries := []string{
		`SELECT coalesce(jsonb_agg(to_jsonb(e) - 'updated_at' ORDER BY e.id), '[]')::text
		   FROM events e WHERE e.org_id = $1`,
		`SELECT coalesce(jsonb_agg(to_jsonb(s) - 'updated_at' - 'seat_status_version' ORDER BY s.id), '[]')::text
		   FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		`SELECT coalesce(jsonb_agg(to_jsonb(t) - 'updated_at' ORDER BY t.id), '[]')::text
		   FROM ticket_tiers t JOIN sessions s ON s.id = t.session_id
		   JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		`SELECT coalesce(jsonb_agg(to_jsonb(c) - 'updated_at' ORDER BY c.tier_id), '[]')::text
		   FROM ticket_tier_chain c JOIN ticket_tiers t ON t.id = c.tier_id
		   JOIN sessions s ON s.id = t.session_id JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		`SELECT coalesce(jsonb_agg(jsonb_build_array(p.tier_id, p.valid_from, p.valid_to, p.price_amount)
		                           ORDER BY p.tier_id, p.valid_from), '[]')::text
		   FROM ticket_tier_prices p JOIN ticket_tiers t ON t.id = p.tier_id
		   JOIN sessions s ON s.id = t.session_id JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		`SELECT coalesce(jsonb_agg(x ORDER BY x), '[]')::text FROM (
		   SELECT jsonb_build_array(ss.tier_id, ss.status, count(*)) AS x
		     FROM session_seats ss JOIN sessions s ON s.id = ss.session_id
		     JOIN events e ON e.id = s.event_id WHERE e.org_id = $1
		    GROUP BY ss.tier_id, ss.status) q`,
		`SELECT coalesce(jsonb_agg(to_jsonb(l) - 'updated_at' - 'version' ORDER BY l.session_id), '[]')::text
		   FROM inventory_ledger l JOIN sessions s ON s.id = l.session_id
		   JOIN events e ON e.id = s.event_id WHERE e.org_id = $1`,
		`SELECT coalesce(jsonb_agg(to_jsonb(p) ORDER BY p.id), '[]')::text
		   FROM event_publications p JOIN events e ON e.id = p.event_id WHERE e.org_id = $1`,
		`SELECT coalesce(jsonb_agg(to_jsonb(p) - 'updated_at' ORDER BY p.event_id), '[]')::text
		   FROM event_promoters p WHERE p.org_id = $1`,
		`SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY r.session_id), '[]')::text
		   FROM session_external_refs r WHERE r.org_id = $1`,
		`SELECT coalesce(jsonb_agg(to_jsonb(v) - 'updated_at' ORDER BY v.id), '[]')::text
		   FROM venues v WHERE v.org_id = $1`,
		`SELECT count(*)::text FROM media_objects WHERE org_id = $1`,
		`SELECT count(*)::text FROM session_changes WHERE org_id = $1`,
	}
	var b strings.Builder
	for _, q := range queries {
		var s string
		if err := pool.QueryRow(ctx, q, orgID).Scan(&s); err != nil {
			t.Fatalf("snapshot: %v\n%s", err, q)
		}
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return b.String()
}

// reimport posts every bundle of a read for real and returns the answers.
func (f *bundle525Fixture) reimport(h *Handler, in EventBundleReadResponse) {
	f.t.Helper()
	for i, b := range in.Bundles {
		rec, out := f.call(h, b)
		if rec.Code != http.StatusOK {
			f.t.Fatalf("re-import of bundle %d: %d %s", i, rec.Code, rec.Body.String())
		}
		if out.Created {
			f.t.Fatalf("re-import of bundle %d created a session", i)
		}
	}
}

// TestEventBundleRead_RoundTrip is the 3-2 acceptance test: one event with a
// promoter, a channel, a published state, doors/sales-end times, category
// windows and a price schedule.
func TestEventBundleRead_RoundTrip(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	channelID, dropChannel := newDryRunChannel(ctx, t, f)
	defer dropChannel()
	h := newBundle525Handler(t, pool)

	promoter, err := gen.New(pool).InsertOrgPromoter(ctx, f.orgID, "Round trip promoter "+uuid.NewString()[:8], nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM event_promoters WHERE org_id = $1`, f.orgID)
		_, _ = pool.Exec(ctx, `DELETE FROM org_promoters WHERE org_id = $1`, f.orgID)
	}()

	madrid, _ := time.LoadLocation("Europe/Madrid")
	soon := time.Now().In(madrid).Add(30 * 24 * time.Hour).Truncate(time.Hour)
	body := f.payload()
	promoterID := promoter.ID.String()
	doors := "2026-04-26T16:30:00+02:00"
	body.Action.PromoterID = &promoterID
	body.Action.Description = "Round trip description"
	body.Publish = true
	body.ChannelIDs = []string{channelID.String()}
	body.ActionEvent.EndTime = "20:00"
	body.ActionEvent.SellEndTime = "2026-04-26T16:00:00+02:00"
	body.ActionEvent.DoorsOpenTime = &doors
	schedule := []bil24compat.ImportPriceWindow{
		{ValidFrom: soon.Format(time.RFC3339), ValidTo: soon.Add(48 * time.Hour).Format(time.RFC3339), Price: 20},
		{ValidFrom: soon.Add(48 * time.Hour).Format(time.RFC3339), Price: 25},
	}
	body.CategoryList[0].PriceSchedule = &schedule
	body.CategoryList[1].SellStartTime = soon.Format(time.RFC3339)
	body.CategoryList[1].SellEndTime = soon.Add(72 * time.Hour).Format(time.RFC3339)

	rec, created := f.call(h, body)
	if rec.Code != http.StatusOK || !created.Created {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	first, firstRaw := f.readBundles(h, created.EventID)
	if first.EventID != created.EventID || first.Status != "published" ||
		len(first.Bundles) != 1 || len(first.Skipped) != 0 {
		t.Fatalf("read: %s", firstRaw)
	}
	b := first.Bundles[0]
	if b.Action.ArenaEventID != created.EventID.String() || b.ActionEvent.ArenaSessionID != created.SessionID.String() {
		t.Errorf("arenaEventId / arenaSessionId are not filled in: %+v %+v", b.Action, b.ActionEvent)
	}
	for i, id := range created.CompatIDs.CategoryPriceIDs {
		if got, want := b.CategoryList[i].ArenaTierID, created.TierIDs[externalIDString(id)].String(); got != want {
			t.Errorf("categoryList[%d].arenaTierId = %s, want %s", i, got, want)
		}
	}
	if b.Action.PromoterID == nil || *b.Action.PromoterID != promoterID || !b.Publish ||
		len(b.ChannelIDs) != 1 || b.ChannelIDs[0] != channelID.String() {
		t.Errorf("promoter / publish / channels: %v %v %v", b.Action.PromoterID, b.Publish, b.ChannelIDs)
	}
	if b.ActionEvent.Day != "26.04.2026" || b.ActionEvent.Time != "17:00" || b.ActionEvent.EndTime != "20:00" ||
		b.ActionEvent.SellEndTime != "2026-04-26T16:00:00+02:00" || b.ActionEvent.DoorsOpenTime == nil ||
		*b.ActionEvent.DoorsOpenTime != doors {
		t.Errorf("session times: %+v", b.ActionEvent)
	}
	if c := b.CategoryList; len(c) != 2 || c[0].Availability != 100 || c[1].Availability != 40 ||
		c[0].Price != 25 || c[1].Price != 12.5 || c[0].PriceSchedule == nil || len(*c[0].PriceSchedule) != 2 {
		t.Errorf("categories: %+v", c)
	}
	if len(first.Warnings) != 0 {
		t.Errorf("warnings: %+v", first.Warnings)
	}

	// The read result is accepted by the dry run, without a single error.
	dry := dryRun(t, f, h, b)
	if !dry.Valid || len(dry.Errors) != 0 {
		t.Fatalf("dry run of the read bundle: %+v", dry.Errors)
	}
	if dry.EventID == nil || *dry.EventID != created.EventID || dry.SessionID == nil || *dry.SessionID != created.SessionID {
		t.Errorf("the dry run must address the same rows: %+v", dry)
	}

	// Re-importing it changes nothing, and a second read is the first.
	before := catalogSnapshot(ctx, t, pool, f.orgID)
	f.reimport(h, first)
	if after := catalogSnapshot(ctx, t, pool, f.orgID); after != before {
		t.Fatalf("re-importing the read bundle changed the rows:\nbefore %s\nafter  %s", before, after)
	}
	second, secondRaw := f.readBundles(h, created.EventID)
	if secondRaw != firstRaw {
		t.Fatalf("second read differs:\nfirst  %s\nsecond %s", firstRaw, secondRaw)
	}
	if len(second.Bundles) != 1 {
		t.Fatal("bundles")
	}
}

// TestEventBundleRead_RoundTripChainAndSeveralDates: two dates, the first with
// a three-step chain whose head has already handed every place over (the
// state a re-save once grew the hall from), a draft event, and the sessions
// a bundle cannot carry.
func TestEventBundleRead_RoundTripChainAndSeveralDates(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	h := newBundle525Handler(t, pool)

	earlyEnd := time.Now().Add(time.Hour).Truncate(time.Second)
	chain := chainPayload(f, earlyEnd, 50)
	limit := int32(20)
	chain.CategoryList[1].SellLimit = &limit
	rec, one := f.call(h, chain)
	if rec.Code != http.StatusOK {
		t.Fatalf("create chain date: %d %s", rec.Code, rec.Body.String())
	}
	second := f.payload()
	second.ExternalRef = f.externalRef + ":second"
	second.Action.ArenaEventID = one.EventID.String()
	second.ActionEvent.Day = "27.04.2026"
	if rec, _ = f.call(h, second); rec.Code != http.StatusOK {
		t.Fatalf("create second date: %d %s", rec.Code, rec.Body.String())
	}

	// Hand the head's places over (nothing sold: it owns none afterwards).
	early := one.TierIDs[externalIDString(one.CompatIDs.CategoryPriceIDs[0])]
	if _, err := pool.Exec(ctx, `UPDATE ticket_tiers SET sale_window_end = date_trunc('second', now() - interval '1 minute') WHERE id = $1`, early); err != nil {
		t.Fatal(err)
	}
	sweep := tierchain.NewHandler(tierchain.Options{Store: tierchain.NewPGStore(pool)})
	if err := sweep(ctx, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if s := readTier(t, ctx, f, one.SessionID, early); s.places != 0 {
		t.Fatalf("the head owns %d places after the hand-over, want 0", s.places)
	}

	first, firstRaw := f.readBundles(h, one.EventID)
	if len(first.Bundles) != 2 || first.Status != "draft" {
		t.Fatalf("read: %s", firstRaw)
	}
	if first.Bundles[0].Publish || len(first.Bundles[0].ChannelIDs) != 0 {
		t.Errorf("a draft event reads publish false and no channels: %+v", first.Bundles[0])
	}
	cats := first.Bundles[0].CategoryList
	if len(cats) != 3 || cats[0].Availability != 50 || cats[1].Availability != 0 || cats[2].Availability != 0 ||
		*cats[0].NextCategoryIndex != 1 || *cats[1].NextCategoryIndex != 2 || *cats[2].NextCategoryIndex != -1 ||
		cats[1].SellLimit == nil || *cats[1].SellLimit != 20 {
		t.Errorf("chain categories: %+v", cats)
	}
	if first.Bundles[0].ActionEvent.Day != "26.04.2026" || first.Bundles[1].ActionEvent.Day != "27.04.2026" {
		t.Errorf("bundles are not oldest first: %s / %s", first.Bundles[0].ActionEvent.Day, first.Bundles[1].ActionEvent.Day)
	}
	for i, bundle := range first.Bundles {
		if dry := dryRun(t, f, h, bundle); !dry.Valid || len(dry.Errors) != 0 {
			t.Fatalf("dry run of bundle %d: %+v", i, dry.Errors)
		}
	}

	before := catalogSnapshot(ctx, t, pool, f.orgID)
	f.reimport(h, first)
	if err := sweep(ctx, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if after := catalogSnapshot(ctx, t, pool, f.orgID); after != before {
		t.Fatalf("re-importing the chain changed the rows:\nbefore %s\nafter  %s", before, after)
	}
	if _, secondRaw := f.readBundles(h, one.EventID); secondRaw != firstRaw {
		t.Fatalf("second read differs:\nfirst  %s\nsecond %s", firstRaw, secondRaw)
	}

	// More dates a bundle must not carry: cancelled and pay-what-you-want (a
	// seated one needs a plan version; the mapping test covers it). A deleted
	// one is simply absent.
	var sessions [4]uuid.UUID
	for i := range sessions {
		p := f.payload()
		p.ExternalRef = f.externalRef + ":extra:" + string(rune('a'+i))
		p.Action.ArenaEventID = one.EventID.String()
		p.ActionEvent.Day = "0" + string(rune('1'+i)) + ".05.2026"
		rec, out := f.call(h, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("extra date %d: %d %s", i, rec.Code, rec.Body.String())
		}
		sessions[i] = out.SessionID
	}
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	mustExec(`UPDATE sessions SET status = 'cancelled' WHERE id = $1`, sessions[0])
	mustExec(`UPDATE ticket_tiers SET pricing_mode = 'pwyw' WHERE session_id = $1 AND name = 'Balcony'`, sessions[2])
	mustExec(`UPDATE sessions SET deleted_at = now() WHERE id = $1`, sessions[3])

	out, raw := f.readBundles(h, one.EventID)
	if len(out.Bundles) != 3 || len(out.Skipped) != 2 {
		t.Fatalf("bundles %d skipped %+v; %s", len(out.Bundles), out.Skipped, raw)
	}
	want := map[uuid.UUID]string{sessions[0]: SkipSessionCancelled, sessions[2]: SkipSessionPayWhatYouWant}
	for _, s := range out.Skipped {
		if want[s.SessionID] != s.Code {
			t.Errorf("skipped %s as %s, want %s", s.SessionID, s.Code, want[s.SessionID])
		}
	}
	if strings.Contains(raw, sessions[3].String()) {
		t.Error("a deleted session must not appear at all")
	}
}

// TestEventBundleRead_OrganizationBoundary: a foreign, unknown and deleted
// event are the route's own 404, and a member of another organization is
// refused at the organization.
func TestEventBundleRead_OrganizationBoundary(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()
	other := newBundle525Fixture(t, ctx, pool)
	defer other.cleanup()
	h := newBundle525Handler(t, pool)

	rec, created := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	// The other organization's member asks for its own org with this event.
	if rec, _ := other.getEventBundle(h, other.orgID, created.EventID, other.userID); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "event.not_found") {
		t.Errorf("a foreign event: %d %s", rec.Code, rec.Body.String())
	}
	// ... or for the organization that owns it.
	if rec, _ := other.getEventBundle(h, f.orgID, created.EventID, other.userID); rec.Code != http.StatusForbidden {
		t.Errorf("a foreign organization: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := f.getEventBundle(h, f.orgID, uuid.New(), f.userID); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown event: %d", rec.Code)
	}
	if _, err := pool.Exec(ctx, `UPDATE events SET deleted_at = now() WHERE id = $1`, created.EventID); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.getEventBundle(h, f.orgID, created.EventID, f.userID); rec.Code != http.StatusNotFound {
		t.Errorf("a deleted event: %d", rec.Code)
	}
}
