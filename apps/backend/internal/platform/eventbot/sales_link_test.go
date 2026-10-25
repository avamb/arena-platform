package eventbot

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

func TestParseSalesLinkLabel(t *testing.T) {
	t.Parallel()
	if got, key := parseSalesLinkLabel("  Афиша   у  входа "); got != "Афиша у входа" || key != "" {
		t.Errorf("got %q %q", got, key)
	}
	if _, key := parseSalesLinkLabel(" \n "); key != "bot.ql.err_label_empty" {
		t.Errorf("empty label key = %q", key)
	}
	if got, key := parseSalesLinkLabel(strings.Repeat("я", 120)); key != "" || got == "" {
		t.Errorf("120 runes must pass: %q", key)
	}
	if _, key := parseSalesLinkLabel(strings.Repeat("я", 121)); key != "bot.ql.err_label_long" {
		t.Errorf("121 runes key = %q", key)
	}
}

func TestParseSalesLinkURL(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"https://example.com/shop", " HTTP://Example.com ", "https://x.io/a?b=c"} {
		if got, key := parseSalesLinkURL(ok); key != "" || got == "" {
			t.Errorf("%q refused: %q", ok, key)
		}
	}
	for _, bad := range []string{"example.com/shop", "ftp://example.com/x", "https://exa mple.com", "https://", "javascript:alert(1)"} {
		if _, key := parseSalesLinkURL(bad); key != "bot.ql.err_url" {
			t.Errorf("%q accepted or wrong key %q", bad, key)
		}
	}
	if _, key := parseSalesLinkURL("https://example.com/" + strings.Repeat("a", 2040)); key != "bot.ql.err_url_long" {
		t.Errorf("long address key = %q", key)
	}
}

func TestSlTarget_APISendsOnlyTheIDsOfItsKind(t *testing.T) {
	t.Parallel()
	ev, ss, pr := uuid.New(), uuid.New(), uuid.New()
	full := slTarget{Kind: slKindSession, EventID: &ev, SessionID: &ss, PromoterID: &pr, URL: "https://x.test"}
	// A session names its date alone: the server adds the event.
	if got := full.api(); got.SessionId == nil || *got.SessionId != ss || got.EventId != nil || got.PromoterId != nil || got.Url != nil {
		t.Errorf("session target = %+v", got)
	}
	full.Kind = slKindEvent
	if got := full.api(); got.EventId == nil || *got.EventId != ev || got.SessionId != nil {
		t.Errorf("event target = %+v", got)
	}
	full.Kind = slKindURL
	if got := full.api(); got.Url == nil || *got.Url != "https://x.test" || got.EventId != nil {
		t.Errorf("url target = %+v", got)
	}
	if got := (slTarget{Kind: slKindOrgPage}).api(); got.Kind != slKindOrgPage || got.EventId != nil || got.Url != nil {
		t.Errorf("org page target = %+v", got)
	}
}

func TestSlScanSums_CountsTodayAndTheLastSevenUTCDays(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 24, 22, 30, 0, 0, time.UTC)
	days := []openapi.SalesLinkDayStats{
		{Day: "2026-10-17", Hits: 100}, // eight days back: outside the week
		{Day: "2026-10-18", Hits: 3},   // seven days including today
		{Day: "2026-10-23", Hits: 4},
		{Day: "2026-10-24", Hits: 5},
		{Day: "2026-10-25", Hits: 50}, // tomorrow in UTC: never counted
	}
	today, week := slScanSums(days, now)
	if today != 5 || week != 12 {
		t.Errorf("today=%d week=%d, want 5 and 12", today, week)
	}
}

func TestSalesLinkButtonsFollowTheRole(t *testing.T) {
	t.Parallel()
	b := &Bot{texts: &Texts{}}
	owner := &Identity{Current: &Membership{Role: membershipRoleOwner}}
	agent := &Identity{Current: &Membership{Role: "agent"}}
	if b.salesLinkMenuRow("en", owner) == nil || b.salesLinkEventRow("en", owner, uuid.New()) == nil {
		t.Error("the owner must see the menu row and the event button")
	}
	if b.salesLinkMenuRow("en", agent) != nil || b.salesLinkEventRow("en", agent, uuid.New()) != nil {
		t.Error("an agent must see neither")
	}
	if b.salesLinkMenuRow("en", nil) != nil {
		t.Error("nobody must see nothing")
	}
}

func TestSlListPosition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		st         salesLinkListState
		wantPage   int
		wantCursor string
	}{
		{"fresh", salesLinkListState{}, 1, ""},
		{"first page", salesLinkListState{Page: 1, Cursors: []string{""}}, 1, ""},
		{"second page", salesLinkListState{Page: 2, Cursors: []string{"", "c2"}}, 2, "c2"},
		{"cursor lost", salesLinkListState{Page: 3, Cursors: []string{"", "c2"}}, 1, ""},
	}
	for _, c := range cases {
		page, cursor := slListPosition(c.st)
		if page != c.wantPage || cursor != c.wantCursor {
			t.Errorf("%s: got page %d cursor %q, want %d %q", c.name, page, cursor, c.wantPage, c.wantCursor)
		}
	}
}

func TestSlListPager(t *testing.T) {
	t.Parallel()
	b := &Bot{}
	if row := b.slListPager("en", 1, false); row != nil {
		t.Errorf("a single page has no pager: %v", row)
	}
	var data []string
	for _, btn := range b.slListPager("en", 2, true) {
		data = append(data, btn.CallbackData)
	}
	if strings.Join(data, " ") != "ql:lp:1 ql:noop ql:lp:3" {
		t.Errorf("middle page buttons = %v", data)
	}
	data = nil
	for _, btn := range b.slListPager("en", 1, true) {
		data = append(data, btn.CallbackData)
	}
	if strings.Join(data, " ") != "ql:noop ql:lp:2" {
		t.Errorf("first page buttons = %v", data)
	}
}
