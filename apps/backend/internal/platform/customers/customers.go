// Package customers is the platform-side buyer-resolution helper for the
// Bil24 compatibility gateway (spec §12.2, W1-A4b, feature #480).
//
// Scope of this file — the small pieces that do NOT need a database:
//   - NormalizeEmail: lower/trim, IDN kept verbatim (spec §3.2 last paragraph)
//   - NormalizePhone: E.164 via github.com/nyaruka/phonenumbers; the caller
//     passes the default region from the org's first venue country
//     (`IL` for Vino&Co, `CZ` for Lampyris). Invalid input is signalled
//     with ErrInvalidPhone and callers stash the raw value as an attribute
//     rather than an identity.
//   - IdentityKind / IdentitySource constants and small value objects that
//     the Store adapter maps to the gen row structs.
//
// The resolver itself and the interaction with the store live in
// resolve.go. The postgres-backed Store adapter lives in postgres_store.go.
// Every public function is safe to call inside or outside a pgx.Tx via the
// Store interface.
package customers

import (
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nyaruka/phonenumbers"
)

// IdentityKind is one of the six values allowed by the CHECK constraint on
// customer_identities.kind (migration 0091). Strong identities are unique
// platform-wide; weak identities are unique per sales channel.
type IdentityKind string

const (
	// Strong identities.
	KindEmail    IdentityKind = "email"
	KindPhone    IdentityKind = "phone"
	KindTelegram IdentityKind = "telegram"

	// Weak identities.
	KindDevice     IdentityKind = "device"
	KindWCCustomer IdentityKind = "wc_customer"
	KindBil24User  IdentityKind = "bil24_user"
)

// IsStrong reports whether an identity kind is globally unique.
func (k IdentityKind) IsStrong() bool {
	switch k {
	case KindEmail, KindPhone, KindTelegram:
		return true
	}
	return false
}

// IsWeak reports whether an identity kind is per-channel unique.
func (k IdentityKind) IsWeak() bool {
	switch k {
	case KindDevice, KindWCCustomer, KindBil24User:
		return true
	}
	return false
}

// Common source labels for the identities/attributes rows. Callers may pass
// any short label; these constants exist so the resolver and its tests
// agree on the spelling.
const (
	SourceLive          = "live"
	SourceImport        = "import"
	AttrKeyInvalidPhone = "invalid_phone_raw"
)

// Merge-candidate reason strings — spec §12.2.
const (
	MergeReasonEmailOfAPhoneOfB = "email_of_a_phone_of_b"
)

// Sentinel errors — callers use errors.Is.
var (
	// ErrNotFound is what the Store adapter returns when a lookup finds
	// no row. It abstracts pgx.ErrNoRows so the resolver package does
	// not need to import a database driver.
	ErrNotFound = errors.New("customers: not found")

	// ErrInvalidPhone is returned by NormalizePhone when the input does
	// not parse to a valid E.164 number for the supplied region. Callers
	// stash the raw string as an attribute (spec §3.2).
	ErrInvalidPhone = errors.New("customers: invalid phone")

	// ErrInvalidEmail is returned by NormalizeEmail when the input is
	// empty (after trim). The gateway treats missing e-mail as "no
	// strong identity provided" rather than an outright failure, but
	// callers may still want to distinguish the two.
	ErrInvalidEmail = errors.New("customers: invalid email")

	// ErrChannelRequiredForWeak is returned by Resolve when a weak
	// identity is supplied without a channel_id — the partial UNIQUE
	// index on customer_identities requires channel_id NOT NULL for
	// weak rows.
	ErrChannelRequiredForWeak = errors.New("customers: channel_id required for weak identity")
)

// Customer is the resolver-facing subset of the customers row. The
// adapter maps to/from gen.CustomerRow.
type Customer struct {
	ID          uuid.UUID
	SystemID    int64
	DisplayName string
	Locale      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Identity is the resolver-facing subset of customer_identities.
type Identity struct {
	ID              uuid.UUID
	CustomerID      uuid.UUID
	Kind            IdentityKind
	ValueNormalized string
	ChannelID       *uuid.UUID
	VerifiedAt      *time.Time
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	Source          string
}

// NormalizeEmail lowercases and trims raw. Empty input returns
// ErrInvalidEmail. IDN characters are kept verbatim (spec §3.2: "e-mail —
// lower(trim), IDN как есть") — Punycode conversion happens upstream in
// the delivery layer if at all.
func NormalizeEmail(raw string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(raw))
	if n == "" {
		return "", ErrInvalidEmail
	}
	return n, nil
}

// NormalizePhone parses raw into E.164 using the supplied default region
// (ISO 3166-1 alpha-2; e.g. "IL", "CZ"). Numbers that begin with '+' can
// omit the region — libphonenumber infers it from the country prefix.
//
// Returns ErrInvalidPhone when the number does not parse OR parses but
// libphonenumber rejects it as invalid. Callers stash the raw value as a
// customer_attributes row (spec §3.2).
func NormalizePhone(raw, defaultRegion string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", ErrInvalidPhone
	}
	// libphonenumber accepts "" for region only when the number is +E.164.
	// It uppercases the region internally but is stricter than needed —
	// canonicalise here so callers can pass "il" or "Il" safely.
	region := strings.ToUpper(strings.TrimSpace(defaultRegion))
	num, err := phonenumbers.Parse(trimmed, region)
	if err != nil {
		return "", ErrInvalidPhone
	}
	if !phonenumbers.IsValidNumber(num) {
		return "", ErrInvalidPhone
	}
	return phonenumbers.Format(num, phonenumbers.E164), nil
}
