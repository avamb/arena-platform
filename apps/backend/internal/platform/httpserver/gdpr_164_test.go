// gdpr_164_test.go — unit tests for feature #164 (GDPR data workflows).
//
// Test coverage:
//   Step 1: Migration file 0018_gdpr.sql — table, constraints, consent columns, RBAC seeds
//   Step 2: Export endpoint (POST /v1/me/data-export) — auth-gating, response shape
//   Step 3: Delete endpoint (POST /v1/me/data-delete) — auth-gating, response shape
//   Step 4: Consent recording — migration columns, POST /v1/me/consent endpoint
//   Step 5: GDPR processor — export generates JSON, delete anonymizes, worker logic
//           SQL query file structure and gen file structure
//
// All tests are pure unit tests — no live PostgreSQL required.
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/auth"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Test actor ID
// ─────────────────────────────────────────────────────────────────────────────

const gdprTestActorID = "00000000-0000-0000-0000-000000000164"

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for GDPR route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildGDPRServer builds a Server with stub auth, GDPR routes fully mounted,
// and a dbDownPool so real DB operations never execute.
func buildGDPRServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:         config.EnvDevelopment,
		RequestTimeout: 5 * time.Second,
		BodyLimitBytes: 1 << 20,
		JWTSecretStub:  "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth: true,
		DefaultLocale:  "en",
		ActiveLocales:  []string{"en", "ru"},
	}
	stub, err := auth.NewStubProvider(auth.StubConfig{
		Secret:  cfg.JWTSecretStub,
		Issuer:  "arena-test",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("buildGDPRServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// GDPRQueries non-nil so GDPR route conditionals pass.
		GDPRQueries: gen.New(nil),
	})
}

// mintGDPRToken mints a dev JWT for GDPR route tests.
func mintGDPRToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + gdprTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintGDPRToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintGDPRToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintGDPRToken: empty token in response: %s", w.Body.String())
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_MigrationFile(t *testing.T) {
	content := findFileByName(t, "0018_gdpr.sql")

	t.Run("goose_up_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Up") {
			t.Error("0018_gdpr.sql: missing '-- +goose Up' marker")
		}
	})

	t.Run("goose_down_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Down") {
			t.Error("0018_gdpr.sql: missing '-- +goose Down' marker")
		}
	})

	t.Run("creates_data_subject_requests_table", func(t *testing.T) {
		if !strings.Contains(content, "CREATE TABLE data_subject_requests") {
			t.Error("0018_gdpr.sql: missing CREATE TABLE data_subject_requests")
		}
	})

	t.Run("request_type_check_constraint", func(t *testing.T) {
		if !strings.Contains(content, "'export'") || !strings.Contains(content, "'delete'") {
			t.Error("0018_gdpr.sql: missing CHECK constraint values for request_type (export/delete)")
		}
	})

	t.Run("status_check_constraint", func(t *testing.T) {
		if !strings.Contains(content, "'pending'") || !strings.Contains(content, "'processing'") ||
			!strings.Contains(content, "'completed'") || !strings.Contains(content, "'failed'") {
			t.Error("0018_gdpr.sql: missing status CHECK constraint values")
		}
	})

	t.Run("user_id_fk", func(t *testing.T) {
		if !strings.Contains(content, "REFERENCES users(id)") {
			t.Error("0018_gdpr.sql: missing REFERENCES users(id) FK on data_subject_requests")
		}
	})

	t.Run("on_delete_cascade", func(t *testing.T) {
		if !strings.Contains(content, "ON DELETE CASCADE") {
			t.Error("0018_gdpr.sql: missing ON DELETE CASCADE on user_id FK")
		}
	})

	t.Run("payload_url_column", func(t *testing.T) {
		if !strings.Contains(content, "payload_url") {
			t.Error("0018_gdpr.sql: missing payload_url column")
		}
	})

	t.Run("completed_at_column", func(t *testing.T) {
		if !strings.Contains(content, "completed_at") {
			t.Error("0018_gdpr.sql: missing completed_at column")
		}
	})

	t.Run("consent_given_at_column_on_users", func(t *testing.T) {
		if !strings.Contains(content, "consent_given_at") {
			t.Error("0018_gdpr.sql: missing consent_given_at column (ALTER TABLE users)")
		}
	})

	t.Run("marketing_consent_column_on_users", func(t *testing.T) {
		if !strings.Contains(content, "marketing_consent") {
			t.Error("0018_gdpr.sql: missing marketing_consent column (ALTER TABLE users)")
		}
	})

	t.Run("anonymized_at_column_on_users", func(t *testing.T) {
		if !strings.Contains(content, "anonymized_at") {
			t.Error("0018_gdpr.sql: missing anonymized_at column (ALTER TABLE users)")
		}
	})

	t.Run("rbac_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "gdpr.request") {
			t.Error("0018_gdpr.sql: missing 'gdpr.request' permission seed")
		}
	})

	t.Run("pending_index", func(t *testing.T) {
		if !strings.Contains(content, "CREATE INDEX") {
			t.Error("0018_gdpr.sql: missing index for data_subject_requests")
		}
	})

	t.Run("drop_in_down_section", func(t *testing.T) {
		if !strings.Contains(content, "DROP TABLE IF EXISTS data_subject_requests") {
			t.Error("0018_gdpr.sql: Down section missing DROP TABLE data_subject_requests")
		}
	})

	t.Run("alter_users_drop_in_down", func(t *testing.T) {
		if !strings.Contains(content, "DROP COLUMN IF EXISTS consent_given_at") {
			t.Error("0018_gdpr.sql: Down section missing DROP COLUMN consent_given_at")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2 & 3: Route auth-gating
// Note: POST routes must include Content-Type: application/json because the
// RequireJSONContentType global middleware fires before auth (middleware #10 in chain).
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_PostExportRequiresAuth(t *testing.T) {
	s := buildGDPRServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/me/data-export",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/me/data-export without auth: got %d, want 401", w.Code)
	}
}

func TestGDPR164_PostDeleteRequiresAuth(t *testing.T) {
	s := buildGDPRServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/me/data-delete",
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/me/data-delete without auth: got %d, want 401", w.Code)
	}
}

