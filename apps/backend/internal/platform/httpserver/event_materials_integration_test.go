//go:build integration

package httpserver

// event_materials_integration_test.go — task 1-2a: the gallery
// (.../media-items) and the video links (.../videos) of an event. Every
// request goes through the REAL router, so the permission gate, the
// organization binding, the event check and the handlers run together.
//
// Run against a migrated database (AGENTS.md CI-Integration recipe):
//
//	DATABASE_URL=... go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/ -run TestEventMaterials

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
)

type evMatFixture struct {
	*prom0113Fixture
	pool   *pgxpool.Pool
	secret string
}

func newEvMatFixture(t *testing.T) *evMatFixture {
	t.Helper()
	srv, secret := productionIntegrationServer(t)
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	q := gen.New(srv.pgxPool)
	user, err := q.InsertUser(context.Background(), "evmat-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	return &evMatFixture{
		prom0113Fixture: &prom0113Fixture{ts: ts, client: ts.Client(), q: q, userID: user.ID},
		pool:            srv.pgxPool,
		secret:          secret,
	}
}

func (f *evMatFixture) event(t *testing.T, orgID uuid.UUID) uuid.UUID {
	t.Helper()
	ev, err := f.q.InsertEvent(context.Background(), orgID, "EvMat Event "+uuid.NewString()[:6], nil, "draft", "public", nil, "text")
	if err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
	return ev.ID
}

// media inserts a media object row directly (the upload route is not under
// test) and returns its id.
func (f *evMatFixture) media(t *testing.T, orgID *uuid.UUID, ownerType string, deleted bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var deletedAt any
	if deleted {
		deletedAt = time.Now().UTC()
	}
	pool := f.pool
	if _, err := pool.Exec(context.Background(), `INSERT INTO media_objects
		(id, org_id, owner_type, storage_backend, storage_key, content_type, byte_size, checksum_sha256, deleted_at)
		VALUES ($1, $2, $3, 'local', $4, 'image/png', 1024, $5, $6)`,
		id, orgID, ownerType, "evmat/"+id.String(), strings.Repeat("a", 64), deletedAt); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	return id
}

func (f *evMatFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", sql, err)
	}
	return n
}

func ids(t *testing.T, body map[string]any) []string {
	t.Helper()
	items, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("response has no items: %v", body)
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.(map[string]any)["id"].(string))
	}
	return out
}

func positions(t *testing.T, body map[string]any) []int {
	t.Helper()
	items := body["items"].([]any)
	out := make([]int, 0, len(items))
	for _, it := range items {
		out = append(out, int(it.(map[string]any)["position"].(float64)))
	}
	return out
}

func wantStatus(t *testing.T, what string, st int, body map[string]any, wantSt int, wantCode string) {
	t.Helper()
	if st != wantSt || (wantCode != "" && prom0113Code(body) != wantCode) {
		t.Fatalf("%s: status %d code %q body %v, want %d %q", what, st, prom0113Code(body), body, wantSt, wantCode)
	}
}

