// Package brevo contains the small, credential-free boundary used to inspect
// Brevo sender identities. SMTP credentials remain platform configuration.
package brevo

import (
	"context"
	"encoding/json"
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
}

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

// GetSender fetches the Brevo-managed sender record and normalises the DNS
// fields used by its current sender API response.
func (c *Client) GetSender(ctx context.Context, email string) (Sender, error) {
	if !c.Configured() {
		return Sender{}, fmt.Errorf("brevo api is not configured")
	}
	u := c.baseURL + "/v3/senders/" + url.PathEscape(email)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Sender{}, err
	}
	req.Header.Set("api-key", c.apiKey)
	req.Header.Set("accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Sender{}, fmt.Errorf("brevo sender request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Sender{}, fmt.Errorf("brevo sender request returned %s", resp.Status)
	}
	var wire struct {
		Email      string `json:"email"`
		Active     bool   `json:"active"`
		DkimRecord *struct {
			HostName string `json:"hostName"`
			Value    string `json:"value"`
		} `json:"dkimRecord"`
		SpfRecord *struct {
			HostName string `json:"hostName"`
			Value    string `json:"value"`
		} `json:"spfRecord"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wire); err != nil {
		return Sender{}, fmt.Errorf("decode brevo sender: %w", err)
	}
	out := Sender{Email: wire.Email, Active: wire.Active}
	if wire.DkimRecord != nil {
		out.DNSRecords = append(out.DNSRecords, DNSRecord{Type: "DKIM", Host: wire.DkimRecord.HostName, Value: wire.DkimRecord.Value})
	}
	if wire.SpfRecord != nil {
		out.DNSRecords = append(out.DNSRecords, DNSRecord{Type: "SPF", Host: wire.SpfRecord.HostName, Value: wire.SpfRecord.Value})
	}
	return out, nil
}
