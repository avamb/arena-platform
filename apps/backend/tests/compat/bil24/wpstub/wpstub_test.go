package wpstub_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/tests/compat/bil24/wpstub"
)

func post(t *testing.T, url string, body interface{}) *http.Response {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

func TestWpstub_Rejects400WithoutTypeAndData(t *testing.T) {
	s := wpstub.New()
	defer s.Close()

	// Missing both.
	resp := post(t, s.URL(), map[string]interface{}{})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty body: got %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Missing data.
	resp = post(t, s.URL(), map[string]interface{}{"type": "order.paid"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no data: got %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	// Missing type.
	resp = post(t, s.URL(), map[string]interface{}{"data": map[string]interface{}{"id": 1}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("no type: got %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()

	if got := len(s.Received()); got != 0 {
		t.Fatalf("rejected payloads must not be stored, got %d", got)
	}
}

func TestWpstub_Accepts200AndStores(t *testing.T) {
	s := wpstub.New()
	defer s.Close()

	resp := post(t, s.URL(), map[string]interface{}{
		"type": "order.paid",
		"data": map[string]interface{}{"id": 42, "sum": 1900},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d, want 200", resp.StatusCode)
	}
	var body map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("body: %v want {ok:true}", body)
	}
	rec := s.Received()
	if len(rec) != 1 || rec[0].Type != "order.paid" {
		t.Fatalf("stored: %#v", rec)
	}
}

func TestWpstub_TicketRefundedIsDeduplicatedByDataID(t *testing.T) {
	s := wpstub.New()
	defer s.Close()

	for i := 0; i < 3; i++ {
		resp := post(t, s.URL(), map[string]interface{}{
			"type": "ticket.refunded",
			"data": map[string]interface{}{"id": 3460841, "orderId": 2565353},
		})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("iter %d: got %d", i, resp.StatusCode)
		}
		resp.Body.Close()
	}
	rec := s.Received()
	if len(rec) != 1 {
		t.Fatalf("dedup: got %d events, want 1", len(rec))
	}
	if rec[0].Type != "ticket.refunded" {
		t.Fatalf("type: %q", rec[0].Type)
	}

	// A different id must be accepted as a separate event.
	resp := post(t, s.URL(), map[string]interface{}{
		"type": "ticket.refunded",
		"data": map[string]interface{}{"id": 3460842, "orderId": 2565353},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second id: got %d", resp.StatusCode)
	}
	resp.Body.Close()
	if got := len(s.Received()); got != 2 {
		t.Fatalf("after distinct id: %d events", got)
	}
}

func TestWpstub_MalformedJSONIs400(t *testing.T) {
	s := wpstub.New()
	defer s.Close()

	resp, err := http.Post(s.URL(), "application/json", strings.NewReader(`{"type":"ord`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestWpstub_ResetClearsLog(t *testing.T) {
	s := wpstub.New()
	defer s.Close()
	resp := post(t, s.URL(), map[string]interface{}{
		"type": "order.paid",
		"data": map[string]interface{}{"id": 1},
	})
	resp.Body.Close()
	s.Reset()
	if got := len(s.Received()); got != 0 {
		t.Fatalf("Reset should clear log, got %d", got)
	}
	// Refund dedup must also reset.
	for i := 0; i < 2; i++ {
		resp := post(t, s.URL(), map[string]interface{}{
			"type": "ticket.refunded",
			"data": map[string]interface{}{"id": 999},
		})
		resp.Body.Close()
	}
	if got := len(s.Received()); got != 1 {
		t.Fatalf("after Reset+2×refund: %d events, want 1", got)
	}
}

func TestWpstub_MethodNotAllowedForGET(t *testing.T) {
	s := wpstub.New()
	defer s.Close()
	resp, err := http.Get(s.URL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}
