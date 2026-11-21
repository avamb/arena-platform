// public_feed_checkout_idempotency_test.go — П-19: the Idempotency-Key of
// POST /v1/public/feeds/{feed_token}/checkout/start, without a database.
//
// The handler is built with query handles that have NO connection behind
// them (gen.New(nil)): had the hold logic run, the request would panic. A
// replay or a 409 answering cleanly therefore proves the key is checked
// before anything that takes a hold.
package hfeed

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver/hcheckout"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/idempotency"
)

type denyAllRateLimiter struct{}

func (denyAllRateLimiter) CheckFeedToken(string) (bool, int)     { return false, 7 }
func (denyAllRateLimiter) CheckCheckoutToken(string) (bool, int) { return false, 7 }
func (denyAllRateLimiter) CheckIP(string) (bool, int)            { return false, 7 }

// memIdemStore is an in-memory idempotency.Store recording the scopes it
// was asked about.
type memIdemStore struct {
	mu      sync.Mutex
	rows    map[string]idempotency.StoredResponse
	scopes  []string
	lookups int
}

func newMemIdemStore() *memIdemStore {
	return &memIdemStore{rows: map[string]idempotency.StoredResponse{}}
}

func (s *memIdemStore) Lookup(_ context.Context, key, scope string) (idempotency.StoredResponse, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	s.scopes = append(s.scopes, scope)
	r, ok := s.rows[key+"\x00"+scope]
	return r, ok, nil
}

func (s *memIdemStore) Save(_ context.Context, key, scope, _ string, resp idempotency.StoredResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.rows[key+"\x00"+scope]; !ok {
		s.rows[key+"\x00"+scope] = resp
	}
	return nil
}

func (s *memIdemStore) put(key, scope string, resp idempotency.StoredResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if resp.ExpiresAt.IsZero() {
		resp.ExpiresAt = time.Now().Add(time.Hour)
	}
	s.rows[key+"\x00"+scope] = resp
}

func checkoutIdemMiddleware(store idempotency.Store) func(http.Handler) http.Handler {
	return idempotency.Middleware(store, idempotency.Options{
		Scope:        "POST checkout/start",
		ScopeFunc:    CheckoutStartIdempotencyScope,
		Optional:     true,
		MaxBodyBytes: CheckoutStartMaxBody,
	})
}

func newIdemCheckoutHandler(rl RateLimiter, store idempotency.Store) *Handler {
	q := gen.New(nil) // non-nil handles, no connection behind them
	h := New(q, q, q, q, q, q, q, q, q, q, q, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil, rl, hcheckout.PricingRules{}, 0)
	return h.WithCheckoutIdempotency(checkoutIdemMiddleware(store))
}

func checkoutStartRequest(feedToken, key, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost,
		"/v1/public/feeds/"+feedToken+"/checkout/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(idempotency.HeaderName, key)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("feed_token", feedToken)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

const storedCheckoutBody = `{"checkout_token":"tok-1","redirect_url":"https://pay.example/p/1"}`

func TestCheckoutStartIdem_SameKeyReplaysBeforeTheHold(t *testing.T) {
	store := newMemIdemStore()
	// An empty request hash matches any body: the stored answer of the
	// first attempt, whose hold has since been consumed.
	store.put("k-1", "POST checkout/start:feed-a", idempotency.StoredResponse{
		Status: http.StatusCreated, Body: []byte(storedCheckoutBody),
	})
	h := newIdemCheckoutHandler(allowAllRateLimiter{}, store)

	rec := httptest.NewRecorder()
	h.HandlePublicFeedCheckoutStart(rec, checkoutStartRequest("feed-a", "k-1", `{"session_id":"x"}`))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d, want the stored 201; body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != storedCheckoutBody {
		t.Fatalf("body %s, want the stored checkout and payment link", rec.Body.String())
	}
	if rec.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("a replay must say so")
	}
	if len(store.scopes) != 1 || store.scopes[0] != "POST checkout/start:feed-a" {
		t.Fatalf("scopes %v, want one lookup scoped to the feed token", store.scopes)
	}
}

