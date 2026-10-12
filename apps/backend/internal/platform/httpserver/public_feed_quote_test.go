// public_feed_quote_test.go — unit tests (no database) for
// POST /v1/public/feeds/{feed_token}/checkout/quote: the route is mounted,
// answers without a login, shares the public feed rate limits, and refuses a
// malformed request before it touches the database.
package httpserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func buildQuoteServer(t *testing.T, feedTokenLimit, ipLimit int) *Server {
	t.Helper()
	cfg := &config.Config{
		AppEnv:                   config.EnvDevelopment,
		RequestTimeout:           5 * time.Second,
		BodyLimitBytes:           1 << 20,
		JWTSecretStub:            "test-secret-which-is-long-enough-for-hs256",
		EnableStubAuth:           true,
		DefaultLocale:            "en",
		ActiveLocales:            []string{"en"},
		PublicFeedTokenRateLimit: feedTokenLimit,
		PublicAPIIPRateLimit:     ipLimit,
	}
	return New(Options{
		Config:             cfg,
		PublicFeedQueries:  gen.New(nil),
		CheckoutQueries:    gen.New(nil),
		ReservationQueries: gen.New(nil),
		TierQueries:        gen.New(nil),
		PromoQueries:       gen.New(nil),
	})
}

func postQuote(s *Server, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/"+token+"/checkout/quote", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func TestPublicQuote_RouteIsMountedAndNeedsNoLogin(t *testing.T) {
	s := buildQuoteServer(t, 1000, 1000)
	w := postQuote(s, "any-token", "")
	if w.Code == http.StatusNotFound || w.Code == http.StatusUnauthorized {
		t.Fatalf("quote route answered %d; want it mounted and public", w.Code)
	}
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "checkout.empty_body") {
		t.Fatalf("empty body = %d %s, want 400 checkout.empty_body", w.Code, w.Body.String())
	}
}

func TestPublicQuote_MalformedRequestsAreRefusedBeforeTheDatabase(t *testing.T) {
	s := buildQuoteServer(t, 1000, 1000)
	cases := []struct {
		name, body, code string
	}{
		{"not json", `{`, "checkout.invalid_json"},
		{"bad session", `{"session_id":"nope","ga_items":[{"tier_id":"x","quantity":1}]}`, "checkout.invalid_session_id"},
		{"no lines", `{"session_id":"11111111-1111-4111-8111-111111111111"}`, "checkout.missing_items"},
		{"bad tier", `{"session_id":"11111111-1111-4111-8111-111111111111","ga_items":[{"tier_id":"x","quantity":1}]}`, "checkout.invalid_tier_id"},
		{"zero quantity", `{"session_id":"11111111-1111-4111-8111-111111111111","ga_items":[{"tier_id":"22222222-2222-4222-8222-222222222222","quantity":0}]}`, "checkout.invalid_qty"},
	}
	for _, c := range cases {
		w := postQuote(s, "any-token", c.body)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.code) {
			t.Errorf("%s: got %d %s, want 400 %s", c.name, w.Code, w.Body.String(), c.code)
		}
	}
}

// The quote shares the feed token's site-wide bucket and the per-IP one with
// every other public feed route, so it is no cheaper a way to hammer the
// platform — or to guess promo codes — than checkout/start.
func TestPublicQuote_SharesTheFeedRateLimit(t *testing.T) {
	s := buildQuoteServer(t, 1, 100000)
	if w := postQuote(s, "limited-token", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("first request = %d, want 400 (past the limiter)", w.Code)
	}
	w := postQuote(s, "limited-token", "")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "feed.rate_limited") {
		t.Fatalf("second request = %d %s, want 429 feed.rate_limited", w.Code, w.Body.String())
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("429 carries no Retry-After header")
	}
}

func TestPublicQuote_PerIPLimitApplies(t *testing.T) {
	s := buildQuoteServer(t, 100000, 1)
	_ = postQuote(s, "tok-a", "")
	if w := postQuote(s, "tok-b", ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request from the same IP on another token = %d, want 429", w.Code)
	}
}

func capBody(lines, qty int, seats int) string {
	items := make([]string, 0, lines)
	for i := 0; i < lines; i++ {
		items = append(items, fmt.Sprintf(`{"tier_id":"22222222-2222-4222-8222-%012d","quantity":%d}`, i, qty))
	}
	seatKeys := make([]string, 0, seats)
	for i := 0; i < seats; i++ {
		seatKeys = append(seatKeys, fmt.Sprintf(`"S%d"`, i))
	}
	return fmt.Sprintf(`{"session_id":"11111111-1111-4111-8111-111111111111","holder_email":"a@b.co","ga_items":[%s],"seats":[%s]}`,
		strings.Join(items, ","), strings.Join(seatKeys, ","))
}

// One anonymous request must not be able to ask for unbounded pricing work: a
// cart above the caps is refused with a stable 400 before any database read.
// Both public cart routes share the caps (checkCartCaps).
func TestPublicCart_CapsAreRefusedBeforeTheDatabase(t *testing.T) {
	s := buildQuoteServer(t, 100000, 100000)
	cases := []struct {
		name, body, code string
	}{
		{"too many lines", capBody(21, 1, 0), "checkout.cart_too_large"},
		{"too many seats", capBody(0, 1, 101), "checkout.cart_too_large"},
		{"line quantity", capBody(1, 51, 0), "checkout.invalid_qty"},
	}
	for _, route := range []string{"/checkout/quote", "/checkout/start"} {
		for _, c := range cases {
			req := httptest.NewRequest(http.MethodPost, "/v1/public/feeds/cap-token"+route, strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.router.ServeHTTP(w, req)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.code) {
				t.Errorf("%s %s: got %d %s, want 400 %s", route, c.name, w.Code, w.Body.String(), c.code)
			}
		}
	}
}

// A quote is a price for one cart at one moment: no answer of the route, a
// refusal and a 429 included, may be stored by a browser or a proxy.
func TestPublicQuote_IsNeverCached(t *testing.T) {
	s := buildQuoteServer(t, 1, 100000)
	for i, body := range []string{"", ""} { // the second one is rate limited
		w := postQuote(s, "no-cache-token", body)
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("response %d (%d): Cache-Control = %q, want no-store", i, w.Code, got)
		}
	}
}
