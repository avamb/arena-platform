// Package brevo contains the small, credential-free boundary used to inspect
// Brevo sender identities. SMTP credentials remain platform configuration.
package brevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.brevo.com"

type DNSRecord struct {
	Type  string `json:"type"`
	Host  string `json:"host"`
	Value string `json:"value"`
}
type Sender struct {
	Email      string      `json:"email"`
	Active     bool        `json:"active"`
	DNSRecords []DNSRecord `json:"dns_records"`
	// DomainVerified is true when Brevo reports the sender's domain as
	// authenticated or verified. False when the domain lookup failed.
	DomainVerified bool `json:"domain_verified"`
}

// ErrSenderNotFound means Brevo has no sender with the requested address.
var ErrSenderNotFound = errors.New("brevo sender not found")

type Client struct {
	apiKey, baseURL string
	httpClient      *http.Client
}

func New(apiKey, baseURL string, httpClient *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{apiKey: strings.TrimSpace(apiKey), baseURL: strings.TrimRight(baseURL, "/"), httpClient: httpClient}
}

func (c *Client) Configured() bool { return c != nil && c.apiKey != "" }

type wireRecord struct {
	Type     string `json:"type"`
	Value    string `json:"value"`
	HostName string `json:"host_name"`
}

// getJSON performs an authenticated GET on a Brevo path and decodes the body.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("api-key", c.apiKey)
	req.Header.Set("accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("brevo request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("brevo request %s returned %s", path, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode brevo response: %w", err)
	}
	return nil
}

// GetSender lists the account's senders (Brevo has no per-address route),
// finds the one matching email case-insensitively and enriches it with the
// DKIM, SPF, Brevo-code and DMARC records of its domain. A missing sender
// yields ErrSenderNotFound. A failing domain lookup is tolerated: the sender
// is returned without DNS records.
func (c *Client) GetSender(ctx context.Context, email string) (Sender, error) {
	if !c.Configured() {
		return Sender{}, fmt.Errorf("brevo api is not configured")
	}
	var list struct {
		Senders []struct {
			Email  string `json:"email"`
			Active bool   `json:"active"`
		} `json:"senders"`
	}
	if err := c.getJSON(ctx, "/v3/senders", &list); err != nil {
		return Sender{}, fmt.Errorf("brevo sender request: %w", err)
	}
	var out Sender
	found := false
	for _, s := range list.Senders {
		if strings.EqualFold(strings.TrimSpace(s.Email), strings.TrimSpace(email)) {
			out = Sender{Email: s.Email, Active: s.Active}
			found = true
			break
		}
	}
	if !found {
		return Sender{}, ErrSenderNotFound
	}
	at := strings.LastIndex(out.Email, "@")
	if at < 0 {
		return out, nil
	}
	domain := strings.ToLower(strings.TrimSpace(out.Email[at+1:]))
	var d struct {
		Authenticated bool `json:"authenticated"`
		Verified      bool `json:"verified"`
		DNSRecords    struct {
			Dkim      *wireRecord `json:"dkim_record"`
			Dkim1     *wireRecord `json:"dkim1Record"`
			Dkim2     *wireRecord `json:"dkim2Record"`
			BrevoCode *wireRecord `json:"brevo_code"`
			Dmarc     *wireRecord `json:"dmarc_record"`
		} `json:"dns_records"`
	}
	if err := c.getJSON(ctx, "/v3/senders/domains/"+url.PathEscape(domain), &d); err != nil {
		return out, nil
	}
	out.DomainVerified = d.Authenticated || d.Verified
	for _, r := range []struct {
		kind string
		rec  *wireRecord
	}{{"DKIM", d.DNSRecords.Dkim}, {"DKIM", d.DNSRecords.Dkim1}, {"DKIM", d.DNSRecords.Dkim2}, {"BREVO_CODE", d.DNSRecords.BrevoCode}, {"DMARC", d.DNSRecords.Dmarc}} {
		if r.rec != nil && r.rec.Value != "" {
			out.DNSRecords = append(out.DNSRecords, DNSRecord{Type: r.kind, Host: r.rec.HostName, Value: r.rec.Value})
		}
	}
	return out, nil
}
