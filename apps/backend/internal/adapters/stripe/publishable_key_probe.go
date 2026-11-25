// publishable_key_probe.go — ask Stripe whether a publishable key exists and
// belongs to the same account as the secret key.
//
// A publishable key is public and never a credential, so nothing verifies it
// at save time; a mistyped one only surfaces in the buyer's browser.
//
// Step 1: POST /tokens with the publishable key creates a PII token (no card
// data, no money). 401 means Stripe does not know the key.
// Step 2: GET /tokens/{id} with the secret key. A token made under another
// account is invisible to it: 404 resource_missing means the two keys come
// from different accounts.
//
// Only those two verdicts block a save. Every other answer, a timeout, a
// network error, a restricted key that may not read tokens and a missing
// secret key mean "could not check" and never block.
package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
)

// publishableKeyProbeTimeout bounds EACH step so a save never hangs on Stripe.
const publishableKeyProbeTimeout = 5 * time.Second

// probeBodyLimit caps what is read from a probe answer.
const probeBodyLimit = 1 << 16

// ProbePublishableKey returns an error wrapping payments.ErrCredentialRefused
// when Stripe answers 401 to the publishable key, and one wrapping
// payments.ErrBrowserKeyAccountMismatch when the secret key cannot see a token
// the publishable key made. baseURL is the API root WITH its /v1 segment;
// empty means https://api.stripe.com/v1. A nil client uses a default one. An
// empty secretKey runs the existence check only. Keys are never put into an
// error.
func ProbePublishableKey(ctx context.Context, client *http.Client, baseURL, publishableKey, secretKey string) error {
	pk := strings.TrimSpace(publishableKey)
	if pk == "" {
		return nil
	}
	sk := strings.TrimSpace(secretKey)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if client == nil {
		client = &http.Client{}
	}

	// Step 1: a PII token made with the publishable key.
	ctx1, cancel1 := context.WithTimeout(ctx, publishableKeyProbeTimeout)
	defer cancel1()
	form := url.Values{"pii[id_number]": {"000000000"}}
	req, err := http.NewRequestWithContext(ctx1, http.MethodPost, baseURL+"/tokens", strings.NewReader(form.Encode()))
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+pk)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	status, body := resp.StatusCode, readProbeBody(resp)
	if status == http.StatusUnauthorized {
		return fmt.Errorf("%w: stripe does not recognise the publishable key", payments.ErrCredentialRefused)
	}
	if status != http.StatusOK || sk == "" {
		return nil
	}
	var tok struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(body, &tok) != nil || tok.ID == "" {
		return nil
	}

	// Step 2: the secret key reads that token only in the same account.
	ctx2, cancel2 := context.WithTimeout(ctx, publishableKeyProbeTimeout)
	defer cancel2()
	req2, err := http.NewRequestWithContext(ctx2, http.MethodGet, baseURL+"/tokens/"+url.PathEscape(tok.ID), nil)
	if err != nil {
		return nil
	}
	req2.Header.Set("Authorization", "Bearer "+sk)
	resp2, err := client.Do(req2)
	if err != nil {
		return nil
	}
	status2, body2 := resp2.StatusCode, readProbeBody(resp2)
	if status2 == http.StatusNotFound {
		var e struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(body2, &e) == nil && e.Error.Code == "resource_missing" {
			return fmt.Errorf("%w: the secret key cannot see a token made by the publishable key", payments.ErrBrowserKeyAccountMismatch)
		}
	}
	return nil
}

// readProbeBody reads a capped body and closes it.
func readProbeBody(resp *http.Response) []byte {
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, probeBodyLimit))
	return b
}

// ProbeBrowserKey implements payments.BrowserKeyProber.
func (a *Adapter) ProbeBrowserKey(ctx context.Context, browserKey string, secrets map[string]string) error {
	return ProbePublishableKey(ctx, a.client, a.cfg.BaseURL, browserKey, secrets[SecretAPIKey])
}
