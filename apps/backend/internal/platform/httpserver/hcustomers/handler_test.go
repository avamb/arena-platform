package hcustomers

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
// Pure-function tests (no DBTX involved)
// ─────────────────────────────────────────────────────────────────────────────

func TestMaskStrongIdentity(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		value    string
		verified bool
		want     string
	}{
		{"verified email returned in full", "email", "alice@example.com", true, "alice@example.com"},
		{"unverified email masked", "email", "alice@example.com", false, "a***@example.com"},
		{"unverified email with tiny local part", "email", "a@example.com", false, "***"},
		{"verified phone returned in full", "phone", "+15551234567", true, "+15551234567"},
		{"unverified phone masked to last 4", "phone", "+15551234567", false, "***4567"},
		{"unverified short phone fully masked", "phone", "123", false, "***"},
		{"verified telegram returned in full", "telegram", "@alice", true, "@alice"},
		{"unverified telegram masked generic", "telegram", "@alice", false, "@***e"},
		{"unverified telegram tiny value fully masked", "telegram", "ab", false, "***"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskStrongIdentity(tc.kind, tc.value, tc.verified)
			if got != tc.want {
				t.Errorf("maskStrongIdentity(%q, %q, %v) = %q, want %q", tc.kind, tc.value, tc.verified, got, tc.want)
			}
		})
	}
}

func TestIsStrongKind(t *testing.T) {
	cases := []struct {
		kind string
		want bool
	}{
		{"email", true},
		{"phone", true},
		{"telegram", true},
		{"device", false},
		{"wc_customer", false},
		{"bil24_user", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isStrongKind(tc.kind); got != tc.want {
			t.Errorf("isStrongKind(%q) = %v, want %v", tc.kind, got, tc.want)
		}
	}
}

func TestParsePagination(t *testing.T) {
	t.Run("defaults when absent", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/x", nil)
		w := httptest.NewRecorder()
		limit, offset, ok := parsePagination(w, r)
		if !ok || limit != defaultLimit || offset != 0 {
			t.Fatalf("got limit=%d offset=%d ok=%v, want %d/0/true", limit, offset, ok, defaultLimit)
		}
	})

	t.Run("valid explicit values", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/x?limit=10&offset=5", nil)
		w := httptest.NewRecorder()
		limit, offset, ok := parsePagination(w, r)
		if !ok || limit != 10 || offset != 5 {
			t.Fatalf("got limit=%d offset=%d ok=%v, want 10/5/true", limit, offset, ok)
		}
	})

	invalidCases := []struct {
		name  string
		query string
	}{
		{"limit not a number", "limit=abc"},
		{"limit zero", "limit=0"},
		{"limit negative", "limit=-1"},
		{"limit above max", "limit=201"},
		{"offset not a number", "offset=abc"},
		{"offset negative", "offset=-1"},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/x?"+tc.query, nil)
			w := httptest.NewRecorder()
			_, _, ok := parsePagination(w, r)
			if ok {
				t.Fatalf("expected ok=false for query %q", tc.query)
			}
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", w.Code)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Fake gen.DBTX — dispatches on SQL substring, modeled on
// hscanner/human_code_test.go's validateFakeDB/fakeRow pattern, extended
// with a fake pgx.Rows for the :many queries HandleGet relies on.
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
// freely mix bare values (uuid.UUID, string, time.Time, ...) with already-
// boxed nullable pointers (*time.Time, *string, *uuid.UUID — used to model
// a non-NULL nullable column) and typed-nil pointers (used to model SQL
// NULL). A typed-nil pointer or a nil interface leaves dest untouched,
// matching pgx's behavior of leaving a nullable destination at its zero
// value when the column is NULL.
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

type fakeDB struct {
	// customer_org_links lookup
	orgLinkFound bool
	orgLinkVals  []any

	customerVals []any

	identityRows [][]any
	orderRows    [][]any
	attrRows     [][]any
	consentRows  [][]any
}

func (f *fakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("fakeDB: unexpected Exec")
}

func (f *fakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "FROM   customer_org_links"):
		if !f.orgLinkFound {
			return &fakeRow{err: pgx.ErrNoRows}
		}
		return &fakeRow{vals: f.orgLinkVals}
	case strings.Contains(sql, "FROM   customers"):
		if f.customerVals == nil {
			return &fakeRow{err: pgx.ErrNoRows}
		}
		return &fakeRow{vals: f.customerVals}
	}
	return &fakeRow{err: fmt.Errorf("fakeDB.QueryRow: unexpected SQL %q", sql)}
}

func (f *fakeDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	switch {
	case strings.Contains(sql, "FROM   customer_identities"):
		return &fakeRows{data: f.identityRows}, nil
	case strings.Contains(sql, "FROM   orders"):
		return &fakeRows{data: f.orderRows}, nil
	case strings.Contains(sql, "FROM   customer_attributes"):
		return &fakeRows{data: f.attrRows}, nil
	case strings.Contains(sql, "FROM   customer_consents"):
		return &fakeRows{data: f.consentRows}, nil
	}
	return nil, fmt.Errorf("fakeDB.Query: unexpected SQL %q", sql)
}

