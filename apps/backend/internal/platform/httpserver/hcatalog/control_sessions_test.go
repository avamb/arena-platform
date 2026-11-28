package hcatalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/salesstate"
)

func TestParseControlSessionRequest(t *testing.T) {
	t.Parallel()
	venue := uuid.New().String()
	body := func(fields string) []byte {
		return []byte(`{"title":"Town hall","venue_id":"` + venue + `","start_at":"2026-12-05T18:00:00Z"` + fields + `}`)
	}
	cases := []struct {
		name   string
		body   []byte
		status int // 0 = accepted
		code   string
		field  string
	}{
		{"minimal", body(``), 0, "", ""},
		{"full", body(`,"end_at":"2026-12-05T21:30:00Z","capacity":300,"currency":"eur"`), 0, "", ""},
		{"null optionals", body(`,"end_at":null,"capacity":null,"currency":null`), 0, "", ""},
		{"empty end_at is absent", body(`,"end_at":""`), 0, "", ""},
		{"empty body", nil, http.StatusBadRequest, "session.empty_body", ""},
		{"not json", []byte(`{`), http.StatusBadRequest, "session.invalid_json", ""},
		{"no title", []byte(`{"venue_id":"` + venue + `","start_at":"2026-12-05T18:00:00Z"}`), http.StatusBadRequest, "session.missing_title", "title"},
		{"blank title", []byte(`{"title":"   ","venue_id":"` + venue + `","start_at":"2026-12-05T18:00:00Z"}`), http.StatusBadRequest, "session.missing_title", "title"},
		{"title too long", []byte(`{"title":"` + strings.Repeat("я", 201) + `","venue_id":"` + venue + `","start_at":"2026-12-05T18:00:00Z"}`), http.StatusBadRequest, "session.title_too_long", "title"},
		{"title at the limit", []byte(`{"title":"` + strings.Repeat("я", 200) + `","venue_id":"` + venue + `","start_at":"2026-12-05T18:00:00Z"}`), 0, "", ""},
		{"no venue", []byte(`{"title":"T","start_at":"2026-12-05T18:00:00Z"}`), http.StatusBadRequest, "session.missing_venue_id", "venue_id"},
		{"bad venue", []byte(`{"title":"T","venue_id":"nope","start_at":"2026-12-05T18:00:00Z"}`), http.StatusBadRequest, "session.invalid_venue_id", "venue_id"},
		{"no start", []byte(`{"title":"T","venue_id":"` + venue + `"}`), http.StatusBadRequest, "session.missing_start_at", "start_at"},
		{"bad start", []byte(`{"title":"T","venue_id":"` + venue + `","start_at":"tomorrow"}`), http.StatusBadRequest, "session.invalid_start_at", "start_at"},
		{"bad end", body(`,"end_at":"later"`), http.StatusBadRequest, "session.invalid_end_at", "end_at"},
		{"end equals start", body(`,"end_at":"2026-12-05T18:00:00Z"`), http.StatusBadRequest, "session.invalid_date_range", "end_at"},
		{"end before start", body(`,"end_at":"2026-12-05T17:00:00Z"`), http.StatusBadRequest, "session.invalid_date_range", "end_at"},
		{"zero capacity", body(`,"capacity":0`), http.StatusBadRequest, "session.invalid_capacity", "capacity"},
		{"negative capacity", body(`,"capacity":-5`), http.StatusBadRequest, "session.invalid_capacity", "capacity"},
		{"capacity beyond int32", body(`,"capacity":3000000000`), http.StatusBadRequest, "session.invalid_capacity", "capacity"},
		{"fractional capacity", body(`,"capacity":1.5`), http.StatusBadRequest, "session.invalid_json", ""},
		{"bad currency", body(`,"currency":"EURO"`), http.StatusUnprocessableEntity, "session.invalid_currency", "currency"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, ferr := parseControlSessionRequest(c.body)
			if c.status == 0 {
				if ferr != nil {
					t.Fatalf("refused: %+v", ferr)
				}
				return
			}
			if ferr == nil {
				t.Fatalf("accepted, want %d %s", c.status, c.code)
			}
			if ferr.Status != c.status || ferr.Code != c.code || ferr.Field != c.field {
				t.Fatalf("got %d %s field %q, want %d %s field %q", ferr.Status, ferr.Code, ferr.Field, c.status, c.code, c.field)
			}
		})
	}
}

