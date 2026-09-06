package horders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Fake gen.DBTX — dispatches on SQL substring, modeled on
// hcustomers/handler_test.go's fakeDB pattern.
// ─────────────────────────────────────────────────────────────────────────────

type fakeRow struct {
	vals []any
	err  error
}

func (r *fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.vals) {
		return fmt.Errorf("fakeRow.Scan: got %d dest, want %d", len(dest), len(r.vals))
	}
	for i, v := range r.vals {
		if err := assign(dest[i], v); err != nil {
			return err
		}
	}
	return nil
}

// assign copies v into the pointer dest using reflection so callers can
// freely mix bare values with already-boxed nullable pointers (used to model
// a non-NULL nullable column) and typed-nil pointers (used to model SQL
// NULL).
func assign(dest any, v any) error {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		return nil
	}
	dv := reflect.ValueOf(dest).Elem()
	switch {
	case rv.Type() == dv.Type():
		dv.Set(rv)
	case dv.Kind() == reflect.Pointer && dv.Type().Elem() == rv.Type():
		boxed := reflect.New(rv.Type())
		boxed.Elem().Set(rv)
		dv.Set(boxed)
	default:
		return fmt.Errorf("assign: cannot assign %T into %T", v, dest)
	}
	return nil
}

// fakeRows implements pgx.Rows over a fixed slice of pre-built value rows.
type fakeRows struct {
	data [][]any
	pos  int
}

func (f *fakeRows) Next() bool {
	f.pos++
	return f.pos <= len(f.data)
}
func (f *fakeRows) Scan(dest ...any) error {
	row := &fakeRow{vals: f.data[f.pos-1]}
	return row.Scan(dest...)
}
func (f *fakeRows) Err() error                                   { return nil }
func (f *fakeRows) Close()                                       {}
func (f *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (f *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (f *fakeRows) Values() ([]any, error)                       { return f.data[f.pos-1], nil }
func (f *fakeRows) RawValues() [][]byte                          { return nil }
func (f *fakeRows) Conn() *pgx.Conn                              { return nil }

// fakeDB implements gen.DBTX. orderVals models the single "real" order row
// (keyed by realOrgID) so GetOrderByID's org_id predicate can be simulated:
// a query whose org_id argument doesn't match realOrgID returns
// pgx.ErrNoRows, proving tenant isolation the same way the real WHERE clause
// does.
type fakeDB struct {
	orderFound bool
	orderVals  []any
	realOrgID  uuid.UUID

	itemRows  [][]any
	eventRows [][]any

	ticketVals map[uuid.UUID][]any

	// updateStatusVals, when set, is returned by UPDATE orders (used by
	// ordering.Cancel via UpdateOrderStatus).
	updateStatusVals []any
	updateStatusErr  error

	insertEventErr error
}

func (f *fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("fakeDB: unexpected Exec")
}

func (f *fakeDB) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "UPDATE orders"):
		if f.updateStatusErr != nil {
			return &fakeRow{err: f.updateStatusErr}
		}
		if f.updateStatusVals == nil {
			return &fakeRow{err: pgx.ErrNoRows}
		}
		return &fakeRow{vals: f.updateStatusVals}
	case strings.Contains(sql, "INSERT INTO order_events"):
		if f.insertEventErr != nil {
			return &fakeRow{err: f.insertEventErr}
		}
		orderID, _ := args[0].(uuid.UUID)
		return &fakeRow{vals: []any{uuid.New(), orderID, args[1], args[2], json.RawMessage("{}"), time.Now().UTC()}}
	case strings.Contains(sql, "FROM   orders"):
		if !f.orderFound {
			return &fakeRow{err: pgx.ErrNoRows}
		}
		// Simulate the "AND org_id = $2" predicate: args[1] is orgID.
		orgID, _ := args[1].(uuid.UUID)
		if orgID != f.realOrgID {
			return &fakeRow{err: pgx.ErrNoRows}
		}
		return &fakeRow{vals: f.orderVals}
	case strings.Contains(sql, "FROM   tickets"):
		id, _ := args[0].(uuid.UUID)
		vals, ok := f.ticketVals[id]
		if !ok {
			return &fakeRow{err: pgx.ErrNoRows}
		}
		return &fakeRow{vals: vals}
	}
	return &fakeRow{err: fmt.Errorf("fakeDB.QueryRow: unexpected SQL %q", sql)}
}

