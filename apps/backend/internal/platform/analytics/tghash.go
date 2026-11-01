package analytics

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// tgKeyLabel derives the hashing key from the platform's signing secret, so
// the hash needs no new environment variable and a key leak elsewhere does
// not unmask Telegram ids.
const tgKeyLabel = "arena-analytics-tg"

// TGHasher turns a Telegram user id into the identifier analytics stores for
// a person who is not (yet) a linked platform user (spec 50 section 3):
//
//	key  = HMAC-SHA256(JWT_SIGNING_SECRET, "arena-analytics-tg")
//	hash = hex(HMAC-SHA256(key, "tg:" + id))
//
// The raw Telegram id is never stored. The same function lets the rollup glue
// a "tg" identity to an e-mail or a user when the person later links.
type TGHasher struct{ key []byte }

// NewTGHasher derives the key from the signing secret.
func NewTGHasher(signingSecret string) *TGHasher {
	m := hmac.New(sha256.New, []byte(signingSecret))
	m.Write([]byte(tgKeyLabel))
	return &TGHasher{key: m.Sum(nil)}
}

// Hash returns the 64-hex-character identifier of a Telegram id.
func (h *TGHasher) Hash(tgID int64) string {
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte("tg:" + strconv.FormatInt(tgID, 10)))
	return hex.EncodeToString(m.Sum(nil))
}
