// bil24_501_image_test.go — contract tests for feature #501 (W1-B5b):
// GET /compat/bil24/image, the sbt/1.0 seating-plan export of spec §8.
//
// These are the pool-free half of the coverage: the live wire shape, the
// tenant gate and the GA 404 are pinned end-to-end by harness scenario 7
// (tests/compat/bil24), while this file pins the branching that a live
// fixture cannot express cheaply — the rejection ladder, the ETag/304
// revalidation and the "we are not wired" self-gate.
//
// The Handler is built with the in-memory fakeSchema from seat_d2_313_test.go
// and no channel querier, which is the documented nil-surface escape in
// image.go: without channelQ the fid → channel → org gate is skipped, so the
// tests here exercise everything *after* auth. Auth itself is a live concern
// (it needs real sales_channels rows) and belongs to the harness.
package hbil24

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// imageSchemaFixture builds a Handler serving one assigned-seats session with
// four seats across two categories, and returns the session id plus the
// geometry checksum / status version the ETag must be derived from.
func imageSchemaFixture(t *testing.T) (h *Handler, sessionID uuid.UUID, checksum string) {
	t.Helper()
	sessionID = uuid.New()
	planVersionID := uuid.New()
	_, geomRaw, checksum := canonicalGeometry()

	seats := []gen.SessionSeatRow{
		{ID: uuid.New(), SessionID: sessionID, SeatKey: "A|1|1", SectorName: "A", RowName: "1", SeatNumber: "1", Status: "available", SystemSeatID: 1_000_000_501},
		{ID: uuid.New(), SessionID: sessionID, SeatKey: "A|1|2", SectorName: "A", RowName: "1", SeatNumber: "2", Status: "held", SystemSeatID: 1_000_000_502},
		{ID: uuid.New(), SessionID: sessionID, SeatKey: "A|1|3", SectorName: "A", RowName: "1", SeatNumber: "3", Status: "sold", SystemSeatID: 1_000_000_503},
		{ID: uuid.New(), SessionID: sessionID, SeatKey: "A|1|4", SectorName: "A", RowName: "1", SeatNumber: "4", Status: "available", SystemSeatID: 1_000_000_504},
	}

	schema := &fakeSchema{
		rows: map[uuid.UUID]gen.PublicSessionSchemaRow{
			sessionID: {
				ID:                   sessionID,
				EventID:              uuid.New(),
				AdmissionMode:        "assigned_seats",
				SeatingPlanVersionID: &planVersionID,
				SeatStatusVersion:    11,
				Geometry:             json.RawMessage(geomRaw),
				GeometryChecksum:     checksum,
				CapacitySeated:       4,
			},
		},
		seats: map[uuid.UUID][]gen.SessionSeatRow{sessionID: seats},
	}
	return newHandlerWithSchema(nil, nil, schema), sessionID, checksum
}

// getImage issues one request against the route. actionEventId is passed as a
// UUID string: with no compat DB wired, resolveActionEventID falls back to the
// UUID passthrough documented in compat_ids.go.
func getImage(t *testing.T, h *Handler, query, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/compat/bil24/image?"+query, nil)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	rec := httptest.NewRecorder()
	h.HandleBil24Image(rec, req)
	return rec
}

