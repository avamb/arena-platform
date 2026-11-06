package honboarding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrCaptchaNotConfigured means production has no Turnstile secret: the
// routes that need a captcha answer 503 rather than run unprotected.
var ErrCaptchaNotConfigured = errors.New("honboarding: captcha is not configured")

// Verifier checks a Cloudflare Turnstile response token.
type Verifier interface {
	Verify(ctx context.Context, token, remoteIP string) (bool, error)
}

const turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// TurnstileVerifier talks to Cloudflare. With an empty secret it accepts
// everything unless Required (production) is set, in which case it reports
// ErrCaptchaNotConfigured.
type TurnstileVerifier struct {
	Secret   string
	Required bool
	// Action, when set, is the widget action the token must carry. The
	// contact form renders with action "contact", so a token solved on another
	// form that shares the secret (the onboarding form) is refused.
	Action string
	// Hostnames, when not empty, lists the hosts a token may be solved on;
	// Cloudflare reports the host in the verification answer.
	Hostnames []string
	// URL overrides the endpoint in tests.
	URL    string
	Client *http.Client
}

// Expect is what one form requires of a Turnstile answer: the widget action
// it renders with and the hosts the token may have been solved on.
type Expect struct {
	Action    string
	Hostnames []string
}

// ScopedVerifier checks a token against an Expect chosen per request. The
// static sites' forms (spec 62 section 10.2) differ by action (club, contact,
// private_event, my_tickets) and by the hosts of the channel's site.
type ScopedVerifier interface {
	VerifyScoped(ctx context.Context, token, remoteIP string, want Expect) (bool, error)
}

// VerifyFor runs the check with want when the verifier can take it, else
// falls back to its own Verify (test doubles). It is the ONE entry point the
// site forms share, so the "my tickets" link request reuses it unchanged.
func VerifyFor(ctx context.Context, v Verifier, token, remoteIP string, want Expect) (bool, error) {
	if sv, ok := v.(ScopedVerifier); ok {
		return sv.VerifyScoped(ctx, token, remoteIP, want)
	}
	return v.Verify(ctx, token, remoteIP)
}

// HostnamesOfOrigins returns the host names of absolute http(s) origins, in
// order, skipping anything else. A port is dropped, as Cloudflare reports the
// bare host.
func HostnamesOfOrigins(origins []string) []string {
	var out []string
	for _, o := range origins {
		u, err := url.Parse(strings.TrimSpace(o))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" {
			continue
		}
		out = append(out, strings.ToLower(u.Hostname()))
	}
	return out
}

// Verify implements Verifier.
func (v *TurnstileVerifier) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
	return v.VerifyScoped(ctx, token, remoteIP, Expect{Action: v.Action, Hostnames: v.Hostnames})
}

// VerifyScoped implements ScopedVerifier. An empty want.Action or an empty
// want.Hostnames skips that comparison.
func (v *TurnstileVerifier) VerifyScoped(ctx context.Context, token, remoteIP string, want Expect) (bool, error) {
	if strings.TrimSpace(v.Secret) == "" {
		if v.Required {
			return false, ErrCaptchaNotConfigured
		}
		return true, nil
	}
	if strings.TrimSpace(token) == "" {
		return false, nil
	}
	endpoint := v.URL
	if endpoint == "" {
		endpoint = turnstileVerifyURL
	}
	form := url.Values{"secret": {v.Secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	// endpoint is the fixed Cloudflare URL; only tests override it.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode())) //nolint:gosec // see above
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req) //nolint:gosec // see above
	if err != nil {
		return false, fmt.Errorf("turnstile: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Success  bool   `json:"success"`
		Action   string `json:"action"`
		Hostname string `json:"hostname"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return false, fmt.Errorf("turnstile: decode: %w", err)
	}
	if !out.Success {
		return false, nil
	}
	if want.Action != "" && out.Action != want.Action {
		return false, nil
	}
	if len(want.Hostnames) > 0 && !hostAllowed(out.Hostname, want.Hostnames) {
		return false, nil
	}
	return true, nil
}

// hostAllowed reports whether host is one of allowed, ignoring case.
func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, a := range allowed {
		if strings.ToLower(strings.TrimSpace(a)) == host {
			return true
		}
	}
	return false
}
