// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: checkout_consents.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CheckoutConsentRow is one buyer consent given at checkout (migration 0155,
// task P2). ClientIP and UserAgent are personal data: they are answered only
// to the order's organization and the platform operator, never logged.
type CheckoutConsentRow struct {
	ID                    uuid.UUID
	OrgID                 uuid.UUID
	CheckoutSessionID     uuid.UUID
	Kind                  string
	TermsVersion          string
	Locale                *string
	AcceptedAt            time.Time
	ClientIP              *string // host(client_ip): the address without a mask
	UserAgent             *string
	WithdrawalNoticeShown bool
	// What the checkbox showed and where (spec 38, 5.1 item 3).
	OriginHost      *string // scheme and host only
	SellerLegalName *string
	TermsURL        *string
	PrivacyURL      *string
	CreatedAt       time.Time
	// The rendering of the organizer terms accepted (migration 0159, P3a)
	// and the SHA-256 of its HTML; nil for a consent to the platform terms.
	LegalRenderingID *uuid.UUID
	TermsSHA256      *string
}

// InsertCheckoutConsentParams carries one consent row. ClientIP is a textual
// IPv4/IPv6 address or nil; the caller has parsed it already.
type InsertCheckoutConsentParams struct {
	OrgID                 uuid.UUID
	CheckoutSessionID     uuid.UUID
	Kind                  string
	TermsVersion          string
	Locale                *string
	AcceptedAt            time.Time
	ClientIP              *string
	UserAgent             *string
	WithdrawalNoticeShown bool
	OriginHost            *string
	SellerLegalName       *string
	TermsURL              *string
	PrivacyURL            *string
	LegalRenderingID      *uuid.UUID
	TermsSHA256           *string
}

const checkoutConsentColumns = `id, org_id, checkout_session_id, kind, terms_version, locale,
       accepted_at, host(client_ip), user_agent, withdrawal_notice_shown,
       origin_host, seller_legal_name, terms_url, privacy_url, created_at,
       legal_rendering_id, terms_sha256`

func scanCheckoutConsent(row interface{ Scan(...any) error }) (CheckoutConsentRow, error) {
	var r CheckoutConsentRow
	err := row.Scan(&r.ID, &r.OrgID, &r.CheckoutSessionID, &r.Kind, &r.TermsVersion, &r.Locale,
		&r.AcceptedAt, &r.ClientIP, &r.UserAgent, &r.WithdrawalNoticeShown,
		&r.OriginHost, &r.SellerLegalName, &r.TermsURL, &r.PrivacyURL, &r.CreatedAt,
		&r.LegalRenderingID, &r.TermsSHA256)
	return r, err
}

const insertCheckoutConsent = `-- name: InsertCheckoutConsent :one
INSERT INTO checkout_consents (
    org_id, checkout_session_id, kind, terms_version, locale,
    accepted_at, client_ip, user_agent, withdrawal_notice_shown,
    origin_host, seller_legal_name, terms_url, privacy_url,
    legal_rendering_id, terms_sha256
) VALUES ($1, $2, $3, $4, $5, $6, $7::inet, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING ` + checkoutConsentColumns

// InsertCheckoutConsent writes one consent row. It runs on the checkout
// transaction that creates the checkout session (P2), never on the pool.
func (q *Queries) InsertCheckoutConsent(ctx context.Context, p InsertCheckoutConsentParams) (CheckoutConsentRow, error) {
	return scanCheckoutConsent(q.db.QueryRow(ctx, insertCheckoutConsent,
		p.OrgID, p.CheckoutSessionID, p.Kind, p.TermsVersion, p.Locale,
		p.AcceptedAt, p.ClientIP, p.UserAgent, p.WithdrawalNoticeShown,
		p.OriginHost, p.SellerLegalName, p.TermsURL, p.PrivacyURL,
		p.LegalRenderingID, p.TermsSHA256))
}

const listCheckoutConsentsBySession = `-- name: ListCheckoutConsentsBySession :many
SELECT ` + checkoutConsentColumns + `
FROM checkout_consents
WHERE org_id = $1
  AND checkout_session_id = $2
ORDER BY accepted_at, id`

// ListCheckoutConsentsBySession lists the consents of one checkout session of
// the organization, oldest first. A session of another organization lists
// nothing.
func (q *Queries) ListCheckoutConsentsBySession(ctx context.Context, orgID, checkoutSessionID uuid.UUID) ([]CheckoutConsentRow, error) {
	rows, err := q.db.Query(ctx, listCheckoutConsentsBySession, orgID, checkoutSessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CheckoutConsentRow{}
	for rows.Next() {
		r, err := scanCheckoutConsent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