// TestBil24_501_Image_ServesSBT10Plan pins the success path: the sbt/1.0
// document, the caching headers and the composite ETag of spec §8.
func TestBil24_501_Image_ServesSBT10Plan(t *testing.T) {
	h, sessionID, checksum := imageSchemaFixture(t)

	rec := getImage(t, h, "type=seatingPlan&actionEventId="+sessionID.String()+"&userId=0&fid=1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("Content-Type = %q, want image/svg+xml…", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	// Both halves are load-bearing: geometry alone would keep serving a
	// stale free/taken bitmap after every reservation.
	wantETag := `"` + checksum + `:11"`
	if got := rec.Header().Get("ETag"); got != wantETag {
		t.Errorf("ETag = %s, want %s", got, wantETag)
	}

	body := rec.Body.String()
	for _, want := range []string{
		`xmlns:sbt="http://www.w3.org/2015/sbt/1.0"`,
		`sbt:statusVersion="11"`,
		`<sbt:category `,
		`<circle sbt:id="1000000501" sbt:state="1"`,
		// held and sold both collapse to the single "taken" state 4.
		`sbt:id="1000000502" sbt:state="4"`,
		`sbt:id="1000000503" sbt:state="4"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

// TestBil24_501_Image_IfNoneMatchReturns304 pins the revalidation contract:
// 304, no body, and the validator headers repeated so a cache can refresh its
// stored entry instead of discarding it.
func TestBil24_501_Image_IfNoneMatchReturns304(t *testing.T) {
	h, sessionID, _ := imageSchemaFixture(t)
	query := "type=seatingPlan&actionEventId=" + sessionID.String() + "&userId=0&fid=1"

	first := getImage(t, h, query, "")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("first response carries no ETag")
	}

	second := getImage(t, h, query, etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304 (body %s)", second.Code, second.Body.String())
	}
	if second.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", second.Body.String())
	}
	if got := second.Header().Get("ETag"); got != etag {
		t.Errorf("304 ETag = %s, want %s", got, etag)
	}
	if cc := second.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("304 Cache-Control = %q, want no-cache", cc)
	}

	// A stale validator must re-download rather than silently 304.
	stale := getImage(t, h, query, `"someone-elses-checksum:3"`)
	if stale.Code != http.StatusOK {
		t.Errorf("stale If-None-Match status = %d, want 200", stale.Code)
	}
}

// TestBil24_501_Image_RejectionsAreIndistinguishable is the non-enumerability
// guard. The route is unauthenticated (it rides an <img> URL), so every
// "you may not have this" reason must answer with the same status AND the same
// body — otherwise anyone could probe which session ids exist by diffing the
// responses.
func TestBil24_501_Image_RejectionsAreIndistinguishable(t *testing.T) {
	h, sessionID, _ := imageSchemaFixture(t)

	cases := map[string]string{
		"unsupported artefact kind": "type=poster&actionEventId=" + sessionID.String() + "&fid=1",
		"missing type":              "actionEventId=" + sessionID.String() + "&fid=1",
		"unknown session":           "type=seatingPlan&actionEventId=" + uuid.New().String() + "&fid=1",
		"malformed actionEventId":   "type=seatingPlan&actionEventId=not-an-id&fid=1",
		"absent actionEventId":      "type=seatingPlan&fid=1",
	}

	var canonical string
	for name, query := range cases {
		rec := getImage(t, h, query, "")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (body %s)", name, rec.Code, rec.Body.String())
			continue
		}
		if canonical == "" {
			canonical = rec.Body.String()
			continue
		}
		// The envelope carries a request id, so compare the stable part:
		// a differing error code would be the actual information leak.
		if got, want := errorCodeOf(t, rec.Body.String()), errorCodeOf(t, canonical); got != want {
			t.Errorf("%s: error code %q differs from %q — the 404 surface is enumerable",
				name, got, want)
		}
	}
}

// TestBil24_501_Image_SelfGatesWithoutSchemaQuerier pins the operator-facing
// distinction: a Handler with no schema surface is a deployment problem, and
// answering 404 there would quietly tell every site that all plans vanished.
func TestBil24_501_Image_SelfGatesWithoutSchemaQuerier(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, ReservationDeps{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)))

	rec := getImage(t, h, "type=seatingPlan&actionEventId="+uuid.New().String()+"&fid=1", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %s)", rec.Code, rec.Body.String())
	}
	if code := errorCodeOf(t, rec.Body.String()); code != "dependency.database_unavailable" {
		t.Errorf("error code = %q, want dependency.database_unavailable", code)
	}
}

// errorCodeOf digs the platform ErrorEnvelope code out of a JSON error body.
func errorCodeOf(t *testing.T, body string) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("error body %q is not an ErrorEnvelope: %v", body, err)
	}
	return env.Error.Code
}