func TestEventMaterials_GalleryCRUDLimitsAndOrder(t *testing.T) {
	f := newEvMatFixture(t)
	orgA := f.org(t, "A")
	keyA := f.key(t, orgA, "event.read", "event.update")
	evA := f.event(t, orgA)
	base := "/v1/organizations/" + orgA.String() + "/events/" + evA.String()
	gal := base + "/media-items"

	var photo []uuid.UUID
	for i := 0; i < 8; i++ {
		photo = append(photo, f.media(t, &orgA, "event_gallery", false))
	}
	add := func(m uuid.UUID, caption string) (int, map[string]any) {
		return f.do(t, http.MethodPost, gal, keyA, fmt.Sprintf(`{"media_id":%q,"caption":%q}`, m, caption))
	}

	// Empty gallery.
	st, body := f.do(t, http.MethodGet, gal, keyA, "")
	if st != http.StatusOK || len(ids(t, body)) != 0 || body["max_items"] != float64(6) {
		t.Fatalf("empty list: %d %v", st, body)
	}

	// First photo: position 0, caption trimmed.
	st, body = add(photo[0], "  Opening night  ")
	wantStatus(t, "add first", st, body, http.StatusCreated, "")
	if body["position"] != float64(0) || body["caption"] != "Opening night" || body["media_id"] != photo[0].String() {
		t.Fatalf("first item wrong: %v", body)
	}
	if st, body = add(photo[1], ""); st != http.StatusCreated || body["caption"] != nil || body["position"] != float64(1) {
		t.Fatalf("second item (blank caption -> null): %d %v", st, body)
	}

	// The same photo twice.
	st, body = add(photo[0], "")
	wantStatus(t, "duplicate", st, body, http.StatusConflict, "event.gallery_duplicate")

	// Fill to the limit, then the seventh.
	for i := 2; i < 6; i++ {
		if st, body = add(photo[i], ""); st != http.StatusCreated {
			t.Fatalf("add %d: %d %v", i, st, body)
		}
	}
	st, body = add(photo[6], "")
	wantStatus(t, "seventh photo", st, body, http.StatusConflict, "event.gallery_full")
	if n := f.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1`, evA); n != 6 {
		t.Fatalf("rows after refused seventh = %d, want 6", n)
	}

	// Bad input.
	for _, c := range []struct {
		body, code string
		status     int
	}{
		{`{"media_id":"nope"}`, "event.invalid_media_id", 400},
		{`{}`, "event.invalid_media_id", 400},
		{`{"media_id":"` + photo[7].String() + `","caption":"` + strings.Repeat("я", 201) + `"}`, "event.invalid_caption", 400},
	} {
		if st, body := f.do(t, http.MethodPost, gal, keyA, c.body); st != c.status || prom0113Code(body) != c.code {
			t.Errorf("POST %.50s: %d %q, want %d %s", c.body, st, prom0113Code(body), c.status, c.code)
		}
	}
	if st, _ := f.do(t, http.MethodPost, gal, keyA, ""); st != http.StatusBadRequest {
		t.Errorf("empty body: %d, want 400", st)
	}

	// Caption: set, clear with null, clear with blank, key required, too long.
	_, body = f.do(t, http.MethodGet, gal, keyA, "")
	order := ids(t, body)
	item0 := gal + "/" + order[0]
	st, body = f.do(t, http.MethodPatch, item0, keyA, `{"caption":" New "}`)
	if st != http.StatusOK || body["caption"] != "New" {
		t.Fatalf("patch caption: %d %v", st, body)
	}
	if st, body = f.do(t, http.MethodPatch, item0, keyA, `{"caption":null}`); st != http.StatusOK || body["caption"] != nil {
		t.Fatalf("clear caption: %d %v", st, body)
	}
	f.do(t, http.MethodPatch, item0, keyA, `{"caption":"x"}`)
	if st, body = f.do(t, http.MethodPatch, item0, keyA, `{"caption":"  "}`); st != http.StatusOK || body["caption"] != nil {
		t.Fatalf("blank caption: %d %v", st, body)
	}
	for _, b := range []string{`{}`, `{"caption":5}`, `{"caption":"` + strings.Repeat("x", 201) + `"}`} {
		if st, body := f.do(t, http.MethodPatch, item0, keyA, b); st != http.StatusBadRequest {
			t.Errorf("PATCH %.30s: %d %v, want 400", b, st, body)
		}
	}
	if st, body := f.do(t, http.MethodPatch, gal+"/"+uuid.NewString(), keyA, `{"caption":"x"}`); st != http.StatusNotFound || prom0113Code(body) != "event.gallery_item_not_found" {
		t.Errorf("unknown item: %d %q", st, prom0113Code(body))
	}

	// Reorder: a full reversal needs every position to change, which only
	// works as ONE statement (a deferrable unique is checked at its end).
	rev := make([]string, len(order))
	for i, id := range order {
		rev[len(order)-1-i] = id
	}
	revJSON := `{"item_ids":["` + strings.Join(rev, `","`) + `"]}`
	st, body = f.do(t, http.MethodPut, gal+"/order", keyA, revJSON)
	if st != http.StatusOK {
		t.Fatalf("reorder: %d %v", st, body)
	}
	if got := ids(t, body); strings.Join(got, ",") != strings.Join(rev, ",") {
		t.Fatalf("reordered ids = %v, want %v", got, rev)
	}
	if got := positions(t, body); fmt.Sprint(got) != "[0 1 2 3 4 5]" {
		t.Fatalf("positions after reorder = %v", got)
	}
	// The list reads back in the same order.
	_, body = f.do(t, http.MethodGet, gal, keyA, "")
	if strings.Join(ids(t, body), ",") != strings.Join(rev, ",") {
		t.Fatalf("list after reorder = %v", ids(t, body))
	}
	// Mismatches: missing id, extra foreign id, repeated id, null, bad uuid.
	for _, c := range []struct {
		name, b string
		status  int
		code    string
	}{
		{"missing one", `{"item_ids":["` + strings.Join(rev[1:], `","`) + `"]}`, 409, "event.gallery_order_mismatch"},
		{"unknown id", `{"item_ids":["` + strings.Join(append([]string{uuid.NewString()}, rev[1:]...), `","`) + `"]}`, 409, "event.gallery_order_mismatch"},
		{"empty list", `{"item_ids":[]}`, 409, "event.gallery_order_mismatch"},
		{"repeated", `{"item_ids":["` + rev[0] + `","` + rev[0] + `"]}`, 400, "event.invalid_order"},
		{"null", `{"item_ids":null}`, 400, "event.invalid_order"},
		{"bad uuid", `{"item_ids":["x"]}`, 400, "event.invalid_order"},
	} {
		if st, body := f.do(t, http.MethodPut, gal+"/order", keyA, c.b); st != c.status || prom0113Code(body) != c.code {
			t.Errorf("reorder %s: %d %q, want %d %s", c.name, st, prom0113Code(body), c.status, c.code)
		}
	}
	_, body = f.do(t, http.MethodGet, gal, keyA, "")
	if strings.Join(ids(t, body), ",") != strings.Join(rev, ",") {
		t.Fatalf("refused reorders changed the order: %v", ids(t, body))
	}

	// Delete from the middle: the gap closes, the rest keeps its order, and
	// the freed slot is the next insert's (the end of the list).
	if st, body = f.do(t, http.MethodDelete, gal+"/"+rev[2], keyA, ""); st != http.StatusNoContent {
		t.Fatalf("delete: %d %v", st, body)
	}
	_, body = f.do(t, http.MethodGet, gal, keyA, "")
	want := []string{rev[0], rev[1], rev[3], rev[4], rev[5]}
	if strings.Join(ids(t, body), ",") != strings.Join(want, ",") || fmt.Sprint(positions(t, body)) != "[0 1 2 3 4]" {
		t.Fatalf("after delete: ids %v positions %v", ids(t, body), positions(t, body))
	}
	if st, body = f.do(t, http.MethodDelete, gal+"/"+rev[2], keyA, ""); st != http.StatusNotFound {
		t.Fatalf("delete twice: %d %v", st, body)
	}
	if st, body = add(photo[6], "last"); st != http.StatusCreated || body["position"] != float64(5) {
		t.Fatalf("add after delete: %d %v", st, body)
	}
	// The deleted photo's media can come back.
	if n := f.count(t, `SELECT count(*) FROM media_objects WHERE id=$1 AND deleted_at IS NULL`, photo[2]); n != 1 {
		t.Fatalf("media object of a removed photo must stay (media GC owns it)")
	}

	// Audit rows were written in the same transaction.
	for action, min := range map[string]int{"v1.event_gallery.add": 7, "v1.event_gallery.update": 3, "v1.event_gallery.reorder": 1, "v1.event_gallery.remove": 1} {
		if n := f.count(t, `SELECT count(*) FROM audit_events WHERE action=$1 AND metadata->>'event_id'=$2`, action, evA.String()); n < min {
			t.Errorf("audit %s rows = %d, want >= %d", action, n, min)
		}
	}
}

func TestEventMaterials_GalleryMediaMustBeThisOrganizationsGalleryPhoto(t *testing.T) {
	f := newEvMatFixture(t)
	orgA, orgB := f.org(t, "A"), f.org(t, "B")
	keyA := f.key(t, orgA, "event.read", "event.update")
	evA := f.event(t, orgA)
	gal := "/v1/organizations/" + orgA.String() + "/events/" + evA.String() + "/media-items"

	cases := map[string]uuid.UUID{
		"foreign organization": f.media(t, &orgB, "event_gallery", false),
		"wrong owner type":     f.media(t, &orgA, "event_poster", false),
		"report photo":         f.media(t, &orgA, "event_report", false),
		"deleted":              f.media(t, &orgA, "event_gallery", true),
		"no organization":      f.media(t, nil, "event_gallery", false),
		"unknown":              uuid.New(),
	}
	for name, id := range cases {
		st, body := f.do(t, http.MethodPost, gal, keyA, fmt.Sprintf(`{"media_id":%q}`, id))
		if st != http.StatusUnprocessableEntity || prom0113Code(body) != "event.invalid_media" {
			t.Errorf("%s: %d %q, want 422 event.invalid_media", name, st, prom0113Code(body))
		}
	}
	if n := f.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1`, evA); n != 0 {
		t.Fatalf("refused photos left %d rows", n)
	}
	// The same refusal for a foreign id does not say whether the object exists.
	_, a := f.do(t, http.MethodPost, gal, keyA, fmt.Sprintf(`{"media_id":%q}`, cases["foreign organization"]))
	_, b := f.do(t, http.MethodPost, gal, keyA, fmt.Sprintf(`{"media_id":%q}`, cases["unknown"]))
	ea, eb := a["error"].(map[string]any), b["error"].(map[string]any)
	if ea["code"] != eb["code"] || ea["message"] != eb["message"] || fmt.Sprint(ea["details"]) != fmt.Sprint(eb["details"]) {
		t.Errorf("foreign and unknown media answer differently: %v vs %v", ea, eb)
	}
}

