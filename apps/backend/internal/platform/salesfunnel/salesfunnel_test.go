package salesfunnel

import (
	"strings"
	"testing"
	"time"
)

func TestParseStep_OnlyTheThreeClientSteps(t *testing.T) {
	for _, s := range []string{"view", "session_open", "seats_chosen"} {
		if got, ok := ParseStep(s); !ok || got != s {
			t.Errorf("ParseStep(%q) = %q, %v; want accepted", s, got, ok)
		}
	}
	// The server steps are counted from the orders: a beacon must not write them.
	for _, s := range []string{"", "checkout_start", "payment_open", "paid", "View", "page_view", " view"} {
		if _, ok := ParseStep(s); ok {
			t.Errorf("ParseStep(%q) accepted; want refused", s)
		}
	}
}

func TestParseSurface(t *testing.T) {
	for _, s := range Surfaces {
		if _, ok := ParseSurface(s); !ok {
			t.Errorf("ParseSurface(%q) refused", s)
		}
	}
	for _, s := range []string{"", "web", "Widget", "tickets"} {
		if _, ok := ParseSurface(s); ok {
			t.Errorf("ParseSurface(%q) accepted", s)
		}
	}
}

func TestSteps_AreTheSixInFunnelOrder(t *testing.T) {
	want := "view,session_open,seats_chosen,checkout_start,payment_open,paid"
	if got := strings.Join(Steps, ","); got != want {
		t.Errorf("Steps = %s; want %s", got, want)
	}
}

func TestLimitKey_IsBoundedAndSeparatesContexts(t *testing.T) {
	long := strings.Repeat("A", 5000)
	if k := LimitKey(long, "", ""); len(k) > MaxTokenLength+len("funnel/t/") {
		t.Errorf("token key %d bytes; want it cut", len(k))
	}
	if k := LimitKey("", long, long); len(k) > 2*MaxSlugLength+len("funnel/p//") {
		t.Errorf("page key %d bytes; want it cut", len(k))
	}
	if LimitKey("tok", "", "") == LimitKey("", "tok", "") {
		t.Error("a token and a slug must not share a bucket")
	}
	if LimitKey("", "Acme", "Gala") != LimitKey("", "acme", "gala") {
		t.Error("slugs are matched case-insensitively")
	}
}

func TestDeduper_SameVisitorSameCountOnceADay(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	d := &Deduper{now: func() time.Time { return now }}
	if d.SeenToday("1.2.3.4", "UA", "ev", "", "view", "widget") {
		t.Fatal("the first beacon of the day must count")
	}
	if !d.SeenToday("1.2.3.4", "UA", "ev", "", "view", "widget") {
		t.Fatal("the same beacon again must be dropped")
	}
	// Another visitor, another step, another event are separate counts.
	for name, args := range map[string][]string{
		"other ip":    {"5.6.7.8", "UA", "ev", "", "view", "widget"},
		"other agent": {"1.2.3.4", "UB", "ev", "", "view", "widget"},
		"other step":  {"1.2.3.4", "UA", "ev", "", "session_open", "widget"},
		"other event": {"1.2.3.4", "UA", "ev2", "", "view", "widget"},
	} {
		if d.SeenToday(args[0], args[1], args[2:]...) {
			t.Errorf("%s was dropped as a duplicate", name)
		}
	}
	// A failed write is undone, so the retry counts.
	d.Forget("1.2.3.4", "UA", "ev", "", "view", "widget")
	if d.SeenToday("1.2.3.4", "UA", "ev", "", "view", "widget") {
		t.Error("after Forget the beacon must count again")
	}
}

func TestDeduper_NewDayStartsOver(t *testing.T) {
	now := time.Date(2026, 11, 1, 23, 59, 0, 0, time.UTC)
	d := &Deduper{now: func() time.Time { return now }}
	d.SeenToday("ip", "ua", "x")
	now = now.Add(2 * time.Minute)
	if d.SeenToday("ip", "ua", "x") {
		t.Error("the next UTC day must count the visitor again")
	}
}

func TestDeduper_ZeroValueWorksAndIsBounded(t *testing.T) {
	var d Deduper
	if d.SeenToday("ip", "ua", "a") {
		t.Fatal("zero Deduper must work")
	}
	d.mu.Lock()
	for i := 0; i < MaxDedupeEntries; i++ {
		var k [16]byte
		k[0], k[1], k[2] = byte(i), byte(i>>8), byte(i>>16)
		k[3] = 1
		d.seen[k] = struct{}{}
	}
	d.mu.Unlock()
	d.SeenToday("ip", "ua", "b")
	d.mu.Lock()
	n := len(d.seen)
	d.mu.Unlock()
	if n > MaxDedupeEntries {
		t.Errorf("the filter holds %d entries; want at most %d", n, MaxDedupeEntries)
	}
}
