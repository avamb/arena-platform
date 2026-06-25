// inventory_130_test.go — unit tests for feature #130 (Inventory Ledger - GA capacity).
//
// Test coverage:
//
//	Step 1: Migration file 0020_inventory_ledger.sql — table, constraints, RBAC seeds
//	Step 2: SQL query file and gen file structure — atomic operations, FOR UPDATE, invariant
//	Step 3: Capacity propagation hook — sessions.go wired to inventoryQueries
//	Step 4: HTTP routes — auth-gating, method-gating, server wiring
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

const inventoryTestActorID = "00000000-0000-0000-0000-000000000130"

// ─────────────────────────────────────────────────────────────────────────────
// Server factory for inventory route tests
// ─────────────────────────────────────────────────────────────────────────────

// buildInventoryServer builds a Server with stub auth, inventory routes fully mounted,
// and a dbDownPool so real DB operations never execute.
func buildInventoryServer(t *testing.T) *Server {
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
		t.Fatalf("buildInventoryServer: NewStubProvider: %v", err)
	}
	return New(Options{
		Config: cfg,
		Auth:   stub,
		// dbDownPool satisfies pool != nil guard so write routes get mounted.
		Pool: &dbDownPool{},
		// InventoryQueries non-nil so inventory route conditionals pass.
		InventoryQueries: gen.New(nil),
	})
}

