// seat_d1_312_test.go — contract tests for feature #312 Wave SEAT-D1:
//
//	GET_SEAT_LIST admission_mode branch (general_admission tier-facade
//	vs assigned_seats / hybrid real seats) and the RESERVATION command:
//	dispatcher validation (seatList vs categoryList mutual exclusivity +
//	admission_mode gate) plus the REAL hold wiring (second half of
//	SEAT-D1): seatId → seat_key translation, org/channel resolution from
//	the session + fid, the injected hcheckout hold callbacks, the
//	platform-computed financial fields (sum/discount/charge/totalSum),
//	cartTimeout, and the UN_RESERVE release path.
//
// The tests use in-memory fakes for the seating / reservation
// dependencies so they exercise the real branching logic without a live
// PostgreSQL pool (the hbil24 fake-query precedent). The tier-facade
// fallback path is covered indirectly by the pre-existing #157 test
// suite in the httpserver package.
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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
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

func (f *fakeSeats) GetSessionSeatByID(_ context.Context, id, sessionID uuid.UUID) (gen.SessionSeatRow, error) {
	if f.err != nil {
		return gen.SessionSeatRow{}, f.err
	}
	for _, s := range f.seats[sessionID] {
		if s.ID == id {
			return s, nil
		}
	}
	return gen.SessionSeatRow{}, pgx.ErrNoRows
}

// fakeResCtx implements ReservationContextQuerier in memory.
type fakeResCtx struct {
	orgBySession map[uuid.UUID]uuid.UUID
	channels     map[uuid.UUID]gen.SalesChannelRow
}

func (f *fakeResCtx) GetSessionOrgContext(_ context.Context, sessionID uuid.UUID) (gen.SessionOrgContextRow, error) {
	orgID, ok := f.orgBySession[sessionID]
	if !ok {
		return gen.SessionOrgContextRow{}, pgx.ErrNoRows
	}
	return gen.SessionOrgContextRow{SessionID: sessionID, OrgID: orgID}, nil
}

func (f *fakeResCtx) GetSalesChannelByID(_ context.Context, id, orgID uuid.UUID) (gen.SalesChannelRow, error) {
	ch, ok := f.channels[id]
	if !ok || ch.OrgID != orgID {
		return gen.SalesChannelRow{}, pgx.ErrNoRows
	}
	return ch, nil
}

// fakeTiers implements TierPriceQuerier in memory.
type fakeTiers struct {
	tiers map[uuid.UUID]gen.TicketTierRow
}

func (f *fakeTiers) GetTicketTierByID(_ context.Context, id, _ uuid.UUID) (gen.TicketTierRow, error) {
	t, ok := f.tiers[id]
	if !ok {
		return gen.TicketTierRow{}, pgx.ErrNoRows
	}
	return t, nil
}

func (f *fakeTiers) ListTicketTiersBySession(_ context.Context, _ uuid.UUID) ([]gen.TicketTierRow, error) {
	out := make([]gen.TicketTierRow, 0, len(f.tiers))
	for _, t := range f.tiers {
		out = append(out, t)
	}
	return out, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// newHandler wires a Handler with the SEAT-D1 dependencies but nil query
// pools for the tier/event/checkout/ticket/barcode fields the tested
// branches do not touch. Reservation deps are empty — RESERVATION tests
// that exercise the real hold wiring use newHandlerWithReserve.
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
		ReservationDeps{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	)
}

