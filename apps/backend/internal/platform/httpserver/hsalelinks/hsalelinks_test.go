package hsalelinks

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

func TestParseUTM(t *testing.T) {
	ok := []struct{ in, key, want string }{
		{`{"utm_source":"print"}`, "utm_source", "print"},
		{`{"utm_campaign":" spring-2026 "}`, "utm_campaign", "spring-2026"},
	}
	for _, c := range ok {
		got, err := parseUTM(json.RawMessage(c.in))
		if err != nil || got[c.key] != c.want {
			t.Errorf("parseUTM(%s) = %v, %v", c.in, got, err)
		}
	}
	for _, in := range []string{``, `null`, `{}`, `{"utm_term":""}`} {
		if got, err := parseUTM(json.RawMessage(in)); err != nil || len(got) != 0 {
			t.Errorf("parseUTM(%q) = %v, %v, want empty", in, got, err)
		}
	}
	bad := []string{
		`{"utm_source":"a b"}`, `{"ref":"x"}`, `{"utm_source":1}`, `[1]`, `"x"`,
		`{"utm_content":"` + strings.Repeat("a", 65) + `"}`,
	}
	for _, in := range bad {
		if _, err := parseUTM(json.RawMessage(in)); err == nil {
			t.Errorf("parseUTM(%s) accepted", in)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	row := gen.SalesLinkRow{ID: uuid.New(), CreatedAt: time.Date(2026, 10, 24, 12, 30, 15, 123456000, time.UTC)}
	at, id, ok := decodeCursor(encodeCursor(row))
	if !ok || !at.Equal(row.CreatedAt) || id != row.ID {
		t.Errorf("round trip = %v %v %v", at, id, ok)
	}
	for _, bad := range []string{"", "!!", "bm9wZQ", encodeCursor(row)[:10]} {
		if _, _, ok := decodeCursor(bad); ok {
			t.Errorf("decodeCursor(%q) accepted", bad)
		}
	}
}

func TestStatsRange(t *testing.T) {
	now := time.Date(2026, 10, 24, 18, 0, 0, 0, time.UTC)
	from, to, err := statsRange(httptest.NewRequest("GET", "/x", nil), now)
	if err != nil || to.Format("2006-01-02") != "2026-10-24" || from.Format("2006-01-02") != "2026-09-25" {
		t.Errorf("default range = %v %v %v", from, to, err)
	}
	for _, q := range []string{"?from=nope", "?to=2026-13-40", "?from=2026-10-25&to=2026-10-24", "?from=2025-01-01&to=2026-10-24"} {
		if _, _, err := statsRange(httptest.NewRequest("GET", "/x"+q, nil), now); err == nil {
			t.Errorf("range %s accepted", q)
		}
	}
	// 366 days inclusive is the limit.
	if _, _, err := statsRange(httptest.NewRequest("GET", "/x?from=2025-10-24&to=2026-10-24", nil), now); err != nil {
		t.Errorf("366-day range refused: %v", err)
	}
}

func TestValidateURL(t *testing.T) {
	for _, u := range []string{"https://example.test/a?b=1", "http://example.test"} {
		if _, ok := validateURL(u); !ok {
			t.Errorf("validateURL(%q) refused", u)
		}
	}
	for _, u := range []string{"", "ftp://example.test", "javascript:alert(1)", "//example.test", "https://u:p@example.test", "https://", "example.test"} {
		if _, ok := validateURL(u); ok {
			t.Errorf("validateURL(%q) accepted", u)
		}
	}
}

func TestCleanLabel(t *testing.T) {
	if _, ok := cleanLabel("   "); ok {
		t.Error("blank label accepted")
	}
	if got, ok := cleanLabel("  Poster  "); !ok || got != "Poster" {
		t.Errorf("cleanLabel = %q %v", got, ok)
	}
	if _, ok := cleanLabel(strings.Repeat("é", 120)); !ok {
		t.Error("120 characters refused")
	}
	if _, ok := cleanLabel(strings.Repeat("é", 121)); ok {
		t.Error("121 characters accepted")
	}
}
