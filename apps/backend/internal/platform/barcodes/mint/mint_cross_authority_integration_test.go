//go:build integration

// mint_cross_authority_integration_test.go — proves, against a live
// PostgreSQL, that mint.EAN13 enforces uniqueness ACROSS barcode
// authorities, not just within the target one (the "random EAN-13"
// change's core safety property: the owner will later import already-sold
// tickets into the 'legacy_bil24' authority, and GetBarcodeByExternalRefAny
// looks across every authority in one round-trip, so a cross-authority
// duplicate external_ref would be ambiguous).
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/barcodes/mint/ \
//	    -run TestEAN13_CrossAuthorityCollisionForcesRedraw
package mint

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// TestEAN13_CrossAuthorityCollisionForcesRedraw seeds a barcodes row under a
// DIFFERENT authority (legacy_bil24) carrying the exact external_ref the
// injected generator will draw first, then calls EAN13 targeting the
// 'platform' authority with that same colliding candidate as its first
// draw. EAN13 must NOT win on the first attempt (InsertBarcodeIfUnique's
// WHERE NOT EXISTS spans all authorities), must redraw, and must succeed
// with the second, non-colliding candidate — proving the guard is real and
// not scoped only to (authority_id, external_ref).
func TestEAN13_CrossAuthorityCollisionForcesRedraw(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping mint cross-authority integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pgxpool.New: %v — DATABASE_URL is set, so a connection failure must fail the gate", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool.Ping: %v — DATABASE_URL is set, so an unreachable database must fail the gate", err)
	}

	q := gen.New(pool)

	platformAuthority, err := q.GetBarcodeAuthorityByType(ctx, "platform")
	if err != nil {
		t.Fatalf("GetBarcodeAuthorityByType(platform): %v — run arena-migrate/arena-seed first", err)
	}

	// A dedicated legacy_bil24 authority row to own the colliding barcode —
	// mirrors the real future import, which lands in this exact authority.
	legacyAuthority, err := q.InsertBarcodeAuthority(ctx, "legacy_bil24", "mint_test legacy_bil24")
	if err != nil {
		t.Fatalf("InsertBarcodeAuthority(legacy_bil24): %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM barcode_authorities WHERE id = $1", legacyAuthority.ID)
	}()

	// A random-looking but fixed 13-digit collision fixture (the checksum
	// need not be valid — InsertBarcodeIfUnique does not validate it, only
	// EAN13's own draws are expected to be checksum-valid).
	const colliding = "2199999999990"
	const fresh = "2188888888887"
	defer func() {
		_, _ = pool.Exec(ctx, "DELETE FROM barcodes WHERE external_ref IN ($1, $2)", colliding, fresh)
	}()

	if _, err := q.InsertBarcode(ctx, legacyAuthority.ID, colliding, nil); err != nil {
		t.Fatalf("seed colliding barcode under legacy_bil24: %v", err)
	}

	draws := []string{colliding, fresh}
	callIdx := 0
	gen := func() (string, error) {
		if callIdx >= len(draws) {
			t.Fatalf("gen called more times than the fixture provides (%d)", callIdx+1)
		}
		v := draws[callIdx]
		callIdx++
		return v, nil
	}

	won, err := EAN13(ctx, q, platformAuthority.ID, nil, gen)
	if err != nil {
		t.Fatalf("EAN13: %v", err)
	}
	if won != fresh {
		t.Fatalf("EAN13 = %q, want the redrawn candidate %q — a cross-authority collision must force a retry", won, fresh)
	}
	if callIdx != 2 {
		t.Fatalf("generator was called %d times, want 2 (one losing draw, one winning draw)", callIdx)
	}

	// The colliding candidate must NOT have been written under 'platform'
	// too (that would make GetBarcodeByExternalRefAny ambiguous).
	row, err := q.GetBarcodeByRef(ctx, platformAuthority.ID, colliding)
	if err == nil {
		t.Fatalf("colliding candidate %q was written under platform authority too: %+v", colliding, row)
	}

	// The winning candidate must be resolvable under 'platform'.
	wonRow, err := q.GetBarcodeByRef(ctx, platformAuthority.ID, fresh)
	if err != nil {
		t.Fatalf("GetBarcodeByRef(platform, %q): %v", fresh, err)
	}
	if wonRow.AuthorityID != platformAuthority.ID {
		t.Errorf("won barcode authority = %s, want %s", wonRow.AuthorityID, platformAuthority.ID)
	}

	// GetBarcodeByExternalRefAny must still resolve the ORIGINAL legacy row
	// for the colliding ref (unambiguously, since platform never won it).
	anyRow, err := q.GetBarcodeByExternalRefAny(ctx, colliding)
	if err != nil {
		t.Fatalf("GetBarcodeByExternalRefAny(%q): %v", colliding, err)
	}
	if anyRow.AuthorityID != legacyAuthority.ID {
		t.Errorf("GetBarcodeByExternalRefAny(%q) authority = %s, want the legacy_bil24 authority %s",
			colliding, anyRow.AuthorityID, legacyAuthority.ID)
	}
}