// newHandlerWithReserve wires a Handler with the full RESERVATION /
// UN_RESERVE dependency set (fake queriers + fake hold callbacks).
func newHandlerWithReserve(admQ AdmissionQuerier, seatQ SeatQuerier, deps ReservationDeps) *Handler {
	return New(
		nil, nil, nil, nil, nil,
		admQ,
		seatQ,
		nil,
		deps,
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

// reserveFixture bundles the standard happy-path RESERVATION test world:
// one hybrid session with two seats bound to a fixed-price tier, one org,
// one sales channel, and recording fake hold callbacks.
type reserveFixture struct {
	sessionID uuid.UUID
	orgID     uuid.UUID
	channelID uuid.UUID
	tierID    uuid.UUID
	seatIDs   []uuid.UUID
	seats     *fakeSeats
	adm       *fakeAdmission
	deps      ReservationDeps

	seatedCalls []hcheckout.SeatedHoldInput
	gaCalls     []hcheckout.GAHoldInput
	released    []uuid.UUID
}

func newReserveFixture(admissionMode string) *reserveFixture {
	f := &reserveFixture{
		sessionID: uuid.New(),
		orgID:     uuid.New(),
		channelID: uuid.New(),
		tierID:    uuid.New(),
		seatIDs:   []uuid.UUID{uuid.New(), uuid.New()},
	}
	seatRows := []gen.SessionSeatRow{
		{ID: f.seatIDs[0], SessionID: f.sessionID, SeatKey: "A-1-1", SectorName: "A", RowName: "1", SeatNumber: "1", TierID: &f.tierID, Status: "available"},
		{ID: f.seatIDs[1], SessionID: f.sessionID, SeatKey: "A-1-2", SectorName: "A", RowName: "1", SeatNumber: "2", TierID: &f.tierID, Status: "available"},
	}
	f.seats = &fakeSeats{seats: map[uuid.UUID][]gen.SessionSeatRow{f.sessionID: seatRows}}
	f.adm = &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		f.sessionID: {ID: f.sessionID, AdmissionMode: admissionMode, CapacityTotal: 100},
	}}

	reservationID := uuid.New()
	f.deps = ReservationDeps{
		CtxQ: &fakeResCtx{
			orgBySession: map[uuid.UUID]uuid.UUID{f.sessionID: f.orgID},
			channels: map[uuid.UUID]gen.SalesChannelRow{
				f.channelID: {ID: f.channelID, OrgID: f.orgID, Name: "bil24-agent"},
			},
		},
		TierQ: &fakeTiers{tiers: map[uuid.UUID]gen.TicketTierRow{
			f.tierID: {ID: f.tierID, SessionID: f.sessionID, Name: "Parterre", PricingMode: "fixed", PriceAmount: 2500, Currency: "CZK"},
		}},
		SeatedReserve: func(_ context.Context, in hcheckout.SeatedHoldInput) (hcheckout.SeatedHoldResult, error) {
			f.seatedCalls = append(f.seatedCalls, in)
			held := make([]gen.SessionSeatRow, 0, len(in.SeatKeys))
			for _, key := range in.SeatKeys {
				for _, s := range seatRows {
					if s.SeatKey == key {
						heldRow := s
						heldRow.Status = "held"
						held = append(held, heldRow)
					}
				}
			}
			return hcheckout.SeatedHoldResult{
				Reservation: gen.ReservationRow{
					ID:        reservationID,
					OrgID:     in.OrgID,
					ChannelID: in.ChannelID,
					SessionID: in.SessionID,
					Quantity:  int32(len(held)), //nolint:gosec // test fixture
					State:     "draft",
					ExpiresAt: in.ExpiresAt,
				},
				Seats: held,
			}, nil
		},
		GAReserve: func(_ context.Context, in hcheckout.GAHoldInput) (gen.ReservationRow, error) {
			f.gaCalls = append(f.gaCalls, in)
			var qty int32
			for _, it := range in.Items {
				qty += it.Quantity
			}
			return gen.ReservationRow{
				ID:        reservationID,
				OrgID:     in.OrgID,
				ChannelID: in.ChannelID,
				SessionID: in.SessionID,
				Quantity:  qty,
				State:     "draft",
				ExpiresAt: in.ExpiresAt,
			}, nil
		},
		Release: func(_ context.Context, id uuid.UUID) (gen.ReservationRow, error) {
			f.released = append(f.released, id)
			return gen.ReservationRow{ID: id, State: "cancelled"}, nil
		},
	}
	return f
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

// TestBil24_312_GetSeatList_GA_UsesTierFacade ensures general_admission
// still routes through the pre-#312 tier-facade branch (no seatList
// projection with real seats).
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
// RESERVATION — dispatcher validation
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

func TestBil24_312_Reservation_Seated_NonUUIDSeatEntry_InvalidRequest(t *testing.T) {
	sessionID := uuid.New()
	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "assigned_seats"},
	}}
	h := newHandler(adm, nil, nil)
	body := `{"command":"RESERVATION","actionEventId":"` + sessionID.String() +
		`","seatList":["NOT-A-UUID"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d (ADR-005 seatId must be a UUID), got %d", ResultCodeInvalidRequest, rc)
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

// TestBil24_312_Reservation_NoReserveDeps_ServiceUnavailable pins the
// self-gating contract of the REAL wiring: a structurally valid seated
// request against a handler without reservation callbacks reports the
// reservation service as unavailable (resultCode=-99) instead of the old
// scaffold echo.
func TestBil24_312_Reservation_NoReserveDeps_ServiceUnavailable(t *testing.T) {
	h := newHandler(nil, nil, nil)
	sid := uuid.New().String()
	body := `{"command":"RESERVATION","actionEventId":"` + sid +
		`","seatList":["` + uuid.New().String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInternalError {
		t.Errorf("want %d (reservation service unavailable), got %d; body: %v",
			ResultCodeInternalError, rc, resp)
	}
	if !strings.Contains(resp["description"].(string), "reservation service unavailable") {
		t.Errorf("description should mention reservation service unavailable, got %v", resp["description"])
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// RESERVATION — real hold wiring (feature #312 second half)
// ─────────────────────────────────────────────────────────────────────────────

// TestBil24_312_Reservation_Seated_RealHold verifies the full seated flow:
// seatId → seat_key translation, org/channel resolution from session +
// fid, the SeatedReserve callback invocation, and the legacy response
// contract (real reservationId, cartTimeout, platform-computed financial
// fields with totalSum = sum - discount + charge).
func TestBil24_312_Reservation_Seated_RealHold(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)

	body := `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","seatList":["` + f.seatIDs[0].String() + `","` + f.seatIDs[1].String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}

	// The callback received translated seat_keys and the resolved tenant.
	if len(f.seatedCalls) != 1 {
		t.Fatalf("SeatedReserve calls: want 1, got %d", len(f.seatedCalls))
	}
	call := f.seatedCalls[0]
	if call.OrgID != f.orgID || call.ChannelID != f.channelID || call.SessionID != f.sessionID {
		t.Errorf("SeatedReserve tenant context wrong: %+v", call)
	}
	if len(call.SeatKeys) != 2 || call.SeatKeys[0] != "A-1-1" || call.SeatKeys[1] != "A-1-2" {
		t.Errorf("SeatedReserve seat keys: want [A-1-1 A-1-2], got %v", call.SeatKeys)
	}

	// Real reservation id (a UUID string, not the old "pending" scaffold).
	ridStr, _ := resp["reservationId"].(string)
	if _, err := uuid.Parse(ridStr); err != nil {
		t.Errorf("reservationId: want a real UUID string, got %v", resp["reservationId"])
	}
	if _, scaffolded := resp["status"]; scaffolded {
		t.Errorf("response must not carry the scaffold status field, got %v", resp["status"])
	}

	// cartTimeout is positive seconds until the hold expiry.
	ct, _ := resp["cartTimeout"].(float64)
	if ct <= 0 || ct > hcheckout.DefaultReservationTTL.Seconds() {
		t.Errorf("cartTimeout: want (0, %v], got %v", hcheckout.DefaultReservationTTL.Seconds(), resp["cartTimeout"])
	}

	// Financial fields: 2 seats × 2500 = 5000; zero-rate rules → charge 0.
	if got := int64(resp["sum"].(float64)); got != 5000 {
		t.Errorf("sum: want 5000, got %d", got)
	}
	if got := int64(resp["totalSum"].(float64)); got != 5000 {
		t.Errorf("totalSum: want 5000, got %d", got)
	}
	sum := int64(resp["sum"].(float64))
	discount := int64(resp["discount"].(float64))
	charge := int64(resp["charge"].(float64))
	totalSum := int64(resp["totalSum"].(float64))
	if totalSum != sum-discount+charge {
		t.Errorf("financial invariant broken: totalSum=%d, sum-discount+charge=%d", totalSum, sum-discount+charge)
	}
	if resp["currency"] != "CZK" {
		t.Errorf("currency: want CZK, got %v", resp["currency"])
	}

	// Held seats echoed as ADR-005 id strings.
	seatList, ok := resp["seatList"].([]any)
	if !ok || len(seatList) != 2 {
		t.Fatalf("seatList projection wrong: %v", resp["seatList"])
	}
	if seatList[0] != f.seatIDs[0].String() {
		t.Errorf("seatList[0]: want %s, got %v", f.seatIDs[0], seatList[0])
	}
}

