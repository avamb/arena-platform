package money

import (
	"encoding/json"
	"math"
	"testing"
)

func TestMajor(t *testing.T) {
	cases := []struct {
		minor int64
		want  float64
	}{
		{0, 0},
		{1, 0.01},
		{95, 0.95},
		{1890, 18.9},
		{1985, 19.85},
		{50000, 500},
		{52500, 525},
		{-1250, -12.5},
		{123456789, 1234567.89},
	}
	for _, c := range cases {
		if got := Major(c.minor); got != c.want {
			t.Errorf("Major(%d) = %v, want %v", c.minor, got, c.want)
		}
	}
}

// TestMajor_JSONRendering is the wire-level assertion of spec §2.1: no
// trailing zeros, no float dust.
func TestMajor_JSONRendering(t *testing.T) {
	cases := map[int64]string{
		1890:  "18.9",
		1985:  "19.85",
		52500: "525",
		0:     "0",
	}
	for minor, want := range cases {
		b, err := json.Marshal(Major(minor))
		if err != nil {
			t.Fatalf("marshal %d: %v", minor, err)
		}
		if string(b) != want {
			t.Errorf("json(Major(%d)) = %s, want %s", minor, b, want)
		}
	}
}

func TestMinor_RoundsHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		major float64
		want  int64
	}{
		{0, 0},
		{0.005, 1},
		{-0.005, -1},
		{0.014, 1},
		{18.9, 1890},
		{19.85, 1985},
		{525, 52500},
		{24.999999, 2500},
		{-12.5, -1250},
	}
	for _, c := range cases {
		if got := Minor(c.major); got != c.want {
			t.Errorf("Minor(%v) = %d, want %d", c.major, got, c.want)
		}
	}
}

// TestMajorMinor_Reversible proves the round trip is lossless for every amount
// the wire may legally carry (at most two decimals).
func TestMajorMinor_Reversible(t *testing.T) {
	for minor := int64(-10000); minor <= 10000; minor++ {
		if got := Minor(Major(minor)); got != minor {
			t.Fatalf("Minor(Major(%d)) = %d", minor, got)
		}
	}
	for _, x := range []float64{0, 0.01, 0.99, 1.05, 18.9, 19.85, 525, 1234.56} {
		if got := Major(Minor(x)); got != x {
			t.Errorf("Major(Minor(%v)) = %v", x, got)
		}
	}
}

func TestScaleFor(t *testing.T) {
	cases := map[string]int64{
		"":      DefaultScale,
		"CZK":   100,
		"eur":   100,
		"ILS":   100,
		" usd ": 100,
		"JPY":   1,
		"ISK":   1,
		"huf":   1,
	}
	for cur, want := range cases {
		if got := ScaleFor(cur); got != want {
			t.Errorf("ScaleFor(%q) = %d, want %d", cur, got, want)
		}
	}
}

func TestScaledHelpers(t *testing.T) {
	if got := MajorScale(1890, ScaleFor("JPY")); got != 1890 {
		t.Errorf("MajorScale(1890, JPY) = %v, want 1890", got)
	}
	if got := MinorScale(1890.4, ScaleFor("JPY")); got != 1890 {
		t.Errorf("MinorScale(1890.4, JPY) = %d, want 1890", got)
	}
	if got := MajorScale(1890, ScaleFor("CZK")); got != 18.9 {
		t.Errorf("MajorScale(1890, CZK) = %v, want 18.9", got)
	}
}

func TestPtrHelpers(t *testing.T) {
	if MajorPtr(nil) != nil || MinorPtr(nil) != nil {
		t.Fatal("nil input must yield nil output")
	}
	minor := int64(1985)
	if got := MajorPtr(&minor); got == nil || *got != 19.85 {
		t.Errorf("MajorPtr(1985) = %v, want 19.85", got)
	}
	major := 19.85
	if got := MinorPtr(&major); got == nil || *got != 1985 {
		t.Errorf("MinorPtr(19.85) = %v, want 1985", got)
	}
}

func TestRoundMinor(t *testing.T) {
	cases := []struct {
		in   float64
		want int64
	}{
		{94.5, 95},
		{94.4, 94},
		{-94.5, -95},
		{2500, 2500},
	}
	for _, c := range cases {
		if got := RoundMinor(c.in); got != c.want {
			t.Errorf("RoundMinor(%v) = %d, want %d", c.in, got, c.want)
		}
	}
	if RoundMinor(math.Trunc(0)) != 0 {
		t.Error("RoundMinor(0) must be 0")
	}
}
