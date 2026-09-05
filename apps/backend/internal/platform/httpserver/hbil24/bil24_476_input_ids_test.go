// bil24_476_input_ids_test.go — feature #476 (W1-A2b) pins the
// wave-1 int64-input rule for GET_SEAT_LIST and GET_SCHEMA (spec §4, §7.2,
// §7.15). When the handler is wired with a compatDB, a UUID string on the
// actionEventId field is rejected with resultCode=-2 (invalid request)
// before any DB round-trip happens.
//
// The tests use a panicDBTX stub for the compatDB slot: bil24compat.
// ParseLegacyIntID short-circuits on UUID input before compatids.Resolve
// touches the DB, so any query call is a regression. When the compatDB is
// nil (existing unit-test constructor path) the fallback keeps accepting
// UUID strings so the seat_d1_312 / seat_d2_313 test harness stays green
// during the step-by-step migration.
package hbil24

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/bil24compat"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// panicDBTX satisfies gen.DBTX but panics on every method. Any call is a
// contract violation — ParseLegacyIntID must reject the UUID before the
// caller reaches compatids.Resolve.
type panicDBTX struct{}

func (panicDBTX) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("panicDBTX.Exec: DB must not be touched when input is a UUID")
}

func (panicDBTX) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("panicDBTX.Query: DB must not be touched when input is a UUID")
}

func (panicDBTX) QueryRow(context.Context, string, ...any) pgx.Row {
	panic("panicDBTX.QueryRow: DB must not be touched when input is a UUID")
}

// seatsEmpty is the smallest fakeSeats value that satisfies the
// GET_SEAT_LIST seat-service-availability guard (`seatQ != nil`).
func seatsEmpty() *fakeSeats {
	return &fakeSeats{seats: map[uuid.UUID][]gen.SessionSeatRow{}}
}

// schemaEmpty is the smallest fakeSchema value that satisfies the
// GET_SCHEMA schema-service-availability guard (`schemaQ != nil`).
func schemaEmpty() *fakeSchema {
	return &fakeSchema{
		rows:  map[uuid.UUID]gen.PublicSessionSchemaRow{},
		seats: map[uuid.UUID][]gen.SessionSeatRow{},
	}
}

// TestBil24_476_GetSeatList_UUIDInput_RejectedWithCompatDB pins the wave-1
// invariant: with compatDB wired, GET_SEAT_LIST refuses a UUID
// actionEventId with -2 before any DB call.
func TestBil24_476_GetSeatList_UUIDInput_RejectedWithCompatDB(t *testing.T) {
	// Non-nil seatQ passes the seat-service-availability guard; the ID
	// parse runs next.
	h := newHandler(nil, seatsEmpty(), nil).WithCompatDB(panicDBTX{})
	sessionUUID := uuid.New().String()
	resp := postJSON(t, h,
		`{"command":"GET_SEAT_LIST","actionEventId":"`+sessionUUID+`"}`)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeInvalidRequest {
		t.Errorf("GET_SEAT_LIST UUID input: want %d, got %d; body: %v",
			ResultCodeInvalidRequest, rc, resp)
	}
}

// TestBil24_476_GetSchema_UUIDInput_RejectedWithCompatDB pins the wave-1
// invariant for GET_SCHEMA (spec §7.15).
func TestBil24_476_GetSchema_UUIDInput_RejectedWithCompatDB(t *testing.T) {
	h := newHandlerWithSchema(nil, nil, schemaEmpty()).
		WithCompatDB(panicDBTX{})
	sessionUUID := uuid.New().String()
	resp := postJSON(t, h,
		`{"command":"GET_SCHEMA","actionEventId":"`+sessionUUID+`"}`)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeInvalidRequest {
		t.Errorf("GET_SCHEMA UUID input: want %d, got %d; body: %v",
			ResultCodeInvalidRequest, rc, resp)
	}
}

// TestBil24_476_GetSeatList_NonNumericInput_RejectedWithCompatDB proves
// that garbage (non-UUID, non-int64) also returns -2.
func TestBil24_476_GetSeatList_NonNumericInput_RejectedWithCompatDB(t *testing.T) {
	h := newHandler(nil, seatsEmpty(), nil).WithCompatDB(panicDBTX{})
	resp := postJSON(t, h,
		`{"command":"GET_SEAT_LIST","actionEventId":"not-an-id"}`)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeInvalidRequest {
		t.Errorf("GET_SEAT_LIST non-numeric input: want %d, got %d; body: %v",
			ResultCodeInvalidRequest, rc, resp)
	}
}

