-- orgs.sql — sqlc query definitions for the organizations table (feature #119).
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.

-- name: InsertOrganization :one
INSERT INTO organizations (name, slug, country, default_locale, reservation_ttl_seconds)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, created_at, updated_at, deleted_at;

-- name: GetOrganizationByID :one
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, created_at, updated_at, deleted_at
FROM   organizations
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: GetOrganizationBySlug :one
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, created_at, updated_at, deleted_at
FROM   organizations
WHERE  slug = $1
  AND  deleted_at IS NULL;

-- name: ListOrganizations :many
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, created_at, updated_at, deleted_at
FROM   organizations
WHERE  deleted_at IS NULL
ORDER  BY created_at ASC, id ASC;

-- name: UpdateOrganization :one
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
       updated_at              = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, created_at, updated_at, deleted_at;

-- name: GetTicketPDFFormatByTicketID :one
-- SEAT-C4: resolve the organizer-level ticket_pdf_format flag
-- ('mobile' | 'a4' | 'both') for the organization that owns a ticket.
-- Read at delivery-enqueue time so the ticket.deliver worker payload
-- carries the flag without re-joining at send time.
SELECT o.ticket_pdf_format
FROM   tickets       t
JOIN   sessions      s ON s.id = t.session_id
JOIN   events        e ON e.id = s.event_id
JOIN   organizations o ON o.id = e.org_id
WHERE  t.id = $1;

-- name: SoftDeleteOrganization :one
UPDATE organizations
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, created_at, updated_at, deleted_at;
