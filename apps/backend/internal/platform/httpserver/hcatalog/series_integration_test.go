//go:build integration

// series_integration_test.go — the tours API of spec 63 Л-2 against a live
// database migrated past 0222, driven through the REAL handlers: CRUD and
// archive, the tour link of PATCH .../events/{id} (a foreign or archived tour
// is 422 event.invalid_series, series_id/series_name come back), and the one
// address space of tour and event slugs (409 both ways).
package hcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/audit"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

type seriesRig struct {
	t    *testing.T
	pool *pgxpool.Pool
	h    *Handler
	org  uuid.UUID
}

func (r *seriesRig) newOrg(tag string) uuid.UUID {
	r.t.Helper()
	ctx := context.Background()
	suffix := uuid.NewString()
	org, err := gen.New(r.pool).InsertOrganization(ctx, "Series "+tag+" "+suffix, "series-"+tag+"-"+suffix, "DE", "en", 1200)
	if err != nil {
		r.t.Fatalf("InsertOrganization: %v", err)
	}
	r.t.Cleanup(func() {
		bg := context.Background()
		_, _ = r.pool.Exec(bg, `DELETE FROM events WHERE org_id = $1`, org.ID)
		_, _ = r.pool.Exec(bg, `DELETE FROM event_series WHERE org_id = $1`, org.ID)
		_, _ = r.pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, org.ID)
	})
	return org.ID
}

func newSeriesRig(t *testing.T) *seriesRig {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	q := gen.New(pool)
	rig := &seriesRig{t: t, pool: pool}
	rig.h = New(q, nil, nil, nil, nil, q, nil, pool, audit.NewPGWriter(pool), slog.Default(), nil).
		WithMembershipQueries(q)
	rig.org = rig.newOrg("a")
	return rig
}

// call runs a handler as a service actor of orgID with the route params.
func (r *seriesRig) call(fn http.HandlerFunc, method string, orgID uuid.UUID, params map[string]string, body any, query string) *httptest.ResponseRecorder {
	r.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, "/?"+query, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("org_id", orgID.String())
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	ctx = auth.WithActor(ctx, auth.Actor{ID: uuid.NewString(), Type: auth.ActorTypeService, OrgID: orgID.String()})
	w := httptest.NewRecorder()
	fn(w, req.WithContext(ctx))
	return w
}

func errCode(w *httptest.ResponseRecorder) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Error.Code
}

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("%s: got %d, want %d: %s", what, w.Code, want, w.Body.String())
	}
}

func decodeSeries(t *testing.T, w *httptest.ResponseRecorder) SeriesDetailResponse {
	t.Helper()
	var s SeriesDetailResponse
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v: %s", err, w.Body.String())
	}
	return s
}

func (r *seriesRig) newEvent(orgID uuid.UUID, name, slug string) uuid.UUID {
	r.t.Helper()
	var id uuid.UUID
	var slugArg *string
	if slug != "" {
		slugArg = &slug
	}
	if err := r.pool.QueryRow(context.Background(),
		`INSERT INTO events (org_id, name, slug) VALUES ($1, $2, $3) RETURNING id`, orgID, name, slugArg).Scan(&id); err != nil {
		r.t.Fatalf("event %s: %v", name, err)
	}
	return id
}

