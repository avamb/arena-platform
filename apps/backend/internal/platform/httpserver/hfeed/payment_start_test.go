package hfeed

import (
	"strings"
	"testing"
)

// The return-URL allow-list is the only thing standing between the widget's
// payment redirect and an open redirect: whatever it approves becomes Stripe's
// success_url and cancel_url, which a buyer's browser follows automatically
// after a payment. Everything below is a rule that must not soften by
// accident.

func TestReturnURLPolicy_AcceptsAnAllowedOriginAndKeepsThePath(t *testing.T) {
	p := ReturnURLPolicy{
		AllowedOrigins: []string{"https://tickets.example.com"},
		Fallback:       "https://fallback.example.com",
	}
	got, err := p.Resolve("https://tickets.example.com/shows/summer")
	if err != nil {
		t.Fatalf("Resolve returned an error: %v", err)
	}
	// The embedding page may live at a sub-path; sending the buyer back to
	// the site root would lose the widget.
	if got != "https://tickets.example.com/shows/summer" {
		t.Errorf("Resolve = %q; want the allowed URL with its path preserved", got)
	}
}

func TestReturnURLPolicy_DropsQueryAndFragment(t *testing.T) {
	p := ReturnURLPolicy{AllowedOrigins: []string{"https://tickets.example.com"}}
	got, err := p.Resolve("https://tickets.example.com/shows?utm=x&checkout_token=attacker#frag")
	if err != nil {
		t.Fatalf("Resolve returned an error: %v", err)
	}
	// The only query parameter the provider redirect may carry is arena's own
	// checkout_token, appended by ReturnURLWithToken. A caller-supplied one
	// would let a stranger's token be shown to this buyer.
	if strings.ContainsAny(got, "?#") {
		t.Errorf("Resolve = %q; want the query and fragment stripped", got)
	}
	if got != "https://tickets.example.com/shows" {
		t.Errorf("Resolve = %q; want https://tickets.example.com/shows", got)
	}
}

func TestReturnURLPolicy_RefusesAForeignOriginAndFallsBack(t *testing.T) {
	p := ReturnURLPolicy{
		AllowedOrigins: []string{"https://tickets.example.com"},
		Fallback:       "https://tickets.example.com",
	}
	for _, candidate := range []string{
		"https://evil.example.com/steal",
		"https://tickets.example.com.evil.com/",
		"https://eviltickets.example.com/",
		"http://tickets.example.com/", // scheme must match too
		"//tickets.example.com/",      // protocol-relative is not absolute
		"javascript:alert(1)",         //nolint:gosec // intentionally hostile input
		"/relative/path",
		"",
		"   ",
		"not a url at all",
	} {
		got, err := p.Resolve(candidate)
		if err != nil {
			t.Fatalf("Resolve(%q) returned an error instead of falling back: %v", candidate, err)
		}
		if got != "https://tickets.example.com" {
			t.Errorf("Resolve(%q) = %q; want the configured fallback", candidate, got)
		}
	}
}

func TestReturnURLPolicy_RefusesEverythingWithNoFallback(t *testing.T) {
	p := ReturnURLPolicy{AllowedOrigins: []string{"https://tickets.example.com"}}
	_, err := p.Resolve("https://evil.example.com/")
	if err == nil {
		t.Fatal("Resolve accepted a foreign origin with no fallback configured")
	}
	pse, ok := AsPaymentStartError(err)
	if !ok {
		t.Fatalf("error %v is not a *PaymentStartError", err)
	}
	if pse.Code != ErrCodeInvalidReturnURL {
		t.Errorf("code = %q; want %q", pse.Code, ErrCodeInvalidReturnURL)
	}
	if pse.Status != 400 {
		t.Errorf("status = %d; want 400", pse.Status)
	}
}