func TestParseControlSessionRequest_Defaults(t *testing.T) {
	t.Parallel()
	venue := uuid.New()
	in, ferr := parseControlSessionRequest([]byte(`{"title":"  Town hall  ","venue_id":"` + venue.String() + `","start_at":"2026-12-05T18:00:00+02:00"}`))
	if ferr != nil {
		t.Fatalf("refused: %+v", ferr)
	}
	if in.Title != "Town hall" {
		t.Errorf("title = %q, want it trimmed", in.Title)
	}
	if in.VenueID != venue {
		t.Errorf("venue = %s, want %s", in.VenueID, venue)
	}
	if got := in.EndAt.Sub(in.StartAt); got != 3*time.Hour {
		t.Errorf("default length = %s, want 3h", got)
	}
	if in.Capacity != 1 || in.CapacityOverride != nil {
		t.Errorf("capacity = %d override %v, want 1 and none", in.Capacity, in.CapacityOverride)
	}
	if in.Currency != "" {
		t.Errorf("currency = %q, want it left to the venue", in.Currency)
	}

	in, ferr = parseControlSessionRequest([]byte(`{"title":"T","venue_id":"` + venue.String() + `","start_at":"2026-12-05T18:00:00Z","end_at":"2026-12-05T20:00:00Z","capacity":250,"currency":" eur "}`))
	if ferr != nil {
		t.Fatalf("refused: %+v", ferr)
	}
	if got := in.EndAt.Sub(in.StartAt); got != 2*time.Hour {
		t.Errorf("length = %s, want 2h", got)
	}
	if in.Capacity != 250 || in.CapacityOverride == nil || *in.CapacityOverride != 250 {
		t.Errorf("capacity = %d override %v, want 250 for both", in.Capacity, in.CapacityOverride)
	}
	if in.Currency != "EUR" {
		t.Errorf("currency = %q, want EUR", in.Currency)
	}
}

func envelopeCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not JSON: %s", rec.Body.String())
	}
	e, _ := env["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// A sales_mode control_only in a session create body is refused with the
// lock code and names the route to use.
func TestSalesModeLockedCreate(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	org := uuid.New()
	salesModeLockedCreate(rec, httptest.NewRequest(http.MethodPost, "/", nil), org)
	if rec.Code != http.StatusConflict || envelopeCode(t, rec) != "session.sales_mode_locked" {
		t.Fatalf("got %d %s, want 409 session.sales_mode_locked", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/organizations/"+org.String()+"/control-sessions") {
		t.Errorf("message does not name the control-sessions route: %s", rec.Body.String())
	}
}

func TestWriteControlOnlyEvent(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeControlOnlyEvent(rec, httptest.NewRequest(http.MethodPost, "/", nil), uuid.New(), "why")
	if rec.Code != http.StatusConflict || envelopeCode(t, rec) != "event.control_only" {
		t.Fatalf("got %d %s, want 409 event.control_only", rec.Code, rec.Body.String())
	}
}

// The parse whitelist knows control_only (so the lock rule, not "invalid
// value", answers) but an unknown mode is still refused as invalid.
func TestParseSalesModeWrite_ControlOnly(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	out, ok := parseSalesModeWrite(rec, httptest.NewRequest(http.MethodPost, "/", nil), str("control_only"), nil)
	if !ok || out.Mode != gen.SessionSalesModeControlOnly {
		t.Fatalf("control_only: ok=%v mode=%q status=%d", ok, out.Mode, rec.Code)
	}
	rec = httptest.NewRecorder()
	if _, ok := parseSalesModeWrite(rec, httptest.NewRequest(http.MethodPost, "/", nil), str("control-only"), nil); ok || rec.Code != http.StatusBadRequest {
		t.Fatalf("control-only: ok=%v status=%d, want 400", ok, rec.Code)
	}
	if code := envelopeCode(t, rec); code != "session.invalid_sales_mode" {
		t.Fatalf("code = %s", code)
	}
}

// A control event is a draft container. ListEventSalesFacts counts its future
// control session in future_sessions (selling and places_left leave it out),
// so while the session is ahead the event reads like a draft event with no
// tiers, and once it has ended like any finished event: never "archived"
// while the date is still to come.
func TestSalesStateFor_ControlEvent(t *testing.T) {
	t.Parallel()
	ahead := gen.EventSalesFacts{SessionCount: 1, FutureSessions: 1}
	if got := salesStateFor("draft", ahead, true); got != SalesStateDraft {
		t.Errorf("future control event = %q, want %q", got, SalesStateDraft)
	}
	past := gen.EventSalesFacts{SessionCount: 1, FutureSessions: 0}
	if got := salesStateFor("draft", past, true); got != SalesStateArchived {
		t.Errorf("past control event = %q, want %q", got, SalesStateArchived)
	}
}

func TestControlOnlySessionReadsEnded(t *testing.T) {
	t.Parallel()
	now := time.Now()
	f := salesstate.Facts{Status: "scheduled", SalesEnd: now.Add(time.Hour), TotalPlaces: 10, FreePlaces: 10, Selling: true, ControlOnly: true}
	eff := salesstate.Effective("", f, now)
	if eff != salesstate.ControlOnly {
		t.Fatalf("effective = %q, want the internal control_only", eff)
	}
	if got := salesstate.Public(eff); got != salesstate.Ended {
		t.Fatalf("public = %q, want ended", got)
	}
	for _, s := range []string{salesstate.Open, salesstate.Soon, salesstate.SoldOut, salesstate.Ended, salesstate.Cancelled, salesstate.External} {
		if got := salesstate.Public(s); got != s {
			t.Errorf("Public(%q) = %q, want it unchanged", s, got)
		}
	}
}
