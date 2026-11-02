package analytics

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Contact codes (spec 50 section 3): the `cid` carried by a link in an e-mail
// to a CRM contact. The alphabet is the one of the sales links (spec 43) -
// lower-case letters and digits without 0 o 1 l i - and a code is ten
// characters long.
const (
	ContactCodeAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	ContactCodeLen      = 10

	// MaxContactCodeBatch is the most CRM contacts one request may ask for.
	MaxContactCodeBatch = 500

	// maxContactCodeAttempts bounds the redraws after a collision. With 31^10
	// possible codes a second attempt is already astronomically rare.
	maxContactCodeAttempts = 8

	// contactCodesLockKey serializes issuing, so two requests for the same
	// contact cannot each create a code (pg_advisory_xact_lock; next to
	// rollupLockKey).
	contactCodesLockKey = 5_050_002
)

// TwentyPersonIDPattern is the accepted shape of a CRM person id. Twenty uses
// UUIDs; the pattern is looser so a different CRM id still works, but it
// excludes anything that could be personal data in a log line (no spaces, no
// at-sign).
var TwentyPersonIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// ValidTwentyPersonID reports whether s is an acceptable CRM person id.
func ValidTwentyPersonID(s string) bool { return TwentyPersonIDPattern.MatchString(s) }

// NewContactCode draws one code from crypto/rand. Bytes outside the largest
// multiple of the alphabet length are rejected, so every character is
// equally likely.
func NewContactCode() (string, error) {
	const limit = 256 - 256%len(ContactCodeAlphabet)
	out := make([]byte, 0, ContactCodeLen)
	buf := make([]byte, ContactCodeLen*2)
	for len(out) < ContactCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("analytics: contact code: %w", err)
		}
		for _, b := range buf {
			if int(b) >= limit {
				continue
			}
			out = append(out, ContactCodeAlphabet[int(b)%len(ContactCodeAlphabet)])
			if len(out) == ContactCodeLen {
				break
			}
		}
	}
	return string(out), nil
}

// IsContactCode reports whether s has exactly the shape of a contact code.
func IsContactCode(s string) bool {
	if len(s) != ContactCodeLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(ContactCodeAlphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

// EnsureContactCodes returns one code for each CRM person id, in the order of
// ids. A contact that already has a code gets the SAME one back (the oldest,
// should several exist), so every mailing to a contact carries one stable
// cid. New codes are written with INSERT ... ON CONFLICT DO NOTHING and
// redrawn on a collision (the idiom of the sales links). ids must be valid and
// free of duplicates: the caller validates, this function does not repair.
//
// It runs on the caller's transaction so the audit row commits with the codes.
func EnsureContactCodes(ctx context.Context, tx pgx.Tx, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(contactCodesLockKey)); err != nil {
		return nil, fmt.Errorf("analytics: lock contact codes: %w", err)
	}
	have := make(map[string]string, len(ids))
	rows, err := tx.Query(ctx, `
SELECT DISTINCT ON (twenty_person_id) twenty_person_id, code
  FROM analytics.contact_codes
 WHERE twenty_person_id = ANY($1::text[])
 ORDER BY twenty_person_id, created_at, code`, ids)
	if err != nil {
		return nil, fmt.Errorf("analytics: read contact codes: %w", err)
	}
	for rows.Next() {
		var id, code string
		if err := rows.Scan(&id, &code); err != nil {
			rows.Close()
			return nil, err
		}
		have[id] = code
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]string, len(ids))
	for i, id := range ids {
		if code, ok := have[id]; ok {
			out[i] = code
			continue
		}
		code, err := insertContactCode(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		have[id] = code
		out[i] = code
	}
	return out, nil
}

func insertContactCode(ctx context.Context, tx pgx.Tx, twentyID string) (string, error) {
	for attempt := 0; attempt < maxContactCodeAttempts; attempt++ {
		code, err := NewContactCode()
		if err != nil {
			return "", err
		}
		tag, err := tx.Exec(ctx, `
INSERT INTO analytics.contact_codes (code, twenty_person_id) VALUES ($1, $2)
ON CONFLICT (code) DO NOTHING`, code, twentyID)
		if err != nil {
			return "", fmt.Errorf("analytics: insert contact code: %w", err)
		}
		if tag.RowsAffected() == 1 {
			return code, nil
		}
	}
	return "", fmt.Errorf("analytics: no free contact code after %d draws", maxContactCodeAttempts)
}
