package hcatalog

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// signingSecretBytes is the entropy of a generated webhook signing secret
// (hex-encoded, so the secret itself is twice as long).
const signingSecretBytes = 32

// resolveSigningSecret returns the operator-supplied secret, or a freshly
// generated one when the field was left blank.
//
// Both registration forms in the admin (channel → WordPress webhook,
// organization → MACS scanner webhook) say "generated if left blank", but
// until 2026-09-18 the handlers stored the blank value as-is: every
// subscriber on production had an empty secret, the dispatchers therefore
// never sent X-Arena-Signature, and the "copy the secret now" box in the
// admin copied an empty string.
func resolveSigningSecret(supplied string) (string, error) {
	if s := strings.TrimSpace(supplied); s != "" {
		return s, nil
	}
	buf := make([]byte, signingSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate signing secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
