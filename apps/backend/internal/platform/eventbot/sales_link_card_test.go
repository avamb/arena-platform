package eventbot

import (
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// The card names the reason the server gives, and only a reason another target
// would mend names the Retarget button (checked on the texts in the e2e; here
// the key choice).
func TestSlWhyKey_FollowsTheServerReason(t *testing.T) {
	for _, c := range []struct {
		name string
		link openapi.SalesLink
		want string
	}{
		{"leads to its target", openapi.SalesLink{DestinationUrl: "https://t/x"}, ""},
		{"archived wins over any reason", openapi.SalesLink{Status: "archived", DestinationReason: "event_not_published", DestinationDegraded: true}, "bot.ql.card_archived"},
		{"archived reason", openapi.SalesLink{DestinationReason: "archived", DestinationDegraded: true}, "bot.ql.card_archived"},
		{"event not published", openapi.SalesLink{DestinationReason: "event_not_published", DestinationDegraded: true}, "bot.ql.card_why_event_unpublished"},
		{"event without a page", openapi.SalesLink{DestinationReason: "event_no_slug", DestinationDegraded: true}, "bot.ql.card_why_event_no_page"},
		{"date cancelled", openapi.SalesLink{DestinationReason: "session_not_live", DestinationDegraded: true}, "bot.ql.card_why_session"},
		{"promoter gone", openapi.SalesLink{DestinationReason: "promoter_unavailable", DestinationDegraded: true}, "bot.ql.card_why_promoter"},
		{"no page address", openapi.SalesLink{DestinationReason: "no_page_address"}, "bot.ql.card_why_no_address"},
		{"organization gone", openapi.SalesLink{DestinationReason: "org_not_found", DestinationDegraded: true}, "bot.ql.card_why_no_address"},
		{"older server: degraded without a reason", openapi.SalesLink{DestinationDegraded: true}, "bot.ql.card_degraded"},
		{"unknown reason", openapi.SalesLink{DestinationReason: "something_new", DestinationDegraded: true}, "bot.ql.card_degraded"},
	} {
		if got := slWhyKey(c.link); got != c.want {
			t.Errorf("%s: key = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSlSameTarget(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	ev := func(id uuid.UUID) slTarget { return slTarget{Kind: slKindEvent, EventID: &id} }
	ss := func(id uuid.UUID) slTarget { return slTarget{Kind: slKindSession, SessionID: &id, EventID: &a} }
	pr := func(id uuid.UUID) slTarget { return slTarget{Kind: slKindPromoterPage, PromoterID: &id} }
	for _, c := range []struct {
		name string
		x, y slTarget
		same bool
	}{
		{"same event", ev(a), ev(a), true},
		{"other event", ev(a), ev(b), false},
		{"event vs its date", ev(a), ss(a), false},
		{"same date", ss(a), ss(a), true},
		{"other date of the same event", ss(a), ss(b), false},
		{"same promoter", pr(a), pr(a), true},
		{"other promoter", pr(a), pr(b), false},
		{"org page twice", slTarget{Kind: slKindOrgPage}, slTarget{Kind: slKindOrgPage, Name: "x"}, true},
		{"org page vs promoter", slTarget{Kind: slKindOrgPage}, pr(a), false},
		{"same url", slTarget{Kind: slKindURL, URL: "https://a/x"}, slTarget{Kind: slKindURL, URL: "https://a/x"}, true},
		{"other url", slTarget{Kind: slKindURL, URL: "https://a/x"}, slTarget{Kind: slKindURL, URL: "https://a/y"}, false},
		{"names do not matter", ev(a), slTarget{Kind: slKindEvent, EventID: &a, Name: "Swan"}, true},
	} {
		if got := slSameTarget(c.x, c.y); got != c.same {
			t.Errorf("%s: same = %v, want %v", c.name, got, c.same)
		}
	}
	// The stored target of a link compares equal to the dialog's choice of it.
	u := "https://a/x"
	stored := openapi.SalesLinkTarget{Kind: slKindURL, Url: &u}
	if !slSameTarget(slTargetOf(stored), slTarget{Kind: slKindURL, URL: u}) {
		t.Error("the stored url target must equal the typed one")
	}
	storedSess := openapi.SalesLinkTarget{Kind: slKindSession, EventId: &a, SessionId: &b}
	if !slSameTarget(slTargetOf(storedSess), ss(b)) || slSameTarget(slTargetOf(storedSess), ss(a)) {
		t.Error("the stored session target must compare by its date")
	}
}
