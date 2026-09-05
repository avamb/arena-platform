// translate_int_test.go — unit tests for the wave-1 int64 wire translation
// helpers (feature #476). Covers ParseLegacyIntID (pure) exhaustively; the
// db-backed ResolveLegacyIntID happy path is exercised in the compatids
// integration tests and in the handler-level tests.

package bil24compat

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseLegacyIntID_ValidPositiveInt(t *testing.T) {
	got, err := ParseLegacyIntID("1000000010")
	if err != nil {
		t.Fatalf("ParseLegacyIntID(valid int): unexpected error: %v", err)
	}
	if got != 1_000_000_010 {
		t.Errorf("ParseLegacyIntID(valid int): got %d, want %d", got, int64(1_000_000_010))
	}
}

func TestParseLegacyIntID_SmallPositiveInt(t *testing.T) {
	got, err := ParseLegacyIntID("1")
	if err != nil {
		t.Fatalf("ParseLegacyIntID(1): unexpected error: %v", err)
	}
	if got != 1 {
		t.Errorf("ParseLegacyIntID(1): got %d, want 1", got)
	}
}

func TestParseLegacyIntID_EmptyString(t *testing.T) {
	_, err := ParseLegacyIntID("")
	if !errors.Is(err, ErrLegacyIDInvalid) {
		t.Fatalf("ParseLegacyIntID(\"\"): want ErrLegacyIDInvalid, got %v", err)
	}
}

func TestParseLegacyIntID_UUIDRejected(t *testing.T) {
	id := uuid.New().String()
	_, err := ParseLegacyIntID(id)
	if !errors.Is(err, ErrLegacyIDUUIDRejected) {
		t.Fatalf("ParseLegacyIntID(uuid): want ErrLegacyIDUUIDRejected, got %v", err)
	}
	if !strings.Contains(err.Error(), id) {
		t.Errorf("error should include the offending UUID: %v", err)
	}
}

func TestParseLegacyIntID_UUIDNilRejected(t *testing.T) {
	// uuid.Nil ("00000000-…") is still a syntactically valid UUID and must
	// be rejected as such (not fall through to strconv.ParseInt).
	_, err := ParseLegacyIntID(uuid.Nil.String())
	if !errors.Is(err, ErrLegacyIDUUIDRejected) {
		t.Fatalf("ParseLegacyIntID(uuid.Nil): want ErrLegacyIDUUIDRejected, got %v", err)
	}
}

func TestParseLegacyIntID_NonNumeric(t *testing.T) {
	_, err := ParseLegacyIntID("legacy-order-99999")
	if !errors.Is(err, ErrLegacyIDInvalid) {
		t.Fatalf("ParseLegacyIntID(non-numeric): want ErrLegacyIDInvalid, got %v", err)
	}
}

func TestParseLegacyIntID_Zero(t *testing.T) {
	_, err := ParseLegacyIntID("0")
	if !errors.Is(err, ErrLegacyIDInvalid) {
		t.Fatalf("ParseLegacyIntID(0): want ErrLegacyIDInvalid, got %v", err)
	}
}

func TestParseLegacyIntID_Negative(t *testing.T) {
	_, err := ParseLegacyIntID("-5")
	if !errors.Is(err, ErrLegacyIDInvalid) {
		t.Fatalf("ParseLegacyIntID(-5): want ErrLegacyIDInvalid, got %v", err)
	}
}

func TestParseLegacyIntID_Float(t *testing.T) {
	_, err := ParseLegacyIntID("1.5")
	if !errors.Is(err, ErrLegacyIDInvalid) {
		t.Fatalf("ParseLegacyIntID(float): want ErrLegacyIDInvalid, got %v", err)
	}
}

func TestParseLegacyIntID_Overflow(t *testing.T) {
	_, err := ParseLegacyIntID("99999999999999999999999999")
	if !errors.Is(err, ErrLegacyIDInvalid) {
		t.Fatalf("ParseLegacyIntID(overflow): want ErrLegacyIDInvalid, got %v", err)
	}
}