func (f *fakeDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	switch {
	case strings.Contains(sql, "FROM   orders"):
		if !f.orderFound {
			return &fakeRows{}, nil
		}
		return &fakeRows{data: [][]any{f.orderVals}}, nil
	case strings.Contains(sql, "FROM   order_items"):
		return &fakeRows{data: f.itemRows}, nil
	case strings.Contains(sql, "FROM   order_events"):
		return &fakeRows{data: f.eventRows}, nil
	}
	return nil, fmt.Errorf("fakeDB.Query: unexpected SQL %q", sql)
}

// newOrderVals builds an OrderRow value slice in scanOrderRow's exact column
// order (orders.sql.go).
func newOrderVals(
	id uuid.UUID, systemID int64, orgID, channelID, eventID, sessionID uuid.UUID,
	checkoutID, reservationID uuid.UUID,
	status, currency string, total int64, created time.Time,
) []any {
	return []any{
		id, systemID, orgID, channelID, eventID, sessionID, (*uuid.UUID)(nil),
		checkoutID, reservationID, (*string)(nil), "web", status, currency,
		int64(0), int64(0), int64(0), total, int32(0), (*uuid.UUID)(nil),
		(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
		(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil),
		json.RawMessage("{}"), created, created,
	}
}

// newOrderItemVals builds an OrderItemRow value slice in scanOrderItemRow's
// exact column order.
func newOrderItemVals(id, orderID uuid.UUID, ordinal int32, tierID uuid.UUID, ticketID *uuid.UUID, unitPrice int64) []any {
	return []any{
		id, orderID, ordinal, "seat", tierID, (*uuid.UUID)(nil), ticketID,
		unitPrice, int64(0), int64(0), unitPrice,
	}
}

// newOrderEventVals builds an OrderEventRow value slice in scanOrderEventRow's
// exact column order.
func newOrderEventVals(id, orderID uuid.UUID, eventType, actor string, created time.Time) []any {
	return []any{id, orderID, eventType, actor, json.RawMessage(`{}`), created}
}

// newTicketVals builds a TicketRow value slice in scanTicketRow's exact
// column order (tickets.sql.go).
func newTicketVals(id, checkoutID, sessionID uuid.UUID, status string, issuedAt time.Time) []any {
	return []any{
		id, checkoutID, sessionID, (*uuid.UUID)(nil), (*string)(nil),
		status, issuedAt, issuedAt, issuedAt,
		(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), int32(0),
		(*time.Time)(nil), (*string)(nil), (*string)(nil), (*uuid.UUID)(nil),
		(*time.Time)(nil), (*int64)(nil), false, (*string)(nil), int64(3000000001),
	}
}

func testHandler(db *fakeDB) *Handler {
	return New(gen.New(db), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// failingPool is a TxStarter whose BeginTx always fails. HandleCancel treats
// any hcheckout.ReleaseHold failure that is not ErrHoldNotFound or
// *NotReleasableError as merely log-worthy — it never aborts the 200
// response — so a pool that can't even start a transaction is a valid,
// minimal stand-in for "hold release didn't happen" in a happy-path cancel
// test.
type failingPool struct{}

func (failingPool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errors.New("failingPool: BeginTx always fails")
}

func testCancelHandler(db *fakeDB) *Handler {
	return New(gen.New(db), failingPool{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func chiRequest(method, target string, params map[string]string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleGet
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleGet_CrossOrgOrder_Returns404(t *testing.T) {
	realOrgID := uuid.New()
	otherOrgID := uuid.New()
	orderID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	db := &fakeDB{
		orderFound: true,
		realOrgID:  realOrgID,
		orderVals: newOrderVals(orderID, 2000000001, realOrgID, uuid.New(), uuid.New(), uuid.New(),
			uuid.New(), uuid.New(), "pending_payment", "eur", 5000, now),
	}
	h := testHandler(db)

	// The request's org_id path param does NOT match the order's real org_id.
	r := chiRequest(http.MethodGet, "/v1/organizations/"+otherOrgID.String()+"/orders/"+orderID.String(),
		map[string]string{"org_id": otherOrgID.String(), "id": orderID.String()})
	w := httptest.NewRecorder()
	h.HandleGet(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-org order, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleGet_NotFound(t *testing.T) {
	orgID := uuid.New()
	orderID := uuid.New()
	db := &fakeDB{orderFound: false, realOrgID: orgID}
	h := testHandler(db)

	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/orders/"+orderID.String(),
		map[string]string{"org_id": orgID.String(), "id": orderID.String()})
	w := httptest.NewRecorder()
	h.HandleGet(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleGet_FullAssembly(t *testing.T) {
	orgID := uuid.New()
	orderID := uuid.New()
	channelID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	checkoutID := uuid.New()
	reservationID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	itemWithTicketID := uuid.New()
	itemNoTicketID := uuid.New()
	ticketID := uuid.New()
	tierID := uuid.New()

	db := &fakeDB{
		orderFound: true,
		realOrgID:  orgID,
		orderVals: newOrderVals(orderID, 2000000001, orgID, channelID, eventID, sessionID,
			checkoutID, reservationID, "paid", "eur", 5000, now),
		itemRows: [][]any{
			newOrderItemVals(itemWithTicketID, orderID, 0, tierID, &ticketID, 2500),
			newOrderItemVals(itemNoTicketID, orderID, 1, tierID, nil, 2500),
		},
		eventRows: [][]any{
			newOrderEventVals(uuid.New(), orderID, "created", "system", now),
			newOrderEventVals(uuid.New(), orderID, "paid", "system", now.Add(time.Minute)),
		},
		ticketVals: map[uuid.UUID][]any{
			ticketID: newTicketVals(ticketID, checkoutID, sessionID, "active", now),
		},
	}
	h := testHandler(db)

	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/orders/"+orderID.String(),
		map[string]string{"org_id": orgID.String(), "id": orderID.String()})
	w := httptest.NewRecorder()
	h.HandleGet(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON body: %v", err)
	}

	if body["id"] != orderID.String() {
		t.Errorf("id = %v, want %v", body["id"], orderID.String())
	}
	if body["status"] != "paid" {
		t.Errorf("status = %v, want paid", body["status"])
	}
	if body["org_id"] != orgID.String() {
		t.Errorf("org_id = %v, want %v", body["org_id"], orgID.String())
	}

	items, ok := body["items"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("expected 2 items, got %#v", body["items"])
	}
	var itemWithTicket, itemWithoutTicket map[string]any
	for _, raw := range items {
		it := raw.(map[string]any)
		if it["ticket_id"] != nil {
			itemWithTicket = it
		} else {
			itemWithoutTicket = it
		}
	}
	if itemWithTicket == nil || itemWithoutTicket == nil {
		t.Fatalf("expected one item with ticket_id and one without, got %#v", items)
	}
	if itemWithTicket["ticket_id"] != ticketID.String() {
		t.Errorf("ticket_id = %v, want %v", itemWithTicket["ticket_id"], ticketID.String())
	}

	tickets, ok := body["tickets"].([]any)
	if !ok || len(tickets) != 1 {
		t.Fatalf("expected 1 ticket, got %#v", body["tickets"])
	}
	ticket := tickets[0].(map[string]any)
	if ticket["id"] != ticketID.String() {
		t.Errorf("ticket id = %v, want %v", ticket["id"], ticketID.String())
	}
	if ticket["status"] != "active" {
		t.Errorf("ticket status = %v, want active", ticket["status"])
	}

	events, ok := body["events"].([]any)
	if !ok || len(events) != 2 {
		t.Fatalf("expected 2 events, got %#v", body["events"])
	}
	firstEvent := events[0].(map[string]any)
	if firstEvent["type"] != "created" {
		t.Errorf("first event type = %v, want created", firstEvent["type"])
	}
}

func TestHandleGet_DependencyUnavailable_WhenNilQueries(t *testing.T) {
	h := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	orgID := uuid.New()
	orderID := uuid.New()
	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/orders/"+orderID.String(),
		map[string]string{"org_id": orgID.String(), "id": orderID.String()})
	w := httptest.NewRecorder()
	h.HandleGet(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleList
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleList_ReturnsSummaries(t *testing.T) {
	orgID := uuid.New()
	orderID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	db := &fakeDB{
		orderFound: true,
		realOrgID:  orgID,
		orderVals: newOrderVals(orderID, 2000000002, orgID, uuid.New(), uuid.New(), uuid.New(),
			uuid.New(), uuid.New(), "pending_payment", "eur", 3000, now),
	}
	h := testHandler(db)

	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/orders",
		map[string]string{"org_id": orgID.String()})
	w := httptest.NewRecorder()
	h.HandleList(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON body: %v", err)
	}
	orders, ok := body["orders"].([]any)
	if !ok || len(orders) != 1 {
		t.Fatalf("expected 1 order, got %#v", body["orders"])
	}
	first := orders[0].(map[string]any)
	if first["id"] != orderID.String() {
		t.Errorf("id = %v, want %v", first["id"], orderID.String())
	}
	if body["limit"] != float64(defaultLimit) {
		t.Errorf("limit = %v, want %v", body["limit"], defaultLimit)
	}
	if body["offset"] != float64(0) {
		t.Errorf("offset = %v, want 0", body["offset"])
	}
}

func TestHandleList_RejectsInvalidPagination(t *testing.T) {
	orgID := uuid.New()
	db := &fakeDB{realOrgID: orgID}
	h := testHandler(db)

	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/orders?limit=0",
		map[string]string{"org_id": orgID.String()})
	w := httptest.NewRecorder()
	h.HandleList(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleList_DependencyUnavailable_WhenNilQueries(t *testing.T) {
	h := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	orgID := uuid.New()
	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/orders",
		map[string]string{"org_id": orgID.String()})
	w := httptest.NewRecorder()
	h.HandleList(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleCancel
// ─────────────────────────────────────────────────────────────────────────────

func TestHandleCancel_Success(t *testing.T) {
	orgID := uuid.New()
	orderID := uuid.New()
	reservationID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	pendingVals := newOrderVals(orderID, 2000000003, orgID, uuid.New(), uuid.New(), uuid.New(),
		uuid.New(), reservationID, "pending_payment", "eur", 4000, now)
	cancelledVals := newOrderVals(orderID, 2000000003, orgID, uuid.New(), uuid.New(), uuid.New(),
		uuid.New(), reservationID, "cancelled", "eur", 4000, now)

	db := &fakeDB{
		orderFound:       true,
		realOrgID:        orgID,
		orderVals:        pendingVals,
		updateStatusVals: cancelledVals,
	}
	h := testCancelHandler(db)

	r := chiRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/orders/"+orderID.String()+"/cancel",
		map[string]string{"org_id": orgID.String(), "id": orderID.String()})
	w := httptest.NewRecorder()
	h.HandleCancel(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON body: %v", err)
	}
	if body["status"] != "cancelled" {
		t.Errorf("status = %v, want cancelled", body["status"])
	}
	if body["id"] != orderID.String() {
		t.Errorf("id = %v, want %v", body["id"], orderID.String())
	}
}

func TestHandleCancel_CrossOrgOrder_Returns404(t *testing.T) {
	realOrgID := uuid.New()
	otherOrgID := uuid.New()
	orderID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	db := &fakeDB{
		orderFound: true,
		realOrgID:  realOrgID,
		orderVals: newOrderVals(orderID, 2000000004, realOrgID, uuid.New(), uuid.New(), uuid.New(),
			uuid.New(), uuid.New(), "pending_payment", "eur", 4000, now),
	}
	h := testCancelHandler(db)

	r := chiRequest(http.MethodPost, "/v1/organizations/"+otherOrgID.String()+"/orders/"+orderID.String()+"/cancel",
		map[string]string{"org_id": otherOrgID.String(), "id": orderID.String()})
	w := httptest.NewRecorder()
	h.HandleCancel(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-org order, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCancel_InvalidTransition_Returns409(t *testing.T) {
	orgID := uuid.New()
	orderID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)

	db := &fakeDB{
		orderFound: true,
		realOrgID:  orgID,
		orderVals: newOrderVals(orderID, 2000000005, orgID, uuid.New(), uuid.New(), uuid.New(),
			uuid.New(), uuid.New(), "paid", "eur", 4000, now),
	}
	h := testCancelHandler(db)

	r := chiRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/orders/"+orderID.String()+"/cancel",
		map[string]string{"org_id": orgID.String(), "id": orderID.String()})
	w := httptest.NewRecorder()
	h.HandleCancel(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleCancel_DependencyUnavailable_WhenNilQueries(t *testing.T) {
	h := New(nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	orgID := uuid.New()
	orderID := uuid.New()
	r := chiRequest(http.MethodPost, "/v1/organizations/"+orgID.String()+"/orders/"+orderID.String()+"/cancel",
		map[string]string{"org_id": orgID.String(), "id": orderID.String()})
	w := httptest.NewRecorder()
	h.HandleCancel(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
