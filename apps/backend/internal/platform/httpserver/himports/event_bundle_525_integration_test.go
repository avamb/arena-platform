//go:build integration

// event_bundle_525_integration_test.go — live-PostgreSQL coverage for the
// arena-native event bundle (feature #525, W1-E1c; event-bundle spec §3.2,
// §3.3, §4 and the five scenarios of §9).
//
// The unit tests in event_bundle_524_test.go prove the pre-transaction
// validation ladder without a database. Everything asserted HERE needs real
// rows: the session/event/venue/tier upserts driven by externalRef instead of
// a Bil24 id, the arena-minted compatibility_id_map rows the response echoes
// back, the session_external_refs binding, and the poster checksum dedup.
//
// Run with:
//
//	DATABASE_URL=postgres://arena:arena@localhost:55432/arena?sslmode=disable \
//	    go test -tags integration \
//	    ./apps/backend/internal/platform/httpserver/himports/ \
//	    -run TestEventBundle525
package himports

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/storage"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fixture
// ─────────────────────────────────────────────────────────────────────────────

// bundle525Fixture is an organization plus a member user. Unlike the Bil24
// fixture it mints NO external identifiers: an arena bundle addresses
// everything by externalRef on the first call and by the ids arena minted
// afterwards. Every literal that feeds a globally-unique column (the external
// ref, the venue name) is randomized per run because this suite is pointed at
// the shared dev stand — see AGENTS.md.
type bundle525Fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	orgID  uuid.UUID
	userID uuid.UUID

	externalRef string
	venueName   string
}

func newBundle525Fixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *bundle525Fixture {
	t.Helper()
	q := gen.New(pool)

	suffix := uuid.NewString()
	// orgs_name_unique_active makes the DISPLAY NAME unique too, not just the
	// slug, so both carry the per-run suffix.
	org, err := q.InsertOrganization(ctx, "Bundle525 Test Org "+suffix, "bundle525-"+suffix, "HU", "en", 1200)
	if err != nil {
		t.Fatalf("newBundle525Fixture: InsertOrganization: %v", err)
	}
	user, err := q.InsertUser(ctx, "bundle525-"+suffix+"@test.arena.local", "x", "en")
	if err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
		t.Fatalf("newBundle525Fixture: InsertUser: %v", err)
	}
	if _, err := q.InsertMembership(ctx, user.ID, org.ID, "organizer"); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, user.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, org.ID)
		t.Fatalf("newBundle525Fixture: InsertMembership: %v", err)
	}

	return &bundle525Fixture{
		t: t, pool: pool, orgID: org.ID, userID: user.ID,
		externalRef: "wp:bundle525:" + suffix,
		venueName:   "Bundle525 Hall " + suffix,
	}
}

