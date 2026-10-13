package httpserver

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

// Without the refund engine the scan rule cannot be checked: a cancellation
// that would return money is refused with 503, never let through on a guess.
func TestCancelMoneyAllowedForScan_NoEngineIsUnavailable(t *testing.T) {
	s := &Server{}
	for _, mode := range []string{"manual", "automatic"} {
		r := httptest.NewRequest(http.MethodPost, "/v1/tickets/x/cancel",
			bytes.NewBufferString(`{"reason":"r","refund_mode":"`+mode+`","refund_amount":100}`))
		w := httptest.NewRecorder()
		if s.cancelMoneyAllowedForScan(w, r, uuid.New(), uuid.New()) || w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s without the refund engine: allowed or %d; want refused with 503", mode, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/tickets/x/cancel", bytes.NewBufferString(`{"reason":"r","refund_mode":"none"}`))
	if !s.cancelMoneyAllowedForScan(httptest.NewRecorder(), r, uuid.New(), uuid.New()) {
		t.Error("a cancellation without money was refused")
	}
}
