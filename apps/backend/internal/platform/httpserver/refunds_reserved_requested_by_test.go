package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestRefundCreate_ReservedRequestedByReturns400 (PAY-03 fifth review,
// M-4): "ticket.cancel:" is the marker of the refund the ticket-cancel route
// writes. A client of POST /v1/refunds must not be able to dress a flat
// refund up as one, whatever case or padding it uses.
func TestRefundCreate_ReservedRequestedByReturns400(t *testing.T) {
	s := buildRefundServer(t)
	tok := mintRefundToken(t, s)
	for _, rb := range []string{"ticket.cancel:" + uuid.NewString(), "  Ticket.Cancel:" + uuid.NewString()} {
		body := `{"payment_intent_id":"00000000-0000-0000-0000-000000000001","amount":1000,"currency":"EUR","requested_by":"` + rb + `"}`
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/refunds", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		s.router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("requested_by %q: got %d, want 400", rb, w.Code)
		}
		var env struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if env.Error.Code != "refund.reserved_requested_by" {
			t.Fatalf("requested_by %q: code %q, body %s", rb, env.Error.Code, w.Body.String())
		}
	}
}
