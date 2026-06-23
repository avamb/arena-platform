// webhook.go provides HMAC-SHA256 signature verification helpers for inbound
// payment provider webhooks.
//
// Two providers are currently supported:
//
//   - Stripe  — uses the "Stripe-Signature" header with format t=<ts>,v1=<sig>
//   - AllPay  — uses the "X-AllPay-Signature" header containing a hex HMAC-SHA256
//
// Both helpers return ErrInvalidWebhookSignature (possibly wrapped) on failure.
package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultWebhookTolerance is the maximum age of a Stripe webhook timestamp before
// it is rejected as a potential replay attack. Stripe recommends 300 seconds (5 min).
const DefaultWebhookTolerance = 5 * time.Minute

// ─────────────────────────────────────────────────────────────────────────────
// Stripe webhook signature verification
// ─────────────────────────────────────────────────────────────────────────────

// VerifyStripeSignature verifies the HMAC-SHA256 signature of a Stripe webhook.
//
// Parameters:
//   - signatureHeader: raw value of the "Stripe-Signature" HTTP header,
//     format: "t=<unix_timestamp>,v1=<hex_sig>[,v1=<additional_sig>]"
//   - body: raw (unmodified) request body bytes
//   - secret: Stripe webhook endpoint secret (whsec_…)
//   - tolerance: maximum age of the timestamp; pass 0 to skip the check
//
// Returns ErrInvalidWebhookSignature (possibly wrapped with extra context) when:
//   - the header is malformed
//   - the timestamp is too old (> tolerance)
//   - no v1 signature matches the computed HMAC
func VerifyStripeSignature(signatureHeader string, body []byte, secret string, tolerance time.Duration) error {
	if signatureHeader == "" {
		return fmt.Errorf("%w: missing Stripe-Signature header", ErrInvalidWebhookSignature)
	}

	// Parse "t=<ts>,v1=<sig1>[,v1=<sig2>,...]"
	var (
		timestamp  int64
		signatures []string
	)
	for _, part := range strings.Split(signatureHeader, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				return fmt.Errorf("%w: invalid timestamp in Stripe-Signature header", ErrInvalidWebhookSignature)
			}
			timestamp = ts
		case "v1":
			if kv[1] != "" {
				signatures = append(signatures, kv[1])
			}
		}
	}

	if timestamp == 0 {
		return fmt.Errorf("%w: missing or zero timestamp in Stripe-Signature header", ErrInvalidWebhookSignature)
	}
	if len(signatures) == 0 {
		return fmt.Errorf("%w: no v1 signature found in Stripe-Signature header", ErrInvalidWebhookSignature)
	}

	// Replay-attack protection: reject if the event is too old.
	if tolerance > 0 {
		age := time.Since(time.Unix(timestamp, 0))
		if age > tolerance {
			return fmt.Errorf("%w: Stripe webhook timestamp is %s old (tolerance %s); possible replay attack",
				ErrInvalidWebhookSignature, age.Round(time.Second), tolerance)
		}
	}

	// Signed payload is "<timestamp>.<body>" per Stripe specification.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	// Accept if any of the provided v1 signatures matches.
	for _, sig := range signatures {
		if hmac.Equal([]byte(sig), []byte(expected)) {
			return nil
		}
	}

	return fmt.Errorf("%w: computed HMAC does not match any provided v1 signature", ErrInvalidWebhookSignature)
}

// ─────────────────────────────────────────────────────────────────────────────
// AllPay webhook signature verification
// ─────────────────────────────────────────────────────────────────────────────

// VerifyAllPaySignature verifies the HMAC-SHA256 signature of an AllPay webhook.
//
// AllPay sends the signature as a lowercase hex-encoded HMAC-SHA256 of the raw
// body using the shared secret, in the "X-AllPay-Signature" header.
//
// Parameters:
//   - signatureHeader: raw value of the "X-AllPay-Signature" header (hex HMAC)
//   - body: raw (unmodified) request body bytes
//   - secret: AllPay webhook shared secret
//
// Returns ErrInvalidWebhookSignature when the header is absent or the HMAC
// does not match.
func VerifyAllPaySignature(signatureHeader string, body []byte, secret string) error {
	if strings.TrimSpace(signatureHeader) == "" {
		return fmt.Errorf("%w: missing X-AllPay-Signature header", ErrInvalidWebhookSignature)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))

	// AllPay transmits lowercase hex; normalise the incoming header just in case.
	if !hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(signatureHeader))), []byte(expected)) {
		return fmt.Errorf("%w: AllPay HMAC-SHA256 mismatch", ErrInvalidWebhookSignature)
	}

	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Generic helper
// ─────────────────────────────────────────────────────────────────────────────

// ComputeHMACSHA256 returns the lowercase hex-encoded HMAC-SHA256 of payload
// using the given secret. It is exposed so that tests and tooling can generate
// valid webhook signatures without reimplementing the primitive.
func ComputeHMACSHA256(secret string, payload []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
