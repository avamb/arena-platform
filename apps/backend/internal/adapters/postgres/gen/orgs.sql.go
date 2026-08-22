// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: orgs.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// OrganizationRow — shared result type for all org queries
// ─────────────────────────────────────────────────────────────────────────────

// OrganizationRow is the result type returned by all organization queries.
// deleted_at is nil for active organizations and non-nil for soft-deleted ones.
type OrganizationRow struct {
	ID                       uuid.UUID  `json:"id"`
	DisplayNumber            int64      `json:"display_number"`
	Name                     string     `json:"name"`
	Slug                     string     `json:"slug"`
	Country                  string     `json:"country"`
	DefaultLocale            string     `json:"default_locale"`
	ReservationTTLSeconds    int32      `json:"reservation_ttl_seconds"`
	LegalName                *string    `json:"legal_name"`
	TaxID                    *string    `json:"tax_id"`
	TaxIDScheme              *string    `json:"tax_id_scheme"`
	RegistrationNumber       *string    `json:"registration_number"`
	LegalAddressLine1        *string    `json:"legal_address_line1"`
	LegalAddressLine2        *string    `json:"legal_address_line2"`
	LegalAddressPostalCode   *string    `json:"legal_address_postal_code"`
	LegalAddressCity         *string    `json:"legal_address_city"`
	LegalAddressCountry      *string    `json:"legal_address_country"`
	ContactEmail             *string    `json:"contact_email"`
	ContactPhone             *string    `json:"contact_phone"`
	WebsiteURL               *string    `json:"website_url"`
	KybStatus                string     `json:"kyb_status"`
	KybVerifiedAt            *time.Time `json:"kyb_verified_at"`
	SenderEmail              *string    `json:"sender_email"`
	SenderVerificationStatus string     `json:"sender_verification_status"`
	LogoMediaID              *uuid.UUID `json:"logo_media_id"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
	DeletedAt                *time.Time `json:"deleted_at"`
}

// scanOrganizationRow scans a single organizations row into an OrganizationRow.
func scanOrganizationRow(row interface {
	Scan(dest ...any) error
}) (OrganizationRow, error) {
	var o OrganizationRow
	err := row.Scan(
		&o.ID,
		&o.DisplayNumber,
		&o.Name,
		&o.Slug,
		&o.Country,
		&o.DefaultLocale,
		&o.ReservationTTLSeconds,
		&o.LegalName, &o.TaxID, &o.TaxIDScheme, &o.RegistrationNumber,
		&o.LegalAddressLine1, &o.LegalAddressLine2, &o.LegalAddressPostalCode, &o.LegalAddressCity, &o.LegalAddressCountry,
		&o.ContactEmail, &o.ContactPhone, &o.WebsiteURL, &o.KybStatus, &o.KybVerifiedAt, &o.SenderEmail, &o.SenderVerificationStatus, &o.LogoMediaID,
		&o.CreatedAt,
		&o.UpdatedAt,
		&o.DeletedAt,
	)
	return o, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertOrganization
// ─────────────────────────────────────────────────────────────────────────────

const insertOrganization = `-- name: InsertOrganization :one
INSERT INTO organizations (name, slug, country, default_locale, reservation_ttl_seconds)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at`

// InsertOrganization creates a new active organization row.
// Returns the created row including the uuidv7 PK assigned by the database.
// Callers must check for unique-constraint violations (pgconn error code 23505)
// on name and slug when deleted_at IS NULL.
func (q *Queries) InsertOrganization(ctx context.Context, name, slug, country, defaultLocale string, reservationTTL int32) (OrganizationRow, error) {
	row := q.db.QueryRow(ctx, insertOrganization, name, slug, country, defaultLocale, reservationTTL)
	return scanOrganizationRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetOrganizationByID
// ─────────────────────────────────────────────────────────────────────────────

const getOrganizationByID = `-- name: GetOrganizationByID :one
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at
FROM   organizations
WHERE  id = $1
  AND  deleted_at IS NULL`

// GetOrganizationByID fetches an active organization by its UUID primary key.
// Returns pgx.ErrNoRows when no active organization with that ID exists.
func (q *Queries) GetOrganizationByID(ctx context.Context, id uuid.UUID) (OrganizationRow, error) {
	row := q.db.QueryRow(ctx, getOrganizationByID, id)
	return scanOrganizationRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetOrganizationBySlug
// ─────────────────────────────────────────────────────────────────────────────

const getOrganizationBySlug = `-- name: GetOrganizationBySlug :one
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at
FROM   organizations
WHERE  slug = $1
  AND  deleted_at IS NULL`

// GetOrganizationBySlug fetches an active organization by its unique slug.
// Returns pgx.ErrNoRows when no active organization with that slug exists.
func (q *Queries) GetOrganizationBySlug(ctx context.Context, slug string) (OrganizationRow, error) {
	row := q.db.QueryRow(ctx, getOrganizationBySlug, slug)
	return scanOrganizationRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// ListOrganizations
// ─────────────────────────────────────────────────────────────────────────────

const listOrganizations = `-- name: ListOrganizations :many
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at
FROM   organizations
WHERE  deleted_at IS NULL
ORDER  BY created_at ASC, id ASC`

// ListOrganizations returns all active (non-deleted) organizations ordered by
// creation time ascending then by id for stable pagination.
func (q *Queries) ListOrganizations(ctx context.Context) ([]OrganizationRow, error) {
	rows, err := q.db.Query(ctx, listOrganizations)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []OrganizationRow
	for rows.Next() {
		o, err := scanOrganizationRow(rows)
		if err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

// ListOrganizationsPage returns one stable page of active organizations.
// search matches organization names and slugs case-insensitively; pass an empty
// string to return every active organization.
const listOrganizationsPage = `-- name: ListOrganizationsPage :many
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at
FROM   organizations
WHERE  deleted_at IS NULL
  AND  ($1 = '' OR name ILIKE '%' || $1 || '%' OR slug ILIKE '%' || $1 || '%')
ORDER  BY created_at ASC, id ASC
LIMIT  $2 OFFSET $3`

func (q *Queries) ListOrganizationsPage(ctx context.Context, search string, limit, offset int32) ([]OrganizationRow, error) {
	rows, err := q.db.Query(ctx, listOrganizationsPage, search, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orgs []OrganizationRow
	for rows.Next() {
		o, err := scanOrganizationRow(rows)
		if err != nil {
			return nil, err
		}
		orgs = append(orgs, o)
	}
	return orgs, rows.Err()
}

const countOrganizationsPage = `-- name: CountOrganizationsPage :one
SELECT COUNT(*)
FROM   organizations
WHERE  deleted_at IS NULL
  AND  ($1 = '' OR name ILIKE '%' || $1 || '%' OR slug ILIKE '%' || $1 || '%')`

// CountOrganizationsPage returns the total number of active organizations
// matching search, independently of a page's limit and offset.
func (q *Queries) CountOrganizationsPage(ctx context.Context, search string) (int64, error) {
	var total int64
	err := q.db.QueryRow(ctx, countOrganizationsPage, search).Scan(&total)
	return total, err
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateOrganization
// ─────────────────────────────────────────────────────────────────────────────

const updateOrganization = `-- name: UpdateOrganization :one
UPDATE organizations
SET    name                    = COALESCE(NULLIF($2, ''), name),
       slug                    = COALESCE(NULLIF($3, ''), slug),
       country                 = COALESCE(NULLIF($4, ''), country),
       default_locale          = COALESCE(NULLIF($5, ''), default_locale),
       reservation_ttl_seconds = CASE WHEN $6::integer > 0 THEN $6::integer ELSE reservation_ttl_seconds END,
       legal_name = $7, tax_id = $8, tax_id_scheme = $9, registration_number = $10,
       legal_address_line1 = $11, legal_address_line2 = $12, legal_address_postal_code = $13,
       legal_address_city = $14, legal_address_country = $15, contact_email = $16,
       contact_phone = $17, website_url = $18, kyb_status = $19,
       kyb_verified_at = CASE WHEN $19 = 'verified' AND kyb_status <> 'verified' THEN now() WHEN $19 <> 'verified' THEN NULL ELSE kyb_verified_at END,
       logo_media_id = CASE WHEN $20::uuid IS NOT NULL THEN $20::uuid ELSE logo_media_id END,
       updated_at              = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at`

// UpdateOrganization applies a partial update to an active organization.
// Empty string fields are ignored (existing value kept). reservationTTL=0
// is also ignored. Returns pgx.ErrNoRows when the org does not exist or
// has been soft-deleted.
func (q *Queries) UpdateOrganization(ctx context.Context, id uuid.UUID, name, slug, country, defaultLocale string, reservationTTL int32, legalName, taxID, taxIDScheme, registrationNumber, addressLine1, addressLine2, postalCode, city, addressCountry, contactEmail, contactPhone, websiteURL *string, kybStatus string, logoMediaID *uuid.UUID) (OrganizationRow, error) {
	row := q.db.QueryRow(ctx, updateOrganization, id, name, slug, country, defaultLocale, reservationTTL, legalName, taxID, taxIDScheme, registrationNumber, addressLine1, addressLine2, postalCode, city, addressCountry, contactEmail, contactPhone, websiteURL, kybStatus, logoMediaID)
	return scanOrganizationRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// PatchOrgLogoMediaID
// ─────────────────────────────────────────────────────────────────────────────

const patchOrgLogoMediaID = `-- name: PatchOrgLogoMediaID :one
UPDATE organizations
SET    logo_media_id = $2,
       updated_at    = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at`

// PatchOrgLogoMediaID sets or clears logo_media_id for an active organization.
// Returns pgx.ErrNoRows when the org does not exist or has been soft-deleted.
func (q *Queries) PatchOrgLogoMediaID(ctx context.Context, orgID uuid.UUID, logoMediaID *uuid.UUID) (OrganizationRow, error) {
	row := q.db.QueryRow(ctx, patchOrgLogoMediaID, orgID, logoMediaID)
	return scanOrganizationRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetTicketPDFFormatByTicketID
// ─────────────────────────────────────────────────────────────────────────────

const getTicketPDFFormatByTicketID = `-- name: GetTicketPDFFormatByTicketID :one
SELECT o.ticket_pdf_format
FROM   tickets       t
JOIN   sessions      s ON s.id = t.session_id
JOIN   events        e ON e.id = s.event_id
JOIN   organizations o ON o.id = e.org_id
WHERE  t.id = $1`

// GetTicketPDFFormatByTicketID resolves the organizer-level
// ticket_pdf_format flag ('mobile' | 'a4' | 'both', SEAT-C4) for the
// organization that owns the given ticket. Read at delivery-enqueue time
// so the ticket.deliver worker payload carries the flag without
// re-joining at send time. Returns pgx.ErrNoRows for an unknown ticket.
func (q *Queries) GetTicketPDFFormatByTicketID(ctx context.Context, ticketID uuid.UUID) (string, error) {
	var format string
	err := q.db.QueryRow(ctx, getTicketPDFFormatByTicketID, ticketID).Scan(&format)
	return format, err
}

const getOrgBrandingByTicketID = `-- name: GetOrgBrandingByTicketID :one
SELECT o.name, o.website_url, o.legal_name, o.legal_address_line1, o.legal_address_line2,
       o.legal_address_postal_code, o.legal_address_city, o.legal_address_country,
       o.contact_email, o.logo_media_id, o.sender_email, o.sender_verification_status
FROM   tickets t
JOIN   sessions s ON s.id = t.session_id
JOIN   events   e ON e.id = s.event_id
JOIN   organizations o ON o.id = e.org_id
WHERE  t.id = $1`

// OrgBrandingRow holds branding fields for populating a delivery payload.
type OrgBrandingRow struct {
	Name                     string     `json:"name"`
	WebsiteURL               *string    `json:"website_url"`
	LegalName                *string    `json:"legal_name"`
	LegalAddressLine1        *string    `json:"legal_address_line1"`
	LegalAddressLine2        *string    `json:"legal_address_line2"`
	LegalAddressPostalCode   *string    `json:"legal_address_postal_code"`
	LegalAddressCity         *string    `json:"legal_address_city"`
	LegalAddressCountry      *string    `json:"legal_address_country"`
	ContactEmail             *string    `json:"contact_email"`
	LogoMediaID              *uuid.UUID `json:"logo_media_id"`
	SenderEmail              *string    `json:"sender_email"`
	SenderVerificationStatus string     `json:"sender_verification_status"`
}

// GetOrgBrandingByTicketID returns the org branding fields for the
// organization that owns the given ticket. Used by delivery enqueueing
// to populate the delivery.Payload org fields (AB-45).
func (q *Queries) GetOrgBrandingByTicketID(ctx context.Context, ticketID uuid.UUID) (OrgBrandingRow, error) {
	row := q.db.QueryRow(ctx, getOrgBrandingByTicketID, ticketID)
	var b OrgBrandingRow
	err := row.Scan(
		&b.Name,
		&b.WebsiteURL,
		&b.LegalName,
		&b.LegalAddressLine1,
		&b.LegalAddressLine2,
		&b.LegalAddressPostalCode,
		&b.LegalAddressCity,
		&b.LegalAddressCountry,
		&b.ContactEmail,
		&b.LogoMediaID,
		&b.SenderEmail,
		&b.SenderVerificationStatus,
	)
	return b, err
}

// GetSenderIdentityByTicketID resolves the owning organization's Brevo sender
// identity immediately before delivery. This deliberately avoids trusting a
// stale worker payload after an identity is revoked.
const getSenderIdentityByTicketID = `-- name: GetSenderIdentityByTicketID :one
SELECT o.sender_email, o.sender_verification_status
FROM tickets t
JOIN sessions s ON s.id = t.session_id
JOIN events e ON e.id = s.event_id
JOIN organizations o ON o.id = e.org_id
WHERE t.id = $1`

func (q *Queries) GetSenderIdentityByTicketID(ctx context.Context, ticketID uuid.UUID) (*string, string, error) {
	var senderEmail *string
	var status string
	err := q.db.QueryRow(ctx, getSenderIdentityByTicketID, ticketID).Scan(&senderEmail, &status)
	return senderEmail, status, err
}

// UpdateOrganizationSenderEmail stores a requested Brevo sender address. Any
// change resets verification: callers cannot mark an address verified.
const updateOrganizationSenderEmail = `-- name: UpdateOrganizationSenderEmail :one
UPDATE organizations
SET sender_email = $2,
    sender_verification_status = CASE WHEN $2 IS NULL THEN 'not_configured' ELSE 'pending' END,
    sender_verified_at = NULL,
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
RETURNING sender_email, sender_verification_status`

func (q *Queries) UpdateOrganizationSenderEmail(ctx context.Context, id uuid.UUID, senderEmail *string) (*string, string, error) {
	var email *string
	var status string
	err := q.db.QueryRow(ctx, updateOrganizationSenderEmail, id, senderEmail).Scan(&email, &status)
	return email, status, err
}

// ─────────────────────────────────────────────────────────────────────────────
// SoftDeleteOrganization
// ─────────────────────────────────────────────────────────────────────────────

const softDeleteOrganization = `-- name: SoftDeleteOrganization :one
UPDATE organizations
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at`

// SoftDeleteOrganization marks an organization as deleted by setting deleted_at
// to the current timestamp. The row is not physically removed so the audit log
// and downstream references remain intact. Returns pgx.ErrNoRows when the org
// does not exist or has already been deleted.
func (q *Queries) SoftDeleteOrganization(ctx context.Context, id uuid.UUID) (OrganizationRow, error) {
	row := q.db.QueryRow(ctx, softDeleteOrganization, id)
	return scanOrganizationRow(row)
}
