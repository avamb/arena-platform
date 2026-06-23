// reservation_131_test.go — unit tests for feature #131 (Reservation state machine + TTL worker).
//
// Test coverage:
//   Step 1: Migration file 0021_reservations.sql — table, state machine, FKs, RBAC seeds
//   Step 2: SQL query file and gen file structure — InsertReservation, GetExpiredReservations, FOR UPDATE SKIP LOCKED
//   Step 3: State machine — validReservationTransitions, terminal states
//   Step 4: HTTP routes — auth-gating, processor structure, server wiring
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
)

// ─────────────────────────────────────────────────────────────────────────────
// Test actor ID
// ─────────────────────────────────────────────────────────────────────────────

const reservationTestActorID = "00000000-0000-0000-0000-000000000131"

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for reservation route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildReservationServer builds a Server with stub auth, reservation routes fully mounted,
// and a dbDownPool so real DB operations never execute.
func buildReservationServer(t *testing.T) *Server {
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
		t.Fatalf("buildReservationServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// Both queries non-nil so all reservation route conditionals pass.
		ReservationQueries: gen.New(nil),
		InventoryQueries:   gen.New(nil),
	})
}

// mintReservationToken mints a dev JWT for reservation route tests.
func mintReservationToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + reservationTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintReservationToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintReservationToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintReservationToken: empty token in response: %s", w.Body.String())
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation131_Step1_MigrationExists(t *testing.T) {
	content := findFileByName(t, "0021_reservations.sql")

	t.Run("goose_up_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Up") {
			t.Error("0021_reservations.sql: missing '-- +goose Up' marker")
		}
	})

	t.Run("goose_down_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Down") {
			t.Error("0021_reservations.sql: missing '-- +goose Down' marker")
		}
	})

	t.Run("creates_reservations_table", func(t *testing.T) {
		if !strings.Contains(content, "CREATE TABLE reservations") {
			t.Error("0021_reservations.sql: missing 'CREATE TABLE reservations'")
		}
	})

	t.Run("state_check_constraint", func(t *testing.T) {
		// The state column must have a CHECK constraint listing all valid states.
		if !strings.Contains(content, "state IN") {
			t.Error("0021_reservations.sql: missing state CHECK constraint with 'state IN'")
		}
	})

	t.Run("draft_state_in_constraint", func(t *testing.T) {
		if !strings.Contains(content, "'draft'") {
			t.Error("0021_reservations.sql: missing 'draft' state in CHECK constraint")
		}
	})

	t.Run("active_state_in_constraint", func(t *testing.T) {
		if !strings.Contains(content, "'active'") {
			t.Error("0021_reservations.sql: missing 'active' state in CHECK constraint")
		}
	})

	t.Run("converted_state_in_constraint", func(t *testing.T) {
		if !strings.Contains(content, "'converted'") {
			t.Error("0021_reservations.sql: missing 'converted' state in CHECK constraint")
		}
	})

	t.Run("expired_state_in_constraint", func(t *testing.T) {
		if !strings.Contains(content, "'expired'") {
			t.Error("0021_reservations.sql: missing 'expired' state in CHECK constraint")
		}
	})

	t.Run("cancelled_state_in_constraint", func(t *testing.T) {
		if !strings.Contains(content, "'cancelled'") {
			t.Error("0021_reservations.sql: missing 'cancelled' state in CHECK constraint")
		}
	})

	t.Run("expires_at_column", func(t *testing.T) {
		if !strings.Contains(content, "expires_at") {
			t.Error("0021_reservations.sql: missing 'expires_at' column")
		}
	})

	t.Run("fk_to_sessions", func(t *testing.T) {
		if !strings.Contains(content, "REFERENCES sessions") {
			t.Error("0021_reservations.sql: missing FK to sessions table")
		}
	})

	t.Run("fk_to_organizations", func(t *testing.T) {
		if !strings.Contains(content, "REFERENCES organizations") {
			t.Error("0021_reservations.sql: missing FK to organizations table")
		}
	})

	t.Run("fk_to_sales_channels", func(t *testing.T) {
		if !strings.Contains(content, "REFERENCES sales_channels") {
			t.Error("0021_reservations.sql: missing FK to sales_channels table")
		}
	})

	t.Run("reservation_create_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "reservation.create") {
			t.Error("0021_reservations.sql: missing 'reservation.create' permission seed")
		}
	})

	t.Run("reservation_read_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "reservation.read") {
			t.Error("0021_reservations.sql: missing 'reservation.read' permission seed")
		}
	})

	t.Run("reservation_activate_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "reservation.activate") {
			t.Error("0021_reservations.sql: missing 'reservation.activate' permission seed")
		}
	})

	t.Run("reservation_cancel_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "reservation.cancel") {
			t.Error("0021_reservations.sql: missing 'reservation.cancel' permission seed")
		}
	})

	t.Run("drop_in_down_section", func(t *testing.T) {
		if !strings.Contains(content, "DROP TABLE IF EXISTS reservations") {
			t.Error("0021_reservations.sql: Down section missing DROP TABLE reservations")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file and gen file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation131_Step2_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "reservations.sql")

	t.Run("insert_reservation_query", func(t *testing.T) {
		if !strings.Contains(content, "InsertReservation") {
			t.Error("reservations.sql: missing 'InsertReservation' query")
		}
	})

	t.Run("get_expired_reservations_query", func(t *testing.T) {
		if !strings.Contains(content, "GetExpiredReservations") {
			t.Error("reservations.sql: missing 'GetExpiredReservations' query")
		}
	})

	t.Run("for_update_skip_locked", func(t *testing.T) {
		if !strings.Contains(content, "FOR UPDATE SKIP LOCKED") {
			t.Error("reservations.sql: missing 'FOR UPDATE SKIP LOCKED' for TTL worker query")
		}
	})

	t.Run("update_reservation_state_query", func(t *testing.T) {
		if !strings.Contains(content, "UpdateReservationState") {
			t.Error("reservations.sql: missing 'UpdateReservationState' query")
		}
	})

	t.Run("get_reservation_by_id_query", func(t *testing.T) {
		if !strings.Contains(content, "GetReservationByID") {
			t.Error("reservations.sql: missing 'GetReservationByID' query")
		}
	})

	t.Run("list_by_session_query", func(t *testing.T) {
		if !strings.Contains(content, "ListReservationsBySession") {
			t.Error("reservations.sql: missing 'ListReservationsBySession' query")
		}
	})

	t.Run("list_by_user_query", func(t *testing.T) {
		if !strings.Contains(content, "ListReservationsByUser") {
			t.Error("reservations.sql: missing 'ListReservationsByUser' query")
		}
	})
}

func TestReservation131_Step2_GenFileExists(t *testing.T) {
	content := findFileByName(t, "reservations.sql.go")

	t.Run("reservation_row_struct", func(t *testing.T) {
		if !strings.Contains(content, "ReservationRow") {
			t.Error("reservations.sql.go: missing 'ReservationRow' struct")
		}
	})

	t.Run("insert_reservation_method", func(t *testing.T) {
		if !strings.Contains(content, "InsertReservation") {
			t.Error("reservations.sql.go: missing 'InsertReservation' method")
		}
	})

	t.Run("get_expired_reservations_method", func(t *testing.T) {
		if !strings.Contains(content, "GetExpiredReservations") {
			t.Error("reservations.sql.go: missing 'GetExpiredReservations' method")
		}
	})

	t.Run("update_reservation_state_method", func(t *testing.T) {
		if !strings.Contains(content, "UpdateReservationState") {
			t.Error("reservations.sql.go: missing 'UpdateReservationState' method")
		}
	})

	t.Run("scan_reservation_row_helper", func(t *testing.T) {
		if !strings.Contains(content, "scanReservationRow") {
			t.Error("reservations.sql.go: missing 'scanReservationRow' helper")
		}
	})

	t.Run("for_update_skip_locked_in_gen", func(t *testing.T) {
		if !strings.Contains(content, "FOR UPDATE SKIP LOCKED") {
			t.Error("reservations.sql.go: missing 'FOR UPDATE SKIP LOCKED' in GetExpiredReservations SQL constant")
		}
	})

	t.Run("expires_at_field", func(t *testing.T) {
		if !strings.Contains(content, "ExpiresAt") {
			t.Error("reservations.sql.go: ReservationRow missing 'ExpiresAt' field")
		}
	})

	t.Run("cancelled_at_field", func(t *testing.T) {
		if !strings.Contains(content, "CancelledAt") {
			t.Error("reservations.sql.go: ReservationRow missing 'CancelledAt' field")
		}
	})

	t.Run("expired_at_field", func(t *testing.T) {
		if !strings.Contains(content, "ExpiredAt") {
			t.Error("reservations.sql.go: ReservationRow missing 'ExpiredAt' field")
		}
	})
}

func TestReservation131_Step2_QuerierInterface(t *testing.T) {
	content := findFileByName(t, "querier.go")

	t.Run("insert_reservation_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "InsertReservation") {
			t.Error("querier.go: missing 'InsertReservation' in Querier interface")
		}
	})

	t.Run("get_expired_reservations_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "GetExpiredReservations") {
			t.Error("querier.go: missing 'GetExpiredReservations' in Querier interface")
		}
	})

	t.Run("update_reservation_state_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "UpdateReservationState") {
			t.Error("querier.go: missing 'UpdateReservationState' in Querier interface")
		}
	})

	t.Run("get_reservation_by_id_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "GetReservationByID") {
			t.Error("querier.go: missing 'GetReservationByID' in Querier interface")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: State machine
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation131_Step3_StateMachineInCode(t *testing.T) {
	content := findFileByName(t, "reservations.go")

	t.Run("valid_reservation_transitions_defined", func(t *testing.T) {
		if !strings.Contains(content, "validReservationTransitions") {
			t.Error("reservations.go: missing 'validReservationTransitions' map")
		}
	})

	t.Run("draft_state_present", func(t *testing.T) {
		if !strings.Contains(content, `"draft"`) {
			t.Error("reservations.go: missing 'draft' state in transitions map")
		}
	})

	t.Run("active_state_present", func(t *testing.T) {
		if !strings.Contains(content, `"active"`) {
			t.Error("reservations.go: missing 'active' state in transitions map")
		}
	})

	t.Run("converted_state_present", func(t *testing.T) {
		if !strings.Contains(content, `"converted"`) {
			t.Error("reservations.go: missing 'converted' state in transitions map")
		}
	})

	t.Run("expired_state_present", func(t *testing.T) {
		if !strings.Contains(content, `"expired"`) {
			t.Error("reservations.go: missing 'expired' state in transitions map")
		}
	})

	t.Run("cancelled_state_present", func(t *testing.T) {
		if !strings.Contains(content, `"cancelled"`) {
			t.Error("reservations.go: missing 'cancelled' state in transitions map")
		}
	})
}

func TestReservation131_Step3_StateMachineLogic(t *testing.T) {
	// Draft can transition to active and cancelled.
	t.Run("draft_can_activate", func(t *testing.T) {
		if !isValidReservationTransition("draft", "active") {
			t.Error("draft → active transition should be valid")
		}
	})

	t.Run("draft_can_cancel", func(t *testing.T) {
		if !isValidReservationTransition("draft", "cancelled") {
			t.Error("draft → cancelled transition should be valid")
		}
	})

	t.Run("draft_cannot_convert", func(t *testing.T) {
		if isValidReservationTransition("draft", "converted") {
			t.Error("draft → converted transition should NOT be valid")
		}
	})

	t.Run("draft_cannot_expire", func(t *testing.T) {
		if isValidReservationTransition("draft", "expired") {
			t.Error("draft → expired transition should NOT be valid")
		}
	})

	// Active can transition to converted, expired, or cancelled.
	t.Run("active_can_convert", func(t *testing.T) {
		if !isValidReservationTransition("active", "converted") {
			t.Error("active → converted transition should be valid")
		}
	})

	t.Run("active_can_expire", func(t *testing.T) {
		if !isValidReservationTransition("active", "expired") {
			t.Error("active → expired transition should be valid")
		}
	})

	t.Run("active_can_cancel", func(t *testing.T) {
		if !isValidReservationTransition("active", "cancelled") {
			t.Error("active → cancelled transition should be valid")
		}
	})

	// Terminal states have no valid transitions.
	t.Run("converted_is_terminal", func(t *testing.T) {
		for _, to := range []string{"draft", "active", "expired", "cancelled"} {
			if isValidReservationTransition("converted", to) {
				t.Errorf("converted → %s should NOT be valid (terminal state)", to)
			}
		}
	})

	t.Run("expired_is_terminal", func(t *testing.T) {
		for _, to := range []string{"draft", "active", "converted", "cancelled"} {
			if isValidReservationTransition("expired", to) {
				t.Errorf("expired → %s should NOT be valid (terminal state)", to)
			}
		}
	})

	t.Run("cancelled_is_terminal", func(t *testing.T) {
		for _, to := range []string{"draft", "active", "converted", "expired"} {
			if isValidReservationTransition("cancelled", to) {
				t.Errorf("cancelled → %s should NOT be valid (terminal state)", to)
			}
		}
	})

	// Verify the terminal states have empty transition maps (not just missing entries).
	t.Run("terminal_states_have_empty_maps", func(t *testing.T) {
		for _, terminal := range []string{"converted", "expired", "cancelled"} {
			allowed, ok := validReservationTransitions[terminal]
			if !ok {
				t.Errorf("validReservationTransitions[%q] missing — terminal state must have empty map", terminal)
				continue
			}
			if len(allowed) != 0 {
				t.Errorf("validReservationTransitions[%q] has %d entries, want 0 (terminal)", terminal, len(allowed))
			}
		}
	})
}

func TestReservation131_Step3_DefaultTTL(t *testing.T) {
	content := findFileByName(t, "reservations.go")

	t.Run("default_ttl_defined", func(t *testing.T) {
		if !strings.Contains(content, "defaultReservationTTL") {
			t.Error("reservations.go: missing 'defaultReservationTTL' constant")
		}
	})

	// Verify the actual value matches the spec (1200s = 20 min).
	t.Run("default_ttl_is_1200s", func(t *testing.T) {
		if defaultReservationTTL != 1200*time.Second {
			t.Errorf("defaultReservationTTL = %v, want 1200s", defaultReservationTTL)
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: HTTP routes — auth-gating
// ─────────────────────────────────────────────────────────────────────────────

const reservationBasePath = "/v1/reservations"
const reservationIDPath = "/v1/reservations/00000000-0000-0000-0000-000000000001"

func TestReservation131_Step4_GetRequiresAuth(t *testing.T) {
	s := buildReservationServer(t)
	req := httptest.NewRequest(http.MethodGet, reservationIDPath, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET /v1/reservations/{id} without auth: got %d, want 401", w.Code)
	}
}

func TestReservation131_Step4_PostRequiresAuth(t *testing.T) {
	s := buildReservationServer(t)
	req := httptest.NewRequest(http.MethodPost, reservationBasePath,
		strings.NewReader(`{"session_id":"00000000-0000-0000-0000-000000000001","channel_id":"00000000-0000-0000-0000-000000000002","org_id":"00000000-0000-0000-0000-000000000003","quantity":1}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /v1/reservations without auth: got %d, want 401", w.Code)
	}
}

func TestReservation131_Step4_PatchActivateRequiresAuth(t *testing.T) {
	s := buildReservationServer(t)
	req := httptest.NewRequest(http.MethodPatch, reservationIDPath+"/activate", nil)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("PATCH /v1/reservations/{id}/activate without auth: got %d, want 401", w.Code)
	}
}

func TestReservation131_Step4_DeleteRequiresAuth(t *testing.T) {
	s := buildReservationServer(t)
	req := httptest.NewRequest(http.MethodDelete, reservationIDPath, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("DELETE /v1/reservations/{id} without auth: got %d, want 401", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Routes are mounted (not 404)
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation131_Step4_RoutesAreMounted(t *testing.T) {
	s := buildReservationServer(t)

	t.Run("GET_reservation_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, reservationIDPath, nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		// Without auth → 401, but NOT 404 (route is mounted)
		if w.Code == http.StatusNotFound {
			t.Error("GET /v1/reservations/{id}: route not mounted (404)")
		}
	})

	t.Run("POST_reservation_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, reservationBasePath,
			strings.NewReader(`{"quantity":1}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("POST /v1/reservations: route not mounted (404)")
		}
	})

	t.Run("PATCH_activate_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPatch, reservationIDPath+"/activate", nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("PATCH /v1/reservations/{id}/activate: route not mounted (404)")
		}
	})

	t.Run("DELETE_reservation_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodDelete, reservationIDPath, nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("DELETE /v1/reservations/{id}: route not mounted (404)")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Processor structure
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation131_Step4_ProcessorStructure(t *testing.T) {
	content := findFileByName(t, "reservation_processor.go")

	t.Run("reservation_processor_struct", func(t *testing.T) {
		if !strings.Contains(content, "ReservationProcessor") {
			t.Error("reservation_processor.go: missing 'ReservationProcessor' struct")
		}
	})

	t.Run("new_reservation_processor_constructor", func(t *testing.T) {
		if !strings.Contains(content, "NewReservationProcessor") {
			t.Error("reservation_processor.go: missing 'NewReservationProcessor' constructor")
		}
	})

	t.Run("process_expired_reservations_method", func(t *testing.T) {
		if !strings.Contains(content, "ProcessExpiredReservations") {
			t.Error("reservation_processor.go: missing 'ProcessExpiredReservations' method")
		}
	})

	t.Run("uses_for_update_skip_locked_pattern", func(t *testing.T) {
		// The processor must use GetExpiredReservations (which uses FOR UPDATE SKIP LOCKED)
		if !strings.Contains(content, "GetExpiredReservations") {
			t.Error("reservation_processor.go: missing 'GetExpiredReservations' call — FOR UPDATE SKIP LOCKED not used")
		}
	})

	t.Run("releases_capacity_on_expiry", func(t *testing.T) {
		if !strings.Contains(content, "ReleaseCapacity") {
			t.Error("reservation_processor.go: missing 'ReleaseCapacity' call — inventory not released on expiry")
		}
	})

	t.Run("marks_expired_state", func(t *testing.T) {
		if !strings.Contains(content, `"expired"`) {
			t.Error("reservation_processor.go: missing 'expired' state — reservations not marked as expired")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Server wiring — fields exist on Server and Options
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation131_Step4_ServerWiring(t *testing.T) {
	content := findFileByName(t, "server.go")

	t.Run("server_struct_has_reservationQueries", func(t *testing.T) {
		if !strings.Contains(content, "reservationQueries") {
			t.Error("server.go: Server struct missing 'reservationQueries' field")
		}
	})

	t.Run("options_struct_has_ReservationQueries", func(t *testing.T) {
		if !strings.Contains(content, "ReservationQueries") {
			t.Error("server.go: Options struct missing 'ReservationQueries' field")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Content-Type validation on POST
// ─────────────────────────────────────────────────────────────────────────────

func TestReservation131_Step4_PostContentTypeValidation(t *testing.T) {
	s := buildReservationServer(t)

	t.Run("missing_content_type_returns_415", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, reservationBasePath,
			strings.NewReader(`{"quantity":1}`))
		// No Content-Type header → RequireJSONContentType middleware fires first → 415
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST /v1/reservations no Content-Type: got %d, want 415", w.Code)
		}
	})

	t.Run("wrong_content_type_returns_415", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, reservationBasePath,
			strings.NewReader(`{"quantity":1}`))
		req.Header.Set("Content-Type", "text/plain")
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST /v1/reservations text/plain: got %d, want 415", w.Code)
		}
	})
}
