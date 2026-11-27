package conversions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func siteDelivery(t *testing.T, provider string, ev SiteEvent) Delivery {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return Delivery{ID: uuid.New(), OrgID: uuid.New(), Provider: provider, Kind: ev.Kind, EventID: ev.EventID, Payload: raw}
}

func testSiteEvent(kind string) SiteEvent {
	return SiteEvent{
		Kind: kind, EventID: "evt-1", Time: time.Unix(1700000000, 0),
		Context: BuyerContext{FBP: "fb.1.1.2", FBCLID: "abc", GAClientID: "111.222", ClientIP: "203.0.113.5",
			UserAgent: "UA/1", PageURL: "https://site.example/e/1"},
		VisitorID: "vis-1", GASessionID: "1700000001", ContentID: "E-1", ContentName: "Concert",
		Seller: "Box", City: "Prague", Value: 12.5, Currency: "eur", CheckoutType: "external",
	}
}

func TestMetaSendEvent_Mapping(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = nil
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	names := map[string]string{
		KindPageView: "PageView", KindViewContent: "ViewContent", KindInitiateCheckout: "InitiateCheckout",
		KindLead: "Lead", KindSubscribe: "CompleteRegistration", KindTicketsClick: "TicketsClick",
	}
	for kind, want := range names {
		ev := testSiteEvent(kind)
		if kind == KindLead {
			ev.Email, ev.Phone = "A@B.com", "+1 (555) 000"
		}
		if err := (MetaSender{BaseURL: srv.URL}).SendEvent(context.Background(), "px", "tok", "TEST1", ev); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		e := got["data"].([]any)[0].(map[string]any)
		if e["event_name"] != want || e["event_id"] != "evt-1" || e["action_source"] != "website" ||
			e["event_source_url"] != "https://site.example/e/1" || got["test_event_code"] != "TEST1" {
			t.Fatalf("%s: %v", kind, e)
		}
		ud := e["user_data"].(map[string]any)
		if len(ud["external_id"].([]any)[0].(string)) != 64 || ud["fbp"] != "fb.1.1.2" ||
			!strings.HasPrefix(ud["fbc"].(string), "fb.1.") || ud["client_user_agent"] != "UA/1" {
			t.Fatalf("%s user_data: %v", kind, ud)
		}
		if (kind == KindLead) != (ud["em"] != nil && ud["ph"] != nil) {
			t.Fatalf("%s em/ph: %v", kind, ud)
		}
		cd, _ := e["custom_data"].(map[string]any)
		switch kind {
		case KindViewContent:
			if cd["content_type"] != "product" || cd["content_ids"].([]any)[0] != "E-1" || cd["content_name"] != "Concert" {
				t.Fatalf("view_content custom: %v", cd)
			}
		case KindTicketsClick:
			if cd["content_type"] != "product" || cd["content_ids"].([]any)[0] != "E-1" || cd["content_name"] != "Concert" ||
				cd["seller"] != "Box" || cd["city"] != "Prague" || cd["checkout_type"] != "external" ||
				cd["value"].(float64) != 12.5 || cd["currency"] != "EUR" {
				t.Fatalf("tickets_click custom: %v", cd)
			}
		case KindInitiateCheckout:
			if cd["seller"] != "Box" || cd["city"] != "Prague" || cd["checkout_type"] != "external" ||
				cd["value"].(float64) != 12.5 || cd["currency"] != "EUR" {
				t.Fatalf("checkout custom: %v", cd)
			}
		}
	}
	// zero value and currency are omitted
	ev := testSiteEvent(KindInitiateCheckout)
	ev.Value, ev.Currency = 0, ""
	if err := (MetaSender{BaseURL: srv.URL}).SendEvent(context.Background(), "px", "tok", "", ev); err != nil {
		t.Fatal(err)
	}
	cd := got["data"].([]any)[0].(map[string]any)["custom_data"].(map[string]any)
	if _, ok := cd["value"]; ok || got["test_event_code"] != nil {
		t.Fatalf("custom: %v body: %v", cd, got)
	}
}