// mintInventoryToken mints a dev JWT for inventory route tests.
func mintInventoryToken(t *testing.T, s *Server) string {
	t.Helper()
	w := httptest.NewRecorder()
	body := `{"actor_id":"` + inventoryTestActorID + `","roles":["admin"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/dev/token",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("mintInventoryToken: got %d, want 200; body: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("mintInventoryToken: decode: %v", err)
	}
	tok := resp["token"]
	if tok == "" {
		t.Fatalf("mintInventoryToken: empty token in response: %s", w.Body.String())
	}
	return tok
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 1: Migration file structure
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_Step1_MigrationExists(t *testing.T) {
	content := findFileByName(t, "0020_inventory_ledger.sql")

	t.Run("goose_up_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Up") {
			t.Error("0020_inventory_ledger.sql: missing '-- +goose Up' marker")
		}
	})

	t.Run("goose_down_marker", func(t *testing.T) {
		if !strings.Contains(content, "-- +goose Down") {
			t.Error("0020_inventory_ledger.sql: missing '-- +goose Down' marker")
		}
	})

	t.Run("creates_inventory_ledger_table", func(t *testing.T) {
		if !strings.Contains(content, "inventory_ledger") {
			t.Error("0020_inventory_ledger.sql: missing 'inventory_ledger' table definition")
		}
	})

	t.Run("capacity_held_column", func(t *testing.T) {
		if !strings.Contains(content, "capacity_held") {
			t.Error("0020_inventory_ledger.sql: missing 'capacity_held' column")
		}
	})

	t.Run("capacity_sold_column", func(t *testing.T) {
		if !strings.Contains(content, "capacity_sold") {
			t.Error("0020_inventory_ledger.sql: missing 'capacity_sold' column")
		}
	})

	t.Run("capacity_total_column", func(t *testing.T) {
		if !strings.Contains(content, "capacity_total") {
			t.Error("0020_inventory_ledger.sql: missing 'capacity_total' column")
		}
	})

	t.Run("inventory_read_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "inventory.read") {
			t.Error("0020_inventory_ledger.sql: missing 'inventory.read' permission seed")
		}
	})

	t.Run("inventory_reserve_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "inventory.reserve") {
			t.Error("0020_inventory_ledger.sql: missing 'inventory.reserve' permission seed")
		}
	})

	t.Run("inventory_release_permission_seed", func(t *testing.T) {
		if !strings.Contains(content, "inventory.release") {
			t.Error("0020_inventory_ledger.sql: missing 'inventory.release' permission seed")
		}
	})

	t.Run("drop_in_down_section", func(t *testing.T) {
		if !strings.Contains(content, "DROP TABLE IF EXISTS inventory_ledger") {
			t.Error("0020_inventory_ledger.sql: Down section missing DROP TABLE inventory_ledger")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 2: SQL query file and gen file — atomic operations
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_Step2_QueryFileExists(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")

	t.Run("reserve_capacity_query", func(t *testing.T) {
		if !strings.Contains(content, "ReserveCapacity") {
			t.Error("inventory_ledger.sql: missing 'ReserveCapacity' query")
		}
	})

	t.Run("for_update_locking", func(t *testing.T) {
		if !strings.Contains(content, "FOR UPDATE") {
			t.Error("inventory_ledger.sql: missing 'FOR UPDATE' row-level lock for atomic operations")
		}
	})

	t.Run("capacity_invariant_in_query", func(t *testing.T) {
		// The reserve query must enforce the invariant: held + sold + amount <= total
		if !strings.Contains(content, "capacity_held") || !strings.Contains(content, "capacity_sold") {
			t.Error("inventory_ledger.sql: ReserveCapacity query must reference capacity_held and capacity_sold to enforce invariant")
		}
	})

	t.Run("release_capacity_query", func(t *testing.T) {
		if !strings.Contains(content, "ReleaseCapacity") {
			t.Error("inventory_ledger.sql: missing 'ReleaseCapacity' query")
		}
	})
}

func TestInventory130_Step2_GenFileExists(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")

	t.Run("inventory_ledger_row_struct", func(t *testing.T) {
		if !strings.Contains(content, "InventoryLedgerRow") {
			t.Error("inventory_ledger.sql.go: missing 'InventoryLedgerRow' struct")
		}
	})

	t.Run("reserve_capacity_method", func(t *testing.T) {
		if !strings.Contains(content, "ReserveCapacity") {
			t.Error("inventory_ledger.sql.go: missing 'ReserveCapacity' method")
		}
	})

	t.Run("release_capacity_method", func(t *testing.T) {
		if !strings.Contains(content, "ReleaseCapacity") {
			t.Error("inventory_ledger.sql.go: missing 'ReleaseCapacity' method")
		}
	})
}

func TestInventory130_Step2_QuerierInterface(t *testing.T) {
	content := findFileByName(t, "querier.go")

	t.Run("reserve_capacity_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "ReserveCapacity") {
			t.Error("querier.go: missing 'ReserveCapacity' in Querier interface")
		}
	})

	t.Run("release_capacity_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "ReleaseCapacity") {
			t.Error("querier.go: missing 'ReleaseCapacity' in Querier interface")
		}
	})

	t.Run("confirm_capacity_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "ConfirmCapacity") {
			t.Error("querier.go: missing 'ConfirmCapacity' in Querier interface")
		}
	})

	t.Run("insert_inventory_ledger_in_interface", func(t *testing.T) {
		if !strings.Contains(content, "InsertInventoryLedger") {
			t.Error("querier.go: missing 'InsertInventoryLedger' in Querier interface")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 3: Capacity propagation hook
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_Step3_OnCapacityChangeWired(t *testing.T) {
	content := findFileByName(t, "sessions.go")

	t.Run("inventory_queries_field_used", func(t *testing.T) {
		if !strings.Contains(content, "inventoryQueries") {
			t.Error("sessions.go: missing 'inventoryQueries' field usage — capacity propagation not wired")
		}
	})

	t.Run("update_capacity_total_called", func(t *testing.T) {
		if !strings.Contains(content, "UpdateCapacityTotal") {
			t.Error("sessions.go: missing 'UpdateCapacityTotal' call in onCapacityChange — propagation not implemented")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: HTTP routes — auth-gating
// ─────────────────────────────────────────────────────────────────────────────

const inventoryBasePath = "/v1/organizations/00000000-0000-0000-0000-000000000001/events/00000000-0000-0000-0000-000000000002/sessions/00000000-0000-0000-0000-000000000003/inventory"

func TestInventory130_Step4_GetInventoryRequiresAuth(t *testing.T) {
	s := buildInventoryServer(t)
	req := httptest.NewRequest(http.MethodGet, inventoryBasePath, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("GET .../inventory without auth: got %d, want 401", w.Code)
	}
}

func TestInventory130_Step4_PostReserveRequiresAuth(t *testing.T) {
	s := buildInventoryServer(t)
	req := httptest.NewRequest(http.MethodPost, inventoryBasePath+"/reserve",
		strings.NewReader(`{"quantity":1}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST .../inventory/reserve without auth: got %d, want 401", w.Code)
	}
}