func newCustomerVals(id uuid.UUID, systemID int64, displayName, locale *string, created, updated time.Time) []any {
	return []any{id, systemID, displayName, locale, (*uuid.UUID)(nil), (*time.Time)(nil), created, updated}
}

func newIdentityVals(id, customerID uuid.UUID, kind, value string, verifiedAt *time.Time, seen time.Time, source string) []any {
	return []any{id, customerID, kind, value, (*uuid.UUID)(nil), verifiedAt, seen, seen, source}
}

func newOrderVals(id uuid.UUID, systemID int64, orgID, channelID, eventID, sessionID uuid.UUID, checkoutID, reservationID uuid.UUID, status, currency string, total int64, created time.Time) []any {
	return []any{
		id, systemID, orgID, channelID, eventID, sessionID, (*uuid.UUID)(nil),
		checkoutID, reservationID, (*string)(nil), "web", status, currency,
		int64(0), int64(0), int64(0), total, int32(0), (*uuid.UUID)(nil),
		(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
		(*time.Time)(nil), (*time.Time)(nil), (*time.Time)(nil), json.RawMessage("{}"), created, created,
	}
}

func newAttrVals(id, customerID uuid.UUID, orgID *uuid.UUID, key string, value []byte, source string, created time.Time) []any {
	return []any{id, customerID, orgID, key, value, source, (*time.Time)(nil), created}
}

func newConsentVals(customerID, orgID uuid.UUID, kind string, given time.Time, withdrawn *time.Time, source string) []any {
	return []any{customerID, orgID, kind, given, withdrawn, source}
}

func testHandler(db *fakeDB) *Handler {
	return New(gen.New(db), slog.New(slog.NewTextHandler(io.Discard, nil)))
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

func TestHandleGet_NotFound_WhenNoOrgLink(t *testing.T) {
	orgID := uuid.New()
	custID := uuid.New()
	db := &fakeDB{orgLinkFound: false}
	h := testHandler(db)

	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/customers/"+custID.String(),
		map[string]string{"org_id": orgID.String(), "id": custID.String()})
	w := httptest.NewRecorder()
	h.HandleGet(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON body: %v", err)
	}
}

func TestHandleGet_FullCardAssembly(t *testing.T) {
	orgID := uuid.New()
	custID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	displayName := "Alice Example"

	verifiedAt := now
	emailID := uuid.New()
	phoneID := uuid.New()
	deviceID := uuid.New()

	orderID := uuid.New()
	channelID := uuid.New()
	eventID := uuid.New()
	sessionID := uuid.New()
	checkoutID := uuid.New()
	reservationID := uuid.New()

	attrOrgID := orgID

	db := &fakeDB{
		orgLinkFound: true,
		orgLinkVals:  []any{custID, orgID, (*time.Time)(nil), (*time.Time)(nil), int32(1), int32(2), "web", []byte("{}")},
		customerVals: newCustomerVals(custID, 1000000001, &displayName, nil, now, now),
		identityRows: [][]any{
			newIdentityVals(emailID, custID, "email", "alice@example.com", &verifiedAt, now, "checkout"),
			newIdentityVals(phoneID, custID, "phone", "+15551234567", nil, now, "checkout"),
			newIdentityVals(deviceID, custID, "device", "device-abc-123", nil, now, "widget"),
		},
		orderRows: [][]any{
			newOrderVals(orderID, 2000000001, orgID, channelID, eventID, sessionID, checkoutID, reservationID, "paid", "eur", 5000, now),
		},
		attrRows: [][]any{
			newAttrVals(uuid.New(), custID, &attrOrgID, "vip", []byte(`true`), "manual", now),
			newAttrVals(uuid.New(), custID, nil, "language", []byte(`"en"`), "import", now),
		},
		consentRows: [][]any{
			newConsentVals(custID, orgID, "terms", now, nil, "checkout"),
		},
	}
	h := testHandler(db)

	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/customers/"+custID.String(),
		map[string]string{"org_id": orgID.String(), "id": custID.String()})
	w := httptest.NewRecorder()
	h.HandleGet(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON body: %v", err)
	}

	if body["id"] != custID.String() {
		t.Errorf("id = %v, want %v", body["id"], custID.String())
	}
	if body["display_name"] != displayName {
		t.Errorf("display_name = %v, want %v", body["display_name"], displayName)
	}

	identities, ok := body["identities"].([]any)
	if !ok || len(identities) != 3 {
		t.Fatalf("expected 3 identities, got %#v", body["identities"])
	}
	byKind := map[string]map[string]any{}
	for _, raw := range identities {
		ident := raw.(map[string]any)
		byKind[ident["kind"].(string)] = ident
	}
	if got := byKind["email"]["value"]; got != "alice@example.com" {
		t.Errorf("verified email should be unmasked, got %v", got)
	}
	if got := byKind["email"]["verified"]; got != true {
		t.Errorf("email verified flag = %v, want true", got)
	}
	if got := byKind["phone"]["value"]; got != "***4567" {
		t.Errorf("unverified phone should be masked, got %v", got)
	}
	if got := byKind["phone"]["verified"]; got != false {
		t.Errorf("phone verified flag = %v, want false", got)
	}
	if got := byKind["device"]["value"]; got != "device-abc-123" {
		t.Errorf("weak identity should never be masked, got %v", got)
	}

	orders, ok := body["orders"].([]any)
	if !ok || len(orders) != 1 {
		t.Fatalf("expected 1 order, got %#v", body["orders"])
	}
	order := orders[0].(map[string]any)
	if order["status"] != "paid" {
		t.Errorf("order status = %v, want paid", order["status"])
	}

	attrs, ok := body["attributes"].([]any)
	if !ok || len(attrs) != 2 {
		t.Fatalf("expected 2 attributes, got %#v", body["attributes"])
	}

	consents, ok := body["consents"].([]any)
	if !ok || len(consents) != 1 {
		t.Fatalf("expected 1 consent, got %#v", body["consents"])
	}
	consent := consents[0].(map[string]any)
	if consent["kind"] != "terms" {
		t.Errorf("consent kind = %v, want terms", consent["kind"])
	}
	if consent["withdrawn_at"] != nil {
		t.Errorf("withdrawn_at = %v, want nil", consent["withdrawn_at"])
	}
}