// cleanup removes every row the bundle may have written, in FK-safe order.
func (f *bundle525Fixture) cleanup() {
	ctx := context.Background()
	exec := func(label, sql string, args ...any) {
		if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
			f.t.Logf("bundle525Fixture cleanup: %s: %v", label, err)
		}
	}
	// compatibility_id_map is keyed by platform_id, so it must be swept while
	// the catalog rows it points at are still readable.
	exec("compat_map_tiers", `DELETE FROM compatibility_id_map WHERE platform_id IN (
	          SELECT tt.id FROM ticket_tiers tt
	          JOIN sessions s ON s.id = tt.session_id
	          JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
	exec("compat_map_sessions", `DELETE FROM compatibility_id_map WHERE platform_id IN (
	          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
	exec("compat_map_events", `DELETE FROM compatibility_id_map WHERE platform_id IN (
	          SELECT id FROM events WHERE org_id = $1)`, f.orgID)
	exec("compat_map_venues", `DELETE FROM compatibility_id_map WHERE platform_id IN (
	          SELECT id FROM venues WHERE org_id = $1)`, f.orgID)
	exec("session_external_refs", `DELETE FROM session_external_refs WHERE org_id = $1`, f.orgID)
	exec("inventory_ledger", `DELETE FROM inventory_ledger WHERE session_id IN (
	          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
	exec("ticket_tiers", `DELETE FROM ticket_tiers WHERE session_id IN (
	          SELECT s.id FROM sessions s JOIN events e ON e.id = s.event_id WHERE e.org_id = $1)`, f.orgID)
	exec("sessions", `DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`, f.orgID)
	exec("events", `DELETE FROM events WHERE org_id = $1`, f.orgID)
	exec("media_objects", `DELETE FROM media_objects WHERE org_id = $1`, f.orgID)
	exec("venues", `DELETE FROM venues WHERE org_id = $1`, f.orgID)
	exec("memberships", `DELETE FROM memberships WHERE org_id = $1`, f.orgID)
	exec("users", `DELETE FROM users WHERE id = $1`, f.userID)
	exec("organizations", `DELETE FROM organizations WHERE id = $1`, f.orgID)
}

// payload builds a valid two-category arena bundle carrying NO external ids —
// the shape a WordPress site sends the very first time it publishes an event.
func (f *bundle525Fixture) payload() bil24compat.ImportSessionRequest {
	return bil24compat.ImportSessionRequest{
		Source:      bil24compat.SourceArena,
		ExternalRef: f.externalRef,
		Action: bil24compat.ImportSessionAction{
			ActionName:     "Bundle525",
			FullActionName: "Bundle525 Grand Tasting",
			Description:    "created by the #525 integration test",
			Age:            "18+",
		},
		ActionEvent: bil24compat.ImportSessionActionEvent{
			Day:      "26.04.2026",
			Time:     "17:00",
			Currency: "EUR",
		},
		Venue: bil24compat.ImportSessionVenue{
			VenueName: f.venueName,
			Address:   "1 Test Street",
			Timezone:  "Europe/Madrid",
		},
		CategoryList: []bil24compat.ImportSessionCategory{
			{CategoryPriceName: "Parter", Price: 25, Availability: 100},
			{CategoryPriceName: "Balcony", Price: 12.5, Availability: 40},
		},
	}
}

// call drives the real event-bundle handler against the real pool,
// authenticated as the fixture's member user.
func (f *bundle525Fixture) call(h *Handler, body bil24compat.ImportSessionRequest) (*httptest.ResponseRecorder, ImportSessionResponse) {
	f.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("marshal payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost,
		"/v1/organizations/"+f.orgID.String()+"/imports/event-bundle", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", f.orgID.String())
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: f.userID.String(), Type: auth.ActorTypeUser})

	rec := httptest.NewRecorder()
	h.HandleEventBundle(rec, req.WithContext(ctx))

	var out ImportSessionResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			f.t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
		}
	}
	return rec, out
}

func newBundle525Handler(t *testing.T, pool *pgxpool.Pool) *Handler {
	t.Helper()
	return New(gen.New(pool), pool, nil, nil).WithMembershipQueries(gen.New(pool))
}

// newBundle525Media wires a real mediastore.Repo over a temp-dir local storage
// backend, which is what the poster side-load and its checksum dedup need.
func newBundle525Media(t *testing.T, pool *pgxpool.Pool) *mediastore.Repo {
	t.Helper()
	st, err := storage.NewLocalStorage(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	repo, err := mediastore.New(mediastore.Options{Pool: pool, Storage: st})
	if err != nil {
		t.Fatalf("mediastore.New: %v", err)
	}
	return repo
}

// ─────────────────────────────────────────────────────────────────────────────
// §9 scenario 1 — create from scratch
// ─────────────────────────────────────────────────────────────────────────────

// TestEventBundle525_CreateMintsArenaIdentity is spec §9 scenario 1: a bundle
// with no identifiers at all creates the whole subtree, mints compat ids at or
// above the 1e9 ceiling with source='arena', binds the externalRef, and
// side-loads the poster.
func TestEventBundle525_CreateMintsArenaIdentity(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()

	posterBytes := []byte("\x89PNG\r\n\x1a\n" + f.externalRef)
	var posterHits int
	poster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posterHits++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(posterBytes)
	}))
	defer poster.Close()

	h := newBundle525Handler(t, pool).WithMedia(newBundle525Media(t, pool))

	body := f.payload()
	body.Action.BigPosterURL = poster.URL + "/poster.png"

	rec, res := f.call(h, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !res.Created {
		t.Errorf("create: created = false, want true")
	}
	if res.EventID == uuid.Nil || res.SessionID == uuid.Nil {
		t.Fatalf("create: got nil ids: %+v", res)
	}
	if res.ExternalRef == nil || *res.ExternalRef != f.externalRef {
		t.Errorf("create: external_ref = %v, want %q", res.ExternalRef, f.externalRef)
	}

	// §4: every id arena hands back must be one it minted itself.
	const ceiling int64 = 1_000_000_000
	minted := []struct {
		label string
		id    int64
	}{
		{"action_id", res.CompatIDs.ActionID},
		{"action_event_id", res.CompatIDs.ActionEventID},
		{"venue_id", res.CompatIDs.VenueID},
	}
	for _, m := range minted {
		if m.id < ceiling {
			t.Errorf("create: compat_ids.%s = %d, want >= 1e9 (arena-minted)", m.label, m.id)
		}
	}
	if len(res.CompatIDs.CategoryPriceIDs) != 2 {
		t.Fatalf("create: len(category_price_ids) = %d, want 2 (%v)",
			len(res.CompatIDs.CategoryPriceIDs), res.CompatIDs.CategoryPriceIDs)
	}
	for i, id := range res.CompatIDs.CategoryPriceIDs {
		if id < ceiling {
			t.Errorf("create: category_price_ids[%d] = %d, want >= 1e9", i, id)
		}
		if _, ok := res.TierIDs[externalIDString(id)]; !ok {
			t.Errorf("create: tier_ids has no key %d; got %v", id, res.TierIDs)
		}
	}

	assertCompatSource(t, ctx, pool, "action", res.EventID, "arena")
	assertCompatSource(t, ctx, pool, "action_event", res.SessionID, "arena")

	assertRowCount(t, ctx, pool, 1,
		`SELECT count(*) FROM session_external_refs WHERE org_id = $1 AND external_ref = $2`,
		f.orgID, f.externalRef)
	assertSessionRow(t, ctx, pool, res.SessionID, sessionExpectation{
		orgID:    f.orgID,
		eventID:  res.EventID,
		currency: "EUR",
		capacity: 140,
		status:   "draft",
		// 17:00 in Europe/Madrid on 26 Apr 2026 (CEST, UTC+2) is 15:00Z.
		startAtUTC: "2026-04-26T15:00:00Z",
	})

	// Poster: stored once, with the checksum of exactly the bytes served.
	var posterMedia *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT poster_media_id FROM events WHERE id = $1`, res.EventID).
		Scan(&posterMedia); err != nil {
		t.Fatalf("read event poster: %v", err)
	}
	if posterMedia == nil {
		t.Fatalf("create: event poster_media_id is null; warnings=%+v", res.Warnings)
	}
	sum := sha256.Sum256(posterBytes)
	var checksum string
	if err := pool.QueryRow(ctx, `SELECT checksum_sha256 FROM media_objects WHERE id = $1`, *posterMedia).
		Scan(&checksum); err != nil {
		t.Fatalf("read media object: %v", err)
	}
	if checksum != hex.EncodeToString(sum[:]) {
		t.Errorf("poster checksum = %s, want %s", checksum, hex.EncodeToString(sum[:]))
	}

	// §3.3: re-importing the SAME poster must not create a second blob.
	rec2, res2 := f.call(h, body)
	if rec2.Code != http.StatusOK {
		t.Fatalf("poster repeat: status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	if res2.SessionID != res.SessionID {
		t.Errorf("poster repeat: session_id = %s, want %s", res2.SessionID, res.SessionID)
	}
	assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM media_objects WHERE org_id = $1`, f.orgID)
	if posterHits < 2 {
		t.Errorf("poster repeat: upstream was fetched %d times, want >= 2 (dedup is by checksum, not by skipping the fetch)", posterHits)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// §9 scenario 2 — exact repeat
// ─────────────────────────────────────────────────────────────────────────────

// TestEventBundle525_RepeatIsIdempotentOnExternalRef is spec §9 scenario 2: the
// same bundle sent twice — still carrying no identifiers — resolves through
// session_external_refs and updates the same rows.
func TestEventBundle525_RepeatIsIdempotentOnExternalRef(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()

	h := newBundle525Handler(t, pool)

	rec, first := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("first bundle: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	rec2, second := f.call(h, f.payload())
	if rec2.Code != http.StatusOK {
		t.Fatalf("repeat bundle: status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	if second.Created {
		t.Errorf("repeat bundle: created = true, want false (idempotent on externalRef)")
	}
	if second.EventID != first.EventID || second.SessionID != first.SessionID {
		t.Errorf("repeat bundle: ids moved: %s/%s → %s/%s",
			first.EventID, first.SessionID, second.EventID, second.SessionID)
	}
	assertSameCompatIDs(t, second.CompatIDs, first.CompatIDs)
	// Tier matching is by normalised NAME when the bundle carries no
	// categoryPriceId, so a repeat must not duplicate the two tiers.
	assertRowCount(t, ctx, pool, 2, `SELECT count(*) FROM ticket_tiers WHERE session_id = $1`, first.SessionID)
	assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM events WHERE org_id = $1`, f.orgID)
	assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM venues WHERE org_id = $1`, f.orgID)
	if hasWarning(second.Warnings, WarnTierNotInPayload) {
		t.Errorf("repeat bundle: unexpected %s warning: %+v", WarnTierNotInPayload, second.Warnings)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// §9 scenario 3 — edit by externalRef
// ─────────────────────────────────────────────────────────────────────────────

// TestEventBundle525_EditByExternalRef is spec §9 scenario 3: an edit changes a
// price and adds a category, and a later bundle that OMITS a category leaves it
// in place with an import.tier_not_in_payload warning — a bundle can add and
// edit, never delete.
func TestEventBundle525_EditByExternalRef(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()

	h := newBundle525Handler(t, pool)

	rec, first := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("first bundle: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// ── edit: reprice Parter, add a third category ──────────────────────────
	edit := f.payload()
	edit.Action.ActionID = first.CompatIDs.ActionID
	edit.ActionEvent.ActionEventID = first.CompatIDs.ActionEventID
	edit.Venue.VenueID = first.CompatIDs.VenueID
	edit.CategoryList = []bil24compat.ImportSessionCategory{
		{CategoryPriceID: first.CompatIDs.CategoryPriceIDs[0], CategoryPriceName: "Parter", Price: 30, Availability: 100},
		{CategoryPriceID: first.CompatIDs.CategoryPriceIDs[1], CategoryPriceName: "Balcony", Price: 12.5, Availability: 40},
		{CategoryPriceName: "Loge", Price: 50, Availability: 10},
	}
	recEdit, edited := f.call(h, edit)
	if recEdit.Code != http.StatusOK {
		t.Fatalf("edit bundle: status = %d, want 200; body=%s", recEdit.Code, recEdit.Body.String())
	}
	if edited.Created {
		t.Errorf("edit bundle: created = true, want false")
	}
	if edited.SessionID != first.SessionID {
		t.Errorf("edit bundle: session_id = %s, want %s", edited.SessionID, first.SessionID)
	}
	if hasWarning(edited.Warnings, WarnTierNotInPayload) {
		t.Errorf("edit bundle: unexpected %s warning: %+v", WarnTierNotInPayload, edited.Warnings)
	}
	if len(edited.CompatIDs.CategoryPriceIDs) != 3 {
		t.Fatalf("edit bundle: len(category_price_ids) = %d, want 3", len(edited.CompatIDs.CategoryPriceIDs))
	}
	// The two pre-existing categories keep their ids; the new one is minted.
	for i := 0; i < 2; i++ {
		if edited.CompatIDs.CategoryPriceIDs[i] != first.CompatIDs.CategoryPriceIDs[i] {
			t.Errorf("edit bundle: category_price_ids[%d] = %d, want %d (stable)",
				i, edited.CompatIDs.CategoryPriceIDs[i], first.CompatIDs.CategoryPriceIDs[i])
		}
	}
	assertTierPrices(t, ctx, pool, edited.TierIDs, map[string]int64{
		externalIDString(edited.CompatIDs.CategoryPriceIDs[0]): 3000,
		externalIDString(edited.CompatIDs.CategoryPriceIDs[2]): 5000,
	})
	assertRowCount(t, ctx, pool, 3, `SELECT count(*) FROM ticket_tiers WHERE session_id = $1`, first.SessionID)

	// ── shrink: drop Loge from the payload ──────────────────────────────────
	shrunk := edit
	shrunk.CategoryList = edit.CategoryList[:2]
	recShrunk, shrunkRes := f.call(h, shrunk)
	if recShrunk.Code != http.StatusOK {
		t.Fatalf("shrunk bundle: status = %d, want 200; body=%s", recShrunk.Code, recShrunk.Body.String())
	}
	if !hasWarning(shrunkRes.Warnings, WarnTierNotInPayload) {
		t.Errorf("shrunk bundle: missing %s warning; got %+v", WarnTierNotInPayload, shrunkRes.Warnings)
	}
	assertRowCount(t, ctx, pool, 3,
		`SELECT count(*) FROM ticket_tiers WHERE session_id = $1`, first.SessionID)
}

// ─────────────────────────────────────────────────────────────────────────────
// §9 scenario 4 — identifier errors
// ─────────────────────────────────────────────────────────────────────────────

// TestEventBundle525_RejectsForeignAndMalformedIdentifiers is spec §9 scenario
// 4: an id belonging to another organization is indistinguishable from an
// unknown one (404), a below-ceiling id is out of range for source=arena (422),
// and an externalRef already bound to a different session is a 409.
func TestEventBundle525_RejectsForeignAndMalformedIdentifiers(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	owner := newBundle525Fixture(t, ctx, pool)
	defer owner.cleanup()
	other := newBundle525Fixture(t, ctx, pool)
	defer other.cleanup()

	h := newBundle525Handler(t, pool)

	rec, owned := owner.call(h, owner.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("owner bundle: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// ── foreign-org actionEventId → 404 import.compat_id_unknown ────────────
	foreign := other.payload()
	foreign.ActionEvent.ActionEventID = owned.CompatIDs.ActionEventID
	recForeign, _ := other.call(h, foreign)
	if recForeign.Code != http.StatusNotFound {
		t.Fatalf("foreign id: status = %d, want 404; body=%s", recForeign.Code, recForeign.Body.String())
	}
	assertErrorCode(t, recForeign, "import.compat_id_unknown")

	// ── below-ceiling id under source=arena → 422 ───────────────────────────
	low := other.payload()
	low.ActionEvent.ActionEventID = 4711
	recLow, _ := other.call(h, low)
	if recLow.Code != http.StatusUnprocessableEntity {
		t.Fatalf("below-ceiling id: status = %d, want 422; body=%s", recLow.Code, recLow.Body.String())
	}
	assertErrorCode(t, recLow, "import.arena_id_out_of_range")

	// ── externalRef bound elsewhere → 409 ───────────────────────────────────
	// A second session of the SAME organization, addressed by its own ids but
	// carrying the ref already bound to the first one.
	secondRef := owner.payload()
	secondRef.ExternalRef = owner.externalRef + ":second"
	secondRef.Action.ActionID = owned.CompatIDs.ActionID
	recSecond, secondSession := owner.call(h, secondRef)
	if recSecond.Code != http.StatusOK {
		t.Fatalf("second session: status = %d, want 200; body=%s", recSecond.Code, recSecond.Body.String())
	}
	if secondSession.SessionID == owned.SessionID {
		t.Fatalf("second session: reused the first session; the refs should have separated them")
	}

	clash := owner.payload()
	clash.ExternalRef = owner.externalRef
	clash.ActionEvent.ActionEventID = secondSession.CompatIDs.ActionEventID
	recClash, _ := owner.call(h, clash)
	if recClash.Code != http.StatusConflict {
		t.Fatalf("ref clash: status = %d, want 409; body=%s", recClash.Code, recClash.Body.String())
	}
	assertErrorCode(t, recClash, "import.external_ref_conflict")
}

// ─────────────────────────────────────────────────────────────────────────────
// §9 scenario 5 — venue matched by name
// ─────────────────────────────────────────────────────────────────────────────

// TestEventBundle525_VenueMatchedByName is spec §9 scenario 5 / §3.2 step 4: a
// second bundle that names the same venue without a venueId reuses the existing
// venue AS IS and says so in a warning.
func TestEventBundle525_VenueMatchedByName(t *testing.T) {
	pool := import517Pool(t)
	ctx := context.Background()
	f := newBundle525Fixture(t, ctx, pool)
	defer f.cleanup()

	h := newBundle525Handler(t, pool)

	rec, first := f.call(h, f.payload())
	if rec.Code != http.StatusOK {
		t.Fatalf("first bundle: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if hasWarning(first.Warnings, WarnVenueMatchedByName) {
		t.Errorf("first bundle: unexpected %s warning (nothing to match yet): %+v",
			WarnVenueMatchedByName, first.Warnings)
	}

	// A DIFFERENT event at the same venue, named with different casing and
	// padding — §3.2 step 4 matches on lower(btrim(name)).
	second := f.payload()
	second.ExternalRef = f.externalRef + ":other-show"
	second.Action.ActionName = "Bundle525 Second Show"
	second.Venue.VenueName = "  " + toUpperASCII(f.venueName) + "  "
	second.Venue.Address = "99 Somewhere Else"
	second.Venue.Timezone = "Europe/Budapest"

	recSecond, res := f.call(h, second)
	if recSecond.Code != http.StatusOK {
		t.Fatalf("second bundle: status = %d, want 200; body=%s", recSecond.Code, recSecond.Body.String())
	}
	if !hasWarning(res.Warnings, WarnVenueMatchedByName) {
		t.Errorf("second bundle: missing %s warning; got %+v", WarnVenueMatchedByName, res.Warnings)
	}
	if res.CompatIDs.VenueID != first.CompatIDs.VenueID {
		t.Errorf("second bundle: venue_id = %d, want %d (matched by name)",
			res.CompatIDs.VenueID, first.CompatIDs.VenueID)
	}
	assertRowCount(t, ctx, pool, 1, `SELECT count(*) FROM venues WHERE org_id = $1`, f.orgID)

	// Reused AS IS: neither the address nor the timezone of the payload is
	// applied to a venue other events already depend on.
	var address, timezone string
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(address, ''), coalesce(timezone, '') FROM venues WHERE org_id = $1`, f.orgID).
		Scan(&address, &timezone); err != nil {
		t.Fatalf("read venue: %v", err)
	}
	if address != "1 Test Street" {
		t.Errorf("venue address = %q, want the original %q", address, "1 Test Street")
	}
	if timezone != "Europe/Madrid" {
		t.Errorf("venue timezone = %q, want the stored %q", timezone, "Europe/Madrid")
	}
	if !hasWarning(res.Warnings, WarnVenueTimezoneKept) {
		t.Errorf("second bundle: missing %s warning; got %+v", WarnVenueTimezoneKept, res.Warnings)
	}
	// The stored timezone, not the payload's, interprets the local wall clock.
	assertSessionRow(t, ctx, pool, res.SessionID, sessionExpectation{
		orgID:      f.orgID,
		eventID:    res.EventID,
		currency:   "EUR",
		capacity:   140,
		status:     "draft",
		startAtUTC: "2026-04-26T15:00:00Z",
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Assertion helpers
// ─────────────────────────────────────────────────────────────────────────────

func assertCompatSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, platformID uuid.UUID, want string) {
	t.Helper()
	var got string
	err := pool.QueryRow(ctx,
		`SELECT source FROM compatibility_id_map WHERE kind = $1 AND platform_id = $2`, kind, platformID).Scan(&got)
	if err != nil {
		t.Fatalf("read compatibility_id_map (%s, %s): %v", kind, platformID, err)
	}
	if got != want {
		t.Errorf("compatibility_id_map(%s, %s).source = %q, want %q", kind, platformID, got, want)
	}
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v; body=%s", err, rec.Body.String())
	}
	if env.Error.Code != want {
		t.Errorf("error code = %q, want %q; body=%s", env.Error.Code, want, rec.Body.String())
	}
}

// assertSameCompatIDs compares the whole §4 identifier block field by field —
// ImportCompatIDs carries a slice and is therefore not comparable with ==.
func assertSameCompatIDs(t *testing.T, got, want ImportCompatIDs) {
	t.Helper()
	if got.ActionID != want.ActionID || got.ActionEventID != want.ActionEventID || got.VenueID != want.VenueID {
		t.Errorf("compat_ids = %+v, want %+v", got, want)
	}
	if len(got.CategoryPriceIDs) != len(want.CategoryPriceIDs) {
		t.Fatalf("compat_ids.category_price_ids = %v, want %v", got.CategoryPriceIDs, want.CategoryPriceIDs)
	}
	for i := range want.CategoryPriceIDs {
		if got.CategoryPriceIDs[i] != want.CategoryPriceIDs[i] {
			t.Errorf("compat_ids.category_price_ids[%d] = %d, want %d",
				i, got.CategoryPriceIDs[i], want.CategoryPriceIDs[i])
		}
	}
}

func toUpperASCII(s string) string {
	out := []byte(s)
	for i, c := range out {
		if c >= 'a' && c <= 'z' {
			out[i] = c - 32
		}
	}
	return string(out)
}