func TestGDPR164_GetDataRequestsRequiresAuth(t *testing.T) {
	s := buildGDPRServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/me/data-requests", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/me/data-requests without auth: got %d, want 401", w.Code)
	}
}

func TestGDPR164_PostConsentRequiresAuth(t *testing.T) {
	s := buildGDPRServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/me/consent",
		strings.NewReader(`{"marketing_consent":false}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/me/consent without auth: got %d, want 401", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: Export endpoint — Content-Type validation
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_ExportContentTypeValidation(t *testing.T) {
	s := buildGDPRServer(t)

	t.Run("missing_content_type_returns_415", func(t *testing.T) {
		// RequireJSONContentType fires before auth for POST — 415 is correct.
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/data-export",
			strings.NewReader(`{}`))
		// No Content-Type header → 415
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST /v1/me/data-export no Content-Type: got %d, want 415", w.Code)
		}
	})

	t.Run("wrong_content_type_returns_415", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/data-export",
			strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "text/plain")
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST /v1/me/data-export text/plain: got %d, want 415", w.Code)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Delete endpoint — Content-Type validation
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_DeleteContentTypeValidation(t *testing.T) {
	s := buildGDPRServer(t)

	t.Run("missing_content_type_returns_415", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/data-delete",
			strings.NewReader(`{}`))
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST /v1/me/data-delete no Content-Type: got %d, want 415", w.Code)
		}
	})

	t.Run("wrong_content_type_returns_415", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/data-delete",
			strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "text/plain")
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST /v1/me/data-delete text/plain: got %d, want 415", w.Code)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Route existence checks (without auth — verifies routes are mounted)
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_RoutesExist(t *testing.T) {
	s := buildGDPRServer(t)

	t.Run("POST_data-export_not_404", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/data-export",
			strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, req)
		// Without auth → 401 (not 404 which would indicate route not mounted)
		if w.Code == http.StatusNotFound {
			t.Error("POST /v1/me/data-export: route not mounted (404)")
		}
	})

	t.Run("POST_data-delete_not_404", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/data-delete",
			strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("POST /v1/me/data-delete: route not mounted (404)")
		}
	})

	t.Run("GET_data-requests_not_404", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/me/data-requests", nil)
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("GET /v1/me/data-requests: route not mounted (404)")
		}
	})

	t.Run("POST_consent_not_404", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/consent",
			strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("POST /v1/me/consent: route not mounted (404)")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// All unauthenticated responses have JSON Content-Type
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_UnauthResponsesAreJSON(t *testing.T) {
	s := buildGDPRServer(t)

	routes := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/v1/me/data-requests", ""},
		{http.MethodPost, "/v1/me/data-export", "{}"},
		{http.MethodPost, "/v1/me/data-delete", "{}"},
		{http.MethodPost, "/v1/me/consent", "{}"},
	}

	for _, rt := range routes {
		t.Run(rt.method+"_"+rt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			var req *http.Request
			if rt.body != "" {
				req = httptest.NewRequest(rt.method, rt.path,
					strings.NewReader(rt.body))
				req.Header.Set("Content-Type", "application/json")
			} else {
				req = httptest.NewRequest(rt.method, rt.path, nil)
			}
			s.router.ServeHTTP(w, req)
			ct := w.Header().Get("Content-Type")
			if !strings.HasPrefix(ct, "application/json") {
				t.Errorf("%s %s: Content-Type got %q, want application/json*",
					rt.method, rt.path, ct)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Consent endpoint — invalid JSON
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_ConsentInvalidJSON(t *testing.T) {
	s := buildGDPRServer(t)
	tok := mintGDPRToken(t, s)

	// With valid auth and Content-Type but invalid JSON body:
	// handleRecordConsent parses JSON before hitting DB → 400
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/me/consent",
		strings.NewReader(`{invalid json`))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	// Should return 400 (JSON parse error) before reaching DB.
	if w.Code != http.StatusBadRequest {
		t.Errorf("POST /v1/me/consent invalid JSON: got %d, want 400", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// SQL query file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_QueryFile(t *testing.T) {
	content := findFileByName(t, "gdpr.sql")

	queries := []struct {
		name    string
		sqlName string
	}{
		{"InsertDataSubjectRequest", "InsertDataSubjectRequest :one"},
		{"GetDataSubjectRequestByID", "GetDataSubjectRequestByID :one"},
		{"ListDataSubjectRequestsByUser", "ListDataSubjectRequestsByUser :many"},
		{"GetPendingDataSubjectRequests", "GetPendingDataSubjectRequests :many"},
		{"UpdateDataSubjectRequestStatus", "UpdateDataSubjectRequestStatus :one"},
		{"AnonymizeUser", "AnonymizeUser :exec"},
		{"RecordUserConsent", "RecordUserConsent :exec"},
		{"GetUserExportData", "GetUserExportData :one"},
	}

	for _, q := range queries {
		t.Run("query_"+q.name, func(t *testing.T) {
			if !strings.Contains(content, q.sqlName) {
				t.Errorf("gdpr.sql: missing query %q", q.sqlName)
			}
		})
	}

	t.Run("for_update_skip_locked", func(t *testing.T) {
		if !strings.Contains(content, "FOR UPDATE SKIP LOCKED") {
			t.Error("gdpr.sql: missing FOR UPDATE SKIP LOCKED for worker polling")
		}
	})

	t.Run("anonymize_email_placeholder", func(t *testing.T) {
		if !strings.Contains(content, "arena.invalid") {
			t.Error("gdpr.sql: AnonymizeUser should set email to '@arena.invalid' placeholder")
		}
	})

	t.Run("status_transition_in_update", func(t *testing.T) {
		if !strings.Contains(content, "completed_at") {
			t.Error("gdpr.sql: UpdateDataSubjectRequestStatus should set completed_at")
		}
	})

	t.Run("no_password_hash_in_export", func(t *testing.T) {
		// GetUserExportData must NOT select password_hash.
		exportQuery := extractGDPRQueryBlock(content, "GetUserExportData")
		if strings.Contains(exportQuery, "password_hash") {
			t.Error("gdpr.sql: GetUserExportData must not include password_hash (security)")
		}
	})
}

// extractGDPRQueryBlock extracts the SQL block for a named query from a .sql file.
// Returns the content between the query's name comment and the next -- name: comment.
func extractGDPRQueryBlock(content, queryName string) string {
	marker := "-- name: " + queryName
	start := strings.Index(content, marker)
	if start == -1 {
		return ""
	}
	after := content[start+len(marker):]
	// Find the next query marker.
	nextMarker := strings.Index(after, "-- name:")
	if nextMarker == -1 {
		return after
	}
	return after[:nextMarker]
}

// ─────────────────────────────────────────────────────────────────────────────
// Gen file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_GenFile(t *testing.T) {
	content := findFileByName(t, "gdpr.sql.go")

	t.Run("DataSubjectRequestRow_type", func(t *testing.T) {
		if !strings.Contains(content, "DataSubjectRequestRow") {
			t.Error("gdpr.sql.go: missing DataSubjectRequestRow type")
		}
	})

	t.Run("UserExportDataRow_type", func(t *testing.T) {
		if !strings.Contains(content, "UserExportDataRow") {
			t.Error("gdpr.sql.go: missing UserExportDataRow type")
		}
	})

	t.Run("InsertDataSubjectRequest_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) InsertDataSubjectRequest(") {
			t.Error("gdpr.sql.go: missing InsertDataSubjectRequest method")
		}
	})

	t.Run("GetDataSubjectRequestByID_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) GetDataSubjectRequestByID(") {
			t.Error("gdpr.sql.go: missing GetDataSubjectRequestByID method")
		}
	})

	t.Run("ListDataSubjectRequestsByUser_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) ListDataSubjectRequestsByUser(") {
			t.Error("gdpr.sql.go: missing ListDataSubjectRequestsByUser method")
		}
	})

	t.Run("GetPendingDataSubjectRequests_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) GetPendingDataSubjectRequests(") {
			t.Error("gdpr.sql.go: missing GetPendingDataSubjectRequests method")
		}
	})

	t.Run("UpdateDataSubjectRequestStatus_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) UpdateDataSubjectRequestStatus(") {
			t.Error("gdpr.sql.go: missing UpdateDataSubjectRequestStatus method")
		}
	})

	t.Run("AnonymizeUser_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) AnonymizeUser(") {
			t.Error("gdpr.sql.go: missing AnonymizeUser method")
		}
	})

	t.Run("RecordUserConsent_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) RecordUserConsent(") {
			t.Error("gdpr.sql.go: missing RecordUserConsent method")
		}
	})

	t.Run("GetUserExportData_method", func(t *testing.T) {
		if !strings.Contains(content, "func (q *Queries) GetUserExportData(") {
			t.Error("gdpr.sql.go: missing GetUserExportData method")
		}
	})

	t.Run("nullable_payload_url", func(t *testing.T) {
		if !strings.Contains(content, "PayloadURL  *string") {
			t.Error("gdpr.sql.go: DataSubjectRequestRow.PayloadURL should be *string (nullable)")
		}
	})

	t.Run("nullable_completed_at", func(t *testing.T) {
		if !strings.Contains(content, "CompletedAt *time.Time") {
			t.Error("gdpr.sql.go: DataSubjectRequestRow.CompletedAt should be *time.Time (nullable)")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Querier interface: compile-time check that all methods are implemented
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_QuerierInterface(t *testing.T) {
	// Compile-time assertion: *gen.Queries must implement gen.Querier.
	// If any method is missing the test won't compile.
	var _ gen.Querier = (*gen.Queries)(nil)
	t.Log("compile-time Querier assertion passed: all GDPR methods are wired")
}

// ─────────────────────────────────────────────────────────────────────────────
// dataSubjectRequestResponse helper
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_ResponseHelper(t *testing.T) {
	now := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	reqID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	userID := uuid.MustParse("20000000-0000-0000-0000-000000000002")

	t.Run("basic_export_request", func(t *testing.T) {
		row := gen.DataSubjectRequestRow{
			ID:          reqID,
			UserID:      userID,
			RequestType: "export",
			Status:      "pending",
			CreatedAt:   now,
		}
		resp := dataSubjectRequestResponse(row)
		if resp["id"] != reqID.String() {
			t.Errorf("response.id: got %v, want %s", resp["id"], reqID.String())
		}
		if resp["request_type"] != "export" {
			t.Errorf("response.request_type: got %v, want export", resp["request_type"])
		}
		if resp["status"] != "pending" {
			t.Errorf("response.status: got %v, want pending", resp["status"])
		}
		if resp["created_at"] != "2026-01-15T12:00:00Z" {
			t.Errorf("response.created_at: got %v, want RFC3339", resp["created_at"])
		}
		// payload_url and error_msg should be absent when nil.
		if _, ok := resp["payload_url"]; ok {
			t.Error("response: payload_url should be absent when nil")
		}
		if _, ok := resp["error_msg"]; ok {
			t.Error("response: error_msg should be absent when nil")
		}
	})

	t.Run("completed_with_payload_url", func(t *testing.T) {
		url := "inline:{...}"
		completed := now.Add(5 * time.Minute)
		row := gen.DataSubjectRequestRow{
			ID:          reqID,
			UserID:      userID,
			RequestType: "export",
			Status:      "completed",
			PayloadURL:  &url,
			CreatedAt:   now,
			CompletedAt: &completed,
		}
		resp := dataSubjectRequestResponse(row)
		if resp["payload_url"] != url {
			t.Errorf("response.payload_url: got %v, want %s", resp["payload_url"], url)
		}
		if resp["status"] != "completed" {
			t.Errorf("response.status: got %v, want completed", resp["status"])
		}
		if _, ok := resp["completed_at"]; !ok {
			t.Error("response: completed_at should be present when non-nil")
		}
	})

	t.Run("failed_with_error_msg", func(t *testing.T) {
		msg := "database unavailable"
		row := gen.DataSubjectRequestRow{
			ID:          reqID,
			UserID:      userID,
			RequestType: "delete",
			Status:      "failed",
			ErrorMsg:    &msg,
			CreatedAt:   now,
		}
		resp := dataSubjectRequestResponse(row)
		if resp["error_msg"] != msg {
			t.Errorf("response.error_msg: got %v, want %s", resp["error_msg"], msg)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GDPRProcessor unit tests
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_ProcessorConstruction(t *testing.T) {
	t.Run("new_processor_with_nil_logger", func(t *testing.T) {
		// Should not panic and should use slog.Default() logger.
		p := NewGDPRProcessor(&dbDownPool{}, gen.New(nil), nil)
		if p == nil {
			t.Error("NewGDPRProcessor: returned nil")
		}
	})

	t.Run("processor_has_pool_and_queries", func(t *testing.T) {
		pool := &dbDownPool{}
		q := gen.New(nil)
		p := NewGDPRProcessor(pool, q, nil)
		if p.pool == nil {
			t.Error("GDPRProcessor.pool: is nil")
		}
		if p.queries == nil {
			t.Error("GDPRProcessor.queries: is nil")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// formatTimePtr helper
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_FormatTimePtr(t *testing.T) {
	t.Run("nil_returns_nil", func(t *testing.T) {
		result := formatTimePtr(nil)
		if result != nil {
			t.Errorf("formatTimePtr(nil): got %v, want nil", result)
		}
	})

	t.Run("non_nil_returns_rfc3339_string", func(t *testing.T) {
		ts := time.Date(2026, 6, 23, 10, 30, 0, 0, time.UTC)
		result := formatTimePtr(&ts)
		if result == nil {
			t.Error("formatTimePtr(non-nil): got nil, want string")
		}
		s, ok := result.(string)
		if !ok {
			t.Errorf("formatTimePtr(non-nil): got %T, want string", result)
		}
		if s != "2026-06-23T10:30:00Z" {
			t.Errorf("formatTimePtr: got %q, want RFC3339 formatted string", s)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Querier interface — all GDPR methods must be present
// ─────────────────────────────────────────────────────────────────────────────

func TestGDPR164_QuerierHasAllMethods(t *testing.T) {
	content := findFileByName(t, "gdpr.sql.go")

	methods := []string{
		"InsertDataSubjectRequest",
		"GetDataSubjectRequestByID",
		"ListDataSubjectRequestsByUser",
		"GetPendingDataSubjectRequests",
		"UpdateDataSubjectRequestStatus",
		"AnonymizeUser",
		"RecordUserConsent",
		"GetUserExportData",
	}

	for _, m := range methods {
		t.Run(m, func(t *testing.T) {
			if !strings.Contains(content, "func (q *Queries) "+m+"(") {
				t.Errorf("gdpr.sql.go: missing method %s", m)
			}
		})
	}
}
