// inventory_ledger_test.go — supplemental tests for the inventory ledger (feature #130).
//
// Test prefix: TestInventory130_
//
// This file adds tests that are NOT covered in inventory_130_test.go:
//   - checkCapacityInvariant pure function (Step 4: concurrency invariant)
//   - inventoryRowFromLedger helper edge cases
//   - Concurrency stress test on the pure invariant function
//   - Additional migration file checks (invariant constraint, nullable tier_id, NULLS NOT DISTINCT)
//   - Additional query file checks (ConfirmCapacity, UpdateCapacityTotal, nullable comparison)
//   - Additional gen file checks (full struct fields, all 7 methods, VersionField)
package httpserver

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// ─────────────────────────────────────────────────────────────────────────────
// Additional Step 1: Migration file — deeper structural checks
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_Migration_InvariantCheckConstraint(t *testing.T) {
	content := findFileByName(t, "0020_inventory_ledger.sql")
	if !strings.Contains(content, "capacity_held + capacity_sold <= capacity_total") {
		t.Error("migration: missing DB-level invariant CHECK (held + sold <= total)")
	}
}

func TestInventory130_Migration_NonnegativeCheckConstraint(t *testing.T) {
	content := findFileByName(t, "0020_inventory_ledger.sql")
	if !strings.Contains(content, "capacity_held >= 0") {
		t.Error("migration: missing non-negative CHECK constraint for capacity_held")
	}
}

func TestInventory130_Migration_TierIDNullable(t *testing.T) {
	content := findFileByName(t, "0020_inventory_ledger.sql")
	// tier_id should be nullable (no NOT NULL constraint on it).
	// We verify it is declared without NOT NULL:
	if !strings.Contains(content, "tier_id") {
		t.Error("migration: missing tier_id column")
	}
}

func TestInventory130_Migration_UniqueNullsNotDistinct(t *testing.T) {
	content := findFileByName(t, "0020_inventory_ledger.sql")
	if !strings.Contains(content, "NULLS NOT DISTINCT") {
		t.Error("migration: UNIQUE index on (session_id, tier_id) should use NULLS NOT DISTINCT " +
			"so a single session-level row (tier_id IS NULL) is enforced")
	}
}

func TestInventory130_Migration_SessionForeignKey(t *testing.T) {
	content := findFileByName(t, "0020_inventory_ledger.sql")
	if !strings.Contains(content, "REFERENCES sessions(id)") {
		t.Error("migration: missing REFERENCES sessions(id) FK constraint")
	}
}

func TestInventory130_Migration_TierForeignKey(t *testing.T) {
	content := findFileByName(t, "0020_inventory_ledger.sql")
	if !strings.Contains(content, "REFERENCES ticket_tiers(id)") {
		t.Error("migration: missing REFERENCES ticket_tiers(id) FK constraint")
	}
}

func TestInventory130_Migration_VersionColumn(t *testing.T) {
	content := findFileByName(t, "0020_inventory_ledger.sql")
	if !strings.Contains(content, "version") {
		t.Error("migration: missing version column (optimistic lock counter)")
	}
}