func TestReturnURLPolicy_WildcardAllowsAnyOrigin(t *testing.T) {
	// "*" is the local/dev CORS default; production config validation already
	// forbids it, so this only has to behave predictably, not safely.
	p := ReturnURLPolicy{AllowedOrigins: []string{"*"}}
	got, err := p.Resolve("https://anything.example.org/page")
	if err != nil {
		t.Fatalf("Resolve returned an error: %v", err)
	}
	if got != "https://anything.example.org/page" {
		t.Errorf("Resolve = %q; want the candidate accepted verbatim", got)
	}
}

func TestReturnURLPolicy_OriginComparisonIgnoresCaseAndTrailingSlash(t *testing.T) {
	p := ReturnURLPolicy{AllowedOrigins: []string{"HTTPS://Tickets.Example.COM/"}}
	got, err := p.Resolve("https://tickets.example.com/shows")
	if err != nil {
		t.Fatalf("Resolve returned an error: %v", err)
	}
	if got != "https://tickets.example.com/shows" {
		t.Errorf("Resolve = %q; host and scheme comparison must be case-insensitive", got)
	}
}

func TestReturnURLPolicy_PortIsPartOfTheOrigin(t *testing.T) {
	p := ReturnURLPolicy{AllowedOrigins: []string{"http://localhost:5173"}}

	got, err := p.Resolve("http://localhost:5173/embed")
	if err != nil {
		t.Fatalf("Resolve returned an error: %v", err)
	}
	if got != "http://localhost:5173/embed" {
		t.Errorf("Resolve = %q; want the matching port accepted", got)
	}

	// A different port is a different origin and must NOT be accepted — with
	// no fallback that means an error, not a silent pass.
	if _, err := p.Resolve("http://localhost:9999/embed"); err == nil {
		t.Error("Resolve accepted a different port on the same host")
	}
}

func TestReturnURLWithToken_UsesTheParameterTheWidgetReads(t *testing.T) {
	// getCheckoutTokenFromSearch (apps/widget/src/lib/store.ts) reads exactly
	// `checkout_token`. Rename it on either side and a paying buyer lands on
	// a page that shows them an empty cart.
	got := ReturnURLWithToken("https://tickets.example.com/shows", "abc123")
	if got != "https://tickets.example.com/shows?checkout_token=abc123" {
		t.Errorf("ReturnURLWithToken = %q; want ?checkout_token=abc123 appended", got)
	}
}

func TestReturnURLWithToken_AppendsToAnExistingQuery(t *testing.T) {
	got := ReturnURLWithToken("https://tickets.example.com/shows?lang=cs", "abc123")
	if got != "https://tickets.example.com/shows?lang=cs&checkout_token=abc123" {
		t.Errorf("ReturnURLWithToken = %q; want the token appended with &", got)
	}
}

func TestReturnURLWithToken_EscapesTheToken(t *testing.T) {
	got := ReturnURLWithToken("https://tickets.example.com/", "a b&c=d")
	if strings.Contains(got, "a b&c=d") {
		t.Errorf("ReturnURLWithToken = %q; the token must be query-escaped", got)
	}
}

// TestStripePaymentStarter_NilQueriesIsNotConfigured proves a deployment with
// no payment wiring answers a specific, loud error rather than panicking or —
// worse — handing the buyer a dead redirect, which is exactly what this whole
// change exists to remove.
func TestStripePaymentStarter_NilQueriesIsNotConfigured(t *testing.T) {
	s := NewStripePaymentStarter(nil, nil, "")
	_, err := s.StartHostedCheckout(t.Context(), HostedCheckoutRequest{})
	if err == nil {
		t.Fatal("StartHostedCheckout succeeded with no queries wired")
	}
	pse, ok := AsPaymentStartError(err)
	if !ok {
		t.Fatalf("error %v is not a *PaymentStartError", err)
	}
	if pse.Code != ErrCodePaymentNotConfigured {
		t.Errorf("code = %q; want %q", pse.Code, ErrCodePaymentNotConfigured)
	}
}
