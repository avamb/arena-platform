// Package users provides the user model and credential-management helpers used
// by the identity endpoints introduced in Wave 1 (feature #114).
//
// Scope for this milestone:
//   - Email normalisation (lowercase + trim)
//   - Password hashing via bcrypt at cost ≥ 12 (compliance spec §Identity)
//   - Verification-token generation (crypto/rand, 32 bytes → 64-char hex)
//
// The higher-level registration and verification flows live in the httpserver
// package (handlers) and the adapters/postgres/gen package (DB queries).
package users

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// BcryptCost is the work factor used when hashing passwords.
// The compliance spec requires cost ≥ 12.
const BcryptCost = 12

// VerificationTokenBytes is the number of random bytes used to generate an
// email verification token. 32 bytes → 64-char hex string, providing 256 bits
// of entropy — well above the minimum for a one-time URL token.
const VerificationTokenBytes = 32

// Sentinel errors returned by functions in this package. Callers should use
// errors.Is / errors.As rather than string matching.
var (
	// ErrEmailRequired is returned when an empty email is supplied.
	ErrEmailRequired = errors.New("users: email is required")

	// ErrPasswordTooShort is returned when the plaintext password is shorter
	// than MinPasswordLength bytes.
	ErrPasswordTooShort = errors.New("users: password is too short")

	// ErrPasswordTooLong is returned when the plaintext password exceeds
	// bcrypt's 72-byte effective limit. Accepting longer passwords gives users
	// false confidence that all characters contribute to security.
	ErrPasswordTooLong = errors.New("users: password must not exceed 72 characters")
)

// MinPasswordLength is the minimum number of bytes a plaintext password must
// have. Per the compliance spec §Identity: minimum 8 characters.
const MinPasswordLength = 8

// NormalizeEmail trims leading/trailing whitespace and converts to lowercase
// so that "User@Example.COM " and "user@example.com" map to the same account.
// Returns ErrEmailRequired when the normalised result is empty.
func NormalizeEmail(raw string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(raw))
	if n == "" {
		return "", ErrEmailRequired
	}
	return n, nil
}

// HashPassword hashes plaintext using bcrypt at BcryptCost and returns the
// Modular Crypt Format string (e.g. "$2a$12$...").
//
// Returns ErrPasswordTooShort / ErrPasswordTooLong for out-of-range lengths,
// or a wrapped error on bcrypt failure.
func HashPassword(plaintext string) (string, error) {
	if len(plaintext) < MinPasswordLength {
		return "", ErrPasswordTooShort
	}
	// bcrypt only uses the first 72 bytes of the plaintext; passwords longer
	// than that silently truncate. We reject them explicitly so callers are
	// not misled into thinking the full string was hashed.
	if len(plaintext) > 72 {
		return "", ErrPasswordTooLong
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plaintext), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("users: bcrypt hash failed: %w", err)
	}
	return string(hash), nil
}

// CheckPassword compares a bcrypt hash stored in the database against the
// supplied plaintext. Returns nil when they match, bcrypt.ErrMismatchedHashAndPassword
// when they do not, or another error on unexpected failure.
func CheckPassword(hash, plaintext string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
}

// GenerateVerificationToken returns a cryptographically random hex string of
// length 2*VerificationTokenBytes (64 characters). The token is suitable for
// use in single-use email verification URLs.
func GenerateVerificationToken() (string, error) {
	b := make([]byte, VerificationTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("users: failed to generate verification token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
