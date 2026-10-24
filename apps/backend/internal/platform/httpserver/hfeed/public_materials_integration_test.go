//go:build integration

// public_materials_integration_test.go — task 1-2c against a live, migrated
// PostgreSQL (migration 0153): the hosted page and the widget feed carry the
// event's materials, the photo report is hidden until the event is over, and
// no field that existed before changed.
//
//	DATABASE_URL=postgres://arena:arena@localhost:45432/arena_ci_12c?sslmode=disable \
//	    go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/hfeed/ \
//	    -run TestPublicMaterials
package hfeed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

type materialsFixture struct {
	*publicPageFixture
	gallery, report []uuid.UUID
	venue, session  uuid.UUID
}

func newMaterialsFixture(t *testing.T, pool *pgxpool.Pool) *materialsFixture {
	t.Helper()
	ctx := context.Background()
	f := &materialsFixture{publicPageFixture: newPublicPageFixture(t, ctx, pool, true)}

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("fixture %q: %v", sql, err)
		}
	}
	media := func(ownerType string, withSize bool) uuid.UUID {
		id := uuid.New()
		if withSize {
			exec(`INSERT INTO media_objects (id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256, width, height)
			      VALUES ($1, $2, $3, 'local', $4, 'image/jpeg', 4, repeat('0', 64), 1200, 800)`, id, f.orgID, ownerType, "pm/"+id.String())
		} else {
			exec(`INSERT INTO media_objects (id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256)
			      VALUES ($1, $2, $3, 'local', $4, 'image/jpeg', 4, repeat('0', 64))`, id, f.orgID, ownerType, "pm/"+id.String())
		}
		return id
	}
	f.gallery = []uuid.UUID{media("event_gallery", true), media("event_gallery", false)}
	f.report = []uuid.UUID{media("event_report", false), media("event_report", true)}

	exec(`INSERT INTO event_gallery_items (org_id, event_id, media_id, position, caption) VALUES ($1,$2,$3,1,'Second'), ($1,$2,$4,0,'First')`,
		f.orgID, f.eventID, f.gallery[1], f.gallery[0])
	exec(`INSERT INTO event_videos (org_id, event_id, provider, video_id, url, position) VALUES
	      ($1,$2,'youtube','dQw4w9WgXcQ','https://youtu.be/dQw4w9WgXcQ',0),
	      ($1,$2,'https',NULL,'https://example.com/clip',1)`, f.orgID, f.eventID)
	exec(`INSERT INTO event_partner_links (org_id, event_id, url, seller_name, position) VALUES ($1,$2,'https://ticketportal.cz/e/1','Ticketportal',0)`, f.orgID, f.eventID)
	exec(`INSERT INTO event_map_points (event_id, org_id, latitude, longitude, label) VALUES ($1,$2,50.075538,14.4378,'Main hall')`, f.eventID, f.orgID)
	exec(`INSERT INTO event_report_photos (org_id, event_id, media_id, position, caption) VALUES ($1,$2,$3,0,NULL), ($1,$2,$4,1,'Backstage')`,
		f.orgID, f.eventID, f.report[0], f.report[1])

	f.venue, f.session = uuid.New(), uuid.New()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM sessions WHERE event_id = $1`, f.eventID)
		_, _ = pool.Exec(bg, `DELETE FROM venues WHERE id = $1`, f.venue)
		_, _ = pool.Exec(bg, `DELETE FROM event_gallery_items WHERE event_id = $1`, f.eventID)
		_, _ = pool.Exec(bg, `DELETE FROM event_report_photos WHERE event_id = $1`, f.eventID)
		_, _ = pool.Exec(bg, `DELETE FROM media_objects WHERE org_id = $1`, f.orgID)
	})
	return f
}

// withSession adds one session that ends `endsIn` from now (negative = past).
func (f *materialsFixture) withSession(t *testing.T, endsIn time.Duration) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'Materials Hall', 'Europe/Prague')`, f.venue, f.orgID); err != nil {
		t.Fatalf("venue: %v", err)
	}
	end := time.Now().UTC().Add(endsIn)
	start := end.Add(-2 * time.Hour)
	if _, err := f.pool.Exec(ctx, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, sales_end_at, capacity_total, status, currency, currency_source)
	      VALUES ($1, $2, $3, $4, $5, $4, 50, 'scheduled', 'EUR', 'override')`, f.session, f.eventID, f.venue, start, end); err != nil {
		t.Fatalf("session: %v", err)
	}
}

func (f *materialsFixture) pageBody(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	w := httptest.NewRecorder()
	publicPageHandler(f.pool).HandlePublicPage(w, pageRequest(f.orgSlug, f.eventSlug))
	if w.Code != http.StatusOK {
		t.Fatalf("page status = %d (%s)", w.Code, w.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	return body
}

func (f *materialsFixture) feedBody(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/"+f.token+"/events/"+f.eventID.String(), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("feed_token", f.token)
	rctx.URLParams.Add("event_id", f.eventID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	publicPageHandler(f.pool).HandlePublicFeedEvent(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("feed status = %d (%s)", w.Code, w.Body.String())
	}
	var env struct {
		Event map[string]json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode feed: %v", err)
	}
	return env.Event
}

type decodedMaterials struct {
	Gallery []struct {
		MediaID string  `json:"media_id"`
		URL     string  `json:"url"`
		Caption *string `json:"caption"`
		Width   *int    `json:"width"`
		Height  *int    `json:"height"`
	} `json:"gallery"`
	Videos []struct {
		Provider string  `json:"provider"`
		VideoID  *string `json:"video_id"`
		EmbedURL *string `json:"embed_url"`
		URL      string  `json:"url"`
	} `json:"videos"`
	PartnerLinks []struct {
		URL        string `json:"url"`
		SellerName string `json:"seller_name"`
	} `json:"partner_links"`
	MapPoint *struct {
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Label     *string `json:"label"`
	} `json:"map_point"`
	ReportPhotos []struct {
		MediaID string  `json:"media_id"`
		Caption *string `json:"caption"`
	} `json:"report_photos"`
}

func decodeMaterials(t *testing.T, raw json.RawMessage) decodedMaterials {
	t.Helper()
	if raw == nil {
		t.Fatalf("`materials` is missing from the answer")
	}
	var m decodedMaterials
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode materials: %v (%s)", err, raw)
	}
	return m
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestPublicMaterials_PageShowsGalleryVideosPartnersAndMap(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	f := newMaterialsFixture(t, pool)

	m := decodeMaterials(t, f.pageBody(t)["materials"])

	if len(m.Gallery) != 2 || m.Gallery[0].MediaID != f.gallery[0].String() || m.Gallery[1].MediaID != f.gallery[1].String() {
		t.Fatalf("gallery must follow `position`: %+v", m.Gallery)
	}
	if m.Gallery[0].Caption == nil || *m.Gallery[0].Caption != "First" || m.Gallery[0].Width == nil || *m.Gallery[0].Width != 1200 || *m.Gallery[0].Height != 800 {
		t.Errorf("gallery[0] = %+v", m.Gallery[0])
	}
	if m.Gallery[1].Width != nil || m.Gallery[1].Height != nil {
		t.Errorf("a picture of unknown size carries null width/height: %+v", m.Gallery[1])
	}
	if !strings.Contains(m.Gallery[0].URL, f.gallery[0].String()) {
		t.Errorf("gallery url = %q", m.Gallery[0].URL)
	}

	if len(m.Videos) != 2 || m.Videos[0].Provider != "youtube" ||
		m.Videos[0].EmbedURL == nil || *m.Videos[0].EmbedURL != "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ" ||
		m.Videos[0].URL != "https://youtu.be/dQw4w9WgXcQ" {
		t.Fatalf("videos = %+v", m.Videos)
	}
	if m.Videos[1].Provider != "https" || m.Videos[1].VideoID != nil || m.Videos[1].EmbedURL != nil {
		t.Errorf("a plain https video has neither id nor embed: %+v", m.Videos[1])
	}
	if len(m.PartnerLinks) != 1 || m.PartnerLinks[0].SellerName != "Ticketportal" || m.PartnerLinks[0].URL != "https://ticketportal.cz/e/1" {
		t.Errorf("partner links = %+v", m.PartnerLinks)
	}
	if m.MapPoint == nil || m.MapPoint.Latitude != 50.075538 || m.MapPoint.Longitude != 14.4378 || *m.MapPoint.Label != "Main hall" {
		t.Errorf("map point = %+v", m.MapPoint)
	}
}

func TestPublicMaterials_ReportHiddenUntilTheEventIsOverAndFollowsTheRule(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newMaterialsFixture(t, pool)
	q := gen.New(pool)

	reportLen := func() int {
		t.Helper()
		return len(decodeMaterials(t, f.pageBody(t)["materials"]).ReportPhotos)
	}
	// Both public surfaces and the 1-2b rule must agree for a given clock.
	agrees := func(now time.Time) int {
		t.Helper()
		row, err := q.GetEventPublicMaterials(ctx, f.eventID, now)
		if err != nil {
			t.Fatalf("GetEventPublicMaterials: %v", err)
		}
		var combined []json.RawMessage
		if err := json.Unmarshal(row.ReportPhotos, &combined); err != nil {
			t.Fatal(err)
		}
		legacy, err := q.ListEventReportPhotosPublic(ctx, f.eventID, now)
		if err != nil {
			t.Fatalf("ListEventReportPhotosPublic: %v", err)
		}
		if len(combined) != len(legacy) {
			t.Errorf("at %s the combined query shows %d report photos, ListEventReportPhotosPublic %d", now, len(combined), len(legacy))
		}
		return len(combined)
	}

	// No session at all: nothing is "over" yet.
	if n := reportLen(); n != 0 {
		t.Errorf("event without a session: %d report photos, want 0", n)
	}
	agrees(time.Now().Add(100 * 24 * time.Hour))

	// A session ending in 2 hours: hidden.
	f.withSession(t, 2*time.Hour)
	if n := reportLen(); n != 0 {
		t.Errorf("before the event is over: %d report photos, want 0", n)
	}
	if n := agrees(time.Now().Add(time.Hour)); n != 0 {
		t.Errorf("during the event: %d, want 0", n)
	}
	// The report is hidden but the rest of the block is there.
	if m := decodeMaterials(t, f.pageBody(t)["materials"]); len(m.Gallery) != 2 || m.MapPoint == nil {
		t.Errorf("the other materials must show: %+v", m)
	}
	if n := agrees(time.Now().Add(2*time.Hour + time.Minute)); n != 2 {
		t.Errorf("after the end: %d, want 2", n)
	}

	// The session moves into the past: the report opens at once, in order.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET start_at = now() - interval '4 hours', end_at = now() - interval '2 hours', sales_end_at = now() - interval '4 hours' WHERE id = $1`, f.session); err != nil {
		t.Fatal(err)
	}
	body := f.pageBody(t)
	m := decodeMaterials(t, body["materials"])
	if len(m.ReportPhotos) != 2 || m.ReportPhotos[0].MediaID != f.report[0].String() || m.ReportPhotos[1].Caption == nil || *m.ReportPhotos[1].Caption != "Backstage" {
		t.Fatalf("report after the event = %+v", m.ReportPhotos)
	}
	// The widget feed carries the very same block.
	feed := f.feedBody(t)
	if string(feed["materials"]) != string(body["materials"]) {
		t.Errorf("feed materials differ from the page's:\nfeed: %s\npage: %s", feed["materials"], body["materials"])
	}

	// Cancelled: it no longer counts as the end of the event.
	if _, err := pool.Exec(ctx, `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, f.session); err != nil {
		t.Fatal(err)
	}
	if n := reportLen(); n != 0 {
		t.Errorf("only session cancelled: %d report photos, want 0", n)
	}
}

func TestPublicMaterials_DeletedMediaAndNonPublishedEvents(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	ctx := context.Background()
	f := newMaterialsFixture(t, pool)

	// A deleted media object leaves the gallery.
	if _, err := pool.Exec(ctx, `UPDATE media_objects SET deleted_at = now() WHERE id = $1`, f.gallery[0]); err != nil {
		t.Fatal(err)
	}
	if m := decodeMaterials(t, f.pageBody(t)["materials"]); len(m.Gallery) != 1 || m.Gallery[0].MediaID != f.gallery[1].String() {
		t.Errorf("gallery after a media delete = %+v", m.Gallery)
	}

	// A draft event shows no materials (the feed lookup is the only way in).
	h := publicPageHandler(pool)
	if _, err := pool.Exec(ctx, `UPDATE events SET status = 'draft' WHERE id = $1`, f.eventID); err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(h.loadPublicMaterials(ctx, f.eventID, time.Now())); string(raw) !=
		`{"gallery":[],"videos":[],"partner_links":[],"map_point":null,"report_photos":[]}` {
		t.Errorf("a draft event must show empty materials, got %s", raw)
	}
	// A deleted one too.
	if _, err := pool.Exec(ctx, `UPDATE events SET status = 'published', deleted_at = now() WHERE id = $1`, f.eventID); err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(h.loadPublicMaterials(ctx, f.eventID, time.Now())); !strings.Contains(string(raw), `"gallery":[]`) {
		t.Errorf("a deleted event must show empty materials, got %s", raw)
	}
	// An event with no material rows: empty lists, null point.
	empty := newPublicPageFixture(t, ctx, pool, true)
	body := (&materialsFixture{publicPageFixture: empty}).pageBody(t)
	if got := string(body["materials"]); got != `{"gallery":[],"videos":[],"partner_links":[],"map_point":null,"report_photos":[]}` {
		t.Errorf("empty event materials = %s", got)
	}
}

// TestPublicMaterials_ExistingFieldsUnchanged is the snapshot of task 1-2c on
// the live answers: every key the page and the feed had before is still there
// with the same nested keys, and `materials` is the only new one.
func TestPublicMaterials_ExistingFieldsUnchanged(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	f := newMaterialsFixture(t, pool)
	f.withSession(t, -3*time.Hour)

	page := f.pageBody(t)
	// Optional blocks (seller, promoter, contact_form) are absent for this
	// fixture; every other top-level key existed before 1-2c.
	if got, want := strings.Join(sortedKeys(page), ","), "default_locale,event,feed_token,materials,org"; got != want {
		t.Errorf("page keys = %s, want %s", got, want)
	}
	var org, event map[string]json.RawMessage
	if err := json.Unmarshal(page["org"], &org); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(page["event"], &event); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(sortedKeys(org), ","), "logo_url,name,slug"; got != want {
		t.Errorf("page org keys = %s, want %s", got, want)
	}
	const wantEvent = "age_rating,description,description_html,description_text,first_session_at,first_session_doors_at,first_session_timezone,id,image_url," +
		"last_session_at,poster_url,session_count,short_description,slug,title,venue_names,venues"
	if got := strings.Join(sortedKeys(event), ","); got != wantEvent {
		t.Errorf("page event keys = %s, want %s", got, wantEvent)
	}

	feed := f.feedBody(t)
	const wantFeed = "created_at,description,description_html,description_text,display_number,first_session_at,id,image_url,last_session_at,materials,name,org_id," +
		"poster_media_id,poster_url,sessions,status,updated_at,venue_names,venues,visibility"
	if got := strings.Join(sortedKeys(feed), ","); got != wantFeed {
		t.Errorf("feed event keys = %s, want %s", got, wantFeed)
	}
}

// TestPublicVenues_AddressPerVenueNothingInvented: the page and the feed list
// the REAL venues of the event's visible sessions with their own postal
// address (not the seller's), a cancelled or draft session adds no venue, an
// empty field stays null and nothing is derived from the map point.
func TestPublicVenues_AddressPerVenueNothingInvented(t *testing.T) {
	pool := publicPageIntegrationPool(t)
	f := newMaterialsFixture(t, pool)
	f.withSession(t, 48*time.Hour)
	ctx := context.Background()

	second, cancelledVenue, draftVenue := uuid.New(), uuid.New(), uuid.New()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM sessions WHERE event_id = $1`, f.eventID)
		_, _ = pool.Exec(bg, `DELETE FROM venues WHERE id = ANY($1)`, []uuid.UUID{second, cancelledVenue, draftVenue})
	})
	if _, err := pool.Exec(ctx, `UPDATE venues SET address_line1 = 'Pernerova 51', address_line2 = 'Hall 2', postal_code = '18600', country = 'CZ' WHERE id = $1`, f.venue); err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		id   uuid.UUID
		name string
	}{{second, "Bare Club"}, {cancelledVenue, "Cancelled Place"}, {draftVenue, "Draft Place"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, $3, 'Europe/Berlin')`, v.id, f.orgID, v.name); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(venue uuid.UUID, status string, in time.Duration) {
		end := time.Now().UTC().Add(in)
		if _, err := pool.Exec(ctx, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, sales_end_at, capacity_total, status, currency, currency_source)
		      VALUES ($1, $2, $3, $4, $5, $4, 50, $6, 'EUR', 'override')`, uuid.New(), f.eventID, venue, end.Add(-2*time.Hour), end, status); err != nil {
			t.Fatalf("session %s: %v", status, err)
		}
	}
	insert(second, "scheduled", 72*time.Hour)
	insert(cancelledVenue, "cancelled", 96*time.Hour)
	insert(draftVenue, "draft", 120*time.Hour)

	type venue struct {
		Name          string   `json:"name"`
		Country       *string  `json:"country"`
		City          *string  `json:"city"`
		Address       *string  `json:"address"`
		PostalCode    *string  `json:"postal_code"`
		SessionStarts []string `json:"session_starts"`
	}
	check := func(where string, raw json.RawMessage) {
		t.Helper()
		var vs []venue
		if err := json.Unmarshal(raw, &vs); err != nil {
			t.Fatalf("%s: %v (%s)", where, err, raw)
		}
		if len(vs) != 2 {
			t.Fatalf("%s: want the 2 visible venues, got %+v", where, vs)
		}
		byName := map[string]venue{}
		for _, v := range vs {
			byName[v.Name] = v
		}
		hall, bare := byName["Materials Hall"], byName["Bare Club"]
		if hall.Address == nil || *hall.Address != "Pernerova 51, Hall 2" || hall.PostalCode == nil || *hall.PostalCode != "18600" || hall.Country == nil || *hall.Country != "CZ" {
			t.Errorf("%s: hall = %+v", where, hall)
		}
		if len(hall.SessionStarts) != 1 || len(bare.SessionStarts) != 1 {
			t.Errorf("%s: each venue lists its own session start: %+v / %+v", where, hall, bare)
		}
		if bare.Address != nil || bare.Country != nil || bare.City != nil || bare.PostalCode != nil {
			t.Errorf("%s: an empty venue stays empty, nothing invented: %+v", where, bare)
		}
		if _, ok := byName["Cancelled Place"]; ok {
			t.Errorf("%s: a cancelled session must not add its venue", where)
		}
		if _, ok := byName["Draft Place"]; ok {
			t.Errorf("%s: a draft session must not add its venue", where)
		}
	}
	var pageEvent map[string]json.RawMessage
	if err := json.Unmarshal(f.pageBody(t)["event"], &pageEvent); err != nil {
		t.Fatal(err)
	}
	check("page", pageEvent["venues"])
	check("feed", f.feedBody(t)["venues"])
}
