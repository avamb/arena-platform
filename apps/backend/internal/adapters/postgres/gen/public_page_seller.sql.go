// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: public_page.sql

package gen

import (
	"context"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// GetHostedPageSeller
// ─────────────────────────────────────────────────────────────────────────────

// HostedPageSellerRow is the legal identity of the organization behind a
// hosted page plus the channel settings that decide whether and how it is
// published. Deliberately has NO organizations.contact_email / contact_phone:
// a published contact comes from the channel's public_contact only.
type HostedPageSellerRow struct {
	OrgName            string
	LegalName          *string
	TaxID              *string
	TaxIDScheme        *string
	RegistrationNumber *string
	AddressLine1       *string
	AddressLine2       *string
	AddressPostalCode  *string
	AddressCity        *string
	AddressCountry     *string
	OrgCountry         string
	ShowSeller         bool
	// PublicContact is settings.hosted_page.public_contact as raw JSON, nil
	// when the channel has none.
	PublicContact []byte
}

const getHostedPageSeller = `-- name: GetHostedPageSeller :one
SELECT
    o.name, o.legal_name, o.tax_id, o.tax_id_scheme, o.registration_number,
    o.legal_address_line1, o.legal_address_line2, o.legal_address_postal_code,
    o.legal_address_city, o.legal_address_country, o.country,
    COALESCE(sc.settings #>> '{hosted_page,show_seller}', '') = 'true' AS show_seller,
    sc.settings #> '{hosted_page,public_contact}' AS public_contact
FROM organizations o
JOIN sales_channels sc ON sc.org_id = o.id
JOIN agent_feed_tokens ft ON ft.sales_channel_id = sc.id
WHERE o.id = $1
  AND o.deleted_at IS NULL
  AND sc.deleted_at IS NULL
  AND sc.settings #>> '{hosted_page,enabled}' = 'true'
  AND ft.is_active = true
  AND ($2::text = '' OR ft.token = $2::text)
ORDER BY ft.created_at DESC
LIMIT 1`

// GetHostedPageSeller reads the seller block inputs of a hosted page for the
// organization orgID, through the channel that owns feedToken (empty = the
// organization's newest eligible channel). Returns pgx.ErrNoRows when the
// organization has no such channel.
func (q *Queries) GetHostedPageSeller(ctx context.Context, orgID uuid.UUID, feedToken string) (HostedPageSellerRow, error) {
	row := q.db.QueryRow(ctx, getHostedPageSeller, orgID, feedToken)
	var r HostedPageSellerRow
	err := row.Scan(
		&r.OrgName, &r.LegalName, &r.TaxID, &r.TaxIDScheme, &r.RegistrationNumber,
		&r.AddressLine1, &r.AddressLine2, &r.AddressPostalCode,
		&r.AddressCity, &r.AddressCountry, &r.OrgCountry,
		&r.ShowSeller, &r.PublicContact,
	)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetHostedPageEventPromoterName
// ─────────────────────────────────────────────────────────────────────────────

const getHostedPageEventPromoterName = `-- name: GetHostedPageEventPromoterName :one
SELECT p.name
FROM event_promoters epr
JOIN org_promoters p ON p.id = epr.promoter_id AND p.org_id = epr.org_id
WHERE epr.event_id = $1
  AND epr.org_id   = $2`

// GetHostedPageEventPromoterName returns the name of the promoter an event is
// linked to, pgx.ErrNoRows when the organization itself is the promoter.
func (q *Queries) GetHostedPageEventPromoterName(ctx context.Context, eventID, orgID uuid.UUID) (string, error) {
	var name string
	err := q.db.QueryRow(ctx, getHostedPageEventPromoterName, eventID, orgID).Scan(&name)
	return name, err
}

// ─────────────────────────────────────────────────────────────────────────────
// GetSellerCountries
// ─────────────────────────────────────────────────────────────────────────────

const getSellerCountries = `-- name: GetSellerCountries :one
SELECT legal_address_country, country
FROM organizations
WHERE id = $1`

// SellerCountriesRow carries the two country columns of an organization.
type SellerCountriesRow struct {
	LegalAddressCountry *string
	Country             string
}

// GetSellerCountries returns the organization's legal-address country and its
// plain country, the inputs of the consumer-withdrawal-notice rule.
func (q *Queries) GetSellerCountries(ctx context.Context, orgID uuid.UUID) (SellerCountriesRow, error) {
	var r SellerCountriesRow
	err := q.db.QueryRow(ctx, getSellerCountries, orgID).Scan(&r.LegalAddressCountry, &r.Country)
	return r, err
}
