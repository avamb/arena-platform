package hfeed

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/conversions"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/trackingconsent"
)

// fakeCartConversions dedups on the event id like conversions.PGStore does.
type fakeCartConversions struct {
	queued []conversions.SiteEvent
	seen   map[string]bool
	fail   error
}

func (f *fakeCartConversions) EnqueueEvent(_ context.Context, _ uuid.UUID, ev conversions.SiteEvent) error {
	if f.fail != nil {
		return f.fail
	}
	if f.seen == nil {
		f.seen = map[string]bool{}
	}
	if f.seen[ev.EventID] {
		return nil
	}
	f.seen[ev.EventID] = true
	f.queued = append(f.queued, ev)
	return nil
}

func (f *fakeCartConversions) SessionItem(_ context.Context, id uuid.UUID) (conversions.PurchaseItem, bool) {
	return conversions.PurchaseItem{
		SessionID: id.String(), EventID: "ev-1", EventName: "Concert", Venue: "Hall",
		City: "Prague", Country: "CZ", StartAt: time.Date(2026, 12, 1, 19, 0, 0, 0, time.UTC),
	}, true
}

const (
	ctxYes      = `{"tracking_consent":true,"tracking_consent_text_version":"v1","fbp":"fb.1.1.2","ga_client_id":"1.2"}`
	ctxNo       = `{"tracking_consent":false,"tracking_consent_text_version":"v1","fbp":"fb.1.1.2"}`
	ctxAbsent   = `{"fbp":"fb.1.1.2"}`
	ctxYesNoVer = `{"tracking_consent":true}`
)

func quoteBD() hcheckout.PricingBreakdown {
	return hcheckout.PricingBreakdown{
		Quantity: 3, Total: 6150, Currency: "EUR",
		Lines: []hcheckout.PricingLineItem{
			{TierID: "a", Quantity: 2, UnitPrice: 2000, Subtotal: 4000},
			{TierID: "b", Quantity: 1, UnitPrice: 2000 + 50, Subtotal: 2050},
		},
	}
}

