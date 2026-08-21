package pricing

import (
	"testing"
	"time"
)

func tp(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func tpp(s string) *time.Time { t := tp(s); return &t }

// TestResolve pins the AB-48 resolution contract: containing window
// wins, gaps fall back to base, and next-change reporting covers both
// "window ends" and "window begins" boundaries.
func TestResolve(t *testing.T) {
	t.Parallel()

	earlyBird := Window{From: tp("2026-01-01T00:00:00Z"), To: tpp("2026-02-01T00:00:00Z"), Amount: 1000}
	standard := Window{From: tp("2026-02-01T00:00:00Z"), To: tpp("2026-03-01T00:00:00Z"), Amount: 2000}
	lastCall := Window{From: tp("2026-04-01T00:00:00Z"), To: nil, Amount: 3000}
	windows := []Window{earlyBird, standard, lastCall}

	cases := []struct {
		name       string
		at         string
		wantAmount int64
		wantNext   *time.Time
	}{
		{"before any window falls back to base, next = first start",
			"2025-12-01T00:00:00Z", 500, tpp("2026-01-01T00:00:00Z")},
		{"inside early-bird, next = its end",
			"2026-01-15T00:00:00Z", 1000, tpp("2026-02-01T00:00:00Z")},
		{"boundary instant belongs to the LATER window ('[)')",
			"2026-02-01T00:00:00Z", 2000, tpp("2026-03-01T00:00:00Z")},
		{"gap between windows falls back to base, next = next start",
			"2026-03-15T00:00:00Z", 500, tpp("2026-04-01T00:00:00Z")},
		{"inside open-ended window, no known change",
			"2026-05-01T00:00:00Z", 3000, nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, next := Resolve(500, windows, tp(tc.at))
			if got != tc.wantAmount {
				t.Fatalf("amount = %d, want %d", got, tc.wantAmount)
			}
			switch {
			case tc.wantNext == nil && next != nil:
				t.Fatalf("next = %v, want nil", next)
			case tc.wantNext != nil && (next == nil || !next.Equal(*tc.wantNext)):
				t.Fatalf("next = %v, want %v", next, tc.wantNext)
			}
		})
	}
}

// TestResolve_NoWindows pins the trivial fallback.
func TestResolve_NoWindows(t *testing.T) {
	t.Parallel()
	got, next := Resolve(1234, nil, tp("2026-01-01T00:00:00Z"))
	if got != 1234 || next != nil {
		t.Fatalf("got (%d, %v), want (1234, nil)", got, next)
	}
}
