-- orgs.sql — sqlc query definitions for the organizations table (feature #119).
-- All queries filter WHERE deleted_at IS NULL to respect the soft-delete policy.

-- name: InsertOrganization :one
INSERT INTO organizations (name, slug, country, default_locale, reservation_ttl_seconds)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at;

-- name: GetOrganizationByID :one
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at
FROM   organizations
WHERE  id = $1
  AND  deleted_at IS NULL;

-- name: GetOrganizationBySlug :one
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at
FROM   organizations
WHERE  slug = $1
  AND  deleted_at IS NULL;

-- name: ListOrganizations :many
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at
FROM   organizations
WHERE  deleted_at IS NULL
ORDER  BY created_at ASC, id ASC;

-- name: ListOrganizationsPage :many
-- Returns a stable page of active organizations. Pass an empty search string to
-- return all organizations; otherwise name and slug are matched case-insensitively.
SELECT id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at
FROM   organizations
WHERE  deleted_at IS NULL
  AND  ($1 = '' OR name ILIKE '%' || $1 || '%' OR slug ILIKE '%' || $1 || '%')
ORDER  BY created_at ASC, id ASC
LIMIT  $2 OFFSET $3;

-- name: CountOrganizationsPage :one
-- Returns the number of active organizations matching the name/slug search.
SELECT COUNT(*)
FROM   organizations
WHERE  deleted_at IS NULL
  AND  ($1 = '' OR name ILIKE '%' || $1 || '%' OR slug ILIKE '%' || $1 || '%');

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
       logo_media_id = CASE WHEN $20::uuid IS NOT NULL THEN $20::uuid ELSE logo_media_id END,
       updated_at              = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at;

-- name: PatchOrgLogoMediaID :one
-- PatchOrgLogoMediaID sets or clears logo_media_id for an active organization.
-- Pass NULL to clear the logo; pass a UUID to set it.
UPDATE organizations
SET    logo_media_id = $2,
       updated_at    = now()
WHERE  id = $1
  AND  deleted_at IS NULL
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at;

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
RETURNING id, display_number, name, slug, country, default_locale, reservation_ttl_seconds, legal_name, tax_id, tax_id_scheme, registration_number, legal_address_line1, legal_address_line2, legal_address_postal_code, legal_address_city, legal_address_country, contact_email, contact_phone, website_url, kyb_status, kyb_verified_at, sender_email, sender_verification_status, logo_media_id, created_at, updated_at, deleted_at;

-- name: GetOrgBrandingByTicketID :one
-- Returns org branding fields for the organization that owns a ticket.
-- Used by delivery enqueueing to populate the org fields in the worker payload.
-- contact_email is the public e-mail of the sales page the ticket was sold
-- through (settings.hosted_page.public_contact.email) when it has one; NULL
-- for a hosted page without one (the letter links the page's contact form,
-- GetTicketContactRoute); else the organization's contact_email (HP-4b). registration_number, tax_id and
-- tax_id_scheme are printed in the letter footer and on the PDF.
SELECT o.name, o.website_url, o.legal_name, o.legal_address_line1, o.legal_address_line2,
       o.legal_address_postal_code, o.legal_address_city, o.legal_address_country,
       CASE WHEN NULLIF(btrim(sc.settings #>> '{hosted_page,public_contact,email}'), '') IS NOT NULL
                 THEN btrim(sc.settings #>> '{hosted_page,public_contact,email}')
            WHEN CASE WHEN sc.id IS NOT NULL THEN COALESCE(sc.settings #>> '{hosted_page,enabled}', '') = 'true'
                 ELSE EXISTS (SELECT 1 FROM sales_channels h WHERE h.org_id = o.id AND h.deleted_at IS NULL
                              AND h.settings #>> '{hosted_page,enabled}' = 'true') END THEN NULL
            ELSE o.contact_email END AS contact_email,
       o.logo_media_id, o.sender_email, o.sender_verification_status,
       o.registration_number, o.tax_id, o.tax_id_scheme
FROM   tickets t
JOIN   sessions s ON s.id = t.session_id
JOIN   events   e ON e.id = s.event_id
JOIN   organizations o ON o.id = e.org_id
LEFT JOIN checkout_sessions cs ON cs.id = t.checkout_session_id
LEFT JOIN sales_channels sc ON sc.id = cs.channel_id AND sc.org_id = o.id
WHERE  t.id = $1;

-- name: GetTicketContactRoute :one
-- Where a letter about this ticket sends the buyer to reach the organizer
-- (owner rule 2026-10-13: personal addresses are never published). The
-- sales page's public e-mail when it has one; else, when the ticket was sold
-- through a page with a hosted page (or, without a channel, when the
-- organization has one), the page's "Write to the organizer" form - the
-- page slug is the event's active promoter's when it has one, else the
-- organization's; else the organization's contact_email (today's
-- behaviour for channels without a hosted page). event_slug is given only for
-- a published, not deleted event that still has a publication on an active
-- feed token of a hosted-page channel of the organization (the hosted event
-- page's own resolution): the page of any other one answers 404, so
-- a cancel or refund letter then links the organization's or promoter's page.
-- "hosted page enabled" is the page's own rule (#>> = 'true'). to_owner: the
-- event belongs to a promoter (gen.EventPromotedSQL), so the form's messages
-- go to the organization's owner only.
SELECT NULLIF(btrim(sc.settings #>> '{hosted_page,public_contact,email}'), '') AS public_email,
       CASE WHEN sc.id IS NOT NULL THEN COALESCE(sc.settings #>> '{hosted_page,enabled}', '') = 'true'
            ELSE EXISTS (SELECT 1 FROM sales_channels h WHERE h.org_id = o.id AND h.deleted_at IS NULL
                         AND h.settings #>> '{hosted_page,enabled}' = 'true') END AS hosted_page,
       NULLIF(btrim(o.contact_email), '') AS org_email,
       COALESCE(op.slug, o.slug) AS page_slug,
       CASE WHEN e.status = 'published' AND e.deleted_at IS NULL
                    AND EXISTS (SELECT 1 FROM event_publications pub
                                JOIN agent_feed_tokens pft ON pft.id = pub.feed_token_id
                                JOIN sales_channels psc ON psc.id = pft.sales_channel_id
                                WHERE pub.event_id = e.id AND pft.is_active = true
                                  AND psc.org_id = o.id AND psc.deleted_at IS NULL
                                  AND psc.settings #>> '{hosted_page,enabled}' = 'true')
                   THEN e.slug END AS event_slug,
       EXISTS (SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id
               WHERE m.org_id = o.id AND m.role = 'org_admin' AND m.status = 'active'
                 AND u.deactivated_at IS NULL AND u.anonymized_at IS NULL AND btrim(u.email) <> ''
                 AND NOT EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                                 WHERE ur.user_id = u.id AND ur.org_id IS NULL
                                   AND r.org_id IS NULL AND r.name NOT IN ('org_admin', 'organizer', 'agent'))) AS owner_reachable,
       EXISTS (SELECT 1 FROM event_promoters epx
               JOIN org_promoters opx ON opx.id = epx.promoter_id AND opx.org_id = epx.org_id
               WHERE epx.event_id = e.id AND epx.org_id = e.org_id AND opx.archived_at IS NULL) AS to_owner
FROM   tickets t
JOIN   sessions s ON s.id = t.session_id
JOIN   events   e ON e.id = s.event_id
JOIN   organizations o ON o.id = e.org_id
LEFT JOIN checkout_sessions cs ON cs.id = t.checkout_session_id
LEFT JOIN sales_channels sc ON sc.id = cs.channel_id AND sc.org_id = o.id
LEFT JOIN event_promoters ep ON ep.event_id = e.id AND ep.org_id = o.id
LEFT JOIN org_promoters op ON op.id = ep.promoter_id AND op.org_id = o.id
                          AND op.archived_at IS NULL AND NULLIF(btrim(op.slug), '') IS NOT NULL
WHERE  t.id = $1;