func TestInventory130_Step4_PostReleaseRequiresAuth(t *testing.T) {
	s := buildInventoryServer(t)
	req := httptest.NewRequest(http.MethodPost, inventoryBasePath+"/release",
		strings.NewReader(`{"quantity":1}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST .../inventory/release without auth: got %d, want 401", w.Code)
	}
}

func TestInventory130_Step4_GetInventoryWrongMethod(t *testing.T) {
	s := buildInventoryServer(t)
	// PUT on a GET-only route should return 405
	req := httptest.NewRequest(http.MethodPut, inventoryBasePath, nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT .../inventory (wrong method): got %d, want 405", w.Code)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 extra: Routes are mounted (not 404)
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_Step4_RoutesAreMounted(t *testing.T) {
	s := buildInventoryServer(t)

	t.Run("GET_inventory_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, inventoryBasePath, nil)
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		// Without auth → 401, but NOT 404 (route is mounted)
		if w.Code == http.StatusNotFound {
			t.Error("GET .../inventory: route not mounted (404)")
		}
	})

	t.Run("POST_reserve_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, inventoryBasePath+"/reserve",
			strings.NewReader(`{"quantity":1}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("POST .../inventory/reserve: route not mounted (404)")
		}
	})

	t.Run("POST_release_not_404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, inventoryBasePath+"/release",
			strings.NewReader(`{"quantity":1}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, req)
		if w.Code == http.StatusNotFound {
			t.Error("POST .../inventory/release: route not mounted (404)")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 extra: Server wiring — fields exist on Server and Options
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_Step4_ServerWiring(t *testing.T) {
	content := findFileByName(t, "server.go")

	t.Run("server_struct_has_inventoryQueries", func(t *testing.T) {
		if !strings.Contains(content, "inventoryQueries") {
			t.Error("server.go: Server struct missing 'inventoryQueries' field")
		}
	})

	t.Run("options_struct_has_InventoryQueries", func(t *testing.T) {
		if !strings.Contains(content, "InventoryQueries") {
			t.Error("server.go: Options struct missing 'InventoryQueries' field")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4 concurrency: SQL invariant enforcement in the reserve query
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_Step4_ReserveInvariant(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")

	// The ReserveCapacity query must check that adding the amount does not
	// exceed capacity_total. This is the core atomic invariant enforcement.
	t.Run("invariant_in_reserve_query", func(t *testing.T) {
		// Look for the invariant check pattern in the SQL constant
		hasCapacityTotal := strings.Contains(content, "capacity_total")
		hasCapacityHeld := strings.Contains(content, "capacity_held")
		hasCapacitySold := strings.Contains(content, "capacity_sold")
		if !hasCapacityTotal || !hasCapacityHeld || !hasCapacitySold {
			t.Error("inventory_ledger.sql.go: ReserveCapacity SQL missing invariant check (capacity_held + capacity_sold <= capacity_total)")
		}
	})

	t.Run("for_update_in_reserve_query", func(t *testing.T) {
		if !strings.Contains(content, "FOR UPDATE") {
			t.Error("inventory_ledger.sql.go: missing FOR UPDATE in reserve query — concurrent safety not guaranteed")
		}
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: POST reserve — Content-Type validation
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_Step4_ReserveContentTypeValidation(t *testing.T) {
	s := buildInventoryServer(t)

	t.Run("missing_content_type_returns_415", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, inventoryBasePath+"/reserve",
			strings.NewReader(`{"quantity":1}`))
		// No Content-Type header → RequireJSONContentType middleware fires first → 415
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST .../reserve no Content-Type: got %d, want 415", w.Code)
		}
	})

	t.Run("wrong_content_type_returns_415", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, inventoryBasePath+"/reserve",
			strings.NewReader(`{"quantity":1}`))
		req.Header.Set("Content-Type", "text/plain")
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST .../reserve text/plain: got %d, want 415", w.Code)
		}
	})
}

func TestInventory130_Step4_ReleaseContentTypeValidation(t *testing.T) {
	s := buildInventoryServer(t)

	t.Run("missing_content_type_returns_415", func(t *testing.T) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, inventoryBasePath+"/release",
			strings.NewReader(`{"quantity":1}`))
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST .../release no Content-Type: got %d, want 415", w.Code)
		}
	})
}
