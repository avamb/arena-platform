// Package trackingconsent is the cookie (tracking) consent of a purchase
// (spec 62 §8.1, П-20).
//
// The site's banner asks ONE question — analytics and advertising together —
// and the answer travels with the purchase: in checkout/start as
// tracking_consent {granted, version, at}, and again through
// PATCH /v1/public/checkout/{token}/consent when the buyer changes their mind
// while the order is still open. It lives on checkout_sessions (migration
// 0205), not in the browser alone, because the conversion sender (GA4
// Measurement Protocol, Meta CAPI) runs on the server after the payment: it
// must read the consent FROM THE ORDER AT SEND TIME (ForCheckout + Allows), so
// a refusal given after the payment started but before the send stops the
// conversion. A conversion already sent is not recalled.
//
// Not stated (NULL) reads as "no". categories is accepted and stored for a
// future banner with a "Customize" button; today nothing reads it.
package trackingconsent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxVersionLength mirrors the column CHECK.
const MaxVersionLength = 64

// maxFutureSkew is how far ahead of the server clock a browser-stated time
// may be before it is replaced by the server's.
const maxFutureSkew = 5 * time.Minute

// Input is the wire shape the site sends.
type Input struct {
	Granted    *bool           `json:"granted"`
	Version    string          `json:"version"`
	At         *string         `json:"at,omitempty"`
	Categories json.RawMessage `json:"categories,omitempty"`
}

// Consent is a normalized, storable consent.
type Consent struct {
	Granted    bool
	Version    string
	At         time.Time
	Categories json.RawMessage
}

// Stored is what a checkout carries. Known is false when the buyer never
// stated a choice.
type Stored struct {
	Known      bool
	Granted    bool
	Version    string
	At         *time.Time
	ReceivedAt *time.Time
}

// ErrInvalid reports a consent the site sent in a shape arena cannot store.
var ErrInvalid = errors.New("trackingconsent: invalid consent")

// Normalize checks in and fills the time: the browser's own time when it is
// readable and not in the future, else now.
func Normalize(in Input, now time.Time) (Consent, error) {
	if in.Granted == nil {
		return Consent{}, fmt.Errorf("%w: granted is required", ErrInvalid)
	}
	v := strings.TrimSpace(in.Version)
	if v == "" || len(v) > MaxVersionLength {
		return Consent{}, fmt.Errorf("%w: version must be 1 to %d characters", ErrInvalid, MaxVersionLength)
	}
	at := now.UTC()
	if in.At != nil && strings.TrimSpace(*in.At) != "" {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(*in.At))
		if err != nil {
			return Consent{}, fmt.Errorf("%w: at must be an RFC 3339 time", ErrInvalid)
		}
		if !t.After(now.Add(maxFutureSkew)) {
			at = t.UTC()
		}
	}
	var cats json.RawMessage
	if len(in.Categories) > 0 && string(in.Categories) != "null" {
		var probe map[string]any
		if err := json.Unmarshal(in.Categories, &probe); err != nil {
			return Consent{}, fmt.Errorf("%w: categories must be an object", ErrInvalid)
		}
		cats = in.Categories
	}
	return Consent{Granted: *in.Granted, Version: v, At: at, Categories: cats}, nil
}

// Execer is a pgx connection, pool or transaction.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Querier reads.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store writes c onto the checkout. A choice older than the stored one (two
// tabs, a request overtaken by a later one) does not overwrite it; it
// answers whether c was written.
func Store(ctx context.Context, db Execer, checkoutSessionID uuid.UUID, c Consent) (bool, error) {
	var cats any
	if len(c.Categories) > 0 {
		cats = []byte(c.Categories)
	}
	tag, err := db.Exec(ctx, `
		UPDATE checkout_sessions
		   SET tracking_consent_granted     = $2,
		       tracking_consent_version     = $3,
		       tracking_consent_at          = $4,
		       tracking_consent_categories  = $5::jsonb,
		       tracking_consent_received_at = now()
		 WHERE id = $1
		   AND (tracking_consent_at IS NULL OR tracking_consent_at <= $4)`,
		checkoutSessionID, c.Granted, c.Version, c.At, cats)
	if err != nil {
		return false, fmt.Errorf("trackingconsent: store: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ForCheckout reads the consent a checkout carries now.
func ForCheckout(ctx context.Context, db Querier, checkoutSessionID uuid.UUID) (Stored, error) {
	var (
		granted    *bool
		version    *string
		s          Stored
		at, recvAt *time.Time
	)
	err := db.QueryRow(ctx, `
		SELECT tracking_consent_granted, tracking_consent_version,
		       tracking_consent_at, tracking_consent_received_at
		  FROM checkout_sessions WHERE id = $1`, checkoutSessionID).Scan(&granted, &version, &at, &recvAt)
	if err != nil {
		return Stored{}, err
	}
	if granted != nil {
		s.Known, s.Granted = true, *granted
	}
	if version != nil {
		s.Version = *version
	}
	s.At, s.ReceivedAt = at, recvAt
	return s, nil
}

// ForOrder reads the consent of the checkout an order was created from —
// what a conversion sender calls right before it sends.
func ForOrder(ctx context.Context, db Querier, orderID uuid.UUID) (Stored, error) {
	var csID *uuid.UUID
	if err := db.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, orderID).Scan(&csID); err != nil {
		return Stored{}, err
	}
	if csID == nil {
		// An order without a checkout (a gateway sale) never stated one.
		return Stored{}, nil
	}
	return ForCheckout(ctx, db, *csID)
}

// Allows is the one rule a conversion sender applies: only an explicit yes.
func (s Stored) Allows() bool { return s.Known && s.Granted }
