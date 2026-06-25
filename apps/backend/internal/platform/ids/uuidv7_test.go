package ids_test

import (
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/ids"
)

// -----------------------------------------------------------------------------
// Version and variant bits
// -----------------------------------------------------------------------------

// TestNewUUIDv7_VersionIs7 verifies that the version nibble is 7 (0111),
// satisfying step 4 (correct version bits).
func TestNewUUIDv7_VersionIs7(t *testing.T) {
	t.Parallel()

	id, err := ids.NewUUIDv7()
	if err != nil {
		t.Fatalf("NewUUIDv7() returned unexpected error: %v", err)
	}
	if got := id.Version(); got != 7 {
		t.Errorf("expected UUID version 7, got %d (UUID: %s)", got, id)
	}
}

// TestNewUUIDv7_VariantIsRFC4122 verifies the variant bits are set to RFC 4122
// (10xx), as required by the UUIDv7 specification.
func TestNewUUIDv7_VariantIsRFC4122(t *testing.T) {
	t.Parallel()

	id, err := ids.NewUUIDv7()
	if err != nil {
		t.Fatalf("NewUUIDv7() returned unexpected error: %v", err)
	}
	if got := id.Variant(); got != uuid.RFC4122 {
		t.Errorf("expected RFC4122 variant, got %v (UUID: %s)", got, id)
	}
}

// TestNewUUIDv7_NotZero verifies that a generated UUID is never the zero value.
func TestNewUUIDv7_NotZero(t *testing.T) {
	t.Parallel()

	id, err := ids.NewUUIDv7()
	if err != nil {
		t.Fatalf("NewUUIDv7() returned unexpected error: %v", err)
	}
	var zero uuid.UUID
	if id == zero {
		t.Error("NewUUIDv7() returned the zero UUID")
	}
}

// -----------------------------------------------------------------------------
// Monotonicity — step 4
// -----------------------------------------------------------------------------

// TestNewUUIDv7_Monotonic verifies that 100 sequentially generated UUIDs are
// strictly increasing when compared as strings. UUIDv7 encodes the Unix
// millisecond timestamp in the highest-order bits; the library adds a
// sub-millisecond counter so IDs within the same millisecond are also ordered.
func TestNewUUIDv7_Monotonic(t *testing.T) {
	t.Parallel()

	const count = 100
	generated := make([]string, count)
	for i := 0; i < count; i++ {
		id, err := ids.NewUUIDv7()
		if err != nil {
			t.Fatalf("NewUUIDv7() error at i=%d: %v", i, err)
		}
		generated[i] = id.String()
	}

	for i := 1; i < count; i++ {
		if generated[i-1] >= generated[i] {
			t.Errorf(
				"monotonicity violated at i=%d: %s >= %s",
				i, generated[i-1], generated[i],
			)
		}
	}
}

// TestNewUUIDv7_SortableAsStrings verifies that sorting a slice of generated
// UUID strings produces the same order as the generation order. This is the
// canonical sortability property of UUIDv7.
func TestNewUUIDv7_SortableAsStrings(t *testing.T) {
	t.Parallel()

	const count = 100
	generated := make([]string, count)
	for i := 0; i < count; i++ {
		id, err := ids.NewUUIDv7()
		if err != nil {
			t.Fatalf("NewUUIDv7() error at i=%d: %v", i, err)
		}
		generated[i] = id.String()
	}

	sorted := make([]string, count)
	copy(sorted, generated)
	sort.Strings(sorted)

	for i := range generated {
		if generated[i] != sorted[i] {
			t.Errorf(
				"sort order mismatch at index %d: generation order %s, sorted order %s",
				i, generated[i], sorted[i],
			)
		}
	}
}

// -----------------------------------------------------------------------------
// Uniqueness — step 4 (10k calls)
// -----------------------------------------------------------------------------

// TestNewUUIDv7_Unique10k verifies that 10 000 sequential calls produce no
// duplicate IDs.
func TestNewUUIDv7_Unique10k(t *testing.T) {
	t.Parallel()

	const count = 10_000
	seen := make(map[uuid.UUID]struct{}, count)

	for i := 0; i < count; i++ {
		id, err := ids.NewUUIDv7()
		if err != nil {
			t.Fatalf("NewUUIDv7() error at i=%d: %v", i, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate UUID at i=%d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestNewUUIDv7_10kVersionBits verifies that all 10 000 IDs carry version 7
// bits, proving the version field is not incidentally correct for a small sample.
func TestNewUUIDv7_10kVersionBits(t *testing.T) {
	t.Parallel()

	const count = 10_000
	for i := 0; i < count; i++ {
		id, err := ids.NewUUIDv7()
		if err != nil {
			t.Fatalf("NewUUIDv7() error at i=%d: %v", i, err)
		}
		if id.Version() != 7 {
			t.Errorf("version mismatch at i=%d: expected 7, got %d (UUID: %s)", i, id.Version(), id)
		}
	}
}

// -----------------------------------------------------------------------------
// MustNewUUIDv7
// -----------------------------------------------------------------------------

// TestMustNewUUIDv7_VersionIs7 verifies that the Must variant also produces
// version-7 UUIDs.
func TestMustNewUUIDv7_VersionIs7(t *testing.T) {
	t.Parallel()

	id := ids.MustNewUUIDv7()
	if got := id.Version(); got != 7 {
		t.Errorf("MustNewUUIDv7() version: expected 7, got %d (UUID: %s)", got, id)
	}
}

// TestMustNewUUIDv7_NotZero verifies that the Must variant never returns the
// zero UUID.
func TestMustNewUUIDv7_NotZero(t *testing.T) {
	t.Parallel()

	id := ids.MustNewUUIDv7()
	var zero uuid.UUID
	if id == zero {
		t.Error("MustNewUUIDv7() returned the zero UUID")
	}
}

// TestMustNewUUIDv7_Unique verifies that 100 sequential calls via the Must
// variant produce distinct IDs.
func TestMustNewUUIDv7_Unique(t *testing.T) {
	t.Parallel()

	const count = 100
	seen := make(map[uuid.UUID]struct{}, count)
	for i := 0; i < count; i++ {
		id := ids.MustNewUUIDv7()
		if _, dup := seen[id]; dup {
			t.Fatalf("MustNewUUIDv7() duplicate at i=%d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}
