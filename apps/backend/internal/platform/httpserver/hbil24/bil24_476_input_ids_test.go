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
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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