// TestBil24_312_Reservation_Seated_FeesInCharge verifies the charge field
// carries the platform pipeline fees (5% platform + 2% provider on the
// discounted base) and the totalSum invariant holds.
func TestBil24_312_Reservation_Seated_FeesInCharge(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	f.deps.PricingRules = hcheckout.PricingRules{PlatformFeeRate: 500, ProviderFeeRate: 200}
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)

	body := `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","seatList":["` + f.seatIDs[0].String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
	// 1 seat × 2500 → platform 125, provider 50 → charge 175, totalSum 2675.
	if got := int64(resp["sum"].(float64)); got != 2500 {
		t.Errorf("sum: want 2500, got %d", got)
	}
	if got := int64(resp["charge"].(float64)); got != 175 {
		t.Errorf("charge: want 175 (5%%+2%% fees), got %d", got)
	}
	if got := int64(resp["totalSum"].(float64)); got != 2675 {
		t.Errorf("totalSum: want 2675, got %d", got)
	}
}

func TestBil24_312_Reservation_Seated_MissingFID_InvalidRequest(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	body := `{"command":"RESERVATION","actionEventId":"` + f.sessionID.String() +
		`","seatList":["` + f.seatIDs[0].String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d (fid required), got %d; body: %v", ResultCodeInvalidRequest, rc, resp)
	}
	if !strings.Contains(resp["description"].(string), "fid") {
		t.Errorf("description should mention fid, got %v", resp["description"])
	}
}