func TestEventMaterials_GalleryRaceNeverExceedsTheLimit(t *testing.T) {
	f := newEvMatFixture(t)
	orgA := f.org(t, "A")
	keyA := f.key(t, orgA, "event.read", "event.update")
	evA := f.event(t, orgA)
	gal := "/v1/organizations/" + orgA.String() + "/events/" + evA.String() + "/media-items"

	const n = 14
	var mediaIDs []uuid.UUID
	for i := 0; i < n; i++ {
		mediaIDs = append(mediaIDs, f.media(t, &orgA, "event_gallery", false))
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, full := 0, 0
	for _, m := range mediaIDs {
		wg.Add(1)
		go func(m uuid.UUID) {
			defer wg.Done()
			st, body := f.do(t, http.MethodPost, gal, keyA, fmt.Sprintf(`{"media_id":%q}`, m))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case st == http.StatusCreated:
				created++
			case st == http.StatusConflict && prom0113Code(body) == "event.gallery_full":
				full++
			default:
				t.Errorf("race add: %d %v", st, body)
			}
		}(m)
	}
	wg.Wait()
	if created != 6 || full != n-6 {
		t.Fatalf("created %d, full %d, want 6 and %d", created, full, n-6)
	}
	_, body := f.do(t, http.MethodGet, gal, keyA, "")
	if fmt.Sprint(positions(t, body)) != "[0 1 2 3 4 5]" {
		t.Fatalf("positions after the race = %v", positions(t, body))
	}
}