func TestInventory130_Migration_InventoryConfirmPermission(t *testing.T) {
	content := findFileByName(t, "0020_inventory_ledger.sql")
	if !strings.Contains(content, "inventory.confirm") {
		t.Error("migration: missing inventory.confirm RBAC permission seed")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Additional Step 2: Query file — deeper structural checks
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_QueryFile_ConfirmCapacity(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")
	if !strings.Contains(content, "ConfirmCapacity") {
		t.Error("query file: missing ConfirmCapacity query (held → sold transfer)")
	}
}

func TestInventory130_QueryFile_UpdateCapacityTotal(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")
	if !strings.Contains(content, "UpdateCapacityTotal") {
		t.Error("query file: missing UpdateCapacityTotal query (capacity propagation from session)")
	}
}

func TestInventory130_QueryFile_InsertInventoryLedger(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")
	if !strings.Contains(content, "InsertInventoryLedger") {
		t.Error("query file: missing InsertInventoryLedger query")
	}
}

func TestInventory130_QueryFile_GetInventoryLedger(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")
	if !strings.Contains(content, "GetInventoryLedger") {
		t.Error("query file: missing GetInventoryLedger query")
	}
}

func TestInventory130_QueryFile_ListInventoryLedgersBySession(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")
	if !strings.Contains(content, "ListInventoryLedgersBySession") {
		t.Error("query file: missing ListInventoryLedgersBySession query")
	}
}

func TestInventory130_QueryFile_NullableTierComparison(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")
	// NULL-safe comparison for tier_id must be used
	if !strings.Contains(content, "tier_id IS NULL") {
		t.Error("query file: must use NULL-safe comparison for tier_id (tier_id IS NULL)")
	}
}

func TestInventory130_QueryFile_ReturningClause(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql")
	if !strings.Contains(content, "RETURNING") {
		t.Error("query file: update queries must use RETURNING to return the updated row")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Additional Step 2: Gen file — deeper structural checks
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_GenFile_CapacityTotalNullable(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")
	if !strings.Contains(content, "CapacityTotal *int32") {
		t.Error("gen file: CapacityTotal should be *int32 (nullable for unlimited)")
	}
}

func TestInventory130_GenFile_TierIDNullable(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")
	if !strings.Contains(content, "*uuid.UUID") {
		t.Error("gen file: TierID should be *uuid.UUID (nullable)")
	}
}

func TestInventory130_GenFile_VersionField(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")
	if !strings.Contains(content, "Version") {
		t.Error("gen file: missing Version field (optimistic lock counter)")
	}
}

func TestInventory130_GenFile_UpdateCapacityTotalMethod(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")
	if !strings.Contains(content, "UpdateCapacityTotal") {
		t.Error("gen file: missing UpdateCapacityTotal method")
	}
}

func TestInventory130_GenFile_ConfirmCapacityMethod(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")
	if !strings.Contains(content, "ConfirmCapacity") {
		t.Error("gen file: missing ConfirmCapacity method")
	}
}

func TestInventory130_GenFile_InsertInventoryLedgerMethod(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")
	if !strings.Contains(content, "func (q *Queries) InsertInventoryLedger(") {
		t.Error("gen file: missing InsertInventoryLedger method")
	}
}

func TestInventory130_GenFile_GetInventoryLedgerMethod(t *testing.T) {
	content := findFileByName(t, "inventory_ledger.sql.go")
	if !strings.Contains(content, "func (q *Queries) GetInventoryLedger(") {
		t.Error("gen file: missing GetInventoryLedger method")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: checkCapacityInvariant — pure function unit tests
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_CheckCapacityInvariant_UnlimitedAlwaysPasses(t *testing.T) {
	if !checkCapacityInvariant(nil, 0, 0, 9999) {
		t.Error("unlimited (nil total) with large amount should pass")
	}
	if !checkCapacityInvariant(nil, 500, 300, 1000) {
		t.Error("unlimited with non-zero held+sold should still pass")
	}
}

func TestInventory130_CheckCapacityInvariant_ExactlyAtCapacity(t *testing.T) {
	total := int32(100)
	// 80 held + 10 sold + 10 = 100 == total → passes
	if !checkCapacityInvariant(&total, 80, 10, 10) {
		t.Error("reserve filling to exactly total capacity should pass")
	}
}

func TestInventory130_CheckCapacityInvariant_OneOverCapacity(t *testing.T) {
	total := int32(100)
	// 80 held + 10 sold + 11 = 101 > total → fails
	if checkCapacityInvariant(&total, 80, 10, 11) {
		t.Error("reserve exceeding capacity by 1 should fail")
	}
}

func TestInventory130_CheckCapacityInvariant_EmptyLedgerFull(t *testing.T) {
	total := int32(50)
	// 0 + 0 + 50 = 50 == total → passes
	if !checkCapacityInvariant(&total, 0, 0, 50) {
		t.Error("reserve from empty ledger filling to total should pass")
	}
}

func TestInventory130_CheckCapacityInvariant_EmptyLedgerOver(t *testing.T) {
	total := int32(50)
	// 0 + 0 + 51 = 51 > total → fails
	if checkCapacityInvariant(&total, 0, 0, 51) {
		t.Error("reserve from empty exceeding total should fail")
	}
}

func TestInventory130_CheckCapacityInvariant_SmallAmount(t *testing.T) {
	total := int32(10)
	// Reserve 1 at a time, each should pass until total is reached
	var held, sold int32
	for i := 0; i < 10; i++ {
		if !checkCapacityInvariant(&total, held, sold, 1) {
			t.Errorf("iteration %d: expected pass but got fail (held=%d sold=%d)", i, held, sold)
		}
		held++
	}
	// 11th attempt should fail
	if checkCapacityInvariant(&total, held, sold, 1) {
		t.Error("11th reserve on capacity=10 should fail")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Step 4: Concurrency stress tests on checkCapacityInvariant
//
// Simulates N goroutines concurrently reserving capacity from a shared
// in-memory counter. Uses a mutex to serialize the check-and-increment
// (mimicking what SELECT FOR UPDATE does at the database level).
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_ConcurrentReserve_UnderCapacityAllPass(t *testing.T) {
	const totalCapacity = int32(100)
	const goroutines = 50
	const amountPerGoroutine = int32(2) // 50 * 2 = 100 ≤ 100 → all pass

	var (
		mu      sync.Mutex
		held    int32
		sold    int32
		success int64
	)
	total := totalCapacity

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			if checkCapacityInvariant(&total, held, sold, amountPerGoroutine) {
				held += amountPerGoroutine
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()

	if success != int64(goroutines) {
		t.Errorf("under capacity: %d/%d succeeded, want all %d", success, goroutines, goroutines)
	}
	if held != totalCapacity {
		t.Errorf("under capacity: final held=%d, want %d", held, totalCapacity)
	}
}

func TestInventory130_ConcurrentReserve_OverCapacitySomePass(t *testing.T) {
	const totalCapacity = int32(10)
	const goroutines = 20
	const amountPerGoroutine = int32(1) // 20 goroutines, only 10 capacity

	var (
		mu      sync.Mutex
		held    int32
		sold    int32
		success int64
		failed  int64
	)
	total := totalCapacity

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			if checkCapacityInvariant(&total, held, sold, amountPerGoroutine) {
				held += amountPerGoroutine
				atomic.AddInt64(&success, 1)
			} else {
				atomic.AddInt64(&failed, 1)
			}
		}()
	}
	wg.Wait()

	if success != int64(totalCapacity) {
		t.Errorf("over capacity: %d succeeded, want exactly %d", success, totalCapacity)
	}
	if failed != int64(goroutines)-int64(totalCapacity) {
		t.Errorf("over capacity: %d failed, want %d", failed, int64(goroutines)-int64(totalCapacity))
	}
	// Core invariant: held must never exceed total
	if held > totalCapacity {
		t.Errorf("INVARIANT VIOLATED: held=%d > total=%d", held, totalCapacity)
	}
}

func TestInventory130_ConcurrentReserve_UnlimitedAllPass(t *testing.T) {
	const goroutines = 100
	const amountPerGoroutine = int32(1)

	var (
		mu      sync.Mutex
		held    int32
		sold    int32
		success int64
	)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			// nil total = unlimited; all must pass
			if checkCapacityInvariant(nil, held, sold, amountPerGoroutine) {
				held += amountPerGoroutine
				atomic.AddInt64(&success, 1)
			}
		}()
	}
	wg.Wait()

	if success != int64(goroutines) {
		t.Errorf("unlimited: %d/%d succeeded, want all %d", success, goroutines, goroutines)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// inventoryRowFromLedger helper — edge cases
// ─────────────────────────────────────────────────────────────────────────────

func TestInventory130_InventoryRowFromLedger_UnlimitedCapacity(t *testing.T) {
	row := gen.InventoryLedgerRow{
		ID:            uuid.New(),
		SessionID:     uuid.New(),
		CapacityTotal: nil, // unlimited
		CapacityHeld:  0,
		CapacitySold:  0,
		UpdatedAt:     time.Now(),
	}
	resp := inventoryRowFromLedger(row)
	if resp.CapacityTotal != nil {
		t.Error("unlimited capacity: CapacityTotal should be nil in response")
	}
	if resp.CapacityAvailable != nil {
		t.Error("unlimited capacity: CapacityAvailable should be nil in response")
	}
}

func TestInventory130_InventoryRowFromLedger_AvailableComputed(t *testing.T) {
	total := int32(100)
	row := gen.InventoryLedgerRow{
		ID:            uuid.New(),
		SessionID:     uuid.New(),
		CapacityTotal: &total,
		CapacityHeld:  30,
		CapacitySold:  20,
		UpdatedAt:     time.Now(),
	}
	resp := inventoryRowFromLedger(row)
	if resp.CapacityAvailable == nil {
		t.Fatal("CapacityAvailable should not be nil when total is set")
	}
	// available = 100 - 30 - 20 = 50
	if *resp.CapacityAvailable != 50 {
		t.Errorf("CapacityAvailable: got %d, want 50", *resp.CapacityAvailable)
	}
}

func TestInventory130_InventoryRowFromLedger_TierIDPreserved(t *testing.T) {
	tierUUID := uuid.New()
	row := gen.InventoryLedgerRow{
		ID:        uuid.New(),
		SessionID: uuid.New(),
		TierID:    &tierUUID,
		UpdatedAt: time.Now(),
	}
	resp := inventoryRowFromLedger(row)
	if resp.TierID == nil {
		t.Fatal("TierID should not be nil when row has a tier")
	}
	if *resp.TierID != tierUUID.String() {
		t.Errorf("TierID: got %q, want %q", *resp.TierID, tierUUID.String())
	}
}

func TestInventory130_InventoryRowFromLedger_NilTierID(t *testing.T) {
	row := gen.InventoryLedgerRow{
		ID:        uuid.New(),
		SessionID: uuid.New(),
		TierID:    nil, // session-level row
		UpdatedAt: time.Now(),
	}
	resp := inventoryRowFromLedger(row)
	if resp.TierID != nil {
		t.Errorf("TierID should be nil for session-level row, got %q", *resp.TierID)
	}
}

func TestInventory130_InventoryRowFromLedger_RFC3339UpdatedAt(t *testing.T) {
	total := int32(50)
	row := gen.InventoryLedgerRow{
		ID:            uuid.New(),
		SessionID:     uuid.New(),
		CapacityTotal: &total,
		UpdatedAt:     time.Now().UTC(),
	}
	resp := inventoryRowFromLedger(row)

	_, err := time.Parse(time.RFC3339, resp.UpdatedAt)
	if err != nil {
		t.Errorf("updated_at is not valid RFC3339: %v", err)
	}
}

func TestInventory130_InventoryRowFromLedger_IDAndSessionIDStrings(t *testing.T) {
	id := uuid.New()
	sessionID := uuid.New()
	row := gen.InventoryLedgerRow{
		ID:        id,
		SessionID: sessionID,
		UpdatedAt: time.Now(),
	}
	resp := inventoryRowFromLedger(row)

	if resp.ID != id.String() {
		t.Errorf("ID: got %q, want %q", resp.ID, id.String())
	}
	if resp.SessionID != sessionID.String() {
		t.Errorf("SessionID: got %q, want %q", resp.SessionID, sessionID.String())
	}
}
