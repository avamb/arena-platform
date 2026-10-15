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

// Verify implements Verifier.
func (v *TurnstileVerifier) Verify(ctx context.Context, token, remoteIP string) (bool, error) {
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
	if v.Action != "" && out.Action != v.Action {
		return false, nil
	}
	if len(v.Hostnames) > 0 && !hostAllowed(out.Hostname, v.Hostnames) {
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