func TestEventMaterials_VideosParseLimitAndOrder(t *testing.T) {
	f := newEvMatFixture(t)
	orgA := f.org(t, "A")
	keyA := f.key(t, orgA, "event.read", "event.update")
	evA := f.event(t, orgA)
	vid := "/v1/organizations/" + orgA.String() + "/events/" + evA.String() + "/videos"
	post := func(url string) (int, map[string]any) {
		return f.do(t, http.MethodPost, vid, keyA, fmt.Sprintf(`{"url":%q}`, url))
	}

	st, body := post("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if st != http.StatusCreated || body["provider"] != "youtube" || body["video_id"] != "dQw4w9WgXcQ" || body["position"] != float64(0) {
		t.Fatalf("youtube: %d %v", st, body)
	}
	st, body = post(" https://vimeo.com/123456789 ")
	if st != http.StatusCreated || body["provider"] != "vimeo" || body["video_id"] != "123456789" || body["url"] != "https://vimeo.com/123456789" {
		t.Fatalf("vimeo: %d %v", st, body)
	}
	st, body = post("https://rutube.ru/video/0123456789abcdef0123456789abcdef/")
	if st != http.StatusCreated || body["provider"] != "rutube" || body["video_id"] != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("rutube: %d %v", st, body)
	}
	st, body = post("https://example.com/promo")
	if st != http.StatusCreated || body["provider"] != "https" || body["video_id"] != nil {
		t.Fatalf("https: %d %v", st, body)
	}
	// The fifth.
	st, body = post("https://youtu.be/aaaaaaaaaaa")
	wantStatus(t, "fifth video", st, body, http.StatusConflict, "event.videos_full")
	if n := f.count(t, `SELECT count(*) FROM event_videos WHERE event_id=$1`, evA); n != 4 {
		t.Fatalf("rows = %d, want 4", n)
	}

	// Invalid links change nothing (checked before the limit, so one video is
	// removed first to prove it is the link that is refused).
	_, list := f.do(t, http.MethodGet, vid, keyA, "")
	order := ids(t, list)
	if st, body = f.do(t, http.MethodDelete, vid+"/"+order[3], keyA, ""); st != http.StatusNoContent {
		t.Fatalf("delete: %d %v", st, body)
	}
	for _, bad := range []string{"http://youtu.be/dQw4w9WgXcQ", "javascript:alert(1)", "https://exa mple.com/a", "", "   ", "www.youtube.com/watch?v=dQw4w9WgXcQ", "https://u:p@example.com/", "https://example.com/" + strings.Repeat("a", 500)} {
		st, body := post(bad)
		if st != http.StatusBadRequest || prom0113Code(body) != "event.invalid_video_url" {
			t.Errorf("%.40q: %d %q, want 400 event.invalid_video_url", bad, st, prom0113Code(body))
		}
	}
	if st, body := f.do(t, http.MethodPost, vid, keyA, `{}`); st != http.StatusBadRequest || prom0113Code(body) != "event.invalid_video_url" {
		t.Errorf("missing url: %d %q", st, prom0113Code(body))
	}
	if st, _ := f.do(t, http.MethodPost, vid, keyA, ""); st != http.StatusBadRequest {
		t.Errorf("empty body: %d", st)
	}

	// Reorder (full reversal of three) and mismatch.
	rev := []string{order[2], order[1], order[0]}
	st, body = f.do(t, http.MethodPut, vid+"/order", keyA, `{"video_ids":["`+strings.Join(rev, `","`)+`"]}`)
	if st != http.StatusOK || strings.Join(ids(t, body), ",") != strings.Join(rev, ",") || fmt.Sprint(positions(t, body)) != "[0 1 2]" {
		t.Fatalf("reorder: %d %v", st, body)
	}
	st, body = f.do(t, http.MethodPut, vid+"/order", keyA, `{"video_ids":["`+rev[0]+`"]}`)
	wantStatus(t, "short list", st, body, http.StatusConflict, "event.videos_order_mismatch")
	st, body = f.do(t, http.MethodPut, vid+"/order", keyA, `{"video_ids":["`+rev[0]+`","`+rev[0]+`"]}`)
	wantStatus(t, "repeated id", st, body, http.StatusBadRequest, "event.invalid_order")

	// Delete from the front closes the gap, and a new video goes last.
	if st, _ = f.do(t, http.MethodDelete, vid+"/"+rev[0], keyA, ""); st != http.StatusNoContent {
		t.Fatalf("delete first: %d", st)
	}
	st, body = post("https://youtu.be/bbbbbbbbbbb")
	if st != http.StatusCreated || body["position"] != float64(2) {
		t.Fatalf("add after delete: %d %v", st, body)
	}
	if st, body = f.do(t, http.MethodDelete, vid+"/"+uuid.NewString(), keyA, ""); st != http.StatusNotFound || prom0113Code(body) != "event.video_not_found" {
		t.Fatalf("unknown video: %d %v", st, body)
	}
	for action, min := range map[string]int{"v1.event_video.add": 5, "v1.event_video.remove": 2, "v1.event_video.reorder": 1} {
		if n := f.count(t, `SELECT count(*) FROM audit_events WHERE action=$1 AND metadata->>'event_id'=$2`, action, evA.String()); n < min {
			t.Errorf("audit %s rows = %d, want >= %d", action, n, min)
		}
	}
}

