package analytics

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Identity kinds of analytics.identities. They extend the event id kinds
// (aid, cid, user, tg) with the hashed e-mail used to join an application
// with a visitor (spec 50 section 3).
const (
	IdentityEmailSHA256 = "email_sha256"
)

// AIDPattern is the shape of a visitor id: 8-40 characters of A-Z a-z 0-9 _ -
// (the same rule the receivers use).
var AIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,40}$`)

// ValidAID reports whether s is a well-formed visitor id.
func ValidAID(s string) bool { return AIDPattern.MatchString(s) }

// CleanAID returns s when it is a well-formed visitor id and "" otherwise.
// Callers store the result: an invalid value is ignored, never an error.
func CleanAID(s string) string {
	s = strings.TrimSpace(s)
	if AIDPattern.MatchString(s) {
		return s
	}
	return ""
}

// EmailSHA256 is the hex SHA-256 of the lower-cased, trimmed e-mail: the
// only form in which an address is kept in the analytics schema.
func EmailSHA256(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

// Execer is the part of a pool or a transaction the erasure helpers need.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ForgetEmail deletes the e-mail identity of an address. onboarding.sweep
// and the operator's purge call it when they erase an application's personal
// data, on the same transaction (spec 50 section 11); q must be a transaction
// (it takes a transaction-level advisory lock, the rollup's). Nothing else of the
// person is touched: the visitor id and the history stay, only the link to
// the address is cut.
func ForgetEmail(ctx context.Context, q Execer, email string) error {
	if strings.TrimSpace(email) == "" {
		return nil
	}
	// Wait for a running rollup (and keep it out) until the caller's
	// transaction ends: a join in flight must not put the hash back.
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, RollupLockKey); err != nil {
		return err
	}
	_, err := q.Exec(ctx,
		`DELETE FROM analytics.identities WHERE kind = $1 AND value = $2`,
		IdentityEmailSHA256, EmailSHA256(email))
	return err
}
