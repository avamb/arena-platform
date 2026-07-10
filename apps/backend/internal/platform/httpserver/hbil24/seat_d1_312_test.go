// seat_d1_312_test.go — contract tests for feature #312 Wave SEAT-D1:
//
//	GET_SEAT_LIST admission_mode branch (general_admission tier-facade
//	vs assigned_seats / hybrid real seats) and the new RESERVATION
//	command dispatcher (seatList vs categoryList mutual exclusivity +
//	admission_mode gate).
//
// The tests use in-memory fakes for the seating dependencies so they
// exercise the real branching logic without a live PostgreSQL pool. The
// tier-facade fallback path is covered indirectly by the pre-existing
// #157 test suite in the httpserver package.
package hbil24

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fakes
// ─────────────────────────────────────────────────────────────────────────────

// fakeAdmission implements the AdmissionQuerier contract in memory.
type fakeAdmission struct {
	sessions map[uuid.UUID]gen.SessionAdmissionRow
}

func (f *fakeAdmission) GetSessionAdmissionModeByID(_ context.Context, id uuid.UUID) (gen.SessionAdmissionRow, error) {
	row, ok := f.sessions[id]
	if !ok {
		return gen.SessionAdmissionRow{}, pgx.ErrNoRows
	}
	return row, nil
}

// fakeSeats implements the SeatQuerier contract in memory.
type fakeSeats struct {
	seats map[uuid.UUID][]gen.SessionSeatRow
	err   error
}

func (f *fakeSeats) ListSessionSeats(_ context.Context, id uuid.UUID) ([]gen.SessionSeatRow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.seats[id], nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// newHandler wires a Handler with the SEAT-D1 dependencies but nil query
// pools for the tier/event/checkout/ticket/barcode fields the tested
// branches do not touch. The tier query is nilable; assigned-seat tests
// that need real prices assemble their own tier snapshot via nilable
// fakes elsewhere.
func newHandler(admQ AdmissionQuerier, seatQ SeatQuerier, tierQ *gen.Queries) *Handler {
	return New(
		nil, // eventQueries
		tierQ,
		nil, // checkoutQueries
		nil, // ticketQueries
		nil, // barcodeQueries
		admQ,
		seatQ,
		nil, // schemaQ — SEAT-D2 (#313) tests supply their own via
		//        newHandlerWithSchema in seat_d2_313_test.go.
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	)
}

// postJSON invokes h.HandleBil24Command with the given JSON body and
// returns the decoded response envelope.
func postJSON(t *testing.T, h *Handler, body string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/compat/bil24/json",
		bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleBil24Command(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got %d; body: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, w.Body.String())
	}
	return out
}

func mustResultCode(t *testing.T, resp map[string]any) int {
	t.Helper()
	rc, ok := resp["resultCode"].(float64)
	if !ok {
		t.Fatalf("resultCode missing or not a number: %v", resp)
	}
	return int(rc)
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_SEAT_LIST — SEAT-D1 assigned-seats branch
// ─────────────────────────────────────────────────────────────────────────────

// TestBil24_312_GetSeatList_AssignedSeats_ProjectsRealSeats verifies the
// SEAT-D1 wire projection: assigned_seats sessions emit one entry per
// session_seat with the ADR-005 seatId (session_seats.id string), BSS
// status code, and admissionMode=assigned_seats. The tier snapshot is
// unwired for this test so entries fall back to price=0 without
// tripping the graceful-degrade path.
func TestBil24_312_GetSeatList_AssignedSeats_ProjectsRealSeats(t *testing.T) {
	sessionID := uuid.New()
	tierID := uuid.New()
	seatIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	seats := []gen.SessionSeatRow{
		{ID: seatIDs[0], SessionID: sessionID, SeatKey: "A-1-1", SectorName: "A", RowName: "1", SeatNumber: "1", TierID: &tierID, Status: "available"},
		{ID: seatIDs[1], SessionID: sessionID, SeatKey: "A-1-2", SectorName: "A", RowName: "1", SeatNumber: "2", TierID: &tierID, Status: "held"},
		{ID: seatIDs[2], SessionID: sessionID, SeatKey: "A-1-3", SectorName: "A", RowName: "1", SeatNumber: "3", TierID: &tierID, Status: "sold"},
		{ID: seatIDs[3], SessionID: sessionID, SeatKey: "A-1-4", SectorName: "A", RowName: "1", SeatNumber: "4", TierID: nil, Status: "blocked"},
	}
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "assigned_seats", CapacityTotal: 4},
	}}
	sf := &fakeSeats{seats: map[uuid.UUID][]gen.SessionSeatRow{sessionID: seats}}
	h := newHandler(adm, sf, nil)

	resp := postJSON(t, h, `{"command":"GET_SEAT_LIST","actionEventId":"`+sessionID.String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
	if resp["admissionMode"] != "assigned_seats" {
		t.Errorf("admissionMode: want assigned_seats, got %v", resp["admissionMode"])
	}
	list, ok := resp["seatList"].([]any)
	if !ok {
		t.Fatalf("seatList missing / wrong type: %T %v", resp["seatList"], resp["seatList"])
	}
	if len(list) != 4 {
		t.Fatalf("seatList: want 4 entries, got %d", len(list))
	}

	// Order and BSS codes.
	wantStatusCodes := []int{1, 3, 4, 0}
	for i, entry := range list {
		m := entry.(map[string]any)
		if m["seatId"] != seatIDs[i].String() {
			t.Errorf("seat[%d].seatId: want %s, got %v", i, seatIDs[i], m["seatId"])
		}
		if m["sector"] != "A" {
			t.Errorf("seat[%d].sector: want A, got %v", i, m["sector"])
		}
		if got := int(m["status"].(float64)); got != wantStatusCodes[i] {
			t.Errorf("seat[%d].status: want %d, got %d", i, wantStatusCodes[i], got)
		}
	}

	// Tier-bound seats include categoryPriceId; unbound seat 4 does not.
	first := list[0].(map[string]any)
	if first["categoryPriceId"] != tierID.String() {
		t.Errorf("seat[0].categoryPriceId: want %s, got %v", tierID, first["categoryPriceId"])
	}
	last := list[3].(map[string]any)
	if _, present := last["categoryPriceId"]; present {
		t.Errorf("seat[3] should not carry categoryPriceId when TierID is nil, got %v", last["categoryPriceId"])
	}
}