func TestEventMaterials_GatesAndOrganizationBoundary(t *testing.T) {
	f := newEvMatFixture(t)
	secret := f.secret
	orgA, orgB := f.org(t, "A"), f.org(t, "B")
	keyA := f.key(t, orgA, "event.read", "event.update")
	keyReadOnly := f.key(t, orgA, "event.read")
	keyNoEvent := f.key(t, orgA, "promoter.read")
	keyB := f.key(t, orgB, "event.read", "event.update")
	evA, evB := f.event(t, orgA), f.event(t, orgB)
	mediaA := f.media(t, &orgA, "event_gallery", false)
	mediaB := f.media(t, &orgB, "event_gallery", false)

	baseA := "/v1/organizations/" + orgA.String() + "/events/" + evA.String()
	st, body := f.do(t, http.MethodPost, baseA+"/media-items", keyA, fmt.Sprintf(`{"media_id":%q}`, mediaA))
	if st != http.StatusCreated {
		t.Fatalf("seed photo: %d %v", st, body)
	}
	photoA := body["id"].(string)
	st, body = f.do(t, http.MethodPost, baseA+"/videos", keyA, `{"url":"https://example.com/v"}`)
	if st != http.StatusCreated {
		t.Fatalf("seed video: %d %v", st, body)
	}
	videoA := body["id"].(string)

	// A read-only key reads and may not write.
	for _, p := range []string{"/media-items", "/videos"} {
		if st, _ := f.do(t, http.MethodGet, baseA+p, keyReadOnly, ""); st != http.StatusOK {
			t.Errorf("read-only GET %s: %d", p, st)
		}
		if st, _ := f.do(t, http.MethodGet, baseA+p, keyNoEvent, ""); st != http.StatusForbidden {
			t.Errorf("key without event.read GET %s: %d, want 403", p, st)
		}
	}
	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/media-items", fmt.Sprintf(`{"media_id":%q}`, mediaA)},
		{http.MethodPut, "/media-items/order", `{"item_ids":["` + photoA + `"]}`},
		{http.MethodPatch, "/media-items/" + photoA, `{"caption":"x"}`},
		{http.MethodDelete, "/media-items/" + photoA, ""},
		{http.MethodPost, "/videos", `{"url":"https://example.com/w"}`},
		{http.MethodPut, "/videos/order", `{"video_ids":["` + videoA + `"]}`},
		{http.MethodDelete, "/videos/" + videoA, ""},
	}
	for _, w := range writes {
		if st, _ := f.do(t, w.method, baseA+w.path, keyReadOnly, w.body); st != http.StatusForbidden {
			t.Errorf("read-only key %s %s: %d, want 403", w.method, w.path, st)
		}
	}

	// An agent (a membership role without event.update) is refused too.
	ctx := context.Background()
	u, err := f.q.InsertUser(ctx, "evmat-agent-"+uuid.NewString()+"@example.test", "x", "en")
	if err != nil {
		t.Fatalf("InsertUser: %v", err)
	}
	if _, err := f.q.InsertMembership(ctx, u.ID, orgA, "agent"); err != nil {
		t.Fatalf("InsertMembership: %v", err)
	}
	agent, _, err := auth.IssueJWT(secret, u.ID, nil, nil, "arena-api", "arena-api", time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	for _, w := range writes {
		if st, _ := f.do(t, w.method, baseA+w.path, agent, w.body); st != http.StatusForbidden {
			t.Errorf("agent %s %s: %d, want 403", w.method, w.path, st)
		}
	}

	// Organization B's key, through ITS OWN organization path, names A's event:
	// every route answers 404 and A's rows do not move.
	baseB := "/v1/organizations/" + orgB.String() + "/events/" + evA.String()
	foreign := []struct{ method, path, body string }{
		{http.MethodGet, "/media-items", ""},
		{http.MethodGet, "/videos", ""},
		{http.MethodPost, "/media-items", fmt.Sprintf(`{"media_id":%q}`, mediaB)},
		{http.MethodPut, "/media-items/order", `{"item_ids":["` + photoA + `"]}`},
		{http.MethodPatch, "/media-items/" + photoA, `{"caption":"hijack"}`},
		{http.MethodDelete, "/media-items/" + photoA, ""},
		{http.MethodPost, "/videos", `{"url":"https://example.com/hijack"}`},
		{http.MethodPut, "/videos/order", `{"video_ids":["` + videoA + `"]}`},
		{http.MethodDelete, "/videos/" + videoA, ""},
	}
	for _, c := range foreign {
		st, body := f.do(t, c.method, baseB+c.path, keyB, c.body)
		if st != http.StatusNotFound || prom0113Code(body) != "event.not_found" {
			t.Errorf("foreign event %s %s: %d %q, want 404 event.not_found", c.method, c.path, st, prom0113Code(body))
		}
	}
	// B's key cannot enter A's organization path either.
	if st, _ := f.do(t, http.MethodGet, baseA+"/media-items", keyB, ""); st != http.StatusForbidden {
		t.Errorf("org B key on org A path: %d, want 403", st)
	}
	// A's key cannot reach B's event through A's path.
	if st, body := f.do(t, http.MethodGet, "/v1/organizations/"+orgA.String()+"/events/"+evB.String()+"/media-items", keyA, ""); st != http.StatusNotFound {
		t.Errorf("A's key on B's event: %d %v", st, body)
	}
	if n := f.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1 AND caption IS NULL`, evA); n != 1 {
		t.Errorf("A's photo changed by the foreign calls")
	}
	if n := f.count(t, `SELECT count(*) FROM event_videos WHERE event_id=$1`, evA); n != 1 {
		t.Errorf("A's videos changed by the foreign calls: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM event_gallery_items WHERE event_id=$1`, evB); n != 0 {
		t.Errorf("a photo landed on B's event")
	}

	// A photo of the item id of ANOTHER event of the same organization is not
	// reachable through this event either.
	evA2 := f.event(t, orgA)
	if st, body := f.do(t, http.MethodDelete, "/v1/organizations/"+orgA.String()+"/events/"+evA2.String()+"/media-items/"+photoA, keyA, ""); st != http.StatusNotFound || prom0113Code(body) != "event.gallery_item_not_found" {
		t.Errorf("photo through a sibling event: %d %q", st, prom0113Code(body))
	}
	if st, body := f.do(t, http.MethodDelete, "/v1/organizations/"+orgA.String()+"/events/"+evA2.String()+"/videos/"+videoA, keyA, ""); st != http.StatusNotFound || prom0113Code(body) != "event.video_not_found" {
		t.Errorf("video through a sibling event: %d %q", st, prom0113Code(body))
	}

	// Unknown and soft-deleted events read as not found.
	if st, _ := f.do(t, http.MethodGet, "/v1/organizations/"+orgA.String()+"/events/"+uuid.NewString()+"/videos", keyA, ""); st != http.StatusNotFound {
		t.Errorf("unknown event: %d", st)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE events SET deleted_at = now() WHERE id = $1`, evA2); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if st, _ := f.do(t, http.MethodGet, "/v1/organizations/"+orgA.String()+"/events/"+evA2.String()+"/media-items", keyA, ""); st != http.StatusNotFound {
		t.Errorf("deleted event: %d", st)
	}

	// Bad path ids are 400.
	if st, _ := f.do(t, http.MethodDelete, baseA+"/media-items/not-a-uuid", keyA, ""); st != http.StatusBadRequest {
		t.Errorf("bad item id: %d", st)
	}
}

// Review 141: a pasted link with an upper-case scheme used to pass the parser
// and then fail the case-sensitive CHECK url ~ '^https://' (23514, a 500), and
// a caption with a NUL byte failed the text column (22021, a 500).
func TestEventMaterials_UpperCaseSchemeAndNulCaptionAreNot500(t *testing.T) {
	f := newEvMatFixture(t)
	orgA := f.org(t, "A")
	keyA := f.key(t, orgA, "event.read", "event.update")
	evA := f.event(t, orgA)
	base := "/v1/organizations/" + orgA.String() + "/events/" + evA.String()

	st, body := f.do(t, http.MethodPost, base+"/videos", keyA, `{"url":"HTTPS://YouTu.be/dQw4w9WgXcQ"}`)
	if st != http.StatusCreated || body["provider"] != "youtube" || body["url"] != "https://YouTu.be/dQw4w9WgXcQ" {
		t.Fatalf("upper-case scheme: %d %v", st, body)
	}
	media := f.media(t, &orgA, "event_gallery", false)
	st, body = f.do(t, http.MethodPost, base+"/media-items", keyA, fmt.Sprintf(`{"media_id":%q,"caption":"a\u0000b"}`, media))
	if st != http.StatusCreated || body["caption"] != "ab" {
		t.Fatalf("NUL caption: %d %v", st, body)
	}
}
