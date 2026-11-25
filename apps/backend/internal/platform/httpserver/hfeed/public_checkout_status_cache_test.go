package hfeed

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The order status changes within seconds of a payment and carries ticket
// links: every answer, errors included, must be uncacheable.
func TestHandleGetPublicCheckoutStatus_SetsNoStoreOnErrors(t *testing.T) {
	h := &Handler{} // no queries wired: the 503 dependency path
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/checkout/abc", nil)

	h.HandleGetPublicCheckoutStatus(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
