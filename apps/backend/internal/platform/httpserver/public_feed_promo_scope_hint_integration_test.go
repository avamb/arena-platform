//go:build integration

// public_feed_promo_scope_hint_integration_test.go — a code that exists but is
// bound to other sessions is refused with the stable promo.not_applicable
// code AND the events it works on, so a buyer (and the organizer reading the
// complaint) can see why. (A draft event is never named: hcheckout unit test.)
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestPublicQuote_ScopeHint
package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPublicQuote_ScopeHint(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "psh")
	t.Cleanup(f.cleanup)
	t.Cleanup(f.cleanupPromos)
	hs, _, _ := f.addHybridSession(t, ctx)
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	f.insertPromo(t, ctx, "OTHERDATE", "percent", 30, ", applies_to_session_ids", ", ARRAY[$6::uuid]", hs.String())
	payload := map[string]any{
		"session_id": f.sessionID.String(),
		"ga_items":   []map[string]any{{"tier_id": f.tierID.String(), "quantity": 1}},
		"promo_code": "OTHERDATE",
	}

	code, body := f.quote(t, srv, payload)
	var e struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	if code != http.StatusUnprocessableEntity || e.Error.Code != "promo.not_applicable" {
		t.Fatalf("quote = %d %s, want 422 promo.not_applicable", code, body)
	}
	events, _ := e.Error.Details["events"].(string)
	if !strings.HasPrefix(events, "HOCO Event") || !strings.Contains(e.Error.Message, events) {
		t.Fatalf("refusal does not name the event the code works on: %s", body)
	}
}