func TestCheckoutStartIdem_SameKeyOtherBodyIs409(t *testing.T) {
	store := newMemIdemStore()
	store.put("k-1", "POST checkout/start:feed-a", idempotency.StoredResponse{
		Status: http.StatusCreated, Body: []byte(storedCheckoutBody), RequestHash: "hash-of-another-body",
	})
	h := newIdemCheckoutHandler(allowAllRateLimiter{}, store)

	rec := httptest.NewRecorder()
	h.HandlePublicFeedCheckoutStart(rec, checkoutStartRequest("feed-a", "k-1", `{"session_id":"y"}`))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "idempotency.body_mismatch") {
		t.Fatalf("body %s, want idempotency.body_mismatch", rec.Body.String())
	}
}

func TestCheckoutStartIdem_KeyOfAnotherFeedDoesNotReplay(t *testing.T) {
	store := newMemIdemStore()
	store.put("k-1", "POST checkout/start:feed-a", idempotency.StoredResponse{
		Status: http.StatusCreated, Body: []byte(storedCheckoutBody),
	})
	h := newIdemCheckoutHandler(allowAllRateLimiter{}, store)

	rec := httptest.NewRecorder()
	// Same key on another channel: a miss, so the handler runs and stops at
	// its own validation (no body), long before any hold.
	h.HandlePublicFeedCheckoutStart(rec, checkoutStartRequest("feed-b", "k-1", ``))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "checkout.empty_body") {
		t.Fatalf("status %d body %s, want the handler's own 400", rec.Code, rec.Body.String())
	}
}

func TestCheckoutStartIdem_NoKeyRunsAsBefore(t *testing.T) {
	store := newMemIdemStore()
	h := newIdemCheckoutHandler(allowAllRateLimiter{}, store)

	rec := httptest.NewRecorder()
	h.HandlePublicFeedCheckoutStart(rec, checkoutStartRequest("feed-a", "", `{"session_id":"not-a-uuid"}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "checkout.invalid_session_id") {
		t.Fatalf("status %d body %s, want the handler's own 400", rec.Code, rec.Body.String())
	}
	if store.lookups != 0 {
		t.Fatalf("a request without a key looked the store up %d times", store.lookups)
	}
}

func TestCheckoutStartIdem_RateLimitComesFirst(t *testing.T) {
	store := newMemIdemStore()
	store.put("k-1", "POST checkout/start:feed-a", idempotency.StoredResponse{
		Status: http.StatusCreated, Body: []byte(storedCheckoutBody),
	})
	h := newIdemCheckoutHandler(denyAllRateLimiter{}, store)

	rec := httptest.NewRecorder()
	h.HandlePublicFeedCheckoutStart(rec, checkoutStartRequest("feed-a", "k-1", `{}`))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if store.lookups != 0 {
		t.Fatalf("a rate-limited request reached the idempotency store")
	}
}

// The whole round trip through the middleware with a stand-in for the
// checkout body: one key runs the checkout once and answers the same
// checkout and link every time; without a key every request runs.
func TestCheckoutStartIdem_RoundTrip(t *testing.T) {
	store := newMemIdemStore()
	h := newIdemCheckoutHandler(allowAllRateLimiter{}, store)
	calls := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"checkout_token":"tok-` + strconv.Itoa(calls) + `","redirect_url":"https://pay/` + strconv.Itoa(calls) + `"}`))
	})
	send := func(key, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.serveCheckoutIdempotent(rec, checkoutStartRequest("feed-a", key, body), inner)
		return rec
	}

	first := send("k", `{"a":1}`)
	again := send("k", `{"a":1}`)
	if calls != 1 || first.Body.String() != again.Body.String() {
		t.Fatalf("calls=%d first=%s again=%s, want one checkout answered twice", calls, first.Body.String(), again.Body.String())
	}
	if other := send("k", `{"a":2}`); other.Code != http.StatusConflict {
		t.Fatalf("other body: status %d, want 409", other.Code)
	}
	send("", `{"a":1}`)
	send("", `{"a":1}`)
	if calls != 3 {
		t.Fatalf("calls=%d, want 3 (two keyless requests both run)", calls)
	}
}
