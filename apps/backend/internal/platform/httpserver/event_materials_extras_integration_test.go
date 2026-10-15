//go:build integration

package httpserver

// Task 1-2b: "where else to buy" partner links, the map point and the photo
// report of an event. Run against a migrated database (migration 0153).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

type matEnv struct {
	*sec1Env
	orgA, orgB uuid.UUID
	ownerTok   string // owner of A
	event      uuid.UUID
}

func newMatEnv(t *testing.T) *matEnv {
	t.Helper()
	e := newSec1Env(t)
	m := &matEnv{sec1Env: e, orgA: e.org(t, "A"), orgB: e.org(t, "B"), event: uuid.New()}
	owner := e.user(t, "mat-owner")
	e.member(t, owner, m.orgA, "org_admin")
	m.ownerTok = e.token(t, owner)
	m.exec(t, `INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'Materials Show', 'published', 'public')`, m.event, m.orgA)
	t.Cleanup(func() {
		// Rows of the 0153 tables go with the event (ON DELETE CASCADE).
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM sessions WHERE event_id = $1`, m.event)
		_, _ = e.srv.pgxPool.Exec(context.Background(), `DELETE FROM events WHERE id = $1`, m.event)
	})
	return m
}

func (m *matEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := m.srv.pgxPool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("fixture %q: %v", sql, err)
	}
}

func (m *matEnv) base(org, event uuid.UUID) string {
	return fmt.Sprintf("/v1/organizations/%s/events/%s", org, event)
}

// media inserts an active media object of the given owner type.
func (m *matEnv) media(t *testing.T, org uuid.UUID, ownerType string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	m.exec(t, `INSERT INTO media_objects (id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256)
	           VALUES ($1, $2, $3, 'local', $4, 'image/jpeg', 4, repeat('0', 64))`, id, org, ownerType, "mat/"+id.String())
	t.Cleanup(func() {
		_, _ = m.srv.pgxPool.Exec(context.Background(), `DELETE FROM event_report_photos WHERE media_id = $1`, id)
		_, _ = m.srv.pgxPool.Exec(context.Background(), `DELETE FROM media_objects WHERE id = $1`, id)
	})
	return id
}

func matJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("not JSON: %q: %v", body, err)
	}
	return out
}

func matLinks(t *testing.T, body string) []map[string]any {
	t.Helper()
	raw, _ := matJSON(t, body)["partner_links"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(map[string]any))
	}
	return out
}

func matPhotos(t *testing.T, body string) []map[string]any {
	t.Helper()
	raw, _ := matJSON(t, body)["photos"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(map[string]any))
	}
	return out
}

func TestEventMaterialsExtras_PartnerLinks(t *testing.T) {
	m := newMatEnv(t)
	base := m.base(m.orgA, m.event) + "/partner-links"
	post := func(body string) (int, string) { return m.do(t, http.MethodPost, base, m.ownerTok, "", body) }

	// The seller is read from the address unless the client names one.
	st, out := post(`{"url":"https://www.ticketportal.cz/event/1"}`)
	if st != http.StatusCreated {
		t.Fatalf("create: %d %s", st, out)
	}
	first := matJSON(t, out)["partner_link"].(map[string]any)
	if first["seller_name"] != "ticketportal.cz" || first["position"].(float64) != 0 {
		t.Errorf("first link = %v", first)
	}

	// Bad addresses: http, javascript:, a space, no host.
	for _, bad := range []string{"http://example.com/a", "javascript:alert(1)", "https://exa mple.com/a", "https://", "example.com", ""} {
		body, _ := json.Marshal(map[string]string{"url": bad})
		st, out := post(string(body))
		if st != http.StatusBadRequest || sec1Code(out) != "event.invalid_partner_url" {
			t.Errorf("url %q: %d %s, want 400 event.invalid_partner_url", bad, st, out)
		}
	}
	// A seller name over the limit.
	if st, out := post(`{"url":"https://a.example.com/x","seller_name":"` + strings.Repeat("x", 121) + `"}`); st != http.StatusBadRequest || sec1Code(out) != "event.invalid_seller_name" {
		t.Errorf("long seller: %d %s", st, out)
	}
	// Unknown members are refused, so a typo is not silently ignored.
	if st, _ := post(`{"url":"https://a.example.com/x","sellername":"x"}`); st != http.StatusBadRequest {
		t.Errorf("unknown member: %d, want 400", st)
	}

	if st, out := post(`{"url":"https://goout.net/e/2","seller_name":"  GoOut   CZ "}`); st != http.StatusCreated {
		t.Fatalf("second: %d %s", st, out)
	}
	st, out = post(`{"url":"https://bilety.example.pl/e/3"}`)
	if st != http.StatusCreated {
		t.Fatalf("third: %d %s", st, out)
	}
	third := matJSON(t, out)["partner_link"].(map[string]any)

	// The fourth link is refused.
	if st, out := post(`{"url":"https://fourth.example.com/e/4"}`); st != http.StatusConflict || sec1Code(out) != "event.partner_links_full" {
		t.Errorf("fourth link: %d %s, want 409 event.partner_links_full", st, out)
	}

	// List: three, in order, with the typed seller normalized.
	st, out = m.do(t, http.MethodGet, base, m.ownerTok, "", "")
	links := matLinks(t, out)
	if st != http.StatusOK || len(links) != 3 || links[1]["seller_name"] != "GoOut CZ" {
		t.Fatalf("list: %d %s", st, out)
	}

	// PATCH: a derived seller follows a new address, a typed one stays.
	one := base + "/" + links[0]["id"].(string)
	two := base + "/" + links[1]["id"].(string)
	st, out = m.do(t, http.MethodPatch, one, m.ownerTok, "", `{"url":"https://www.ticketmaster.cz/e/9"}`)
	if st != http.StatusOK || matJSON(t, out)["partner_link"].(map[string]any)["seller_name"] != "ticketmaster.cz" {
		t.Errorf("patch url, derived seller: %d %s", st, out)
	}
	st, out = m.do(t, http.MethodPatch, two, m.ownerTok, "", `{"url":"https://goout.net/e/22"}`)
	if st != http.StatusOK || matJSON(t, out)["partner_link"].(map[string]any)["seller_name"] != "GoOut CZ" {
		t.Errorf("patch url, typed seller: %d %s", st, out)
	}
	if st, out := m.do(t, http.MethodPatch, two, m.ownerTok, "", `{"url":"http://goout.net/e/22"}`); st != http.StatusBadRequest || sec1Code(out) != "event.invalid_partner_url" {
		t.Errorf("patch http url: %d %s", st, out)
	}
	if st, _ := m.do(t, http.MethodPatch, two, m.ownerTok, "", `{}`); st != http.StatusBadRequest {
		t.Errorf("empty patch: %d, want 400", st)
	}

	// Reorder: reverse. A list that is not exactly the event's links is refused.
	ids := []string{links[2]["id"].(string), links[1]["id"].(string), links[0]["id"].(string)}
	body, _ := json.Marshal(map[string]any{"ids": ids})
	st, out = m.do(t, http.MethodPut, base+"/order", m.ownerTok, "", string(body))
	got := matLinks(t, out)
	if st != http.StatusOK || len(got) != 3 || got[0]["id"] != ids[0] || got[2]["id"] != ids[2] || got[0]["position"].(float64) != 0 {
		t.Fatalf("reorder: %d %s", st, out)
	}
	for _, bad := range []string{
		fmt.Sprintf(`{"ids":["%s","%s"]}`, ids[0], ids[1]),
		fmt.Sprintf(`{"ids":["%s","%s","%s"]}`, ids[0], ids[0], ids[1]),
		fmt.Sprintf(`{"ids":["%s","%s","%s"]}`, ids[0], ids[1], uuid.New()),
		`{"ids":[]}`,
	} {
		if st, out := m.do(t, http.MethodPut, base+"/order", m.ownerTok, "", bad); st != http.StatusBadRequest || sec1Code(out) != "event.invalid_order" {
			t.Errorf("bad order %s: %d %s", bad, st, out)
		}
	}

	// Delete the middle one: positions close up, and room is free again.
	if st, _ := m.do(t, http.MethodDelete, base+"/"+ids[1], m.ownerTok, "", ""); st != http.StatusNoContent {
		t.Fatalf("delete: %d", st)
	}
	if st, _ := m.do(t, http.MethodDelete, base+"/"+ids[1], m.ownerTok, "", ""); st != http.StatusNotFound {
		t.Errorf("second delete: %d, want 404", st)
	}
	_, out = m.do(t, http.MethodGet, base, m.ownerTok, "", "")
	got = matLinks(t, out)
	if len(got) != 2 || got[0]["position"].(float64) != 0 || got[1]["position"].(float64) != 1 || got[1]["id"] != ids[2] {
		t.Errorf("after delete: %s", out)
	}
	st, out = post(`{"url":"https://again.example.com/e/5"}`)
	if st != http.StatusCreated || matJSON(t, out)["partner_link"].(map[string]any)["position"].(float64) != 2 {
		t.Errorf("add after delete: %d %s", st, out)
	}
	_ = third
	if st, _ := m.do(t, http.MethodPatch, base+"/"+uuid.NewString(), m.ownerTok, "", `{"seller_name":"x"}`); st != http.StatusNotFound {
		t.Errorf("patch unknown: %d, want 404", st)
	}
	if st, _ := m.do(t, http.MethodPatch, base+"/not-a-uuid", m.ownerTok, "", `{"seller_name":"x"}`); st != http.StatusNotFound {
		t.Errorf("patch malformed id: %d, want 404", st)
	}
}

// Many requests at once never exceed the limit, and the surplus is told the
// list is full.
func TestEventMaterialsExtras_PartnerLinksRace(t *testing.T) {
	m := newMatEnv(t)
	base := m.base(m.orgA, m.event) + "/partner-links"
	const callers = 10
	var wg sync.WaitGroup
	codes := make([]int, callers)
	bodies := make([]string, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := fmt.Sprintf(`{"url":"https://seller%d.example.com/e"}`, i)
			codes[i], bodies[i] = m.do(t, http.MethodPost, base, m.ownerTok, "", req)
		}(i)
	}
	wg.Wait()
	created := 0
	for i, c := range codes {
		switch {
		case c == http.StatusCreated:
			created++
		case c == http.StatusConflict && sec1Code(bodies[i]) == "event.partner_links_full":
		default:
			t.Errorf("caller %d: %d %s, want 201 or 409 event.partner_links_full", i, c, bodies[i])
		}
	}
	if created != 3 {
		t.Errorf("%d links created, want exactly 3", created)
	}
	var n int
	if err := m.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM event_partner_links WHERE event_id = $1`, m.event).Scan(&n); err != nil || n != 3 {
		t.Errorf("rows = %d (%v), want 3", n, err)
	}
}

func TestEventMaterialsExtras_MapPoint(t *testing.T) {
	m := newMatEnv(t)
	base := m.base(m.orgA, m.event) + "/map-point"

	st, out := m.do(t, http.MethodGet, base, m.ownerTok, "", "")
	if st != http.StatusOK || matJSON(t, out)["map_point"] != nil {
		t.Errorf("empty get: %d %s, want 200 with map_point null", st, out)
	}
	if st, _ := m.do(t, http.MethodDelete, base, m.ownerTok, "", ""); st != http.StatusNotFound {
		t.Errorf("delete of nothing: %d, want 404", st)
	}
	for _, bad := range []string{
		`{"latitude":91,"longitude":14}`, `{"latitude":-90.5,"longitude":14}`,
		`{"latitude":50,"longitude":181}`, `{"latitude":50,"longitude":-180.01}`,
		`{"latitude":50}`, `{"longitude":14}`, `{}`, `{"latitude":"50","longitude":14}`,
		`{"latitude":50,"longitude":14,"label":"` + strings.Repeat("x", 201) + `"}`,
	} {
		st, out := m.do(t, http.MethodPut, base, m.ownerTok, "", bad)
		if st != http.StatusBadRequest {
			t.Errorf("PUT %s: %d %s, want 400", bad, st, out)
		}
	}
	// Valid: six decimals are kept, the label is trimmed.
	st, out = m.do(t, http.MethodPut, base, m.ownerTok, "", `{"latitude":50.07554321,"longitude":14.4378,"label":"  Main entrance "}`)
	pt, _ := matJSON(t, out)["map_point"].(map[string]any)
	if st != http.StatusOK || pt == nil || pt["latitude"].(float64) != 50.075543 || pt["longitude"].(float64) != 14.4378 || pt["label"] != "Main entrance" {
		t.Fatalf("put: %d %s", st, out)
	}
	// Replaced as a whole: an absent label clears it.
	st, out = m.do(t, http.MethodPut, base, m.ownerTok, "", `{"latitude":-33.8688,"longitude":151.2093}`)
	pt, _ = matJSON(t, out)["map_point"].(map[string]any)
	if st != http.StatusOK || pt["label"] != nil || pt["latitude"].(float64) != -33.8688 {
		t.Fatalf("replace: %d %s", st, out)
	}
	var rows int
	_ = m.srv.pgxPool.QueryRow(context.Background(), `SELECT count(*) FROM event_map_points WHERE event_id = $1`, m.event).Scan(&rows)
	if rows != 1 {
		t.Errorf("rows = %d, want 1", rows)
	}
	if st, _ := m.do(t, http.MethodDelete, base, m.ownerTok, "", ""); st != http.StatusNoContent {
		t.Errorf("delete: %d", st)
	}
	_, out = m.do(t, http.MethodGet, base, m.ownerTok, "", "")
	if matJSON(t, out)["map_point"] != nil {
		t.Errorf("after delete: %s", out)
	}
}

func TestEventMaterialsExtras_PhotoReport(t *testing.T) {
	m := newMatEnv(t)
	base := m.base(m.orgA, m.event) + "/photo-report"
	add := func(media uuid.UUID, caption string) (int, string) {
		body := fmt.Sprintf(`{"media_id":%q}`, media)
		if caption != "" {
			body = fmt.Sprintf(`{"media_id":%q,"caption":%q}`, media, caption)
		}
		return m.do(t, http.MethodPost, base, m.ownerTok, "", body)
	}

	// A file that is not this organization's event_report object is refused.
	foreign := m.media(t, m.orgB, "event_report")
	wrongType := m.media(t, m.orgA, "event_poster")
	gallery := m.media(t, m.orgA, "event_gallery")
	deleted := m.media(t, m.orgA, "event_report")
	m.exec(t, `UPDATE media_objects SET deleted_at = now() WHERE id = $1`, deleted)
	for name, id := range map[string]uuid.UUID{"another organization": foreign, "wrong owner type": wrongType, "gallery file": gallery, "deleted": deleted, "unknown": uuid.New()} {
		if st, out := add(id, ""); st != http.StatusUnprocessableEntity || sec1Code(out) != "event.invalid_report_media" {
			t.Errorf("%s: %d %s, want 422 event.invalid_report_media", name, st, out)
		}
	}
	if st, _ := m.do(t, http.MethodPost, base, m.ownerTok, "", `{}`); st != http.StatusBadRequest {
		t.Errorf("no media_id: %d, want 400", st)
	}

	good := m.media(t, m.orgA, "event_report")
	st, out := add(good, "  Opening night ")
	photo, _ := matJSON(t, out)["photo"].(map[string]any)
	if st != http.StatusCreated || photo["caption"] != "Opening night" || photo["position"].(float64) != 0 || photo["media_id"] != good.String() {
		t.Fatalf("add: %d %s", st, out)
	}
	if st, out := add(good, ""); st != http.StatusConflict || sec1Code(out) != "event.photo_report_duplicate" {
		t.Errorf("same file twice: %d %s, want 409 event.photo_report_duplicate", st, out)
	}
	if st, out := add(m.media(t, m.orgA, "event_report"), strings.Repeat("c", 201)); st != http.StatusBadRequest || sec1Code(out) != "event.invalid_caption" {
		t.Errorf("long caption: %d %s", st, out)
	}

	// Caption: set, cleared with null, absent member refused.
	one := base + "/" + photo["id"].(string)
	st, out = m.do(t, http.MethodPatch, one, m.ownerTok, "", `{"caption":"Encore"}`)
	if st != http.StatusOK || matJSON(t, out)["photo"].(map[string]any)["caption"] != "Encore" {
		t.Errorf("patch caption: %d %s", st, out)
	}
	st, out = m.do(t, http.MethodPatch, one, m.ownerTok, "", `{"caption":null}`)
	if st != http.StatusOK || matJSON(t, out)["photo"].(map[string]any)["caption"] != nil {
		t.Errorf("clear caption: %d %s", st, out)
	}
	if st, _ := m.do(t, http.MethodPatch, one, m.ownerTok, "", `{}`); st != http.StatusBadRequest {
		t.Errorf("patch without caption: %d, want 400", st)
	}
	if st, _ := m.do(t, http.MethodPatch, base+"/"+uuid.NewString(), m.ownerTok, "", `{"caption":"x"}`); st != http.StatusNotFound {
		t.Errorf("patch unknown photo: %d, want 404", st)
	}

	// Fill the report to 30, then the 31st is refused.
	for i := 1; i < 30; i++ {
		if st, out := add(m.media(t, m.orgA, "event_report"), ""); st != http.StatusCreated {
			t.Fatalf("photo %d: %d %s", i+1, st, out)
		}
	}
	if st, out := add(m.media(t, m.orgA, "event_report"), ""); st != http.StatusConflict || sec1Code(out) != "event.photo_report_full" {
		t.Errorf("31st photo: %d %s, want 409 event.photo_report_full", st, out)
	}
	st, out = m.do(t, http.MethodGet, base, m.ownerTok, "", "")
	photos := matPhotos(t, out)
	if st != http.StatusOK || len(photos) != 30 {
		t.Fatalf("list: %d with %d photos, want 30", st, len(photos))
	}

	// Reorder: move the last photo to the front in one call.
	ids := []string{photos[29]["id"].(string)}
	for _, p := range photos[:29] {
		ids = append(ids, p["id"].(string))
	}
	body, _ := json.Marshal(map[string]any{"ids": ids})
	st, out = m.do(t, http.MethodPut, base+"/order", m.ownerTok, "", string(body))
	got := matPhotos(t, out)
	if st != http.StatusOK || len(got) != 30 || got[0]["id"] != ids[0] || got[29]["id"] != ids[29] {
		t.Fatalf("reorder: %d %s", st, out)
	}
	if st, out := m.do(t, http.MethodPut, base+"/order", m.ownerTok, "", `{"ids":["`+ids[0]+`"]}`); st != http.StatusBadRequest || sec1Code(out) != "event.invalid_order" {
		t.Errorf("partial order: %d %s", st, out)
	}

	// Delete closes the gap and makes room again.
	if st, _ := m.do(t, http.MethodDelete, base+"/"+ids[10], m.ownerTok, "", ""); st != http.StatusNoContent {
		t.Fatalf("delete: %d", st)
	}
	var maxPos, n int
	_ = m.srv.pgxPool.QueryRow(context.Background(), `SELECT max(position), count(*) FROM event_report_photos WHERE event_id = $1`, m.event).Scan(&maxPos, &n)
	if n != 29 || maxPos != 28 {
		t.Errorf("after delete: %d rows, max position %d; want 29 and 28", n, maxPos)
	}
	if st, out := add(m.media(t, m.orgA, "event_report"), ""); st != http.StatusCreated {
		t.Errorf("add after delete: %d %s", st, out)
	}
}

// The photo report is hidden from buyers until the event has ended, by the
// public read query; the organizer sees it always.
func TestEventMaterialsExtras_PhotoReportHiddenUntilTheEventIsOver(t *testing.T) {
	m := newMatEnv(t)
	ctx := context.Background()
	q := gen.New(m.srv.pgxPool)
	base := m.base(m.orgA, m.event) + "/photo-report"
	for i := 0; i < 2; i++ {
		if st, out := m.do(t, http.MethodPost, base, m.ownerTok, "", fmt.Sprintf(`{"media_id":%q}`, m.media(t, m.orgA, "event_report"))); st != http.StatusCreated {
			t.Fatalf("add photo: %d %s", st, out)
		}
	}
	public := func(now time.Time) int {
		t.Helper()
		rows, err := q.ListEventReportPhotosPublic(ctx, m.event, now)
		if err != nil {
			t.Fatalf("ListEventReportPhotosPublic: %v", err)
		}
		return len(rows)
	}
	organizer := func() int {
		t.Helper()
		_, out := m.do(t, http.MethodGet, base, m.ownerTok, "", "")
		return len(matPhotos(t, out))
	}

	// No session at all: nothing to be over yet.
	if n := public(time.Now().Add(100 * 24 * time.Hour)); n != 0 {
		t.Errorf("event without a session: buyers see %d photos, want 0", n)
	}
	if n := organizer(); n != 2 {
		t.Errorf("organizer sees %d photos, want 2", n)
	}

	venue, session := uuid.New(), uuid.New()
	m.exec(t, `INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'Materials Hall', 'Europe/Prague')`, venue, m.orgA)
	t.Cleanup(func() {
		_, _ = m.srv.pgxPool.Exec(ctx, `DELETE FROM sessions WHERE event_id = $1`, m.event)
		_, _ = m.srv.pgxPool.Exec(ctx, `DELETE FROM venues WHERE id = $1`, venue)
	})
	start := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	m.exec(t, `INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
	           VALUES ($1, $2, $3, $4, $4::timestamptz + interval '2 hours', 50, 'scheduled', 'EUR', 'override')`, session, m.event, venue, start)

	// A future session: hidden now, still hidden an hour before it ends.
	if n := public(time.Now()); n != 0 {
		t.Errorf("before the event: buyers see %d photos, want 0", n)
	}
	if n := public(start.Add(time.Hour)); n != 0 {
		t.Errorf("during the event: buyers see %d photos, want 0", n)
	}
	if n := organizer(); n != 2 {
		t.Errorf("organizer sees %d photos before the event, want 2", n)
	}
	// After the last session's end the report opens.
	if n := public(start.Add(2*time.Hour + time.Second)); n != 2 {
		t.Errorf("after the event: buyers see %d photos, want 2", n)
	}

	// The gate follows the session: moved to the past it opens at once...
	m.exec(t, `UPDATE sessions SET start_at = now() - interval '3 hours', end_at = now() - interval '1 hour', sales_end_at = now() - interval '3 hours' WHERE id = $1`, session)
	if n := public(time.Now()); n != 2 {
		t.Errorf("session moved to the past: buyers see %d photos, want 2", n)
	}
	// ...and a cancelled session no longer counts as the event's end.
	m.exec(t, `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, session)
	if n := public(time.Now()); n != 0 {
		t.Errorf("only session cancelled: buyers see %d photos, want 0", n)
	}
	if n := organizer(); n != 2 {
		t.Errorf("organizer sees %d photos, want 2", n)
	}
}

// Another organization's owner, key and manager reach nothing by the ids of
// organization A, and a role without event.update cannot write.
func TestEventMaterialsExtras_StayInsideTheOrganization(t *testing.T) {
	m := newMatEnv(t)
	ctx := context.Background()
	linkID, photoID := uuid.New(), uuid.New()
	media := m.media(t, m.orgA, "event_report")
	m.exec(t, `INSERT INTO event_partner_links (id, org_id, event_id, url, seller_name, position) VALUES ($1, $2, $3, 'https://x.example.com/a', 'x.example.com', 0)`, linkID, m.orgA, m.event)
	m.exec(t, `INSERT INTO event_report_photos (id, org_id, event_id, media_id, position) VALUES ($1, $2, $3, $4, 0)`, photoID, m.orgA, m.event, media)
	m.exec(t, `INSERT INTO event_map_points (event_id, org_id, latitude, longitude) VALUES ($1, $2, 1, 2)`, m.event, m.orgA)

	ownerB := m.user(t, "mat-owner-b")
	m.member(t, ownerB, m.orgB, "org_admin")
	tokB := m.token(t, ownerB)
	keyB := m.key(t, m.orgB, "event.read", "event.update")
	agent := m.user(t, "mat-agent")
	m.member(t, agent, m.orgA, "agent")
	agentTok := m.token(t, agent)
	readKeyA := m.key(t, m.orgA, "event.read")

	type route struct{ method, path, body string }
	routes := func(org uuid.UUID) []route {
		b := m.base(org, m.event)
		return []route{
			{http.MethodGet, b + "/partner-links", ""},
			{http.MethodPost, b + "/partner-links", `{"url":"https://hijack.example.com/a"}`},
			{http.MethodPut, b + "/partner-links/order", fmt.Sprintf(`{"ids":["%s"]}`, linkID)},
			{http.MethodPatch, b + "/partner-links/" + linkID.String(), `{"seller_name":"Hijack"}`},
			{http.MethodDelete, b + "/partner-links/" + linkID.String(), ""},
			{http.MethodGet, b + "/map-point", ""},
			{http.MethodPut, b + "/map-point", `{"latitude":10,"longitude":10}`},
			{http.MethodDelete, b + "/map-point", ""},
			{http.MethodGet, b + "/photo-report", ""},
			{http.MethodPost, b + "/photo-report", fmt.Sprintf(`{"media_id":%q}`, media)},
			{http.MethodPut, b + "/photo-report/order", fmt.Sprintf(`{"ids":["%s"]}`, photoID)},
			{http.MethodPatch, b + "/photo-report/" + photoID.String(), `{"caption":"Hijack"}`},
			{http.MethodDelete, b + "/photo-report/" + photoID.String(), ""},
		}
	}
	// Organization B reaches for A's event through B's own path: the event is
	// not B's, so the route answers as if it did not exist.
	for _, bearer := range []string{tokB, keyB} {
		for _, r := range routes(m.orgB) {
			if st, out := m.do(t, r.method, r.path, bearer, "", r.body); st != http.StatusNotFound || sec1Code(out) != "event.not_found" {
				t.Errorf("%s %s as org B: %d %s, want 404 event.not_found", r.method, r.path, st, out)
			}
		}
	}
	// Through A's path the same callers are not members at all.
	for _, r := range routes(m.orgA) {
		if st, _ := m.do(t, r.method, r.path, tokB, "", r.body); st != http.StatusForbidden {
			t.Errorf("%s %s as owner of B on A's path: %d, want 403", r.method, r.path, st)
		}
		if st, _ := m.do(t, r.method, r.path, keyB, "", r.body); st != http.StatusForbidden {
			t.Errorf("%s %s as key of B on A's path: %d, want 403", r.method, r.path, st)
		}
	}
	// A's rows did not move.
	var links, photos, points int
	var seller string
	_ = m.srv.pgxPool.QueryRow(ctx, `SELECT count(*), max(seller_name) FROM event_partner_links WHERE event_id = $1`, m.event).Scan(&links, &seller)
	_ = m.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM event_report_photos WHERE event_id = $1 AND caption IS NULL`, m.event).Scan(&photos)
	_ = m.srv.pgxPool.QueryRow(ctx, `SELECT count(*) FROM event_map_points WHERE event_id = $1 AND latitude = 1`, m.event).Scan(&points)
	if links != 1 || seller != "x.example.com" || photos != 1 || points != 1 {
		t.Errorf("A's rows changed: links=%d seller=%q photos=%d points=%d", links, seller, photos, points)
	}

	// A's own agent holds no event.update: every write is refused, and a
	// read-only key reads but cannot write.
	for _, r := range routes(m.orgA) {
		if r.method == http.MethodGet {
			continue
		}
		if st, out := m.do(t, r.method, r.path, agentTok, "", r.body); st != http.StatusForbidden || sec1Code(out) != "permissions.denied" {
			t.Errorf("%s %s as agent: %d %s, want 403 permissions.denied", r.method, r.path, st, out)
		}
		if st, _ := m.do(t, r.method, r.path, readKeyA, "", r.body); st != http.StatusForbidden {
			t.Errorf("%s %s with a read-only key: %d, want 403", r.method, r.path, st)
		}
	}
	if st, out := m.do(t, http.MethodGet, m.base(m.orgA, m.event)+"/partner-links", readKeyA, "", ""); st != http.StatusOK {
		t.Errorf("read-only key lists links: %d %s, want 200", st, out)
	}

	// A deleted event is gone for these routes too.
	m.exec(t, `UPDATE events SET deleted_at = now() WHERE id = $1`, m.event)
	for _, r := range routes(m.orgA) {
		if st, out := m.do(t, r.method, r.path, m.ownerTok, "", r.body); st != http.StatusNotFound || sec1Code(out) != "event.not_found" {
			t.Errorf("%s %s on a deleted event: %d %s, want 404 event.not_found", r.method, r.path, st, out)
		}
	}
	// An unknown event likewise.
	if st, _ := m.do(t, http.MethodGet, m.base(m.orgA, uuid.New())+"/partner-links", m.ownerTok, "", ""); st != http.StatusNotFound {
		t.Errorf("unknown event: %d, want 404", st)
	}
}
