package analytics

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// TGHasher turns a Telegram user id into the identifier analytics stores for
// a person who is not (yet) a linked platform user (spec 50 section 3):
//
//	hash = hex(HMAC-SHA256(ANALYTICS_TG_HASH_SECRET, "tg:" + id))
//
// The raw Telegram id is never stored. The same function lets the rollup glue
// a "tg" identity to an e-mail or a user when the person later links.
type TGHasher struct{ key []byte }

// NewTGHasher keys the hash with the dedicated ANALYTICS_TG_HASH_SECRET
// (the same value on arena-bot and arena-worker). It is deliberately not the
// JWT signing secret: the worker never receives that one, and a key that also
// signs tokens should not be an analytics hashing key.
func NewTGHasher(secret string) *TGHasher {
	return &TGHasher{key: []byte(secret)}
}

// Hash returns the 64-hex-character identifier of a Telegram id.
func (h *TGHasher) Hash(tgID int64) string {
	m := hmac.New(sha256.New, h.key)
	m.Write([]byte("tg:" + strconv.FormatInt(tgID, 10)))
	return hex.EncodeToString(m.Sum(nil))
}