// TestBil24_476_Reservation_UUIDInput_RejectedWithCompatDB pins the wave-1
// invariant for RESERVATION (spec §4 / §7.4): with compatDB wired the
// handler refuses a UUID actionEventId with -2 before any DB call. The
// panicDBTX stub guarantees ParseLegacyIntID short-circuits inside
// resolveActionEventID before compatids.Resolve can touch the pool.
func TestBil24_476_Reservation_UUIDInput_RejectedWithCompatDB(t *testing.T) {
	// Minimal handler is enough — actionEventId parsing happens before any
	// dependency (admissionQ / resDeps / channelQ) is consulted, so nil
	// dependencies do not affect this contract.
	h := newMinimalHandler().WithCompatDB(panicDBTX{})
	sessionUUID := uuid.New().String()
	// categoryList satisfies the "seatList or categoryList required" gate
	// that runs after ID resolution; the request never reaches it because
	// the UUID input is rejected first, but including it keeps the fixture
	// self-contained if the ordering ever changes.
	resp := postJSON(t, h,
		`{"command":"RESERVATION","actionEventId":"`+sessionUUID+
			`","fid":"1","token":"x","categoryList":[{"categoryPriceId":"1","quantity":1}]}`)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeInvalidRequest {
		t.Errorf("RESERVATION UUID input: want %d, got %d; body: %v",
			ResultCodeInvalidRequest, rc, resp)
	}
}

// TestBil24_476_Reservation_NonNumericInput_RejectedWithCompatDB proves
// that garbage on the actionEventId field also returns -2.
func TestBil24_476_Reservation_NonNumericInput_RejectedWithCompatDB(t *testing.T) {
	h := newMinimalHandler().WithCompatDB(panicDBTX{})
	resp := postJSON(t, h,
		`{"command":"RESERVATION","actionEventId":"not-an-id","fid":"1","token":"x","categoryList":[{"categoryPriceId":"1","quantity":1}]}`)
	rc := mustResultCode(t, resp)
	if rc != ResultCodeInvalidRequest {
		t.Errorf("RESERVATION non-numeric input: want %d, got %d; body: %v",
			ResultCodeInvalidRequest, rc, resp)
	}
}

// TestBil24_476_ResolveCategoryPriceID_UUIDInput_RejectedWithCompatDB pins
// the RESERVATION request-side wave-1 invariant for the categoryList
// sub-field categoryPriceId (spec §4 / §7.4): with compatDB wired the
// helper refuses a UUID input with bil24compat.ErrLegacyIDUUIDRejected
// before any DB round-trip.  The panicDBTX stub guarantees ParseLegacyIntID
// short-circuits inside resolveCategoryPriceID before compatids.Resolve can
// touch the pool.
func TestBil24_476_ResolveCategoryPriceID_UUIDInput_RejectedWithCompatDB(t *testing.T) {
	h := newMinimalHandler().WithCompatDB(panicDBTX{})
	_, err := h.resolveCategoryPriceID(context.Background(), uuid.New().String())
	if !errors.Is(err, bil24compat.ErrLegacyIDUUIDRejected) {
		t.Fatalf("resolveCategoryPriceID(uuid): want ErrLegacyIDUUIDRejected, got %v", err)
	}
}

// TestBil24_476_ResolveCategoryPriceID_NonNumericInput_RejectedWithCompatDB
// proves that garbage on the categoryPriceId field also returns
// bil24compat.ErrLegacyIDInvalid without a DB round-trip.
func TestBil24_476_ResolveCategoryPriceID_NonNumericInput_RejectedWithCompatDB(t *testing.T) {
	h := newMinimalHandler().WithCompatDB(panicDBTX{})
	_, err := h.resolveCategoryPriceID(context.Background(), "not-an-id")
	if !errors.Is(err, bil24compat.ErrLegacyIDInvalid) {
		t.Fatalf("resolveCategoryPriceID(non-numeric): want ErrLegacyIDInvalid, got %v", err)
	}
}

