// handler_test.go — unit coverage for the customer-imports admin surface
// (feature #520, W1-C7b, epic #468, spec §12.4). Only DB-free paths are
// exercised here: request validation, header/permission preconditions and
// pure DTO/helper functions. The success paths (real INSERT/dry-run/apply)
// need a live Postgres and are covered by scenario 10 of the contract
// harness, tests/compat/bil24/harness_test.go.
package hcustomerimports

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// fakeTxStarter satisfies TxStarter without ever being called by the
// validation-only paths exercised in this file — the nil-guards only check
// that h.pool is non-nil.
type fakeTxStarter struct{}

func (fakeTxStarter) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, nil
}

func newTestHandler() *Handler {
	return New(gen.New(nil), fakeTxStarter{}, nil, nil, nil, slog.Default())
}

func withChiParam(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestHandleCreateCustomerImport_ServiceUnavailableWhenNoQueries(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, slog.Default())
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports", bytes.NewBufferString("{}"))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	h.HandleCreateCustomerImport(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleCreateCustomerImport_MissingAdminReason(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports", bytes.NewBufferString("{}"))
	rec := httptest.NewRecorder()
	h.HandleCreateCustomerImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "superadmin.missing_reason" {
		t.Errorf("error.code = %v, want superadmin.missing_reason", errObj["code"])
	}
}

