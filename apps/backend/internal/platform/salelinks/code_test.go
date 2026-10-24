package salelinks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/attribution"
)

func TestNewCode_AlphabetAndLength(t *testing.T) {
	seen := map[byte]bool{}
	for i := 0; i < 2000; i++ {
		c, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != attribution.CodeLen || !attribution.IsCode(c) {
			t.Fatalf("bad code %q", c)
		}
		for j := 0; j < len(c); j++ {
			seen[c[j]] = true
		}
	}
	// 14000 draws over 31 symbols: every symbol appears (p of a miss is negligible).
	if len(seen) != len(attribution.CodeAlphabet) {
		t.Errorf("only %d of %d symbols drawn", len(seen), len(attribution.CodeAlphabet))
	}
}

func TestNormalizeCode(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"x7k2qpm", "x7k2qpm", true},
		{"X7K2QPM", "x7k2qpm", true},
		{"x7k2qpm?utm_source=a", "x7k2qpm", true},
		{"x7k2qpm/extra", "x7k2qpm", true},
		{"  x7k2qpm  ", "x7k2qpm", true},
		{"x7k2qpmzzzz", "x7k2qpm", true}, // a longer string is cut to 7 on purpose
		{"x7k2qpé", "", false},           // é is 2 bytes: cut mid-rune it is not a code
		{"x7k2qpmé", "x7k2qpm", true},    // the rune starts after the 7th byte and is cut off whole
		{"éx7k2qp", "", false},
		{"x7k2qp", "", false},
		{"x7k2qp0", "", false},
		{"", "", false},
		{"?x7k2qpm", "", false},
		{"/x7k2qpm", "", false},
	} {
		got, ok := NormalizeCode(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("NormalizeCode(%q) = %q %v, want %q %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

type fakeInserter struct {
	refuse int // refuse this many first inserts
	calls  []string
	err    error
}

func (f *fakeInserter) Insert(_ context.Context, code string) (bool, error) {
	f.calls = append(f.calls, code)
	if f.err != nil {
		return false, f.err
	}
	if len(f.calls) <= f.refuse {
		return false, nil
	}
	return true, nil
}

func TestMint(t *testing.T) {
	t.Run("redraws after refusals", func(t *testing.T) {
		ins := &fakeInserter{refuse: 3}
		code, err := Mint(context.Background(), ins)
		if err != nil {
			t.Fatal(err)
		}
		if len(ins.calls) != 4 || code != ins.calls[3] {
			t.Fatalf("calls=%v code=%s", ins.calls, code)
		}
		seen := map[string]bool{}
		for _, c := range ins.calls {
			if seen[c] {
				t.Errorf("code %s was drawn twice", c)
			}
			seen[c] = true
		}
	})
	t.Run("last attempt still counts", func(t *testing.T) {
		ins := &fakeInserter{refuse: MaxMintAttempts - 1}
		if _, err := Mint(context.Background(), ins); err != nil {
			t.Fatalf("attempt %d must be allowed: %v", MaxMintAttempts, err)
		}
		if len(ins.calls) != MaxMintAttempts {
			t.Errorf("calls = %d", len(ins.calls))
		}
	})
	t.Run("gives up after the limit", func(t *testing.T) {
		ins := &fakeInserter{refuse: 1000}
		_, err := Mint(context.Background(), ins)
		if !errors.Is(err, ErrMintExhausted) {
			t.Fatalf("err = %v", err)
		}
		if len(ins.calls) != MaxMintAttempts {
			t.Errorf("calls = %d, want %d", len(ins.calls), MaxMintAttempts)
		}
	})
	t.Run("an insert error stops at once", func(t *testing.T) {
		boom := errors.New("boom")
		ins := &fakeInserter{err: boom}
		_, err := Mint(context.Background(), ins)
		if !errors.Is(err, boom) || len(ins.calls) != 1 {
			t.Fatalf("err=%v calls=%d", err, len(ins.calls))
		}
		if strings.Contains(err.Error(), "exhausted") {
			t.Error("a real error must not look like exhaustion")
		}
	})
}
