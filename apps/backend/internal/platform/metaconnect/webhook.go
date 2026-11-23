package metaconnect

// webhook.go - Meta's app webhook (Webhooks -> Application): the subscription
// check of GET and the X-Hub-Signature-256 of POST, and the field
// business_integration_uninstall (a business removed the app, spec 66
// section 4.1a). Nothing here reads or returns a token.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// WebhookFieldUninstall is the webhook field Meta sends when a business
// removes the app's integration.
const WebhookFieldUninstall = "business_integration_uninstall"

// WebhookBodyLimit bounds the raw body of a webhook POST.
const WebhookBodyLimit = 64 << 10

// VerifyWebhookSignature reports whether header is "sha256=<hex>" of the
// HMAC-SHA256 of the RAW body with the app secret. An empty secret or header
// never verifies.
func VerifyWebhookSignature(body []byte, header, appSecret string) bool {
	if appSecret == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimSpace(header[len(prefix):]))
	if err != nil || len(got) != sha256.Size {
		return false
	}
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// VerifyWebhookSubscription checks the GET subscription request: mode must
// be "subscribe" and token must equal the configured verify token (constant
// time). An empty configured token refuses every request.
func VerifyWebhookSubscription(mode, token, want string) bool {
	if want == "" || mode != "subscribe" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

// webhookPayload is the envelope of an app webhook POST.
type webhookPayload struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      json.RawMessage `json:"id"`
		Changes []struct {
			Field string          `json:"field"`
			Value json.RawMessage `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

// UninstalledBusinesses parses a webhook body and lists the business ids of
// its business_integration_uninstall changes (value.business_manager_id, or
// value.business_id), without duplicates, in order. Other fields are ignored.
// An id may come as a JSON string or number.
func UninstalledBusinesses(body []byte) ([]string, error) {
	var p webhookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range p.Entry {
		for _, c := range e.Changes {
			if c.Field != WebhookFieldUninstall {
				continue
			}
			var v map[string]json.RawMessage
			if err := json.Unmarshal(c.Value, &v); err != nil {
				continue
			}
			id := idText(v["business_manager_id"])
			if id == "" {
				id = idText(v["business_id"])
			}
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// idText reads a JSON string or number as text ("" for anything else).
func idText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&n); err == nil {
		if _, err := n.Int64(); err == nil {
			return n.String()
		}
	}
	return ""
}

// OrgsByBusiness lists the organizations whose connection is to the Meta
// business id.
func OrgsByBusiness(ctx context.Context, q Querier, businessID string) ([]uuid.UUID, error) {
	if businessID == "" {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT org_id FROM org_meta_connections WHERE meta_business_id = $1 ORDER BY org_id FOR UPDATE`, businessID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