func TestHandleGet_DependencyUnavailable_WhenNilQueries(t *testing.T) {
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	orgID := uuid.New()
	custID := uuid.New()
	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/customers/"+custID.String(),
		map[string]string{"org_id": orgID.String(), "id": custID.String()})
	w := httptest.NewRecorder()
	h.HandleGet(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// HandleList
// ─────────────────────────────────────────────────────────────────────────────

// searchFakeDB routes the SearchCustomersByOrg :many query (FROM customers c
// ... JOIN customer_org_links) separately from HandleGet's other queries,
// since both eventually touch "customers" — SearchCustomersByOrg's SQL text
// aliases the table as "c" with a JOIN, which is matched first.
type searchFakeDB struct {
	rows [][]any
}

func (f *searchFakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("searchFakeDB: unexpected Exec")
}
func (f *searchFakeDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return &fakeRow{err: errors.New("searchFakeDB: unexpected QueryRow")}
}
func (f *searchFakeDB) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "FROM   customers c") {
		return &fakeRows{data: f.rows}, nil
	}
	return nil, fmt.Errorf("searchFakeDB.Query: unexpected SQL %q", sql)
}

func TestHandleList_ReturnsSummaries(t *testing.T) {
	orgID := uuid.New()
	now := time.Now().UTC().Truncate(time.Second)
	name := "Bob Buyer"
	db := &searchFakeDB{rows: [][]any{
		newCustomerVals(uuid.New(), 1000000002, &name, nil, now, now),
	}}
	h := New(gen.New(db), slog.New(slog.NewTextHandler(io.Discard, nil)))

	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/customers?q=bob",
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
	customers, ok := body["customers"].([]any)
	if !ok || len(customers) != 1 {
		t.Fatalf("expected 1 customer, got %#v", body["customers"])
	}
	first := customers[0].(map[string]any)
	if first["display_name"] != name {
		t.Errorf("display_name = %v, want %v", first["display_name"], name)
	}
}

func TestHandleList_RejectsOverlongQuery(t *testing.T) {
	orgID := uuid.New()
	db := &searchFakeDB{}
	h := New(gen.New(db), slog.New(slog.NewTextHandler(io.Discard, nil)))

	longQ := strings.Repeat("a", 201)
	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/customers?q="+longQ,
		map[string]string{"org_id": orgID.String()})
	w := httptest.NewRecorder()
	h.HandleList(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleList_RejectsInvalidPagination(t *testing.T) {
	orgID := uuid.New()
	db := &searchFakeDB{}
	h := New(gen.New(db), slog.New(slog.NewTextHandler(io.Discard, nil)))

	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/customers?limit=0",
		map[string]string{"org_id": orgID.String()})
	w := httptest.NewRecorder()
	h.HandleList(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleList_DependencyUnavailable_WhenNilQueries(t *testing.T) {
	h := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	orgID := uuid.New()
	r := chiRequest(http.MethodGet, "/v1/organizations/"+orgID.String()+"/customers",
		map[string]string{"org_id": orgID.String()})
	w := httptest.NewRecorder()
	h.HandleList(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
