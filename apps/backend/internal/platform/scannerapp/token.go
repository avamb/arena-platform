// Package scannerapp is the store of the door-control app on the arena API
// (spec 69, task MA-10): controller passes, scanner devices and their tokens.
//
// A device belongs to ONE organization, taken from the device row and never
// from the request. It speaks either for a person (an organizer device) or
// for a controller pass (a pass device). Every function takes the time as
// now and never reads the clock itself.
package scannerapp

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"
)

// TokenWirePrefix starts every device access token.
const TokenWirePrefix = "sdt_"

// AccessTokenTTL is how long a device access token works after it is issued.
const AccessTokenTTL = time.Hour

// tokenBytes is the entropy of a device token.
const tokenBytes = 32

// tokenBodyLen is the length of base64url (no padding) of tokenBytes.
var tokenBodyLen = base64.RawURLEncoding.EncodedLen(tokenBytes)

// NewToken returns a fresh device token and the SHA-256 hex of it. Only the
// hash is stored; the raw token is shown to the device once.
func NewToken() (raw, hashHex string, err error) {
	var b [tokenBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	raw = TokenWirePrefix + base64.RawURLEncoding.EncodeToString(b[:])
	return raw, HashToken(raw), nil
}

// HashToken is the SHA-256 hex of a raw device token, as stored in
// scanner_devices.access_token_hash.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// RefreshTokenWirePrefix starts every device refresh token. It differs from
// TokenWirePrefix, so a refresh token is never accepted as a Bearer token.
const RefreshTokenWirePrefix = "sdr_"

// RefreshTokenTTL is how long a refresh token works after it is issued; every
// refresh issues a new one, so the term slides.
const RefreshTokenTTL = 30 * 24 * time.Hour

// NewRefreshToken returns a fresh refresh token and the SHA-256 hex of it.
func NewRefreshToken() (raw, hashHex string, err error) {
	var b [tokenBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", "", err
	}
	raw = RefreshTokenWirePrefix + base64.RawURLEncoding.EncodeToString(b[:])
	return raw, HashToken(raw), nil
}

// LooksLikeRefreshToken reports whether raw has the shape of a refresh
// token: the prefix and 43 base64url characters. It reads no database.
func LooksLikeRefreshToken(raw string) bool {
	return hasTokenShape(raw, RefreshTokenWirePrefix)
}

// LooksLikeDeviceToken reports whether raw has the shape of a device token:
// the prefix and 43 base64url characters. It reads no database.
func LooksLikeDeviceToken(raw string) bool {
	return hasTokenShape(raw, TokenWirePrefix)
}

func hasTokenShape(raw, prefix string) bool {
	if !strings.HasPrefix(raw, prefix) {
		return false
	}
	body := raw[len(prefix):]
	if len(body) != tokenBodyLen {
		return false
	}
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
