// Package salelinks holds the pure parts of the organizer sales links (spec 43):
// the code a link is known by, how a link is turned into the address a scanner
// of a QR code lands on, the utm labels added to that address and the in-memory
// scan de-duplication. It has no HTTP and no state of its own beyond Dedup; the
// table lives in the QL-1 migration and the routes in later tasks.
package salelinks

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/attribution"
)

// MaxMintAttempts is how many codes Mint draws before it gives up. A code space
// of 31^7 makes a second draw rare; eight misses in a row mean a bug.
const MaxMintAttempts = 8

// ErrMintExhausted is returned by Mint when every draw collided.
var ErrMintExhausted = errors.New("salelinks: could not mint a free code")

// NewCode draws one candidate code: CodeLen characters of
// attribution.CodeAlphabet from crypto/rand, without modulo bias.
func NewCode() (string, error) {
	alpha := attribution.CodeAlphabet
	n := len(alpha)
	// Bytes below limit map evenly onto the alphabet; the rest are redrawn.
	limit := 256 - 256%n
	out := make([]byte, 0, attribution.CodeLen)
	buf := make([]byte, 16)
	for len(out) < attribution.CodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("salelinks: draw code: %w", err)
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, alpha[int(b)%n])
			if len(out) == attribution.CodeLen {
				break
			}
		}
	}
	return string(out), nil
}

// NormalizeCode reads the code out of what a request carried: lower-cased,
// cut at the first "?" or "/", at most CodeLen characters taken. It is valid
// only when exactly CodeLen characters of the alphabet remain.
func NormalizeCode(raw string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexAny(s, "?/"); i >= 0 {
		s = s[:i]
	}
	if len(s) > attribution.CodeLen {
		s = s[:attribution.CodeLen]
	}
	if !attribution.IsCode(s) {
		return "", false
	}
	return s, true
}

// Inserter claims a code. Insert reports inserted=false (and no error) when the
// code is already taken, which is never an error: Mint then draws again. The
// implementation is one INSERT ... ON CONFLICT DO NOTHING RETURNING.
type Inserter interface {
	Insert(ctx context.Context, code string) (inserted bool, err error)
}

// Mint draws codes until ins accepts one, at most MaxMintAttempts times (the
// idiom of barcodes/mint). A losing attempt is a clean zero-row answer, never a
// unique violation, so Mint is safe inside a transaction.
func Mint(ctx context.Context, ins Inserter) (string, error) {
	for attempt := 1; attempt <= MaxMintAttempts; attempt++ {
		code, err := NewCode()
		if err != nil {
			return "", err
		}
		ok, err := ins.Insert(ctx, code)
		if err != nil {
			return "", fmt.Errorf("salelinks: claim code (attempt %d/%d): %w", attempt, MaxMintAttempts, err)
		}
		if ok {
			return code, nil
		}
	}
	return "", ErrMintExhausted
}
