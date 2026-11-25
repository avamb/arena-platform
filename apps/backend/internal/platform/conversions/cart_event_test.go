package conversions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testCartEvent(kind string, seats int) SiteEvent {
	item := PurchaseItem{
		SessionID: "S-1", EventID: "E-1", EventName: "Concert", Venue: "Hall", City: "Prague", Country: "CZ",
		StartAt: time.Date(2026, 12, 1, 19, 0, 0, 0, time.UTC),
	}
	ev := CartEvent(kind, "atc:cart-1", item, 3, 2050, 6150, "EUR",
		BuyerContext{FBP: "fb.1.1.2", GAClientID: "111.222", ClientIP: "203.0.113.5", UserAgent: "UA/1"}, seats)
	ev.Time = time.Unix(1700000000, 0)
	return ev
}

func captureServer(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*got = nil
		_ = json.Unmarshal(b, got)
		w.WriteHeader(200)
	}))
}

func TestCartEvent_MoneyAndItems(t *testing.T) {
	ev := testCartEvent(KindAddToCart, 7)
	if ev.Value != 61.5 || ev.Currency != "EUR" || ev.SeatsConsidered != 7 || len(ev.Items) != 1 ||
		ev.Items[0].Quantity != 3 || ev.Items[0].Price != 20.5 {
		t.Fatalf("%+v", ev)
	}
	// no session, no items; JSON round trip keeps the items
	if e := CartEvent(KindAddToCart, "x", PurchaseItem{}, 3, 1, 1, "EUR", BuyerContext{}, 0); len(e.Items) != 0 {
		t.Fatalf("items without a session: %+v", e.Items)
	}
	raw, _ := json.Marshal(ev)
	var back SiteEvent
	if err := json.Unmarshal(raw, &back); err != nil || len(back.Items) != 1 || back.Items[0].EventName != "Concert" ||
		back.SeatsConsidered != 7 {
		t.Fatalf("round trip: %v %+v", err, back)
	}
}

func TestMetaSendEvent_AddToCartCustomData(t *testing.T) {
	var got map[string]any
	srv := captureServer(t, &got)
	defer srv.Close()
	if err := (MetaSender{BaseURL: srv.URL}).SendEvent(context.Background(), "px", "tok", "", testCartEvent(KindAddToCart, 7)); err != nil {
		t.Fatal(err)
	}
	e := got["data"].([]any)[0].(map[string]any)
	if e["event_name"] != "AddToCart" || e["event_id"] != "atc:cart-1" {
		t.Fatalf("event: %v", e)
	}
	cd := e["custom_data"].(map[string]any)
	if cd["content_ids"].([]any)[0] != "S-1" || cd["content_type"] != "product" || cd["num_items"].(float64) != 3 ||
		cd["content_name"] != "Concert — Prague, 2026-12-01" || cd["city"] != "Prague" || cd["country"] != "CZ" ||
		cd["event_id"] != "E-1" || cd["event_name"] != "Concert" || cd["venue"] != "Hall" ||
		cd["value"].(float64) != 61.5 || cd["currency"] != "EUR" || cd["seats_considered"].(float64) != 7 {
		t.Fatalf("custom_data: %v", cd)
	}
	c := cd["contents"].([]any)[0].(map[string]any)
	if c["id"] != "S-1" || c["quantity"].(float64) != 3 || c["item_price"].(float64) != 20.5 {
		t.Fatalf("contents: %v", c)
	}
	b, _ := json.Marshal(cd)
	t.Logf("AddToCart custom_data: %s", b)

	// seats_considered 0 is left out; InitiateCheckout carries the same items
	if err := (MetaSender{BaseURL: srv.URL}).SendEvent(context.Background(), "px", "tok", "", testCartEvent(KindInitiateCheckout, 0)); err != nil {
		t.Fatal(err)
	}
	e = got["data"].([]any)[0].(map[string]any)
	cd = e["custom_data"].(map[string]any)
	if _, ok := cd["seats_considered"]; ok || e["event_name"] != "InitiateCheckout" || cd["num_items"].(float64) != 3 {
		t.Fatalf("initiate_checkout: %v", e)
	}
}

func TestGA4SendEvent_AddToCartItems(t *testing.T) {
	var got map[string]any
	srv := captureServer(t, &got)
	defer srv.Close()
	for kind, name := range map[string]string{KindAddToCart: "add_to_cart", KindInitiateCheckout: "begin_checkout"} {
		if err := (GA4Sender{BaseURL: srv.URL}).SendEvent(context.Background(), "G-X", "sec", testCartEvent(kind, 7)); err != nil {
			t.Fatal(err)
		}
		e := got["events"].([]any)[0].(map[string]any)
		p := e["params"].(map[string]any)
		it := p["items"].([]any)[0].(map[string]any)
		if e["name"] != name || p["value"].(float64) != 61.5 || p["currency"] != "EUR" ||
			it["item_id"] != "S-1" || it["quantity"].(float64) != 3 || it["price"].(float64) != 20.5 ||
			it["item_category"] != "Prague" {
			t.Fatalf("%s: %v", kind, e)
		}
	}
}
