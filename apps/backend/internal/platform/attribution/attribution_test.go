package attribution

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 24, 18, 0, 0, 0, time.UTC)

func TestCodeShape(t *testing.T) {
	if len(CodeAlphabet) != 31 {
		t.Fatalf("alphabet has %d characters, want 31", len(CodeAlphabet))
	}
	for _, bad := range "0o1li" {
		if strings.ContainsRune(CodeAlphabet, bad) {
			t.Errorf("alphabet must not contain %q", bad)
		}
	}
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"x7k2qpm", true},
		{"abcdefg", true},
		{"X7K2QPM", false}, // IsCode is case-sensitive
		{"x7k2qp", false},
		{"x7k2qpmm", false},
		{"x7k2qp0", false},
		{"x7k2qpl", false},
		{"", false},
	} {
		if got := IsCode(c.in); got != c.want {
			t.Errorf("IsCode(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if got, ok := NormalizeRef("  X7K2QPM "); !ok || got != "x7k2qpm" {
		t.Errorf("NormalizeRef = %q %v", got, ok)
	}
	if _, ok := NormalizeRef("x7k2qp1"); ok {
		t.Error("a code with a digit 1 must be refused")
	}
}

func TestClean(t *testing.T) {
	landed := testNow.Add(-2 * time.Hour).Format(time.RFC3339)
	long := strings.Repeat("a", 65)
	for _, c := range []struct {
		name  string
		in    string
		ok    bool
		check func(t *testing.T, a Attribution)
	}{
		{
			name: "full valid object",
			in: `{"ref":"X7K2QPM","utm_source":"qr","utm_medium":"offline","utm_campaign":"concert-2026",` +
				`"utm_content":"x7k2qpm","utm_term":"","landed_at":"` + landed + `","extra":123,` +
				`"first":{"ref":"abcdefg","utm_source":"social","landed_at":"` + landed + `"}}`,
			ok: true,
			check: func(t *testing.T, a Attribution) {
				if a.Ref != "x7k2qpm" || a.UTMSource != "qr" || a.UTMMedium != "offline" ||
					a.UTMCampaign != "concert-2026" || a.UTMContent != "x7k2qpm" {
					t.Errorf("touch = %+v", a.Touch)
				}
				if !a.LandedAt.Equal(testNow.Add(-2 * time.Hour)) {
					t.Errorf("landed_at = %v", a.LandedAt)
				}
				if a.First == nil || a.First.Ref != "abcdefg" || a.First.UTMSource != "social" {
					t.Errorf("first = %+v", a.First)
				}
			},
		},
		{
			name: "utm with a bad character is dropped whole, the rest stays",
			in:   `{"utm_source":"qr","utm_campaign":"a b","utm_medium":"off/line","landed_at":"` + landed + `"}`,
			ok:   true,
			check: func(t *testing.T, a Attribution) {
				if a.UTMSource != "qr" || a.UTMCampaign != "" || a.UTMMedium != "" {
					t.Errorf("touch = %+v", a.Touch)
				}
			},
		},
		{
			name: "invalid ref is dropped, utm source keeps the touch",
			in:   `{"ref":"not-a-code","utm_source":"qr"}`,
			ok:   true,
			check: func(t *testing.T, a Attribution) {
				if a.Ref != "" || a.UTMSource != "qr" {
					t.Errorf("touch = %+v", a.Touch)
				}
			},
		},
		{name: "invalid ref and nothing else is not a touch", in: `{"ref":"nope"}`, ok: false},
		{name: "only medium is not a touch", in: `{"utm_medium":"offline"}`, ok: false},
		{name: "campaign alone is a touch", in: `{"utm_campaign":"c1"}`, ok: true},
		{name: "utm over 64 characters is cut like pagestats does", in: `{"utm_source":"` + long + `","ref":"x7k2qpm"}`, ok: true,
			check: func(t *testing.T, a Attribution) {
				if a.UTMSource != long[:64] || a.Ref != "x7k2qpm" {
					t.Errorf("touch = %+v", a.Touch)
				}
			}},
		{name: "utm far over the limit is dropped", in: `{"utm_source":"` + strings.Repeat("a", 300) + `","ref":"x7k2qpm"}`, ok: true,
			check: func(t *testing.T, a Attribution) {
				if a.UTMSource != "" {
					t.Errorf("touch = %+v", a.Touch)
				}
			}},
		{name: "too old drops the touch", in: `{"ref":"x7k2qpm","landed_at":"` + testNow.Add(-Window-time.Second).Format(time.RFC3339) + `"}`, ok: false},
		{name: "exactly at the window edge is kept", in: `{"ref":"x7k2qpm","landed_at":"` + testNow.Add(-Window).Format(time.RFC3339) + `"}`, ok: true},
		{name: "future beyond the skew drops the touch", in: `{"ref":"x7k2qpm","landed_at":"` + testNow.Add(FutureSkew+time.Second).Format(time.RFC3339) + `"}`, ok: false},
		{name: "future within the skew is kept and clamped", in: `{"ref":"x7k2qpm","landed_at":"` + testNow.Add(time.Minute).Format(time.RFC3339) + `"}`, ok: true,
			check: func(t *testing.T, a Attribution) {
				if !a.LandedAt.Equal(testNow) {
					t.Errorf("landed_at = %v, want clamped to now", a.LandedAt)
				}
			}},
		{name: "missing landed_at is set to now", in: `{"ref":"x7k2qpm"}`, ok: true,
			check: func(t *testing.T, a Attribution) {
				if !a.LandedAt.Equal(testNow) {
					t.Errorf("landed_at = %v, want now", a.LandedAt)
				}
			}},
		{name: "unparseable landed_at drops the touch", in: `{"ref":"x7k2qpm","landed_at":"yesterday"}`, ok: false},
		{name: "landed_at with an offset is converted to UTC", in: `{"ref":"x7k2qpm","landed_at":"2026-10-24T19:00:00+02:00"}`, ok: true,
			check: func(t *testing.T, a Attribution) {
				if !a.LandedAt.Equal(time.Date(2026, 10, 24, 17, 0, 0, 0, time.UTC)) || a.LandedAt.Location() != time.UTC {
					t.Errorf("landed_at = %v", a.LandedAt)
				}
			}},
		{name: "first too old is dropped alone", in: `{"ref":"x7k2qpm","first":{"ref":"abcdefg","landed_at":"` +
			testNow.Add(-10*24*time.Hour).Format(time.RFC3339) + `"}}`, ok: true,
			check: func(t *testing.T, a Attribution) {
				if a.First != nil || a.Ref != "x7k2qpm" {
					t.Errorf("attribution = %+v first=%+v", a, a.First)
				}
			}},
		{name: "first without ref or source is dropped", in: `{"ref":"x7k2qpm","first":{"utm_campaign":"c"}}`, ok: true,
			check: func(t *testing.T, a Attribution) {
				if a.First != nil {
					t.Errorf("first = %+v", a.First)
				}
			}},
		{name: "wrong types are ignored", in: `{"ref":5,"utm_source":["qr"],"utm_campaign":"c","first":"x"}`, ok: true,
			check: func(t *testing.T, a Attribution) {
				if a.Ref != "" || a.UTMSource != "" || a.UTMCampaign != "c" || a.First != nil {
					t.Errorf("attribution = %+v", a)
				}
			}},
		{name: "empty object", in: `{}`, ok: false},
		{name: "null", in: `null`, ok: false},
		{name: "array", in: `[1]`, ok: false},
		{name: "string", in: `"x7k2qpm"`, ok: false},
		{name: "invalid json", in: `{"ref":`, ok: false},
		{name: "empty input", in: ``, ok: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			a, ok := Clean([]byte(c.in), testNow)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (attribution %+v)", ok, c.ok, a)
			}
			if !ok {
				if !a.IsZero() || !a.LandedAt.IsZero() {
					t.Errorf("a refused object must be zero, got %+v", a)
				}
				return
			}
			if c.check != nil {
				c.check(t, a)
			}
		})
	}
}

