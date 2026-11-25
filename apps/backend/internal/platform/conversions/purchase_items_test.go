package conversions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func sampleSessions() []OrderSession {
	return []OrderSession{{
		SessionID: "sess-1", EventID: "evt-1", EventName: "Silver Hoof", Venue: "Palac Akropolis",
		City: "Prague", Country: "CZ", Quantity: 2, UnitPrice: 1890,
		StartAt: time.Date(2026, 12, 19, 18, 0, 0, 0, time.UTC),
	}}
}

func captureBody(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, got)
		w.WriteHeader(200)
	}))
}

func TestMetaPurchase_CarriesEventSessionCityAndContents(t *testing.T) {
	var got map[string]any
	srv := captureBody(t, &got)
	defer srv.Close()
	f, _ := newFake("meta", true)
	f.order.Sessions = sampleSessions()
	if _, err := RunOnce(context.Background(), f, Senders{Meta: MetaSender{BaseURL: srv.URL}}, slog.Default(), time.Now()); err != nil || f.status != "sent" {
		t.Fatalf("status=%s err=%v reason=%s", f.status, err, f.reason)
	}
	cd := got["data"].([]any)[0].(map[string]any)["custom_data"].(map[string]any)
	want := map[string]any{
		"currency": "ILS", "value": 18.9, "order_id": "1000000501",
		"content_ids": []any{"sess-1"}, "content_type": "product",
		"content_name": "Silver Hoof — Prague, 2026-12-19",
		"contents":     []any{map[string]any{"id": "sess-1", "quantity": 2.0, "item_price": 18.9}},
		"num_items":    2.0, "event_id": "evt-1", "event_name": "Silver Hoof",
		"city": "Prague", "country": "CZ", "venue": "Palac Akropolis",
	}
	if !reflect.DeepEqual(cd, want) {
		t.Fatalf("custom_data:\n got %v\nwant %v", cd, want)
	}
}

func TestMetaPurchase_MissingDataIsOmitted(t *testing.T) {
	var got map[string]any
	srv := captureBody(t, &got)
	defer srv.Close()
	f, _ := newFake("meta", true)
	f.order.Sessions = []OrderSession{{SessionID: "sess-1", EventName: "Show", Quantity: 1, UnitPrice: 500}}
	if _, err := RunOnce(context.Background(), f, Senders{Meta: MetaSender{BaseURL: srv.URL}}, slog.Default(), time.Now()); err != nil || f.status != "sent" {
		t.Fatalf("status=%s err=%v", f.status, err)
	}
	cd := got["data"].([]any)[0].(map[string]any)["custom_data"].(map[string]any)
	if cd["content_name"] != "Show" {
		t.Fatalf("content_name = %v", cd["content_name"])
	}
	for _, k := range []string{"city", "country", "venue", "event_id"} {
		if _, ok := cd[k]; ok {
			t.Fatalf("%s should be omitted: %v", k, cd)
		}
	}
}

func TestMetaPurchase_NoSessionsKeepsOldShape(t *testing.T) {
	var got map[string]any
	srv := captureBody(t, &got)
	defer srv.Close()
	f, _ := newFake("meta", true)
	if _, err := RunOnce(context.Background(), f, Senders{Meta: MetaSender{BaseURL: srv.URL}}, slog.Default(), time.Now()); err != nil || f.status != "sent" {
		t.Fatalf("status=%s err=%v", f.status, err)
	}
	cd := got["data"].([]any)[0].(map[string]any)["custom_data"].(map[string]any)
	if len(cd) != 3 {
		t.Fatalf("custom_data = %v", cd)
	}
}

func TestMetaPurchase_SeveralSessions(t *testing.T) {
	var got map[string]any
	srv := captureBody(t, &got)
	defer srv.Close()
	f, _ := newFake("meta", true)
	s2 := sampleSessions()[0]
	s2.SessionID, s2.Quantity = "sess-2", 1
	f.order.Sessions = append(sampleSessions(), s2)
	if _, err := RunOnce(context.Background(), f, Senders{Meta: MetaSender{BaseURL: srv.URL}}, slog.Default(), time.Now()); err != nil {
		t.Fatal(err)
	}
	cd := got["data"].([]any)[0].(map[string]any)["custom_data"].(map[string]any)
	if !reflect.DeepEqual(cd["content_ids"], []any{"sess-1", "sess-2"}) || cd["num_items"].(float64) != 3 {
		t.Fatalf("custom_data: %v", cd)
	}
}

func TestGA4Purchase_Items(t *testing.T) {
	var got map[string]any
	srv := captureBody(t, &got)
	defer srv.Close()
	f, _ := newFake("ga4", true)
	f.order.Sessions = sampleSessions()
	if _, err := RunOnce(context.Background(), f, Senders{GA4: GA4Sender{BaseURL: srv.URL}}, slog.Default(), time.Now()); err != nil || f.status != "sent" {
		t.Fatalf("status=%s err=%v", f.status, err)
	}
	p := got["events"].([]any)[0].(map[string]any)["params"].(map[string]any)
	want := []any{map[string]any{
		"item_id": "sess-1", "item_name": "Silver Hoof — Prague, 2026-12-19",
		"item_category": "Prague", "quantity": 2.0, "price": 18.9,
	}}
	if !reflect.DeepEqual(p["items"], want) {
		t.Fatalf("items: got %v want %v", p["items"], want)
	}
}