func TestGA4SendEvent_Mapping(t *testing.T) {
	var got map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	names := map[string]string{
		KindPageView: "page_view", KindViewContent: "view_item", KindInitiateCheckout: "begin_checkout",
		KindLead: "generate_lead", KindSubscribe: "sign_up", KindTicketsClick: "tickets_click",
	}
	for kind, want := range names {
		if err := (GA4Sender{BaseURL: srv.URL}).SendEvent(context.Background(), "G-X", "sec", testSiteEvent(kind)); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if path != "/mp/collect" || got["client_id"] != "111.222" || got["user_id"] != nil {
			t.Fatalf("%s: path=%s body=%v", kind, path, got)
		}
		e := got["events"].([]any)[0].(map[string]any)
		p := e["params"].(map[string]any)
		if e["name"] != want || p["engagement_time_msec"].(float64) != 1 || p["session_id"].(float64) != 1700000001 {
			t.Fatalf("%s: %v", kind, e)
		}
		if kind == KindInitiateCheckout && (p["affiliation"] != "Box" || p["checkout_type"] != "external" ||
			p["value"].(float64) != 12.5 || p["currency"] != "EUR" || p["items"] == nil) {
			t.Fatalf("begin_checkout: %v", p)
		}
		if kind == KindTicketsClick && (p["affiliation"] != "Box" || p["checkout_type"] != "external" ||
			p["value"].(float64) != 12.5 || p["currency"] != "EUR" ||
			p["items"].([]any)[0].(map[string]any)["item_id"] != "E-1" ||
			p["items"].([]any)[0].(map[string]any)["item_name"] != "Concert") {
			t.Fatalf("tickets_click: %v", p)
		}
		if kind == KindViewContent && p["items"].([]any)[0].(map[string]any)["item_id"] != "E-1" {
			t.Fatalf("view_item: %v", p)
		}
	}
	ev := testSiteEvent(KindPageView)
	ev.Context.GAClientID = ""
	err := (GA4Sender{BaseURL: srv.URL}).SendEvent(context.Background(), "G-X", "sec", ev)
	if err == nil || !IsPermanent(err) || err.Error() != "no_client_id" {
		t.Fatalf("err = %v", err)
	}
}

func TestSweep_SiteEventBranch(t *testing.T) {
	var metaBody map[string]any
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &metaBody)
		w.WriteHeader(200)
	}))
	defer meta.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }))
	defer failing.Close()
	senders := Senders{Meta: MetaSender{BaseURL: meta.URL}, GA4: GA4Sender{BaseURL: failing.URL}}
	run := func(f *fakeStore, d Delivery, s Senders) {
		f.due = []Delivery{d}
		if _, err := RunOnce(context.Background(), f, s, slog.Default(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	// meta: sent, no consent query (consent false in the fake), test code from settings
	f, _ := newFake("meta", false)
	f.settings.MetaTestEventCode = "TESTCODE"
	run(f, siteDelivery(t, "meta", testSiteEvent(KindViewContent)), senders)
	if f.status != "sent" || metaBody["test_event_code"] != "TESTCODE" {
		t.Fatalf("status=%s reason=%s body=%v", f.status, f.reason, metaBody)
	}

	// ga4 server error: retry
	f, _ = newFake("ga4", false)
	run(f, siteDelivery(t, "ga4", testSiteEvent(KindPageView)), senders)
	if f.status != "retry" {
		t.Fatalf("status=%s", f.status)
	}

	// ga4 without client id: permanent failure
	f, _ = newFake("ga4", false)
	ev := testSiteEvent(KindPageView)
	ev.Context.GAClientID = ""
	run(f, siteDelivery(t, "ga4", ev), senders)
	if f.status != "failed" || f.reason != "no_client_id" {
		t.Fatalf("status=%s reason=%s", f.status, f.reason)
	}

	// bad payload: failed
	f, _ = newFake("meta", false)
	d := siteDelivery(t, "meta", testSiteEvent(KindPageView))
	d.Payload = []byte(`{not json`)
	run(f, d, senders)
	if f.status != "failed" {
		t.Fatalf("status=%s", f.status)
	}

	// provider no longer configured: skipped
	f, _ = newFake("meta", false)
	f.settings.MetaCAPIToken = ""
	run(f, siteDelivery(t, "meta", testSiteEvent(KindPageView)), senders)
	if f.status != "skipped" || f.reason != "not_configured" {
		t.Fatalf("status=%s reason=%s", f.status, f.reason)
	}
}