func TestMergeTouch(t *testing.T) {
	t0 := testNow.Add(-48 * time.Hour)
	t1 := testNow.Add(-time.Hour)
	qr := Attribution{Touch: Touch{Ref: "x7k2qpm", UTMSource: "qr", LandedAt: t0}}
	soc := Attribution{Touch: Touch{Ref: "abcdefg", UTMSource: "social", LandedAt: t1}}

	t.Run("a different source keeps the earlier touch as first", func(t *testing.T) {
		got := MergeTouch(qr, soc)
		if got.Ref != "abcdefg" || got.UTMSource != "social" || !got.LandedAt.Equal(t1) {
			t.Errorf("last touch = %+v", got.Touch)
		}
		if got.First == nil || got.First.Ref != "x7k2qpm" || got.First.UTMSource != "qr" || !got.First.LandedAt.Equal(t0) {
			t.Errorf("first = %+v", got.First)
		}
	})
	t.Run("an existing first survives a third touch", func(t *testing.T) {
		prev := soc
		prev.First = &First{Ref: "x7k2qpm", UTMSource: "qr", LandedAt: t0}
		next := Attribution{Touch: Touch{UTMSource: "email", LandedAt: testNow}}
		got := MergeTouch(prev, next)
		if got.First == nil || got.First.Ref != "x7k2qpm" {
			t.Errorf("first = %+v", got.First)
		}
		if got.UTMSource != "email" {
			t.Errorf("touch = %+v", got.Touch)
		}
	})
	t.Run("the same source keeps first and nothing new", func(t *testing.T) {
		prev := qr
		prev.First = &First{Ref: "abcdefg", UTMSource: "social", LandedAt: t0.Add(-time.Hour)}
		next := Attribution{Touch: Touch{Ref: "x7k2qpm", UTMSource: "qr", LandedAt: t1}}
		got := MergeTouch(prev, next)
		if got.First == nil || got.First.Ref != "abcdefg" {
			t.Errorf("first = %+v", got.First)
		}
		if !got.LandedAt.Equal(t1) {
			t.Errorf("last touch must be next: %+v", got.Touch)
		}
		again := MergeTouch(qr, Attribution{Touch: Touch{Ref: "x7k2qpm", UTMSource: "qr", LandedAt: t1}})
		if again.First != nil {
			t.Errorf("same source and no earlier first must not invent one: %+v", again.First)
		}
	})
	t.Run("nothing earlier returns next as it is", func(t *testing.T) {
		got := MergeTouch(Attribution{}, soc)
		if got.First != nil || got.Ref != "abcdefg" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("the result does not alias prev", func(t *testing.T) {
		prev := qr
		prev.First = &First{Ref: "abcdefg"}
		got := MergeTouch(prev, soc)
		got.First.Ref = "mutated"
		if prev.First.Ref != "abcdefg" {
			t.Error("MergeTouch must copy First")
		}
	})
}

func TestJSONRoundTrip(t *testing.T) {
	if got := string((Attribution{}).JSON()); got != "{}" {
		t.Errorf("zero JSON = %s", got)
	}
	a := Attribution{
		Touch: Touch{Ref: "x7k2qpm", UTMSource: "qr", LandedAt: time.Date(2026, 10, 24, 18, 2, 11, 500, time.UTC)},
		First: &First{Ref: "abcdefg", UTMSource: "social", LandedAt: time.Date(2026, 10, 20, 9, 0, 0, 0, time.FixedZone("x", 7200))},
	}
	b := a.JSON()
	want := `{"ref":"x7k2qpm","utm_source":"qr","landed_at":"2026-10-24T18:02:11Z","first":{"ref":"abcdefg","utm_source":"social","landed_at":"2026-10-20T07:00:00Z"}}`
	if string(b) != want {
		t.Fatalf("JSON =\n%s\nwant\n%s", b, want)
	}
	var back Attribution
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Ref != "x7k2qpm" || back.First == nil || back.First.Ref != "abcdefg" ||
		!back.LandedAt.Equal(time.Date(2026, 10, 24, 18, 2, 11, 0, time.UTC)) {
		t.Errorf("round trip = %+v first=%+v", back, back.First)
	}

	// Omitted fields stay omitted, and a zero landed_at is not written.
	only := Attribution{Touch: Touch{UTMCampaign: "c1"}}
	if got := string(only.JSON()); got != `{"utm_campaign":"c1"}` {
		t.Errorf("omitempty: %s", got)
	}
	// Touch and First marshal on their own as well.
	tb, _ := json.Marshal(Touch{Ref: "x7k2qpm"})
	if string(tb) != `{"ref":"x7k2qpm"}` {
		t.Errorf("Touch JSON = %s", tb)
	}
	// A cleaned object re-cleans to itself (stored form is a valid input).
	cleaned, ok := Clean(b, testNow.Add(10*time.Minute))
	if !ok {
		t.Fatal("stored form must be a valid input")
	}
	if string(cleaned.JSON()) != string(b) {
		t.Errorf("re-clean changed the object:\n%s\n%s", cleaned.JSON(), b)
	}
}