func TestBil24_312_Reservation_Seated_UnknownChannel_NotFound(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	body := `{"command":"RESERVATION","fid":"` + uuid.New().String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","seatList":["` + f.seatIDs[0].String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeNotFound {
		t.Errorf("want %d (unknown sales channel), got %d; body: %v", ResultCodeNotFound, rc, resp)
	}
}

func TestBil24_312_Reservation_Seated_UnknownSeatID_NotFound(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	unknownSeat := uuid.New()
	body := `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","seatList":["` + unknownSeat.String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeNotFound {
		t.Errorf("want %d (seat not found), got %d; body: %v", ResultCodeNotFound, rc, resp)
	}
	if resp["seatId"] != unknownSeat.String() {
		t.Errorf("seatId detail: want %s, got %v", unknownSeat, resp["seatId"])
	}
}

// TestBil24_312_Reservation_Seated_ConflictMapped verifies that a
// SeatConflictsError from the hold API surfaces as resultCode=-2 with the
// per-seat conflicts detail merged into the envelope.
func TestBil24_312_Reservation_Seated_ConflictMapped(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	f.deps.SeatedReserve = func(_ context.Context, _ hcheckout.SeatedHoldInput) (hcheckout.SeatedHoldResult, error) {
		return hcheckout.SeatedHoldResult{}, &hcheckout.SeatConflictsError{
			Conflicts: []map[string]string{{"seat_key": "A-1-1", "status": "held"}},
		}
	}
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	body := `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","seatList":["` + f.seatIDs[0].String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeInvalidRequest, rc, resp)
	}
	conflicts, ok := resp["conflicts"].([]any)
	if !ok || len(conflicts) != 1 {
		t.Fatalf("conflicts detail missing: %v", resp)
	}
	c := conflicts[0].(map[string]any)
	if c["seat_key"] != "A-1-1" || c["status"] != "held" {
		t.Errorf("conflict entry wrong: %v", c)
	}
}

// TestBil24_312_Reservation_GA_RealHold verifies the GA flow: per-tier
// platform pricing, the GAReserve callback invocation with priced items,
// and the financial fields.
func TestBil24_312_Reservation_GA_RealHold(t *testing.T) {
	f := newReserveFixture("general_admission")
	tier2 := uuid.New()
	f.deps.TierQ.(*fakeTiers).tiers[tier2] = gen.TicketTierRow{
		ID: tier2, SessionID: f.sessionID, Name: "Standing", PricingMode: "fixed", PriceAmount: 1000, Currency: "CZK",
	}
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)

	body := `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + f.tierID.String() + `","quantity":2},{"categoryPriceId":"` +
		tier2.String() + `","quantity":3}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
	if int(resp["totalQuantity"].(float64)) != 5 {
		t.Errorf("totalQuantity: want 5, got %v", resp["totalQuantity"])
	}
	// 2×2500 + 3×1000 = 8000.
	if got := int64(resp["sum"].(float64)); got != 8000 {
		t.Errorf("sum: want 8000, got %d", got)
	}
	if got := int64(resp["totalSum"].(float64)); got != 8000 {
		t.Errorf("totalSum: want 8000, got %d", got)
	}
	ridStr, _ := resp["reservationId"].(string)
	if _, err := uuid.Parse(ridStr); err != nil {
		t.Errorf("reservationId: want a real UUID string, got %v", resp["reservationId"])
	}
	if ct, _ := resp["cartTimeout"].(float64); ct <= 0 {
		t.Errorf("cartTimeout: want > 0, got %v", resp["cartTimeout"])
	}

	// The GAReserve callback received platform-priced items.
	if len(f.gaCalls) != 1 {
		t.Fatalf("GAReserve calls: want 1, got %d", len(f.gaCalls))
	}
	items := f.gaCalls[0].Items
	if len(items) != 2 {
		t.Fatalf("GAReserve items: want 2, got %d", len(items))
	}
	if items[0].TierID != f.tierID || items[0].Quantity != 2 || items[0].UnitPrice != 2500 {
		t.Errorf("items[0] wrong: %+v", items[0])
	}
	if items[1].TierID != tier2 || items[1].Quantity != 3 || items[1].UnitPrice != 1000 {
		t.Errorf("items[1] wrong: %+v", items[1])
	}
}

func TestBil24_312_Reservation_GA_UnknownTier_NotFound(t *testing.T) {
	f := newReserveFixture("general_admission")
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	body := `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + uuid.New().String() + `","quantity":1}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeNotFound {
		t.Errorf("want %d (tier not in session), got %d; body: %v", ResultCodeNotFound, rc, resp)
	}
}

func TestBil24_312_Reservation_GA_PwywTier_Rejected(t *testing.T) {
	f := newReserveFixture("general_admission")
	pwyw := uuid.New()
	f.deps.TierQ.(*fakeTiers).tiers[pwyw] = gen.TicketTierRow{
		ID: pwyw, SessionID: f.sessionID, Name: "PWYW", PricingMode: "pwyw", Currency: "CZK",
	}
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	body := `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + pwyw.String() + `","quantity":1}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d (pwyw unsupported on gateway), got %d; body: %v", ResultCodeInvalidRequest, rc, resp)
	}
}

// TestBil24_312_Reservation_GA_CapacityMapped verifies that a
// CapacityError from the hold API surfaces as resultCode=-2 with the
// per-tier capacity detail.
func TestBil24_312_Reservation_GA_CapacityMapped(t *testing.T) {
	f := newReserveFixture("general_admission")
	tierID := f.tierID
	f.deps.GAReserve = func(_ context.Context, _ hcheckout.GAHoldInput) (gen.ReservationRow, error) {
		return gen.ReservationRow{}, &hcheckout.CapacityError{TierID: &tierID, Requested: 2}
	}
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	body := `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + f.tierID.String() + `","quantity":2}]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeInvalidRequest, rc, resp)
	}
	capacity, ok := resp["capacity"].(map[string]any)
	if !ok {
		t.Fatalf("capacity detail missing: %v", resp)
	}
	if capacity["categoryPriceId"] != f.tierID.String() {
		t.Errorf("capacity.categoryPriceId: want %s, got %v", f.tierID, capacity["categoryPriceId"])
	}
}

func TestBil24_312_Reservation_Hybrid_AcceptsBothPayloads(t *testing.T) {
	f := newReserveFixture("hybrid")
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)

	// Seated payload accepted.
	body := `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","seatList":["` + f.seatIDs[0].String() + `"]}`
	resp := postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Errorf("hybrid seated: want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
	if resp["admissionMode"] != "hybrid" {
		t.Errorf("hybrid seated admissionMode want 'hybrid', got %v", resp["admissionMode"])
	}

	// GA payload also accepted.
	body = `{"command":"RESERVATION","fid":"` + f.channelID.String() +
		`","actionEventId":"` + f.sessionID.String() +
		`","categoryList":[{"categoryPriceId":"` + f.tierID.String() + `","quantity":1}]}`
	resp = postJSON(t, h, body)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Errorf("hybrid GA: want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// UN_RESERVE — hold release
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_312_UnReserve_ReleasesHold(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	rid := uuid.New()
	resp := postJSON(t, h, `{"command":"UN_RESERVE","reservationId":"`+rid.String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
	if resp["status"] != "cancelled" {
		t.Errorf("status: want cancelled, got %v", resp["status"])
	}
	if resp["reservationId"] != rid.String() {
		t.Errorf("reservationId: want %s, got %v", rid, resp["reservationId"])
	}
	if len(f.released) != 1 || f.released[0] != rid {
		t.Errorf("Release callback: want [%s], got %v", rid, f.released)
	}
}

func TestBil24_312_UnReserve_MissingReservationID_InvalidRequest(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	resp := postJSON(t, h, `{"command":"UN_RESERVE"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_312_UnReserve_NotFound(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	f.deps.Release = func(_ context.Context, _ uuid.UUID) (gen.ReservationRow, error) {
		return gen.ReservationRow{}, hcheckout.ErrHoldNotFound
	}
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	resp := postJSON(t, h, `{"command":"UN_RESERVE","reservationId":"`+uuid.New().String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeNotFound {
		t.Errorf("want %d, got %d", ResultCodeNotFound, rc)
	}
}

func TestBil24_312_UnReserve_NotReleasable_InvalidRequest(t *testing.T) {
	f := newReserveFixture("assigned_seats")
	f.deps.Release = func(_ context.Context, _ uuid.UUID) (gen.ReservationRow, error) {
		return gen.ReservationRow{}, &hcheckout.NotReleasableError{State: "converted"}
	}
	h := newHandlerWithReserve(f.adm, f.seats, f.deps)
	resp := postJSON(t, h, `{"command":"UN_RESERVE","reservationId":"`+uuid.New().String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d, got %d", ResultCodeInvalidRequest, rc)
	}
	if !strings.Contains(resp["description"].(string), "converted") {
		t.Errorf("description should carry the blocking state, got %v", resp["description"])
	}
}

func TestBil24_312_UnReserve_NoDeps_ServiceUnavailable(t *testing.T) {
	h := newHandler(nil, nil, nil)
	resp := postJSON(t, h, `{"command":"UN_RESERVE","reservationId":"`+uuid.New().String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInternalError {
		t.Errorf("want %d, got %d", ResultCodeInternalError, rc)
	}
}

// Guard against fixture drift: cartTimeout must be derived from the
// reservation's expiry, not a hard-coded constant.
func TestBil24_312_CartTimeoutSeconds(t *testing.T) {
	if got := cartTimeoutSeconds(time.Now().Add(-time.Minute)); got != 0 {
		t.Errorf("expired hold: want 0, got %d", got)
	}
	got := cartTimeoutSeconds(time.Now().Add(10 * time.Minute))
	if got <= 0 || got > 600 {
		t.Errorf("10-minute hold: want (0, 600], got %d", got)
	}
}
