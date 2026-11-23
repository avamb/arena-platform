// Package secretbox seals organization secrets at rest with AES-256-GCM
// (spec 08_architecture/66 section 9 item 1).
//
// A Box holds one or more 32-byte keys, each with an id of the form "v" +
// digits. The first key of the spec is the current one: Seal always uses it.
// Every other key only opens values sealed before a rotation. A sealed value
// is "<keyid>:<base64url(nonce||ciphertext)>" with a fresh random 12-byte
// nonce, so Open picks the key by its id.
//
// Errors never carry key material or plaintext.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// KeySize is the length of every key in bytes (AES-256).
const KeySize = 32

const nonceSize = 12

var (
	// ErrDisabled is returned by a nil or empty Box: no key is configured.
	ErrDisabled = errors.New("secretbox: no encryption key is configured")
	// ErrUnknownKey means the sealed value names a key id the Box does not hold.
	ErrUnknownKey = errors.New("secretbox: sealed value uses an unknown key id")
	// ErrMalformed means the sealed value is not "<keyid>:<base64url>" of a
	// plausible length.
	ErrMalformed = errors.New("secretbox: sealed value is malformed")
	// ErrOpen means authentication failed: a wrong key, a wrong AAD or a
	// tampered value.
	ErrOpen = errors.New("secretbox: sealed value cannot be opened")
)

var keyIDPattern = regexp.MustCompile(`^v[0-9]+$`)

// Box seals and opens values with a set of versioned keys.
type Box struct {
	current string
	aeads   map[string]cipher.AEAD
}

// ParseKeys builds a Box from a spec such as "v2:<base64>,v1:<base64>". The
// first entry is the current key. Each key must decode (standard or URL
// base64, padded or not) to exactly 32 bytes; ids must be unique.
func ParseKeys(spec string) (*Box, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, errors.New("secretbox: key spec is empty")
	}
	b := &Box{aeads: map[string]cipher.AEAD{}}
	for i, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		id, enc, ok := strings.Cut(part, ":")
		if !ok {
			return nil, fmt.Errorf("secretbox: key entry %d is not <id>:<base64>", i+1)
		}
		id = strings.TrimSpace(id)
		if !keyIDPattern.MatchString(id) {
			return nil, fmt.Errorf("secretbox: key entry %d has an invalid id (want v followed by digits)", i+1)
		}
		if _, dup := b.aeads[id]; dup {
			return nil, fmt.Errorf("secretbox: key id %s is listed twice", id)
		}
		key, err := decodeKey(strings.TrimSpace(enc))
		if err != nil {
			return nil, fmt.Errorf("secretbox: key %s is not valid base64", id)
		}
		if len(key) != KeySize {
			return nil, fmt.Errorf("secretbox: key %s must be %d bytes, got %d", id, KeySize, len(key))
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fmt.Errorf("secretbox: key %s cannot build a cipher", id)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("secretbox: key %s cannot build GCM", id)
		}
		b.aeads[id] = aead
		if i == 0 {
			b.current = id
		}
	}
	return b, nil
}

func decodeKey(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if k, err := enc.DecodeString(s); err == nil {
			return k, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func (b *Box) enabled() bool { return b != nil && b.current != "" && len(b.aeads) > 0 }

// CurrentKeyID is the id of the key Seal uses, "" for a disabled Box.
func (b *Box) CurrentKeyID() string {
	if !b.enabled() {
		return ""
	}
	return b.current
}

// Seal encrypts plaintext with the current key, binding aad to it.
func (b *Box) Seal(plaintext, aad []byte) (string, error) {
	if !b.enabled() {
		return "", ErrDisabled
	}
	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", errors.New("secretbox: cannot read a random nonce")
	}
	out := b.aeads[b.current].Seal(nonce, nonce, plaintext, aad)
	return b.current + ":" + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts a value made by Seal with the key its id names.
func (b *Box) Open(sealed string, aad []byte) ([]byte, error) {
	if !b.enabled() {
		return nil, ErrDisabled
	}
	id, enc, ok := strings.Cut(sealed, ":")
	if !ok || !keyIDPattern.MatchString(id) {
		return nil, ErrMalformed
	}
	aead, ok := b.aeads[id]
	if !ok {
		return nil, ErrUnknownKey
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(raw) < nonceSize+aead.Overhead() {
		return nil, ErrMalformed
	}
	pt, err := aead.Open(nil, raw[:nonceSize], raw[nonceSize:], aad)
	if err != nil {
		return nil, ErrOpen
	}
	return pt, nil
}
