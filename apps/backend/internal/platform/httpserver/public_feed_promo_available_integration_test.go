//go:build integration

// public_feed_promo_available_integration_test.go — the public feed session
// says whether the organization has a promo code a buyer could still use, so
// the widget shows its promo field only then. It is existence only: no code,
// no scope, no per-customer data.
//
// Run with:
//
//	go test -tags integration ./apps/backend/internal/platform/httpserver/ \
//	    -run TestPublicFeedPromoAvailable
package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// sessionPromoFlag reads GET .../events/{id} through the real router and
// returns the flag of the fixture's session, plus the raw body and headers.
func (f *hostedOrgFixture) sessionPromoFlag(t *testing.T, srv *Server) (bool, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/public/feeds/"+f.feedToken+"/events/"+f.eventID.String(), nil)
	req.Header.Set("Origin", hostedTicketsBaseURL)
	rec := httptest.NewRecorder()
	srv.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET event = %d; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Event struct {
			Sessions []map[string]json.RawMessage `json:"sessions"`
		} `json:"event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	for _, s := range body.Event.Sessions {
		var id string
		_ = json.Unmarshal(s["id"], &id)
		if id != f.sessionID.String() {
			continue
		}
		raw, ok := s["promo_codes_available"]
		if !ok {
			t.Fatalf("session carries no promo_codes_available: %v", s)
		}
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("promo_codes_available is not a boolean: %s", raw)
		}
		return v, rec.Header()
	}
	t.Fatalf("session %s not in the event answer", f.sessionID)
	return false, nil
}

func TestPublicFeedPromoAvailable_FollowsTheOrganizationsCodes(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "pav")
	defer f.cleanup()
	defer f.cleanupPromos()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	want := func(name string, expect bool) {
		t.Helper()
		got, _ := f.sessionPromoFlag(t, srv)
		if got != expect {
			t.Errorf("%s: promo_codes_available = %v, want %v", name, got, expect)
		}
	}
	reset := func() {
		for _, sql := range []string{
			`DELETE FROM promo_code_redemptions WHERE promo_code_id IN (SELECT id FROM promo_codes WHERE org_id = $1)`,
			`DELETE FROM promo_codes WHERE org_id = $1`,
		} {
			if _, err := pool.Exec(ctx, sql, f.orgID); err != nil {
				t.Fatalf("reset promos: %v", err)
			}
		}
	}

	// An organization with no codes.
	want("no codes", false)

	// Only an expired code.
	reset()
	f.insertPromo(t, ctx, "OLD", "percent", 10, ", valid_from, valid_until", ", now() - interval '2 days', now() - interval '1 day'")
	want("only an expired code", false)

	// Only a code whose window has not opened yet.
	reset()
	f.insertPromo(t, ctx, "SOON", "percent", 10, ", valid_from", ", now() + interval '1 day'")
	want("only a code that is not yet valid", false)

	// Only an exhausted code (max_uses reached).
	reset()
	spent := f.insertPromo(t, ctx, "SPENT", "percent", 10, ", max_uses", ", 1")
	if _, err := pool.Exec(ctx,
		`INSERT INTO promo_code_redemptions (promo_code_id, discount_amount, order_amount) VALUES ($1, 0, 0)`, spent); err != nil {
		t.Fatalf("insert redemption: %v", err)
	}
	want("only an exhausted code", false)

	// Only a deleted code, only a paused one.
	reset()
	gone := f.insertPromo(t, ctx, "GONE", "percent", 10, "", "")
	if _, err := pool.Exec(ctx, `UPDATE promo_codes SET deleted_at = now() WHERE id = $1`, gone); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	f.insertPromo(t, ctx, "PAUSED", "percent", 10, ", status", ", 'paused'")
	want("only a deleted and a paused code", false)

	// One active code is enough, even beside dead ones.
	f.insertPromo(t, ctx, "LIVE", "percent", 10, ", max_uses, valid_until", ", 5, now() + interval '1 day'")
	want("one active code", true)

	// A code with uses left is still active.
	reset()
	partly := f.insertPromo(t, ctx, "PARTLY", "percent", 10, ", max_uses", ", 2")
	if _, err := pool.Exec(ctx,
		`INSERT INTO promo_code_redemptions (promo_code_id, discount_amount, order_amount) VALUES ($1, 0, 0)`, partly); err != nil {
		t.Fatalf("insert redemption: %v", err)
	}
	want("a code with a use left", true)
}

// The flag is existence only: another organization's code never switches it on,
// and a code limited to another event or category still does (the quote route
// is the authority on whether it fits a cart).
func TestPublicFeedPromoAvailable_IsPerOrganizationAndIgnoresScope(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	a := newHostedOrgFixture(t, ctx, pool, "pva")
	defer a.cleanup()
	defer a.cleanupPromos()
	b := newHostedOrgFixture(t, ctx, pool, "pvb")
	defer b.cleanup()
	defer b.cleanupPromos()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	b.insertPromo(t, ctx, "THEIRS", "percent", 10, "", "")
	if got, _ := a.sessionPromoFlag(t, srv); got {
		t.Error("another organization's code switched the flag on")
	}

	a.insertPromo(t, ctx, "ELSEWHERE", "percent", 10, ", applies_to_tier_ids, max_uses_per_customer", ", ARRAY[$6::uuid], 1", uuid.New().String())
	if got, _ := a.sessionPromoFlag(t, srv); !got {
		t.Error("a code scoped to another category must still count: the quote decides fit")
	}
}

// The flag rides on a cacheable answer, which must keep varying by Origin.
func TestPublicFeedPromoAvailable_StaysCacheableAndVariesByOrigin(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()

	f := newHostedOrgFixture(t, ctx, pool, "pvc")
	defer f.cleanup()
	defer f.cleanupPromos()
	srv := buildHostedCheckoutServer(t, pool, newStubStripe(t).baseURL())

	_, h := f.sessionPromoFlag(t, srv)
	if cc := h.Get("Cache-Control"); !strings.Contains(cc, "public") {
		t.Errorf("Cache-Control = %q, want a public, cacheable answer", cc)
	}
	if !strings.Contains(h.Get("Vary"), "Origin") {
		t.Errorf("Vary = %q, want Origin", h.Get("Vary"))
	}
}
