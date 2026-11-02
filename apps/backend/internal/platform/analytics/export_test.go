package analytics

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/attribution"
)

func TestContactCode_AlphabetMatchesSalesLinksAndShapeHolds(t *testing.T) {
	if ContactCodeAlphabet != attribution.CodeAlphabet {
		t.Fatalf("contact code alphabet drifted from the sales-link alphabet: %q", ContactCodeAlphabet)
	}
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		c, err := NewContactCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(c) != ContactCodeLen || !IsContactCode(c) {
			t.Fatalf("bad code %q", c)
		}
		seen[c] = true
	}
	if len(seen) < 1990 {
		t.Errorf("only %d distinct codes out of 2000", len(seen))
	}
}

func TestContactCode_EveryAlphabetCharacterIsDrawn(t *testing.T) {
	got := map[rune]bool{}
	for i := 0; i < 3000 && len(got) < len(ContactCodeAlphabet); i++ {
		c, _ := NewContactCode()
		for _, r := range c {
			got[r] = true
		}
	}
	if len(got) != len(ContactCodeAlphabet) {
		t.Errorf("drew %d of %d characters", len(got), len(ContactCodeAlphabet))
	}
}

func TestIsContactCode_RejectsForeignShapes(t *testing.T) {
	for _, s := range []string{"", "short", "abcdefghjk1", "abcdefghj0", "ABCDEFGHJK", "abcdefghj!", "abcdefghjkm"} {
		if IsContactCode(s) {
			t.Errorf("%q accepted", s)
		}
	}
	if !IsContactCode("x7k2qpm3ab") {
		t.Error("a valid code was refused")
	}
}

func TestValidTwentyPersonID(t *testing.T) {
	for _, ok := range []string{"2b0f4b1e-8a4e-4a47-a2a0-1f0c6f0a9d11", "abc_DEF.1:2-3"} {
		if !ValidTwentyPersonID(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", " ", "a b", "a@b.c", strings.Repeat("a", 65), "x/y", "x;y"} {
		if ValidTwentyPersonID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestProfileCursor_RoundTripAndRejectsGarbage(t *testing.T) {
	c := ProfileCursor{ChangedAt: time.Date(2026, 11, 2, 1, 2, 3, 456789000, time.UTC), PersonID: uuid.New()}
	got, err := DecodeProfileCursor(c.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if !got.ChangedAt.Equal(c.ChangedAt) || got.PersonID != c.PersonID {
		t.Errorf("round trip: %+v != %+v", got, c)
	}
	for _, bad := range []string{"", "!!!", "aGVsbG8", "MTIzLm5vdC1hLXV1aWQ"} {
		if _, err := DecodeProfileCursor(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTouchLabel(t *testing.T) {
	cases := []struct {
		in   Touch
		want string
	}{
		{Touch{}, ""},
		{Touch{Source: "email", Kind: "email_click", Campaign: "outreach-nov"}, "email / outreach-nov"},
		{Touch{Source: "google", Kind: "organic"}, "google"},
		{Touch{Source: "direct", Kind: "organic"}, "direct"},
		{Touch{Kind: "referral", Campaign: "x"}, "referral / x"},
	}
	for _, c := range cases {
		if got := TouchLabel(c.in); got != c.want {
			t.Errorf("TouchLabel(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDecodeMilestones_SplitsTimesFromGMV(t *testing.T) {
	ms, gmv := decodeMilestones([]byte(`{"application_started":"2026-10-01T10:00:00Z","first_sale":"2026-10-20T08:30:00Z","gmv_30d":{"EUR":12500},"bad":5}`))
	if len(ms) != 2 || ms[MsFirstSale].IsZero() || ms[MsApplicationStarted].IsZero() {
		t.Errorf("milestones = %v", ms)
	}
	if gmv["EUR"] != 12500 {
		t.Errorf("gmv = %v", gmv)
	}
	ms, gmv = decodeMilestones([]byte(`not json`))
	if len(ms) != 0 || gmv != nil {
		t.Errorf("garbage gave %v %v", ms, gmv)
	}
}

func TestDecodeArticles(t *testing.T) {
	got := decodeArticles([]byte(`[{"path":"/blog/a","at":"2026-10-01T10:00:00Z","seconds":75}]`))
	if len(got) != 1 || got[0].Path != "/blog/a" || got[0].Seconds != 75 || got[0].At.IsZero() {
		t.Errorf("articles = %+v", got)
	}
	if decodeArticles([]byte(`oops`)) != nil || decodeArticles(nil) != nil {
		t.Error("garbage must give nil")
	}
}

func TestExplainStored_ListsStoredItemsAndShowsTheRemainder(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	w := DefaultScoreWeights
	sessions := []storedSession{{Start: now, EmailClick: true}, {Start: now}}
	articles := []ArticleRead{{Path: "/blog/a", At: now, Seconds: 80}}
	// visit x2 = 2, email_click = 2, article = 3: stored score 7 is whole.
	text := explainStored(storedProfile{Score: 7, Articles: articles}, sessions, now, w)
	for _, part := range []string{"article x1 = 3.0", "visit x2 = 2.0", "email_click x1 = 2.0"} {
		if !strings.Contains(text, part) {
			t.Errorf("%q lacks %q", text, part)
		}
	}
	if strings.Contains(text, "price page") {
		t.Errorf("nothing is missing, but the text says so: %q", text)
	}
	// A stored score of 12 means a price-page visit (5) that is not stored per visit.
	text = explainStored(storedProfile{Score: 12, Articles: articles}, sessions, now, w)
	if !strings.Contains(text, "price page and other = +5") {
		t.Errorf("remainder missing: %q", text)
	}
	if got := explainStored(storedProfile{Score: 0}, nil, now, w); got != "no activity" {
		t.Errorf("empty = %q", got)
	}
	if got := explainStored(storedProfile{Score: 5}, nil, now, w); got != "price page and other = +5" {
		t.Errorf("only a remainder = %q", got)
	}
}