func TestSeriesAPI_CRUDAndArchive(t *testing.T) {
	rig := newSeriesRig(t)
	org := rig.org

	// Create without a slug: derived from the name, "-2" on the next one.
	w := rig.call(rig.h.HandleCreateSeries, http.MethodPost, org, nil, map[string]any{
		"name": "  Summer   Tour ", "description_html": "<p>Hello</p>", "hide_children_in_lists": true, "position": 3,
		"hero_video_url": "https://youtu.be/dQw4w9WgXcQ",
	}, "")
	mustStatus(t, w, http.StatusCreated, "create")
	first := decodeSeries(t, w)
	if first.Slug != "summer-tour" || first.Name != "Summer Tour" || !first.HideChildrenInLists || first.Position != 3 ||
		first.DescriptionHTML == nil || first.Translations == nil || len(first.Translations) != 0 || first.ArchivedAt != nil {
		t.Fatalf("created: %+v", first)
	}
	w = rig.call(rig.h.HandleCreateSeries, http.MethodPost, org, nil, map[string]any{"name": "Summer Tour"}, "")
	mustStatus(t, w, http.StatusCreated, "create second")
	if s := decodeSeries(t, w); s.Slug != "summer-tour-2" {
		t.Fatalf("suffix on conflict: %q", s.Slug)
	}

	// Validation: 422 series.invalid.
	for name, body := range map[string]map[string]any{
		"no name":         {},
		"blank name":      {"name": "  "},
		"bad slug":        {"name": "x", "slug": "Bad Slug!"},
		"bad video":       {"name": "x", "hero_video_url": "https://example.com/v.mp4"},
		"foreign media":   {"name": "x", "poster_media_id": uuid.NewString()},
		"not-uuid poster": {"name": "x", "hero_media_id": "nope"},
	} {
		w = rig.call(rig.h.HandleCreateSeries, http.MethodPost, org, nil, body, "")
		if w.Code != http.StatusUnprocessableEntity || errCode(w) != "series.invalid" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	// A typed slug that is taken: 409 series.slug_taken.
	w = rig.call(rig.h.HandleCreateSeries, http.MethodPost, org, nil, map[string]any{"name": "Other", "slug": "SUMMER-TOUR"}, "")
	if w.Code != http.StatusConflict || errCode(w) != "series.slug_taken" {
		t.Fatalf("taken slug: %d %s", w.Code, w.Body.String())
	}

	// Get one with its events; event_count in the list.
	ev := rig.newEvent(org, "Show one", "show-one")
	if _, err := rig.pool.Exec(context.Background(), `UPDATE events SET series_id = $1 WHERE id = $2`, first.ID, ev); err != nil {
		t.Fatal(err)
	}
	w = rig.call(rig.h.HandleGetSeries, http.MethodGet, org, map[string]string{"series_id": first.ID}, nil, "")
	mustStatus(t, w, http.StatusOK, "get")
	got := decodeSeries(t, w)
	if got.EventCount != 1 || len(got.Events) != 1 || got.Events[0].ID != ev.String() || got.Events[0].Name != "Show one" {
		t.Fatalf("get events: %+v", got)
	}
	w = rig.call(rig.h.HandleListSeries, http.MethodGet, org, nil, nil, "")
	mustStatus(t, w, http.StatusOK, "list")
	var list struct {
		Items []SeriesResponse `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Items) != 2 || list.Items[1].ID != first.ID || list.Items[1].EventCount != 1 {
		t.Fatalf("list: %s", w.Body.String())
	}
	w = rig.call(rig.h.HandleListSeries, http.MethodGet, org, nil, nil, "include_archived=maybe")
	mustStatus(t, w, http.StatusBadRequest, "bad include_archived")

	// PATCH: partial; null clears; slug to a taken one is 409.
	w = rig.call(rig.h.HandleUpdateSeries, http.MethodPatch, org, map[string]string{"series_id": first.ID}, map[string]any{
		"name": "Winter tour", "description_html": nil, "position": 0,
	}, "")
	mustStatus(t, w, http.StatusOK, "patch")
	if s := decodeSeries(t, w); s.Name != "Winter tour" || s.DescriptionHTML != nil || s.Slug != "summer-tour" ||
		s.HeroVideoURL == nil || s.Position != 0 {
		t.Fatalf("patched: %+v", s)
	}
	w = rig.call(rig.h.HandleUpdateSeries, http.MethodPatch, org, map[string]string{"series_id": first.ID}, map[string]any{"slug": "summer-tour-2"}, "")
	if w.Code != http.StatusConflict || errCode(w) != "series.slug_taken" {
		t.Fatalf("patch to a taken slug: %d %s", w.Code, w.Body.String())
	}
	w = rig.call(rig.h.HandleUpdateSeries, http.MethodPatch, org, map[string]string{"series_id": first.ID}, map[string]any{"slug": "show-one"}, "")
	if w.Code != http.StatusConflict || errCode(w) != "series.slug_taken" {
		t.Fatalf("patch to an event slug: %d %s", w.Code, w.Body.String())
	}
	// Another organization sees nothing.
	other := rig.newOrg("b")
	w = rig.call(rig.h.HandleGetSeries, http.MethodGet, other, map[string]string{"series_id": first.ID}, nil, "")
	if w.Code != http.StatusNotFound || errCode(w) != "series.not_found" {
		t.Fatalf("foreign get: %d %s", w.Code, w.Body.String())
	}
	w = rig.call(rig.h.HandleUpdateSeries, http.MethodPatch, other, map[string]string{"series_id": first.ID}, map[string]any{"name": "x"}, "")
	mustStatus(t, w, http.StatusNotFound, "foreign patch")
	w = rig.call(rig.h.HandleArchiveSeries, http.MethodDelete, other, map[string]string{"series_id": first.ID}, nil, "")
	mustStatus(t, w, http.StatusNotFound, "foreign archive")

	// Archive: 204, hidden from the list, the event keeps series_id.
	w = rig.call(rig.h.HandleArchiveSeries, http.MethodDelete, org, map[string]string{"series_id": first.ID}, nil, "")
	mustStatus(t, w, http.StatusNoContent, "archive")
	w = rig.call(rig.h.HandleListSeries, http.MethodGet, org, nil, nil, "")
	list.Items = nil
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].ID == first.ID {
		t.Fatalf("archived tour still listed: %s", w.Body.String())
	}
	w = rig.call(rig.h.HandleListSeries, http.MethodGet, org, nil, nil, "include_archived=true")
	list.Items = nil
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Items) != 2 {
		t.Fatalf("include_archived: %s", w.Body.String())
	}
	w = rig.call(rig.h.HandleGetSeries, http.MethodGet, org, map[string]string{"series_id": first.ID}, nil, "")
	if s := decodeSeries(t, w); s.ArchivedAt == nil || s.EventCount != 1 {
		t.Fatalf("archived get: %+v", s)
	}
}

func TestSeriesAPI_EventLinkAndSlugNamespace(t *testing.T) {
	rig := newSeriesRig(t)
	org := rig.org
	other := rig.newOrg("b")

	mk := func(orgID uuid.UUID, name string) SeriesDetailResponse {
		w := rig.call(rig.h.HandleCreateSeries, http.MethodPost, orgID, nil, map[string]any{"name": name}, "")
		mustStatus(t, w, http.StatusCreated, "create "+name)
		return decodeSeries(t, w)
	}
	mine, foreign, old := mk(org, "Mine tour"), mk(other, "Foreign tour"), mk(org, "Old tour")
	w := rig.call(rig.h.HandleArchiveSeries, http.MethodDelete, org, map[string]string{"series_id": old.ID}, nil, "")
	mustStatus(t, w, http.StatusNoContent, "archive old")

	ev := rig.newEvent(org, "Event one", "event-one")
	patch := func(body map[string]any) *httptest.ResponseRecorder {
		return rig.call(rig.h.HandleUpdateEvent, http.MethodPatch, org, map[string]string{"id": ev.String()}, body, "")
	}
	seriesIn := func(w *httptest.ResponseRecorder) (*string, *string) {
		var out struct {
			Event struct {
				SeriesID   *string `json:"series_id"`
				SeriesName *string `json:"series_name"`
			} `json:"event"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Event.SeriesID, out.Event.SeriesName
	}

	// Link: the response carries series_id and series_name.
	w = patch(map[string]any{"series_id": mine.ID})
	mustStatus(t, w, http.StatusOK, "link")
	if id, name := seriesIn(w); id == nil || *id != mine.ID || name == nil || *name != "Mine tour" {
		t.Fatalf("linked response: %s", w.Body.String())
	}
	// Other organization's, unknown, archived, garbage: 422 event.invalid_series; the link stays.
	for name, bad := range map[string]string{"foreign": foreign.ID, "unknown": uuid.NewString(), "archived": old.ID, "garbage": "x"} {
		w = patch(map[string]any{"series_id": bad})
		if w.Code != http.StatusUnprocessableEntity || errCode(w) != "event.invalid_series" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	// A PATCH without series_id keeps the link.
	w = patch(map[string]any{"name": "Event one renamed"})
	mustStatus(t, w, http.StatusOK, "rename")
	if id, _ := seriesIn(w); id == nil || *id != mine.ID {
		t.Fatalf("the link was lost: %s", w.Body.String())
	}
	// null unlinks.
	w = patch(map[string]any{"series_id": nil})
	mustStatus(t, w, http.StatusOK, "unlink")
	if id, name := seriesIn(w); id != nil || name != nil {
		t.Fatalf("unlinked response: %s", w.Body.String())
	}

	// One address space: a tour slug equal to an event slug is refused ...
	w = rig.call(rig.h.HandleCreateSeries, http.MethodPost, org, nil, map[string]any{"name": "Clash", "slug": "event-one"}, "")
	if w.Code != http.StatusConflict || errCode(w) != "series.slug_taken" {
		t.Fatalf("series slug = event slug: %d %s", w.Code, w.Body.String())
	}
	// ... and an event slug equal to a tour slug too.
	w = patch(map[string]any{"slug": mine.Slug})
	if w.Code != http.StatusConflict || errCode(w) != "event.duplicate_slug" {
		t.Fatalf("event slug = series slug: %d %s", w.Code, w.Body.String())
	}
	// A derived slug never lands on an event slug.
	rig.newEvent(org, "Second", "second-tour")
	w = rig.call(rig.h.HandleCreateSeries, http.MethodPost, org, nil, map[string]any{"name": "Second tour"}, "")
	mustStatus(t, w, http.StatusCreated, "derived slug")
	if s := decodeSeries(t, w); s.Slug != "second-tour-2" {
		t.Fatalf("derived slug = %q, want second-tour-2", s.Slug)
	}
	// The same slug in another organization is fine.
	w = rig.call(rig.h.HandleCreateSeries, http.MethodPost, other, nil, map[string]any{"name": "x", "slug": mine.Slug}, "")
	mustStatus(t, w, http.StatusCreated, "same slug elsewhere")
}

// A tour accepts only pictures uploaded for a tour (owner_type event_series) of
// its own organization; the default of hide_children_in_lists on create is true.
func TestSeriesAPI_MediaOwnerTypeAndDefaults(t *testing.T) {
	rig := newSeriesRig(t)
	org := rig.org
	other := rig.newOrg("m")
	ctx := context.Background()

	mkMedia := func(orgID uuid.UUID, ownerType string) string {
		t.Helper()
		id := uuid.New()
		if _, err := rig.pool.Exec(ctx, `INSERT INTO media_objects (id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256)
			VALUES ($1, $2, $3, 'local', $4, 'image/png', 10, repeat('a', 64))`, id, orgID, ownerType, "series-test/"+id.String()); err != nil {
			t.Fatalf("insert media: %v", err)
		}
		t.Cleanup(func() { _, _ = rig.pool.Exec(context.Background(), `DELETE FROM media_objects WHERE id = $1`, id) })
		return id.String()
	}
	own := mkMedia(org, "event_series")
	foreignOrg := mkMedia(other, "event_series")
	wrongType := mkMedia(org, "org_logo")

	for name, body := range map[string]map[string]any{
		"another org's media": {"name": "x", "poster_media_id": foreignOrg},
		"wrong owner type":    {"name": "x", "hero_media_id": wrongType},
	} {
		w := rig.call(rig.h.HandleCreateSeries, http.MethodPost, org, nil, body, "")
		if w.Code != http.StatusUnprocessableEntity || errCode(w) != "series.invalid" {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	w := rig.call(rig.h.HandleCreateSeries, http.MethodPost, org, nil, map[string]any{"name": "Pictured", "poster_media_id": own}, "")
	mustStatus(t, w, http.StatusCreated, "own media")
	created := decodeSeries(t, w)
	if !created.HideChildrenInLists {
		t.Errorf("hide_children_in_lists = false on create without the field, want true")
	}
	w = rig.call(rig.h.HandleUpdateSeries, http.MethodPatch, org, map[string]string{"series_id": created.ID}, map[string]any{"hero_media_id": wrongType}, "")
	if w.Code != http.StatusUnprocessableEntity || errCode(w) != "series.invalid" {
		t.Errorf("patch with a wrong owner type: %d %s", w.Code, w.Body.String())
	}
}
