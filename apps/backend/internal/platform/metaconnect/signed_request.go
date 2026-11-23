package metaconnect

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrSignedRequestInvalid is the only error of ParseSignedRequest.
var ErrSignedRequestInvalid = errors.New("metaconnect: signed_request is invalid")

// SignedRequest is the part of Meta's signed_request the callbacks use.
type SignedRequest struct {
	// UserID is the app-scoped user id.
	UserID   string
	IssuedAt int64
}

// Accepted age of a signed_request: issued at most SignedRequestMaxAge ago
// and at most SignedRequestMaxSkew in the future.
const (
	SignedRequestMaxAge  = 24 * time.Hour
	SignedRequestMaxSkew = 5 * time.Minute
)

// ParseSignedRequest verifies Meta's signed_request ("<sig>.<payload>", both
// base64url without padding; sig = HMAC-SHA256(app secret, payload as sent))
// and decodes it. The algorithm must be HMAC-SHA256, the user id present and
// issued_at within SignedRequestMaxAge before now and SignedRequestMaxSkew
// after it. The signature is compared in constant time.
func ParseSignedRequest(raw, appSecret string, now time.Time) (SignedRequest, error) {
	raw = strings.TrimSpace(raw)
	if appSecret == "" || raw == "" || len(raw) > 8192 {
		return SignedRequest{}, ErrSignedRequestInvalid
	}
	sigPart, payloadPart, ok := strings.Cut(raw, ".")
	if !ok || sigPart == "" || payloadPart == "" {
		return SignedRequest{}, ErrSignedRequestInvalid
	}
	sig, err := decodeB64URL(sigPart)
	if err != nil {
		return SignedRequest{}, ErrSignedRequestInvalid
	}
	m := hmac.New(sha256.New, []byte(appSecret))
	m.Write([]byte(payloadPart))
	if !hmac.Equal(sig, m.Sum(nil)) {
		return SignedRequest{}, ErrSignedRequestInvalid
	}
	payload, err := decodeB64URL(payloadPart)
	if err != nil {
		return SignedRequest{}, ErrSignedRequestInvalid
	}
	var body struct {
		Algorithm string          `json:"algorithm"`
		UserID    json.RawMessage `json:"user_id"`
		IssuedAt  int64           `json:"issued_at"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return SignedRequest{}, ErrSignedRequestInvalid
	}
	if !strings.EqualFold(body.Algorithm, "HMAC-SHA256") {
		return SignedRequest{}, ErrSignedRequestInvalid
	}
	uid := jsonID(body.UserID)
	if uid == "" {
		return SignedRequest{}, ErrSignedRequestInvalid
	}
	issued := time.Unix(body.IssuedAt, 0)
	if body.IssuedAt <= 0 || issued.Before(now.Add(-SignedRequestMaxAge)) || issued.After(now.Add(SignedRequestMaxSkew)) {
		return SignedRequest{}, ErrSignedRequestInvalid
	}
	return SignedRequest{UserID: uid, IssuedAt: body.IssuedAt}, nil
}

// decodeB64URL accepts base64url with or without padding.
func decodeB64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// jsonID reads an id Meta may send as a string or as a number.
func jsonID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}
