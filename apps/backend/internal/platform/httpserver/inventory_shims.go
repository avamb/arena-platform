// inventory_shims.go bridges the *Server god-object to the hinventory
// sub-package. All handler bodies live in hinventory/; these thin delegating
// methods preserve the unexported *Server method surface so mount_inventory.go,
// mount_partner.go and the structural test files (inventory_130_test.go,
// inventory_ledger_test.go, external_allocations_145_test.go) compile
// unchanged.
package httpserver

import (
	"net/http"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	inventorydomain "github.com/abhteam/arena_new/apps/backend/internal/domain/inventory"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hinventory"
)

// inventoryHandler constructs an hinventory.Handler from the server's
// dependencies. A fresh handler per request keeps the wiring uniform with
// hbilling / hgeo / hgdpr / hfeed and avoids stale captures when test code
// mutates *Server fields between calls.
func (s *Server) inventoryHandler() *hinventory.Handler {
	return hinventory.New(
		s.inventoryQueries,
		s.allocationQueries,
		s.pool,
		s.logger,
	)
}

// ─── allocation state machine forwarders ──────────────────────────────────────
//
// external_allocations_145_test.go references these unexported package-level
// names at compile time. The canonical source of truth is
// internal/domain/inventory (feature #184); hinventory/external_allocations.go
// carries its own package-private projection of the transition table for the
// moved handler bodies.

// validAllocationTransitions projects inventorydomain.ValidAllocationTransitions
// back to a string-keyed map so the in-package state-machine tests can inspect
// terminal-state emptiness without importing the domain package.
var validAllocationTransitions = func() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(inventorydomain.ValidAllocationTransitions))
	for from, allowed := range inventorydomain.ValidAllocationTransitions {
		row := make(map[string]bool, len(allowed))
		for to := range allowed {
			row[string(to)] = true
		}
		out[string(from)] = row
	}
	return out
}()

// allAllocationStatuses is the complete set of valid external allocation
// statuses, projected from the canonical pure-domain slice
// inventorydomain.AllAllocationStatuses (feature #184).
var allAllocationStatuses = func() []string {
	out := make([]string, 0, len(inventorydomain.AllAllocationStatuses))
	for _, s := range inventorydomain.AllAllocationStatuses {
		out = append(out, string(s))
	}
	return out
}()

// isTerminalAllocationStatus returns true for statuses that admit no further
// transitions. 1-line forwarder to the pure-domain predicate in
// internal/domain/inventory (feature #184).
func isTerminalAllocationStatus(status string) bool {
	return inventorydomain.IsTerminalAllocationStatus(status)
}

// ─── type aliases ─────────────────────────────────────────────────────────────

// inventoryRowResponse keeps the original unexported type name live in package
// httpserver so inventory_ledger_test.go compiles without importing the
// hinventory sub-package.
type inventoryRowResponse = hinventory.InventoryRowResponse

// ─── pure-function forwarders ─────────────────────────────────────────────────
// inventory_ledger_test.go calls these unqualified — keep the original
// lowercase names live in package httpserver so callers do not learn about
// the hinventory sub-package.

// checkCapacityInvariant forwards to hinventory.CheckCapacityInvariant.
func checkCapacityInvariant(total *int32, held, sold, amount int32) bool {
	return hinventory.CheckCapacityInvariant(total, held, sold, amount)
}

// inventoryRowFromLedger forwards to hinventory.InventoryRowFromLedger.
func inventoryRowFromLedger(row gen.InventoryLedgerRow) inventoryRowResponse {
	return hinventory.InventoryRowFromLedger(row)
}

// ─── inventory ledger handler shims ───────────────────────────────────────────

func (s *Server) handleListInventory(w http.ResponseWriter, r *http.Request) {
	s.inventoryHandler().HandleListInventory(w, r)
}

func (s *Server) handleInitInventory(w http.ResponseWriter, r *http.Request) {
	s.inventoryHandler().HandleInitInventory(w, r)
}

func (s *Server) handleReserveCapacity(w http.ResponseWriter, r *http.Request) {
	s.inventoryHandler().HandleReserveCapacity(w, r)
}

func (s *Server) handleReleaseCapacity(w http.ResponseWriter, r *http.Request) {
	s.inventoryHandler().HandleReleaseCapacity(w, r)
}

func (s *Server) handleConfirmCapacity(w http.ResponseWriter, r *http.Request) {
	s.inventoryHandler().HandleConfirmCapacity(w, r)
}

// ─── external allocation handler shims ────────────────────────────────────────

func (s *Server) handleCreateExternalAllocation(w http.ResponseWriter, r *http.Request) {
	s.inventoryHandler().HandleCreateExternalAllocation(w, r)
}

func (s *Server) handleListExternalAllocations(w http.ResponseWriter, r *http.Request) {
	s.inventoryHandler().HandleListExternalAllocations(w, r)
}

func (s *Server) handleGetExternalAllocation(w http.ResponseWriter, r *http.Request) {
	s.inventoryHandler().HandleGetExternalAllocation(w, r)
}

func (s *Server) handlePatchExternalAllocation(w http.ResponseWriter, r *http.Request) {
	s.inventoryHandler().HandlePatchExternalAllocation(w, r)
}
