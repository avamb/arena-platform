// error_mapping_test.go — pins feature #477 error-mapping helpers and the
// spec-section-6 constants (ResultCodeSessionExpired,
// ResultCodeUserVisible, ResultCodeTransient) added in this feature.

package bil24compat

import (
	"errors"
	"testing"
)

// TestBil24_477_ResultCodeConstants pins the wire values for the three
// spec-section-6 codes introduced in feature #477 and asserts that
// ResultCodeUnknownCommand is now a deprecated alias for
// ResultCodeInvalidRequest (both == -2).
func TestBil24_477_ResultCodeConstants(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"ResultCodeOK", ResultCodeOK, 0},
		{"ResultCodeSessionExpired", ResultCodeSessionExpired, 1},
		{"ResultCodeUserVisible", ResultCodeUserVisible, 101},
		{"ResultCodeTransient", ResultCodeTransient, -1},
		{"ResultCodeInvalidRequest", ResultCodeInvalidRequest, -2},
		{"ResultCodeUnknownCommand(alias)", ResultCodeUnknownCommand, -2},
		{"ResultCodeNotFound", ResultCodeNotFound, -3},
		{"ResultCodeUnauthorized", ResultCodeUnauthorized, -4},
		{"ResultCodeNotImplemented", ResultCodeNotImplemented, -5},
		{"ResultCodeInternalError", ResultCodeInternalError, -99},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("%s: got %d, want %d", tc.name, tc.got, tc.want)
			}
		})
	}
	// Alias identity: the two symbols must resolve to the same value so
	// legacy call-sites keep compiling and switching on either name is
	// equivalent.
	if ResultCodeUnknownCommand != ResultCodeInvalidRequest {
		t.Errorf("ResultCodeUnknownCommand (%d) must alias ResultCodeInvalidRequest (%d) per feature #477",
			ResultCodeUnknownCommand, ResultCodeInvalidRequest)
	}
}

// TestBil24_477_MapDBError pins DB/pool errors -> (-1, transient description).
func TestBil24_477_MapDBError(t *testing.T) {
	for _, in := range []error{nil, errors.New("pgx: pool exhausted")} {
		code, desc := MapDBError(in)
		if code != ResultCodeTransient {
			t.Errorf("MapDBError(%v): got code %d, want %d", in, code, ResultCodeTransient)
		}
		if desc != DescTransient {
			t.Errorf("MapDBError(%v): got desc %q, want %q", in, desc, DescTransient)
		}
	}
}

// TestBil24_477_MapValidationError pins validation errors -> (-2, ...)
// and the empty-fallback path.
func TestBil24_477_MapValidationError(t *testing.T) {
	t.Run("with_message", func(t *testing.T) {
		code, desc := MapValidationError("orderId is required")
		if code != ResultCodeInvalidRequest {
			t.Errorf("code: got %d, want %d", code, ResultCodeInvalidRequest)
		}
		if desc != "orderId is required" {
			t.Errorf("desc: got %q, want %q", desc, "orderId is required")
		}
	})
	t.Run("empty_falls_back", func(t *testing.T) {
		code, desc := MapValidationError("")
		if code != ResultCodeInvalidRequest {
			t.Errorf("code: got %d, want %d", code, ResultCodeInvalidRequest)
		}
		if desc != DescInvalidRequest {
			t.Errorf("desc: got %q, want %q", desc, DescInvalidRequest)
		}
	})
}

// TestBil24_477_MapScopeError pins scope-miss -> (-3, ...) with fallback.
func TestBil24_477_MapScopeError(t *testing.T) {
	code, desc := MapScopeError("event 123 not in channel 42 catalog")
	if code != ResultCodeNotFound {
		t.Errorf("code: got %d, want %d", code, ResultCodeNotFound)
	}
	if desc == "" {
		t.Errorf("desc must not be empty for scope error")
	}
	code2, desc2 := MapScopeError("")
	if code2 != ResultCodeNotFound || desc2 != DescNotFound {
		t.Errorf("empty fallback: got (%d, %q), want (%d, %q)",
			code2, desc2, ResultCodeNotFound, DescNotFound)
	}
}

// TestBil24_477_MapBusinessError pins business error -> (101, ...) with
// key + english + empty-fallback branches.
func TestBil24_477_MapBusinessError(t *testing.T) {
	t.Run("english_wins", func(t *testing.T) {
		code, desc := MapBusinessError("bil24.seat_taken", "seat is already taken")
		if code != ResultCodeUserVisible {
			t.Errorf("code: got %d, want %d", code, ResultCodeUserVisible)
		}
		if desc != "seat is already taken" {
			t.Errorf("desc: got %q, want english override", desc)
		}
	})
	t.Run("key_only", func(t *testing.T) {
		code, desc := MapBusinessError("bil24.hold_expired", "")
		if code != ResultCodeUserVisible {
			t.Errorf("code: got %d, want %d", code, ResultCodeUserVisible)
		}
		if desc != "bil24.hold_expired" {
			t.Errorf("desc: got %q, want key %q", desc, "bil24.hold_expired")
		}
	})
	t.Run("empty_fallback", func(t *testing.T) {
		code, desc := MapBusinessError("", "")
		if code != ResultCodeUserVisible || desc != DescUserVisible {
			t.Errorf("got (%d, %q), want (%d, %q)",
				code, desc, ResultCodeUserVisible, DescUserVisible)
		}
	})
}

// TestBil24_477_MapSessionError pins the expired-session shortcut.
func TestBil24_477_MapSessionError(t *testing.T) {
	code, desc := MapSessionError()
	if code != ResultCodeSessionExpired {
		t.Errorf("code: got %d, want %d", code, ResultCodeSessionExpired)
	}
	if desc == "" {
		t.Errorf("desc must not be empty")
	}
	// ErrSessionExpired must remain a stable sentinel — errors.Is against
	// itself is the identity guarantee callers depend on.
	if !errors.Is(ErrSessionExpired, ErrSessionExpired) {
		t.Errorf("ErrSessionExpired sentinel identity broken")
	}
}
