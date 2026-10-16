-- Organizer terms of sale: templates and renderings (migration 0159, task
-- P3a). Hand-maintained mirror of gen/legal_documents.sql.go.

-- name: ListLegalDocuments :many
SELECT ` + legalDocumentColumns +;

-- name: GetLegalDocument :one
SELECT ` + legalDocumentColumns +;

-- name: LockLegalDocument :one
SELECT ` + legalDocumentColumns +;

-- name: InsertLegalDocumentDraft :one
INSERT INTO legal_documents (kind, locale, version, status, body_template, created_by)
SELECT $1::text, $2::text, COALESCE(MAX(version), 0) + 1, 'draft', $3::text, $4::uuid
FROM legal_documents
WHERE kind = $1::text AND locale = $2::text
RETURNING id, kind, locale, version, status, body_template,
       created_by, created_at, published_at, published_by, retired_at;

-- name: RetirePublishedLegalDocument :execrows
UPDATE legal_documents
SET status = 'retired', retired_at = now()
WHERE kind = $1 AND locale = $2 AND status = 'published';

-- name: PublishLegalDocument :one
UPDATE legal_documents
SET status = 'published', published_at = now(), published_by = $2
WHERE id = $1 AND status = 'draft'
RETURNING id, kind, locale, version, status, body_template,
       created_by, created_at, published_at, published_by, retired_at;

-- name: GetPublishedLegalDocument :one
SELECT ` + legalDocumentColumns +;

-- name: ResolveLegalTermsSlug :one
SELECT org_id, promoter_id FROM (
    SELECT o.id AS org_id, NULL::uuid AS promoter_id, 0 AS rank
    FROM organizations o
    WHERE lower(o.slug) = lower($1) AND o.deleted_at IS NULL
    UNION ALL
    SELECT p.org_id, p.id, 1
    FROM org_promoters p
    JOIN organizations o ON o.id = p.org_id
    WHERE lower(p.slug) = lower($1) AND p.archived_at IS NULL AND o.deleted_at IS NULL
) s
ORDER BY rank
LIMIT 1;

-- name: GetLegalTermsSeller :one
SELECT
    o.id, o.slug, o.name, o.legal_name, o.legal_form, o.tax_id, o.tax_id_scheme,
    o.registration_number, o.legal_address_line1, o.legal_address_line2,
    o.legal_address_postal_code, o.legal_address_city, o.legal_address_country,
    o.country, o.refund_policy_mode,
    sc.id, sc.payment_mode, sc.settings #> '{hosted_page,public_contact}'
FROM organizations o
JOIN sales_channels sc ON sc.org_id = o.id
JOIN agent_feed_tokens ft ON ft.sales_channel_id = sc.id
WHERE o.id = $1
  AND o.deleted_at IS NULL
  AND sc.deleted_at IS NULL
  AND ft.is_active = true
  AND (($2::text = '' AND sc.settings #>> '{hosted_page,enabled}' = 'true')
       OR ft.token = $2::text)
ORDER BY ft.created_at DESC
LIMIT 1;

-- name: GetLegalTermsEventBySlug :one
SELECT
    e.id, e.slug, e.refund_policy_mode,
    p.id, p.name, p.legal_id, p.address, p.website
FROM events e
LEFT JOIN event_promoters epr ON epr.event_id = e.id AND epr.org_id = e.org_id
LEFT JOIN org_promoters p ON p.id = epr.promoter_id AND p.org_id = epr.org_id
WHERE e.org_id = $1
  AND lower(e.slug) = lower($2)
  AND e.status = 'published'
  AND e.deleted_at IS NULL
  AND ($3::uuid IS NULL OR epr.promoter_id = $3::uuid);

-- name: GetLegalTermsPublishedEventByID :one
SELECT
    e.id, e.slug, e.refund_policy_mode,
    p.id, p.name, p.legal_id, p.address, p.website
FROM events e
LEFT JOIN event_promoters epr ON epr.event_id = e.id AND epr.org_id = e.org_id
LEFT JOIN org_promoters p ON p.id = epr.promoter_id AND p.org_id = epr.org_id
WHERE e.org_id = $1
  AND e.id = $2
  AND e.status = 'published'
  AND e.deleted_at IS NULL
  AND ($3::uuid IS NULL OR epr.promoter_id = $3::uuid);

-- name: GetLegalTermsEventByID :one
SELECT
    e.id, e.slug, e.refund_policy_mode,
    p.id, p.name, p.legal_id, p.address, p.website
FROM events e
LEFT JOIN event_promoters epr ON epr.event_id = e.id AND epr.org_id = e.org_id
LEFT JOIN org_promoters p ON p.id = epr.promoter_id AND p.org_id = epr.org_id
WHERE e.org_id = $1
  AND e.id = $2
  AND e.deleted_at IS NULL;

-- name: GetLegalTermsPromoter :one
SELECT id, name, legal_id, address, website
FROM org_promoters
WHERE org_id = $1 AND id = $2;

-- name: GetLegalRenderingByInputs :one
SELECT ` + legalRenderingColumns +;

-- name: InsertLegalRendering :one
INSERT INTO legal_renderings (
    org_id, channel_id, event_id, document_id, document_version,
    locale, refund_mode, inputs, inputs_hash, body_html, body_sha256
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11)
ON CONFLICT (document_id, inputs_hash) DO NOTHING
RETURNING id, org_id, channel_id, event_id, document_id, document_version,
       locale, refund_mode, inputs, inputs_hash, body_html, body_sha256, rendered_at;

-- name: GetLegalTermsLink :one
SELECT o.slug, e.slug
FROM events e
JOIN organizations o ON o.id = e.org_id
WHERE e.id = $1;
