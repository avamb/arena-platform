package hreferral

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
)

func TestJudge_StripeCanFlittAndSitesCannot(t *testing.T) {
	reg := paymodules.Registry()
	stripe := gen.ReferralChannel{ID: uuid.New(), Provider: "stripe"}
	flitt := gen.ReferralChannel{ID: uuid.New(), Provider: "flitt"}
	site := gen.ReferralChannel{ID: uuid.New(), Provider: "stripe", IsSite: true}

	cases := []struct {
		name       string
		channels   []gen.ReferralChannel
		supported  bool
		warnings   []string
		reasonOf   map[uuid.UUID]string
		wantSource string
	}{
		{"stripe only", []gen.ReferralChannel{stripe}, true, nil, nil, "event"},
		{"flitt only", []gen.ReferralChannel{flitt}, false, nil, map[uuid.UUID]string{flitt.ID: reasonNoPartialRef}, "event"},
		{"a site's channel", []gen.ReferralChannel{site}, false, nil, map[uuid.UUID]string{site.ID: reasonSellerSite}, "event"},
		{"stripe and flitt", []gen.ReferralChannel{stripe, flitt}, true, []string{warnSomeUnsupported}, map[uuid.UUID]string{flitt.ID: reasonNoPartialRef}, "event"},
		{"nothing yet", nil, true, []string{warnNoChannel}, nil, "event"},
		{"unknown provider", []gen.ReferralChannel{{ID: uuid.New(), Provider: "nonesuch"}}, false, nil, nil, "event"},
	}
	for _, c := range cases {
		cap, warnings := judge(reg, "event", c.channels)
		if cap.Supported != c.supported {
			t.Errorf("%s: supported = %v, want %v (%+v)", c.name, cap.Supported, c.supported, cap)
		}
		if len(warnings) != len(c.warnings) {
			t.Errorf("%s: warnings = %+v, want codes %v", c.name, warnings, c.warnings)
		} else {
			for i, w := range warnings {
				if w.Code != c.warnings[i] {
					t.Errorf("%s: warning %d = %s, want %s", c.name, i, w.Code, c.warnings[i])
				}
			}
		}
		for _, ch := range cap.Channels {
			id, _ := uuid.Parse(ch.ChannelID)
			if want, ok := c.reasonOf[id]; ok && ch.Reason != want {
				t.Errorf("%s: channel %s reason = %q, want %q", c.name, ch.Provider, ch.Reason, want)
			}
		}
	}
}

func TestFoldCashback_OneObjectPerCurrencyInOrder(t *testing.T) {
	out := foldCashback([]gen.ReferralMoney{
		{Currency: "USD", State: "paid", AmountMinor: 100},
		{Currency: "EUR", State: "paid", AmountMinor: 300},
		{Currency: "EUR", State: "due", AmountMinor: 50},
		{Currency: "EUR", State: "not_possible", AmountMinor: 7},
		{Currency: "EUR", State: "capped", AmountMinor: 11},
		{Currency: "EUR", State: "cancelled", AmountMinor: 3},
		{Currency: "EUR", State: "sent", AmountMinor: 20},
		{Currency: "EUR", State: "failed", AmountMinor: 2},
	})
	if len(out) != 2 || out[0].Currency != "EUR" || out[1].Currency != "USD" {
		t.Fatalf("fold = %+v, want EUR then USD", out)
	}
	eur := out[0]
	if eur.PaidMinor != 300 || eur.DueMinor != 50 || eur.NotPossibleMinor != 7 || eur.CappedMinor != 11 ||
		eur.CancelledMinor != 3 || eur.SentMinor != 20 || eur.FailedMinor != 2 {
		t.Errorf("EUR = %+v", eur)
	}
}

func TestCursor_RoundTripAndRefusals(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 30, 45, 123456000, time.UTC)
	id := uuid.New()
	gotAt, gotID, ok := decodeCursor(encodeCursor(at, id))
	if !ok || !gotAt.Equal(at) || gotID != id {
		t.Fatalf("round trip = %v %v %v", gotAt, gotID, ok)
	}
	for _, bad := range []string{"", "!!!", "bm90LWEtY3Vyc29y"} {
		if _, _, ok := decodeCursor(bad); ok {
			t.Errorf("decodeCursor(%q) accepted", bad)
		}
	}
}

func TestParse_RangesAndDefaults(t *testing.T) {
	h := &Handler{now: func() time.Time { return time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC) }}
	i := func(v int) *int { return &v }
	s := func(v string) *string { return &v }
	valid := putBody{FriendDiscountPct: i(10), CashbackPct: i(20)}
	run := func(b putBody) (settings, int) {
		rec := httptest.NewRecorder()
		got, ok := h.parse(rec, httptest.NewRequest(http.MethodPut, "/", nil), b)
		if ok {
			return got, 0
		}
		return got, rec.Code
	}
	got, code := run(valid)
	if code != 0 || got.discount != 10 || got.cashback != 20 || got.cap != 100 || got.maxFriends != nil || got.endsAt != nil {
		t.Fatalf("valid body = %+v code %d", got, code)
	}
	for name, b := range map[string]putBody{
		"discount 0":      {FriendDiscountPct: i(0), CashbackPct: i(20)},
		"cashback 101":    {FriendDiscountPct: i(10), CashbackPct: i(101)},
		"no cashback":     {FriendDiscountPct: i(10)},
		"cap 0":           {FriendDiscountPct: i(10), CashbackPct: i(20), CashbackCapPct: i(0)},
		"max friends 0":   {FriendDiscountPct: i(10), CashbackPct: i(20), MaxFriends: i(0)},
		"max friends big": {FriendDiscountPct: i(10), CashbackPct: i(20), MaxFriends: i(40000)},
		"ends in past":    {FriendDiscountPct: i(10), CashbackPct: i(20), EndsAt: s("2026-01-01T00:00:00Z")},
		"ends garbage":    {FriendDiscountPct: i(10), CashbackPct: i(20), EndsAt: s("tomorrow")},
	} {
		if _, code := run(b); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, code)
		}
	}
	ok := putBody{FriendDiscountPct: i(100), CashbackPct: i(1), CashbackCapPct: i(40), MaxFriends: i(3), EndsAt: s("2026-12-31T20:00:00+02:00")}
	got, code = run(ok)
	if code != 0 || got.discount != 100 || got.cap != 40 || *got.maxFriends != 3 || got.endsAt.Hour() != 18 {
		t.Fatalf("full body = %+v code %d", got, code)
	}
}

func TestReadBody_StrictAndSmall(t *testing.T) {
	for name, body := range map[string]string{
		"empty":        "",
		"not json":     "x",
		"unknown":      `{"friend_discount_pct":1,"nope":2}`,
		"two objects":  `{} {}`,
		"too large":    `{"accept_terms":true,"pad":"` + strings.Repeat("a", maxBodyBytes) + `"}`,
		"wrong type":   `{"friend_discount_pct":"ten"}`,
		"trailing bad": `{"accept_terms":true} x`,
	} {
		rec := httptest.NewRecorder()
		var b putBody
		if readBody(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)), &b) {
			t.Errorf("%s: body accepted", name)
		} else if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	var b putBody
	if !readBody(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"friend_discount_pct":10,"cashback_pct":20,"accept_terms":true}`)), &b) || !b.AcceptTerms {
		t.Fatalf("a valid body was refused: %d", rec.Code)
	}
}

func TestHandlerWithoutDatabaseAnswers503(t *testing.T) {
	h := New(nil, nil, nil)
	rec := httptest.NewRecorder()
	h.HandleGet(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