// TestBil24_476_ResolveSeatToRow_UUIDInput_RejectedWithCompatDB pins the
// RESERVATION seated request-side wave-1 invariant for the seatList entry
// (spec §4 / §7.4): with compatDB wired the helper refuses a UUID input
// with ErrSeatIDInvalid wrapping bil24compat.ErrLegacyIDUUIDRejected —
// before any DB round-trip. The panicDBTX compatDB stub proves the
// short-circuit; a fakeSeats attached to the handler is required by the
// helper signature but the panicDBTX guarantees we never actually reach
// GetSessionSeatBySystemSeatID.
func TestBil24_476_ResolveSeatToRow_UUIDInput_RejectedWithCompatDB(t *testing.T) {
	h := newHandler(nil, seatsEmpty(), nil).WithCompatDB(panicDBTX{})
	_, err := h.resolveSeatToRow(context.Background(), uuid.New().String(), uuid.New())
	if !errors.Is(err, ErrSeatIDInvalid) {
		t.Fatalf("resolveSeatToRow(uuid): want ErrSeatIDInvalid, got %v", err)
	}
	if !errors.Is(err, bil24compat.ErrLegacyIDUUIDRejected) {
		t.Fatalf("resolveSeatToRow(uuid): want wrapped ErrLegacyIDUUIDRejected, got %v", err)
	}
}

// TestBil24_476_ResolveSeatToRow_NonNumericInput_RejectedWithCompatDB
// proves that garbage on the seatList entry field also short-circuits
// before any DB round-trip.
func TestBil24_476_ResolveSeatToRow_NonNumericInput_RejectedWithCompatDB(t *testing.T) {
	h := newHandler(nil, seatsEmpty(), nil).WithCompatDB(panicDBTX{})
	_, err := h.resolveSeatToRow(context.Background(), "not-an-id", uuid.New())
	if !errors.Is(err, ErrSeatIDInvalid) {
		t.Fatalf("resolveSeatToRow(non-numeric): want ErrSeatIDInvalid, got %v", err)
	}
}

// TestBil24_476_ResolveSeatToRow_NilCompatDB_FallbackParsesUUID pins the
// fallback contract for seat resolution: unit-test Handlers that omit
// the pool keep the ADR-005 UUID passthrough (uuid.Parse +
// GetSessionSeatByID). A non-UUID string on the fallback path is
// rejected with ErrSeatIDInvalid; a UUID that matches a fakeSeats row is
// returned as the SessionSeatRow.
func TestBil24_476_ResolveSeatToRow_NilCompatDB_FallbackParsesUUID(t *testing.T) {
	sessionID := uuid.New()
	seatID := uuid.New()
	seats := &fakeSeats{seats: map[uuid.UUID][]gen.SessionSeatRow{
		sessionID: {{ID: seatID, SessionID: sessionID, SeatKey: "A-1", SystemSeatID: 42}},
	}}
	h := newHandler(nil, seats, nil) // no WithCompatDB — compatDB stays nil

	// UUID hit → row returned.
	got, err := h.resolveSeatToRow(context.Background(), seatID.String(), sessionID)
	if err != nil {
		t.Fatalf("resolveSeatToRow(uuid hit, nil compatDB): unexpected error: %v", err)
	}
	if got.ID != seatID || got.SeatKey != "A-1" {
		t.Fatalf("resolveSeatToRow(uuid hit): got %+v", got)
	}

	// Non-UUID → ErrSeatIDInvalid without a DB error surfacing.
	_, err = h.resolveSeatToRow(context.Background(), "not-a-uuid", sessionID)
	if !errors.Is(err, ErrSeatIDInvalid) {
		t.Fatalf("resolveSeatToRow(non-uuid, nil compatDB): want ErrSeatIDInvalid, got %v", err)
	}
}

// TestBil24_476_ResolveCategoryPriceID_NilCompatDB_FallbackAcceptsUUID pins
// the fallback contract: unit-test Handlers that omit the pool keep the
// pre-W1 UUID passthrough so seat_d1_312 / seat_d2_313 / bil24_374 fixtures
// stay green during the step-by-step migration.
func TestBil24_476_ResolveCategoryPriceID_NilCompatDB_FallbackAcceptsUUID(t *testing.T) {
	h := newMinimalHandler() // no WithCompatDB — compatDB stays nil
	want := uuid.New()
	got, err := h.resolveCategoryPriceID(context.Background(), want.String())
	if err != nil {
		t.Fatalf("resolveCategoryPriceID(uuid, nil compatDB): unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("resolveCategoryPriceID(uuid, nil compatDB): got %s, want %s", got, want)
	}
}
