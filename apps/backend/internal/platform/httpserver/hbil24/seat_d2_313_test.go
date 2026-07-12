// seat_d2_313_test.go — contract tests for feature #313 Wave SEAT-D2:
//
//	GET_SCHEMA returns seat coordinates (seatId → x, y) derived from
//	seating_plan_versions.geometry, joinable to GET_SEAT_LIST by
//	seatId (session_seats.id AS STRING, ADR-005).
//
// The tests use in-memory fakes for the schema + admission + seat
// queriers so they exercise the real branching logic without a live
// PostgreSQL pool. The core assertion of the wave — the seatId join
// between GET_SCHEMA and GET_SEAT_LIST — is pinned by
// TestBil24_313_GetSchema_JoinsGetSeatListBySeatID.
package hbil24

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/seating"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fakes
// ─────────────────────────────────────────────────────────────────────────────

// fakeSchema is an in-memory SchemaQuerier: it holds a single
// PublicSessionSchemaRow keyed by session_id plus the session_seats
// slice used by both GetPublicSessionSchema and ListSessionSeats.
type fakeSchema struct {
	rows  map[uuid.UUID]gen.PublicSessionSchemaRow
	seats map[uuid.UUID][]gen.SessionSeatRow
	err   error
}

func (f *fakeSchema) GetPublicSessionSchema(_ context.Context, id uuid.UUID) (gen.PublicSessionSchemaRow, error) {
	if f.err != nil {
		return gen.PublicSessionSchemaRow{}, f.err
	}
	row, ok := f.rows[id]
	if !ok {
		return gen.PublicSessionSchemaRow{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeSchema) ListSessionSeats(_ context.Context, id uuid.UUID) ([]gen.SessionSeatRow, error) {
	return f.seats[id], nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Test helpers
// ─────────────────────────────────────────────────────────────────────────────

// newHandlerWithSchema wires a Handler with the SEAT-D2 schema
// dependency alongside the SEAT-D1 admission + seat queriers so
// GET_SEAT_LIST and GET_SCHEMA can be exercised side-by-side.
func newHandlerWithSchema(admQ AdmissionQuerier, seatQ SeatQuerier, schemaQ SchemaQuerier) *Handler {
	return New(
		nil, // eventQueries
		nil, // tierQueries
		nil, // checkoutQueries
		nil, // ticketQueries
		nil, // barcodeQueries
		admQ,
		seatQ,
		schemaQ,
		ReservationDeps{},
		slog.New(slog.NewJSONHandler(io.Discard, nil)),
	)
}

// canonicalGeometry builds a Canonicalize-shaped geometry payload with
// four seats in one section/row so GET_SCHEMA has non-trivial
// coordinates to project.
func canonicalGeometry() (seating.Geometry, []byte, string) {
	geom := seating.Geometry{
		SchemaVersion: seating.SchemaVersion,
		Canvas:        seating.Canvas{Width: 800, Height: 600},
		Categories: []seating.Category{
			{Index: 1, Name: "VIP", Color: "#ff0000"},
			{Index: 2, Name: "Standard", Color: "#0000ff"},
		},
		Sections: []seating.Section{{
			Key:  "A",
			Name: "Sector A",
			Rows: []seating.Row{{
				Key:  "1",
				Name: "Row 1",
				Seats: []seating.Seat{
					{Key: "A|1|1", Number: "1", X: 100, Y: 200, Radius: 5, CategoryIndex: 1},
					{Key: "A|1|2", Number: "2", X: 110, Y: 200, Radius: 5, CategoryIndex: 1},
					{Key: "A|1|3", Number: "3", X: 120, Y: 200, Radius: 5, CategoryIndex: 2},
					{Key: "A|1|4", Number: "4", X: 130, Y: 200, Radius: 5, CategoryIndex: 2},
				},
			}},
		}},
		StandingZones: []seating.StandingZone{},
		Tables:        []seating.Table{},
	}
	raw, err := seating.CanonicalJSON(geom)
	if err != nil {
		panic(err)
	}
	checksum, err := seating.Checksum(geom)
	if err != nil {
		panic(err)
	}
	return geom, raw, checksum
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_SCHEMA — SEAT-D2 happy path
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_313_GetSchema_ProjectsCoordinatesBySeatID(t *testing.T) {
	sessionID := uuid.New()
	planVersionID := uuid.New()
	seatIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	seats := []gen.SessionSeatRow{
		{ID: seatIDs[0], SessionID: sessionID, SeatKey: "A|1|1", SectorName: "A", RowName: "1", SeatNumber: "1", Status: "available"},
		{ID: seatIDs[1], SessionID: sessionID, SeatKey: "A|1|2", SectorName: "A", RowName: "1", SeatNumber: "2", Status: "available"},
		{ID: seatIDs[2], SessionID: sessionID, SeatKey: "A|1|3", SectorName: "A", RowName: "1", SeatNumber: "3", Status: "held"},
		{ID: seatIDs[3], SessionID: sessionID, SeatKey: "A|1|4", SectorName: "A", RowName: "1", SeatNumber: "4", Status: "sold"},
	}

	_, geomRaw, checksum := canonicalGeometry()

	schema := &fakeSchema{
		rows: map[uuid.UUID]gen.PublicSessionSchemaRow{
			sessionID: {
				ID:                   sessionID,
				EventID:              uuid.New(),
				AdmissionMode:        "assigned_seats",
				SeatingPlanVersionID: &planVersionID,
				SeatStatusVersion:    7,
				Geometry:             json.RawMessage(geomRaw),
				GeometryChecksum:     checksum,
				CapacitySeated:       4,
			},
		},
		seats: map[uuid.UUID][]gen.SessionSeatRow{sessionID: seats},
	}
	h := newHandlerWithSchema(nil, nil, schema)

	resp := postJSON(t, h, `{"command":"GET_SCHEMA","actionEventId":"`+sessionID.String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
	if resp["admissionMode"] != "assigned_seats" {
		t.Errorf("admissionMode: want assigned_seats, got %v", resp["admissionMode"])
	}
	if resp["geometryChecksum"] != checksum {
		t.Errorf("geometryChecksum: want %q, got %v", checksum, resp["geometryChecksum"])
	}
	if int(resp["seatStatusVersion"].(float64)) != 7 {
		t.Errorf("seatStatusVersion: want 7, got %v", resp["seatStatusVersion"])
	}
	canvas, ok := resp["canvas"].(map[string]any)
	if !ok {
		t.Fatalf("canvas missing / wrong type: %T", resp["canvas"])
	}
	if canvas["width"].(float64) != 800 || canvas["height"].(float64) != 600 {
		t.Errorf("canvas: want 800x600, got %v", canvas)
	}

	list, ok := resp["seatSchema"].([]any)
	if !ok {
		t.Fatalf("seatSchema missing / wrong type: %T", resp["seatSchema"])
	}
	if len(list) != 4 {
		t.Fatalf("seatSchema: want 4 entries, got %d", len(list))
	}

	wantX := []float64{100, 110, 120, 130}
	wantCat := []int{1, 1, 2, 2}
	for i, entry := range list {
		m := entry.(map[string]any)
		if m["seatId"] != seatIDs[i].String() {
			t.Errorf("seat[%d].seatId: want %s, got %v", i, seatIDs[i], m["seatId"])
		}
		if m["x"].(float64) != wantX[i] {
			t.Errorf("seat[%d].x: want %v, got %v", i, wantX[i], m["x"])
		}
		if m["y"].(float64) != 200 {
			t.Errorf("seat[%d].y: want 200, got %v", i, m["y"])
		}
		if m["radius"].(float64) != 5 {
			t.Errorf("seat[%d].radius: want 5, got %v", i, m["radius"])
		}
		if int(m["categoryIndex"].(float64)) != wantCat[i] {
			t.Errorf("seat[%d].categoryIndex: want %d, got %v", i, wantCat[i], m["categoryIndex"])
		}
	}
}

// TestBil24_313_GetSchema_JoinsGetSeatListBySeatID is the SEAT-D2 core
// contract assertion: GET_SCHEMA and GET_SEAT_LIST responses can be
// zipped by seatId so that a caller has (seatId, x, y) from GET_SCHEMA
// and (seatId, sector, row, number, status) from GET_SEAT_LIST for the
// same seat. The seatId serialisation format MUST match exactly — this
// is the wire contract mirroring the legacy Bil24 API split.
func TestBil24_313_GetSchema_JoinsGetSeatListBySeatID(t *testing.T) {
	sessionID := uuid.New()
	planVersionID := uuid.New()
	seatIDs := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}
	seats := []gen.SessionSeatRow{
		{ID: seatIDs[0], SessionID: sessionID, SeatKey: "A|1|1", SectorName: "A", RowName: "1", SeatNumber: "1", Status: "available"},
		{ID: seatIDs[1], SessionID: sessionID, SeatKey: "A|1|2", SectorName: "A", RowName: "1", SeatNumber: "2", Status: "held"},
		{ID: seatIDs[2], SessionID: sessionID, SeatKey: "A|1|3", SectorName: "A", RowName: "1", SeatNumber: "3", Status: "sold"},
		{ID: seatIDs[3], SessionID: sessionID, SeatKey: "A|1|4", SectorName: "A", RowName: "1", SeatNumber: "4", Status: "blocked"},
	}
	_, geomRaw, checksum := canonicalGeometry()

	adm := &fakeAdmission{sessions: map[uuid.UUID]gen.SessionAdmissionRow{
		sessionID: {ID: sessionID, AdmissionMode: "assigned_seats", CapacityTotal: 4},
	}}
	sf := &fakeSeats{seats: map[uuid.UUID][]gen.SessionSeatRow{sessionID: seats}}
	schema := &fakeSchema{
		rows: map[uuid.UUID]gen.PublicSessionSchemaRow{
			sessionID: {
				ID:                   sessionID,
				AdmissionMode:        "assigned_seats",
				SeatingPlanVersionID: &planVersionID,
				Geometry:             json.RawMessage(geomRaw),
				GeometryChecksum:     checksum,
				CapacitySeated:       4,
			},
		},
		seats: map[uuid.UUID][]gen.SessionSeatRow{sessionID: seats},
	}
	h := newHandlerWithSchema(adm, sf, schema)

	seatListResp := postJSON(t, h,
		`{"command":"GET_SEAT_LIST","actionEventId":"`+sessionID.String()+`"}`)
	if rc := mustResultCode(t, seatListResp); rc != ResultCodeOK {
		t.Fatalf("GET_SEAT_LIST: want %d, got %d; body: %v", ResultCodeOK, rc, seatListResp)
	}
	seatListEntries := seatListResp["seatList"].([]any)

	schemaResp := postJSON(t, h,
		`{"command":"GET_SCHEMA","actionEventId":"`+sessionID.String()+`"}`)
	if rc := mustResultCode(t, schemaResp); rc != ResultCodeOK {
		t.Fatalf("GET_SCHEMA: want %d, got %d; body: %v", ResultCodeOK, rc, schemaResp)
	}
	schemaEntries := schemaResp["seatSchema"].([]any)

	if len(seatListEntries) != len(schemaEntries) {
		t.Fatalf("cardinality mismatch: GET_SEAT_LIST=%d, GET_SCHEMA=%d",
			len(seatListEntries), len(schemaEntries))
	}

	// Build seatId → coord from GET_SCHEMA and seatId → status from
	// GET_SEAT_LIST; every seatId MUST appear in both maps.
	coords := make(map[string][2]float64, len(schemaEntries))
	for _, e := range schemaEntries {
		m := e.(map[string]any)
		coords[m["seatId"].(string)] = [2]float64{m["x"].(float64), m["y"].(float64)}
	}
	statuses := make(map[string]int, len(seatListEntries))
	for _, e := range seatListEntries {
		m := e.(map[string]any)
		statuses[m["seatId"].(string)] = int(m["status"].(float64))
	}

	for _, id := range seatIDs {
		key := id.String()
		coord, hasCoord := coords[key]
		if !hasCoord {
			t.Errorf("seatId %s missing from GET_SCHEMA", key)
			continue
		}
		status, hasStatus := statuses[key]
		if !hasStatus {
			t.Errorf("seatId %s missing from GET_SEAT_LIST", key)
			continue
		}
		if coord[0] < 100 || coord[0] > 130 {
			t.Errorf("seatId %s coord.x out of range: %v", key, coord[0])
		}
		if status < 0 || status > 4 {
			t.Errorf("seatId %s status out of BSS range: %d", key, status)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// GET_SCHEMA — negative paths
// ─────────────────────────────────────────────────────────────────────────────

func TestBil24_313_GetSchema_NoSchemaQ_ServiceUnavailable(t *testing.T) {
	h := newHandlerWithSchema(nil, nil, nil)
	sid := uuid.New().String()
	resp := postJSON(t, h, `{"command":"GET_SCHEMA","actionEventId":"`+sid+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInternalError {
		t.Errorf("want %d (schema service unavailable), got %d", ResultCodeInternalError, rc)
	}
}

func TestBil24_313_GetSchema_InvalidSessionID_InvalidRequest(t *testing.T) {
	h := newHandlerWithSchema(nil, nil, &fakeSchema{})
	resp := postJSON(t, h, `{"command":"GET_SCHEMA","actionEventId":"NOT_A_UUID"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInvalidRequest {
		t.Errorf("want %d, got %d", ResultCodeInvalidRequest, rc)
	}
}

func TestBil24_313_GetSchema_MissingSession_NotFound(t *testing.T) {
	h := newHandlerWithSchema(nil, nil, &fakeSchema{
		rows:  map[uuid.UUID]gen.PublicSessionSchemaRow{},
		seats: map[uuid.UUID][]gen.SessionSeatRow{},
	})
	resp := postJSON(t, h, `{"command":"GET_SCHEMA","actionEventId":"`+uuid.New().String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeNotFound {
		t.Errorf("want %d, got %d", ResultCodeNotFound, rc)
	}
}

func TestBil24_313_GetSchema_MalformedGeometry_InternalError(t *testing.T) {
	sessionID := uuid.New()
	h := newHandlerWithSchema(nil, nil, &fakeSchema{
		rows: map[uuid.UUID]gen.PublicSessionSchemaRow{
			sessionID: {
				ID:               sessionID,
				AdmissionMode:    "assigned_seats",
				Geometry:         json.RawMessage([]byte("not-json")),
				GeometryChecksum: "deadbeef",
			},
		},
		seats: map[uuid.UUID][]gen.SessionSeatRow{sessionID: {}},
	})
	resp := postJSON(t, h, `{"command":"GET_SCHEMA","actionEventId":"`+sessionID.String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeInternalError {
		t.Errorf("want %d, got %d", ResultCodeInternalError, rc)
	}
}

// TestBil24_313_GetSchema_SeatMissingInGeometry_ZeroFallback pins the
// mid-rebind defence: if session_seats contains a seat_key that the
// current geometry does not (because the plan version was swapped and
// the seats table is still catching up), the response still emits an
// entry with zero coordinates so the response cardinality matches the
// session's seat count.
func TestBil24_313_GetSchema_SeatMissingInGeometry_ZeroFallback(t *testing.T) {
	sessionID := uuid.New()
	seatID := uuid.New()
	_, geomRaw, checksum := canonicalGeometry()
	h := newHandlerWithSchema(nil, nil, &fakeSchema{
		rows: map[uuid.UUID]gen.PublicSessionSchemaRow{
			sessionID: {
				ID:               sessionID,
				AdmissionMode:    "assigned_seats",
				Geometry:         json.RawMessage(geomRaw),
				GeometryChecksum: checksum,
			},
		},
		seats: map[uuid.UUID][]gen.SessionSeatRow{sessionID: {
			{ID: seatID, SessionID: sessionID, SeatKey: "Z|99|999", Status: "available"},
		}},
	})
	resp := postJSON(t, h, `{"command":"GET_SCHEMA","actionEventId":"`+sessionID.String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d; body: %v", ResultCodeOK, rc, resp)
	}
	list := resp["seatSchema"].([]any)
	if len(list) != 1 {
		t.Fatalf("seatSchema: want 1 entry, got %d", len(list))
	}
	entry := list[0].(map[string]any)
	if entry["seatId"] != seatID.String() {
		t.Errorf("seatId: want %s, got %v", seatID, entry["seatId"])
	}
	if entry["x"].(float64) != 0 || entry["y"].(float64) != 0 {
		t.Errorf("orphan seat should fall back to 0,0 coords; got x=%v y=%v",
			entry["x"], entry["y"])
	}
}

// TestBil24_313_GetSchema_SeatKeyFallback ensures that geometries which
// predate the seat.Key field (hand-authored payloads) still resolve
// coordinates via the (section, row, number) tuple.
func TestBil24_313_GetSchema_SeatKeyFallback(t *testing.T) {
	sessionID := uuid.New()
	seatID := uuid.New()

	// Geometry with an empty seat.Key — coordinate lookup must
	// reconstruct the key from (section, row, number).
	geom := seating.Geometry{
		SchemaVersion: seating.SchemaVersion,
		Canvas:        seating.Canvas{Width: 100, Height: 100},
		Categories:    []seating.Category{{Index: 1, Name: "Std", Color: "#000000"}},
		Sections: []seating.Section{{
			Key: "S", Name: "S",
			Rows: []seating.Row{{
				Key: "R", Name: "R",
				Seats: []seating.Seat{
					{Key: "", Number: "7", X: 42, Y: 24, Radius: 3, CategoryIndex: 1},
				},
			}},
		}},
		StandingZones: []seating.StandingZone{},
		Tables:        []seating.Table{},
	}
	raw, err := json.Marshal(geom)
	if err != nil {
		t.Fatalf("marshal geometry: %v", err)
	}

	h := newHandlerWithSchema(nil, nil, &fakeSchema{
		rows: map[uuid.UUID]gen.PublicSessionSchemaRow{
			sessionID: {
				ID:               sessionID,
				AdmissionMode:    "assigned_seats",
				Geometry:         json.RawMessage(raw),
				GeometryChecksum: "deadbeef",
			},
		},
		seats: map[uuid.UUID][]gen.SessionSeatRow{sessionID: {
			{ID: seatID, SessionID: sessionID, SeatKey: "S|R|7", Status: "available"},
		}},
	})
	resp := postJSON(t, h, `{"command":"GET_SCHEMA","actionEventId":"`+sessionID.String()+`"}`)
	if rc := mustResultCode(t, resp); rc != ResultCodeOK {
		t.Fatalf("want %d, got %d", ResultCodeOK, rc)
	}
	entry := resp["seatSchema"].([]any)[0].(map[string]any)
	if entry["x"].(float64) != 42 || entry["y"].(float64) != 24 {
		t.Errorf("fallback lookup failed: got x=%v y=%v; want 42,24",
			entry["x"], entry["y"])
	}
}

// TestBil24_313_BuildSeatKeyCoordinateIndex_UsesKeyWhenPresent is a pure
// unit-level check of the geometry-walk helper.
func TestBil24_313_BuildSeatKeyCoordinateIndex_UsesKeyWhenPresent(t *testing.T) {
	g := seating.Geometry{
		Sections: []seating.Section{{
			Key: "A",
			Rows: []seating.Row{{
				Key: "1",
				Seats: []seating.Seat{
					{Key: "A|1|1", Number: "1", X: 10, Y: 20, Radius: 2, CategoryIndex: 1},
					{Key: "", Number: "2", X: 30, Y: 40, Radius: 2, CategoryIndex: 2},
				},
			}},
		}},
	}
	idx := buildSeatKeyCoordinateIndex(g)
	if c, ok := idx["A|1|1"]; !ok || c.X != 10 || c.Y != 20 || c.CategoryIndex != 1 {
		t.Errorf("A|1|1 lookup: %+v ok=%v", c, ok)
	}
	if c, ok := idx["A|1|2"]; !ok || c.X != 30 || c.Y != 40 || c.CategoryIndex != 2 {
		t.Errorf("A|1|2 (fallback key) lookup: %+v ok=%v", c, ok)
	}
}
