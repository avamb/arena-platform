// webhook_payload_scrub.go — the raw provider webhook body is kept in
// payment_intent_events.event_payload for audit, but never with a client
// secret in it (spec 65 §3.7: the client secret of an embedded checkout
// session is handed to the buyer's browser and never stored or logged).
package hcheckout

import "encoding/json"

// clientSecretKey is the JSON key Stripe uses for the browser credential of
// a Checkout Session or a PaymentIntent.
const clientSecretKey = "client_secret"

// stripClientSecrets returns raw with every "client_secret" member removed
// at any depth. A body that is not valid JSON is returned unchanged (the
// webhook parser has already refused such a body before anything is
// stored); a body without a client secret is returned byte for byte.
func stripClientSecrets(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	if !removeClientSecrets(v) {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

// removeClientSecrets deletes client_secret members in place and reports
// whether it removed any.
func removeClientSecrets(v any) bool {
	removed := false
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t[clientSecretKey]; ok {
			delete(t, clientSecretKey)
			removed = true
		}
		for _, child := range t {
			if removeClientSecrets(child) {
				removed = true
			}
		}
	case []any:
		for _, child := range t {
			if removeClientSecrets(child) {
				removed = true
			}
		}
	}
	return removed
}