func TestHandleCreateCustomerImport_EmptyBody(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports", bytes.NewBufferString(""))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	h.HandleCreateCustomerImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCreateCustomerImport_InvalidJSON(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports", bytes.NewBufferString("{not json"))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	h.HandleCreateCustomerImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleCreateCustomerImport_InvalidSourceLabel(t *testing.T) {
	h := newTestHandler()
	body := `{"source_label":"not_a_real_format","legal_basis":"organizer_contract","file_media_id":"00000000-0000-0000-0000-000000000001"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports", bytes.NewBufferString(body))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	h.HandleCreateCustomerImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var respBody map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &respBody)
	errObj := respBody["error"].(map[string]any)
	if errObj["code"] != "customer_import.invalid_source_label" {
		t.Errorf("error.code = %v, want customer_import.invalid_source_label", errObj["code"])
	}
}

func TestHandleCreateCustomerImport_InvalidLegalBasis(t *testing.T) {
	h := newTestHandler()
	body := `{"source_label":"generic_csv","legal_basis":"because_i_said_so","file_media_id":"00000000-0000-0000-0000-000000000001"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports", bytes.NewBufferString(body))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	h.HandleCreateCustomerImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var respBody map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &respBody)
	errObj := respBody["error"].(map[string]any)
	if errObj["code"] != "customer_import.invalid_legal_basis" {
		t.Errorf("error.code = %v, want customer_import.invalid_legal_basis", errObj["code"])
	}
}

func TestHandleCreateCustomerImport_InvalidFileMediaID(t *testing.T) {
	h := newTestHandler()
	body := `{"source_label":"generic_csv","legal_basis":"organizer_contract","file_media_id":"not-a-uuid"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports", bytes.NewBufferString(body))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	h.HandleCreateCustomerImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var respBody map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &respBody)
	errObj := respBody["error"].(map[string]any)
	if errObj["code"] != "customer_import.invalid_file_media_id" {
		t.Errorf("error.code = %v, want customer_import.invalid_file_media_id", errObj["code"])
	}
}

func TestHandleCreateCustomerImport_InvalidOrgID(t *testing.T) {
	h := newTestHandler()
	body := `{"org_id":"nope","source_label":"generic_csv","legal_basis":"organizer_contract","file_media_id":"00000000-0000-0000-0000-000000000001"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports", bytes.NewBufferString(body))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	h.HandleCreateCustomerImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var respBody map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &respBody)
	errObj := respBody["error"].(map[string]any)
	if errObj["code"] != "customer_import.invalid_org_id" {
		t.Errorf("error.code = %v, want customer_import.invalid_org_id", errObj["code"])
	}
}

// Note: a malformed "mapping" fragment cannot reach the handler's own
// json.Valid(mapping) guard through a real HTTP body — encoding/json's
// outer Unmarshal already validates syntax while scanning the raw-message
// field's extent, so any body that decodes at all carries a syntactically
// valid mapping value. The guard is defensive (e.g. future callers building
// createCustomerImportRequest programmatically); it is not exercised here.

func TestHandleCreateCustomerImport_UnauthenticatedAfterValidation(t *testing.T) {
	// Every field validates but no actor is present in the request context —
	// this must 401, not panic or reach the DB.
	h := newTestHandler()
	body := `{"source_label":"generic_csv","legal_basis":"organizer_contract","file_media_id":"00000000-0000-0000-0000-000000000001"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports", bytes.NewBufferString(body))
	req.Header.Set("X-Admin-Reason", "test")
	rec := httptest.NewRecorder()
	h.HandleCreateCustomerImport(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestHandleDryRunCustomerImport_ServiceUnavailableWhenNoPool(t *testing.T) {
	h := newTestHandler() // pgxPool and media are both nil
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports/00000000-0000-0000-0000-000000000001/dry-run", nil)
	req.Header.Set("X-Admin-Reason", "test")
	req = withChiParam(req, "id", "00000000-0000-0000-0000-000000000001")
	rec := httptest.NewRecorder()
	h.HandleDryRunCustomerImport(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// Both HandleApplyCustomerImport and HandleDryRunCustomerImport funnel
// through the shared runMode, which checks dependency availability before
// the X-Admin-Reason header — so with a nil pgxPool/media (as in
// newTestHandler) this returns 503 regardless of the header. See
// TestHandleDryRunCustomerImport_ServiceUnavailableWhenNoPool for the
// sibling case.
func TestHandleApplyCustomerImport_ServiceUnavailableWhenNoPool(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/customer-imports/00000000-0000-0000-0000-000000000001/apply", nil)
	req.Header.Set("X-Admin-Reason", "test")
	req = withChiParam(req, "id", "00000000-0000-0000-0000-000000000001")
	rec := httptest.NewRecorder()
	h.HandleApplyCustomerImport(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleGetCustomerImport_ServiceUnavailableWhenNoQueries(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/customer-imports/00000000-0000-0000-0000-000000000001", nil)
	req.Header.Set("X-Admin-Reason", "test")
	req = withChiParam(req, "id", "00000000-0000-0000-0000-000000000001")
	rec := httptest.NewRecorder()
	h.HandleGetCustomerImport(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandleGetCustomerImport_InvalidUUID(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/customer-imports/not-a-uuid", nil)
	req.Header.Set("X-Admin-Reason", "test")
	req = withChiParam(req, "id", "not-a-uuid")
	rec := httptest.NewRecorder()
	h.HandleGetCustomerImport(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestHandleListCustomerImportRows_InvalidAction(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/customer-imports/00000000-0000-0000-0000-000000000001/rows?action=bogus", nil)
	req.Header.Set("X-Admin-Reason", "test")
	req = withChiParam(req, "id", "00000000-0000-0000-0000-000000000001")
	rec := httptest.NewRecorder()
	h.HandleListCustomerImportRows(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var respBody map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &respBody)
	errObj := respBody["error"].(map[string]any)
	if errObj["code"] != "customer_import.invalid_action" {
		t.Errorf("error.code = %v, want customer_import.invalid_action", errObj["code"])
	}
}

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(allowedLegalBases)
	want := []string{"explicit_consent", "legitimate_interest", "organizer_contract"}
	if len(got) != len(want) {
		t.Fatalf("sortedKeys() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedKeys() = %v, want %v", got, want)
		}
	}
}

func TestJSONOrNull(t *testing.T) {
	if got := jsonOrNull(nil); string(got) != "null" {
		t.Errorf("jsonOrNull(nil) = %s, want null", got)
	}
	if got := jsonOrNull([]byte(`{"a":1}`)); string(got) != `{"a":1}` {
		t.Errorf("jsonOrNull([]byte) = %s, want passthrough", got)
	}
}

func TestAllowedRowActions_MatchesMigration0098(t *testing.T) {
	want := map[string]bool{
		"created": true, "matched": true, "merge_candidate": true, "skipped": true,
	}
	if len(allowedRowActions) != len(want) {
		t.Fatalf("allowedRowActions has %d entries, want %d", len(allowedRowActions), len(want))
	}
	for k := range want {
		if !allowedRowActions[k] {
			t.Errorf("allowedRowActions missing %q", k)
		}
	}
}

func TestAllowedSourceLabelsAndLegalBases_NonEmpty(t *testing.T) {
	if len(allowedSourceLabels) == 0 {
		t.Fatal("allowedSourceLabels is empty")
	}
	if len(allowedLegalBases) == 0 {
		t.Fatal("allowedLegalBases is empty")
	}
}