func quoteReq(ctxJSON, id, seats string) PublicFeedCheckoutQuoteRequest {
	var req PublicFeedCheckoutQuoteRequest
	body := `{"session_id":"` + uuid.NewString() + `"`
	if ctxJSON != "" {
		body += `,"buyer_context":` + ctxJSON
	}
	if id != "" {
		body += `,"add_to_cart_id":` + id
	}
	if seats != "" {
		body += `,"seats_considered":` + seats
	}
	body += `}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		panic(err)
	}
	return req
}

func TestQuoteAddToCart_OnlyWithExplicitYesAndAnID(t *testing.T) {
	cases := []struct {
		name, ctx, id string
		want          bool
	}{
		{"yes", ctxYes, `"cart-1"`, true},
		{"no", ctxNo, `"cart-1"`, false},
		{"consent absent", ctxAbsent, `"cart-1"`, false},
		{"no buyer context", "", `"cart-1"`, false},
		{"yes without text version", ctxYesNoVer, `"cart-1"`, false},
		{"yes without id", ctxYes, "", false},
		{"yes with a bad id", ctxYes, `"a b"`, false},
		{"yes with a numeric id", ctxYes, `12`, false},
		{"yes with a 65-char id", ctxYes, `"` + strings.Repeat("a", 65) + `"`, false},
	}
	for _, c := range cases {
		f := &fakeCartConversions{}
		h := &Handler{logger: slog.Default(), cartConversions: f}
		r := httptest.NewRequest("POST", "/quote", nil)
		r.Header.Set("User-Agent", "UA/1")
		h.queueQuoteAddToCart(r, uuid.New(), uuid.New(), quoteReq(c.ctx, c.id, ""), quoteBD())
		if got := len(f.queued) == 1; got != c.want {
			t.Errorf("%s: queued=%d, want %v", c.name, len(f.queued), c.want)
		}
	}
}

func TestQuoteAddToCart_PayloadAndDedup(t *testing.T) {
	f := &fakeCartConversions{}
	h := &Handler{logger: slog.Default(), cartConversions: f}
	r := httptest.NewRequest("POST", "/quote", nil)
	r.Header.Set("User-Agent", "UA/1")
	org, sess := uuid.New(), uuid.New()
	req := quoteReq(ctxYes, `"cart-1"`, `7`)
	h.queueQuoteAddToCart(r, org, sess, req, quoteBD())
	h.queueQuoteAddToCart(r, org, sess, req, quoteBD()) // the same cart id again
	if len(f.queued) != 1 {
		t.Fatalf("queued %d, want 1 (same id dedups)", len(f.queued))
	}
	ev := f.queued[0]
	if ev.Kind != conversions.KindAddToCart || ev.EventID != "atc:cart-1" || ev.SeatsConsidered != 7 ||
		ev.Value != 61.5 || ev.Currency != "EUR" || ev.Context.FBP != "fb.1.1.2" || ev.Context.UserAgent != "UA/1" {
		t.Fatalf("event: %+v", ev)
	}
	if len(ev.Items) != 1 || ev.Items[0].SessionID != sess.String() || ev.Items[0].Quantity != 3 ||
		ev.Items[0].Price != 20.5 || ev.Items[0].EventName != "Concert" {
		t.Fatalf("items: %+v", ev.Items)
	}
	// a new cart id queues again
	h.queueQuoteAddToCart(r, org, sess, quoteReq(ctxYes, `"cart-2"`, ""), quoteBD())
	if len(f.queued) != 2 || f.queued[1].SeatsConsidered != 0 {
		t.Fatalf("second cart: %+v", f.queued)
	}
}

func TestQuoteAddToCart_FailureAndNilQueuerAreQuiet(t *testing.T) {
	f := &fakeCartConversions{fail: errors.New("db down")}
	h := &Handler{logger: slog.Default(), cartConversions: f}
	r := httptest.NewRequest("POST", "/quote", nil)
	h.queueQuoteAddToCart(r, uuid.New(), uuid.New(), quoteReq(ctxYes, `"cart-1"`, ""), quoteBD())
	h2 := &Handler{logger: slog.Default()}
	h2.queueQuoteAddToCart(r, uuid.New(), uuid.New(), quoteReq(ctxYes, `"cart-1"`, ""), quoteBD())
}

// The quote decodes the tracking fields leniently: a wrong type never makes
// the body invalid JSON for the quote.
func TestQuoteRequest_TrackingFieldsNeverBreakDecoding(t *testing.T) {
	body := `{"session_id":"` + uuid.NewString() + `","buyer_context":"oops","add_to_cart_id":{"x":1},"seats_considered":"many"}`
	var req PublicFeedCheckoutQuoteRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if parseAddToCartID(req.AddToCartID) != "" || parseSeatsConsidered(req.SeatsConsidered) != 0 ||
		buyerContextConsents(req.BuyerContext) {
		t.Fatal("bad values must read as absent")
	}
}

func TestParseSeatsConsidered(t *testing.T) {
	for in, want := range map[string]int{"0": 0, "5": 5, "1000": 1000, "1001": 0, "-1": 0, "2.5": 0, `"3"`: 0, "": 0} {
		if got := parseSeatsConsidered(json.RawMessage(in)); got != want {
			t.Errorf("%q = %d, want %d", in, got, want)
		}
	}
}

func TestCheckoutTracksCart(t *testing.T) {
	yes := &trackingconsent.Consent{Granted: true, Version: "v1"}
	no := &trackingconsent.Consent{Granted: false, Version: "v1"}
	cases := []struct {
		name   string
		stored *trackingconsent.Consent
		ctx    string
		want   bool
	}{
		{"buyer context yes", yes, ctxYes, true},
		{"buyer context no", no, ctxNo, false},
		{"consent absent", nil, ctxAbsent, false},
		{"no buyer context", nil, "", false},
		{"top-level no overrules a context yes", no, ctxYes, false},
		{"top-level yes without a context yes", yes, ctxAbsent, false},
	}
	for _, c := range cases {
		if got := checkoutTracksCart(c.stored, json.RawMessage(c.ctx)); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInitiateCheckoutEvent_IDAndPayload(t *testing.T) {
	f := &fakeCartConversions{}
	h := &Handler{logger: slog.Default(), cartConversions: f}
	cs, sess := uuid.New(), uuid.New()
	cleaned := conversions.CleanBuyerContext(json.RawMessage(ctxYes), "203.0.113.5", "UA/1")
	h.queueCartEvent(context.Background(), uuid.New(), conversions.KindInitiateCheckout, checkoutEventIDPfx+cs.String(),
		sess, quoteBD(), cleaned, 0)
	if len(f.queued) != 1 {
		t.Fatal("not queued")
	}
	ev := f.queued[0]
	if ev.Kind != conversions.KindInitiateCheckout || ev.EventID != "ic:"+cs.String() || ev.Value != 61.5 ||
		ev.Context.ClientIP != "203.0.113.5" || len(ev.Items) != 1 || ev.SeatsConsidered != 0 {
		t.Fatalf("event: %+v", ev)
	}
}