// TestBil24_312_GetSeatList_GA_Fallback ensures general_admission still
// routes through the pre-#312 tier-facade branch (no seatList projection
// with real seats).
func TestBil24_312_GetSeatList_GA_UsesTierFacade(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "general_admission"},
	}}
	// tierQueries nil triggers the guard rather than tier-facade output;
	// this narrower assertion checks the branching decision (GA path
	// selected → tier-service-unavailable error rather than the seat
	// projection). seatQ is wired non-nil so the outer both-nil guard
	// does not short-circuit before the branch decision.
	sf := &fakeSeats{}
	h := newHandler(adm, sf, nil)
	resp := postJSON(t, h, `{"command":"GET_SEAT_LIST","actionEventId":"`+sessionID.String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInternalError {
		t.Errorf("want %d (tier service unavailable on GA branch), got %d", ResultCodeInternalError, rc)
	}
	if !strings.Contains(resp["description"].(string), "tier service unavailable") {
		t.Errorf("description should mention tier service unavailable, got %v", resp["description"])
	}
}

// TestBil24_312_GetSeatList_BSSStatusCodes checks the §6 BSS mapping —
// pure, pool-free unit test.
func TestBil24_312_GetSeatList_BSSStatusCodes(t *testing.T) {
	cases := []struct {
		status string
		want   int
	}{
		{"available", 1},
		{"held", 3},
		{"sold", 4},
		{"blocked", 0},
		{"", 0},
		{"unknown", 0},
	}
	for _, c := range cases {
		if got := bssStatusCode(c.status); got != c.want {
			t.Errorf("bssStatusCode(%q) = %d; want %d", c.status, got, c.want)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RESERVATION — SEAT-D1 dispatcher
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_312_Reservation_MissingActionEventID_InvalidRequest(t *testing.T) {
	h := newHandler(nil, nil, nil)
	resp := postJSON(t, h, `{"command":"RESERVATION","seatList":["s1"]}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want resultCode=%d, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_312_Reservation_InvalidSessionUUID_InvalidRequest(t *testing.T) {
	h := newHandler(nil, nil, nil)
	resp := postJSON(t, h, `{"command":"RESERVATION","actionEventId":"NOT_A_UUID","seatList":["s1"]}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want resultCode=%d, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_312_Reservation_BothSeatAndCategoryList_InvalidRequest(t *testing.T) {
	h := newHandler(nil, nil, nil)
	sid := uuid.New().String()
	body := `{"command":"RESERVATION","actionEventId":"` + sid +
		`","seatList":["s1"],"categoryList":[{"categoryPriceId":"` +
		uuid.New().String() + `","quantity":1}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want resultCode=%d, got %d", ResultCodeInvalidRequest, rc)
	}
	if !strings.Contains(resp["description"].(string), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' in description, got %v", resp["description"])
	}
}

func TestBil24_312_Reservation_NeitherPayload_InvalidRequest(t *testing.T) {
	h := newHandler(nil, nil, nil)
	sid := uuid.New().String()
	resp := postJSON(t, h, `{"command":"RESERVATION","actionEventId":"`+sid+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want resultCode=%d, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_312_Reservation_Seated_GA_Rejected(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "general_admission"},
	}}
	h := newHandler(adm, nil, nil)
	resp := postJSON(t, h, `{"command":"RESERVATION","actionEventId":"`+sessionID.String()+
		`","seatList":["`+uuid.New().String()+`"]}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d, got %d", ResultCodeInvalidRequest, rc)
	}
	if !strings.Contains(resp["description"].(string), "general_admission") {
		t.Errorf("description should mention general_admission, got %v", resp["description"])
	}
}

func TestBil24_312_Reservation_CategoryList_Assigned_Rejected(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "assigned_seats"},
	}}
	h := newHandler(adm, nil, nil)
	body := `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + uuid.New().String() + `","quantity":2}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d, got %d", ResultCodeInvalidRequest, rc)
	}
	if !strings.Contains(resp["description"].(string), "assigned_seats") {
		t.Errorf("description should mention assigned_seats, got %v", resp["description"])
	}
}

func TestBil24_312_Reservation_SessionNotFound_NotFound(t *testing.T) {
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{}}
	h := newHandler(adm, nil, nil)
	sid := uuid.New().String()
	resp := postJSON(t, h, `{"command":"RESERVATION","actionEventId":"`+sid+
		`","seatList":["`+uuid.New().String()+`"]}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeNotFound {
		t.Errorf("want %d, got %d", ResultCodeNotFound, rc)
	}
}

func TestBil24_312_Reservation_Seated_HappyPath(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "assigned_seats"},
	}}
	h := newHandler(adm, nil, nil)
	s1, s2 := uuid.New().String(), uuid.New().String()
	body := `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","seatList":["` + s1 + `","` + s2 + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
	if resp["admissionMode"] != "assigned_seats" {
		t.Errorf("admissionMode: want 'assigned_seats', got %v", resp["admissionMode"])
	}
	if int(resp["seatCount"].(float64)) != 2 {
		t.Errorf("seatCount: want 2, got %v", resp["seatCount"])
	}
	if resp["status"] != "scaffold_stub" {
		t.Errorf("status: want scaffold_stub, got %v", resp["status"])
	}
	seatList, ok := resp["seatList"].([]any)
	if !ok || len(seatList) != 2 {
		t.Fatalf("seatList projection wrong: %v", resp["seatList"])
	}
}

func TestBil24_312_Reservation_Seated_DuplicateSeat_InvalidRequest(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "assigned_seats"},
	}}
	h := newHandler(adm, nil, nil)
	s1 := uuid.New().String()
	body := `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","seatList":["` + s1 + `","` + s1 + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_312_Reservation_Seated_EmptySeatEntry_InvalidRequest(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "assigned_seats"},
	}}
	h := newHandler(adm, nil, nil)
	body := `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","seatList":["  "]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_312_Reservation_GA_HappyPath(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "general_admission"},
	}}
	h := newHandler(adm, nil, nil)
	tier1, tier2 := uuid.New().String(), uuid.New().String()
	body := `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + tier1 + `","quantity":2},{"categoryPriceId":"` +
		tier2 + `","quantity":3}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
	if resp["admissionMode"] != "general_admission" {
		t.Errorf("admissionMode: want 'general_admission', got %v", resp["admissionMode"])
	}
	if int(resp["totalQuantity"].(float64)) != 5 {
		t.Errorf("totalQuantity: want 5, got %v", resp["totalQuantity"])
	}
}

func TestBil24_312_Reservation_GA_ZeroQuantity_InvalidRequest(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "general_admission"},
	}}
	h := newHandler(adm, nil, nil)
	body := `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + uuid.New().String() + `","quantity":0}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_312_Reservation_GA_InvalidTierUUID_InvalidRequest(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "general_admission"},
	}}
	h := newHandler(adm, nil, nil)
	body := `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"BAD","quantity":1}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_312_Reservation_Hybrid_AcceptsBothPayloads(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "hybrid"},
	}}
	h := newHandler(adm, nil, nil)

	// Seated payload accepted.
	body := `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","seatList":["` + uuid.New().String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Errorf("hybrid seated: want %d, got %d", ResultCodeOK, rc)
	}
	if resp["admissionMode"] != "hybrid" {
		t.Errorf("hybrid seated admissionMode want 'hybrid', got %v", resp["admissionMode"])
	}

	// GA payload also accepted.
	body = `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + uuid.New().String() + `","quantity":1}]}`
	resp = postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Errorf("hybrid GA: want %d, got %d", ResultCodeOK, rc)
	}
}

func TestBil24_312_Reservation_NoAdmissionQ_FallbackAcceptsPayload(t *testing.T) {
	// When admission dependency is nil (SEAT-D rollout not wired),
	// RESERVATION should accept whichever payload the caller supplies
	// without failing on session lookup — matches GET_SEAT_LIST
	// fallback behavior.
	h := newHandler(nil, nil, nil)
	sid := uuid.New().String()
	body := `{"command":"RESERVATION","actionEventId":"` + sid +
		`","seatList":["` + uuid.New().String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Errorf("no-admissionQ fallback: want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
}
