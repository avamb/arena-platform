// Hand-maintained typed query wrappers; follow sqlc output conventions.
// source: legal_documents.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// LegalDocumentRow is one version of a terms template (migration 0163,
// task P3a). A published or retired version never changes (trigger
// legal_documents_guard).
type LegalDocumentRow struct {
	ID           uuid.UUID
	Kind         string
	Locale       string
	Version      int32
	Status       string
	BodyTemplate string
	CreatedBy    *uuid.UUID
	CreatedAt    time.Time
	PublishedAt  *time.Time
	PublishedBy  *uuid.UUID
	RetiredAt    *time.Time
}

const legalDocumentColumns = `id, kind, locale, version, status, body_template,
       created_by, created_at, published_at, published_by, retired_at`

func scanLegalDocument(row interface{ Scan(...any) error }) (LegalDocumentRow, error) {
	var r LegalDocumentRow
	err := row.Scan(&r.ID, &r.Kind, &r.Locale, &r.Version, &r.Status, &r.BodyTemplate,
		&r.CreatedBy, &r.CreatedAt, &r.PublishedAt, &r.PublishedBy, &r.RetiredAt)
	return r, err
}

const listLegalDocuments = `-- name: ListLegalDocuments :many
SELECT ` + legalDocumentColumns + `
FROM legal_documents
WHERE ($1::text = '' OR kind = $1::text)
  AND ($2::text = '' OR locale = $2::text)
ORDER BY kind, locale, version DESC`

// ListLegalDocuments lists the versions of every template, newest version
// first within each kind and language. An empty kind or locale matches all.
func (q *Queries) ListLegalDocuments(ctx context.Context, kind, locale string) ([]LegalDocumentRow, error) {
	rows, err := q.db.Query(ctx, listLegalDocuments, kind, locale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LegalDocumentRow{}
	for rows.Next() {
		r, err := scanLegalDocument(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getLegalDocument = `-- name: GetLegalDocument :one
SELECT ` + legalDocumentColumns + `
FROM legal_documents
WHERE id = $1`

// GetLegalDocument reads one version. pgx.ErrNoRows when there is none.
func (q *Queries) GetLegalDocument(ctx context.Context, id uuid.UUID) (LegalDocumentRow, error) {
	return scanLegalDocument(q.db.QueryRow(ctx, getLegalDocument, id))
}

const lockLegalDocument = `-- name: LockLegalDocument :one
SELECT ` + legalDocumentColumns + `
FROM legal_documents
WHERE id = $1
FOR UPDATE`

// LockLegalDocument reads one version and locks its row until the end of the
// transaction (the publish step).
func (q *Queries) LockLegalDocument(ctx context.Context, id uuid.UUID) (LegalDocumentRow, error) {
	return scanLegalDocument(q.db.QueryRow(ctx, lockLegalDocument, id))
}

const insertLegalDocumentDraft = `-- name: InsertLegalDocumentDraft :one
INSERT INTO legal_documents (kind, locale, version, status, body_template, created_by)
SELECT $1::text, $2::text, COALESCE(MAX(version), 0) + 1, 'draft', $3::text, $4::uuid
FROM legal_documents
WHERE kind = $1::text AND locale = $2::text
RETURNING ` + legalDocumentColumns

// InsertLegalDocumentDraft stores a new draft as the next version number of
// its kind and language. Two concurrent drafts of the same pair race on the
// UNIQUE (kind, locale, version): the loser gets 23505.
func (q *Queries) InsertLegalDocumentDraft(ctx context.Context, kind, locale, body string, createdBy *uuid.UUID) (LegalDocumentRow, error) {
	return scanLegalDocument(q.db.QueryRow(ctx, insertLegalDocumentDraft, kind, locale, body, createdBy))
}

const retirePublishedLegalDocument = `-- name: RetirePublishedLegalDocument :execrows
UPDATE legal_documents
SET status = 'retired', retired_at = now()
WHERE kind = $1 AND locale = $2 AND status = 'published'`

// RetirePublishedLegalDocument retires the published version of a kind and
// language, if any. It runs in the publish transaction BEFORE the new
// version is published (legal_documents_one_published is not deferrable).
func (q *Queries) RetirePublishedLegalDocument(ctx context.Context, kind, locale string) (int64, error) {
	tag, err := q.db.Exec(ctx, retirePublishedLegalDocument, kind, locale)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const publishLegalDocument = `-- name: PublishLegalDocument :one
UPDATE legal_documents
SET status = 'published', published_at = now(), published_by = $2
WHERE id = $1 AND status = 'draft'
RETURNING ` + legalDocumentColumns

// PublishLegalDocument publishes a draft. pgx.ErrNoRows when the row is not
// a draft (any more).
func (q *Queries) PublishLegalDocument(ctx context.Context, id uuid.UUID, publishedBy *uuid.UUID) (LegalDocumentRow, error) {
	return scanLegalDocument(q.db.QueryRow(ctx, publishLegalDocument, id, publishedBy))
}

const getPublishedLegalDocument = `-- name: GetPublishedLegalDocument :one
SELECT ` + legalDocumentColumns + `
FROM legal_documents
WHERE kind = $1 AND locale = $2 AND status = 'published'`

// GetPublishedLegalDocument reads the published version of a kind and
// language. pgx.ErrNoRows when none is published.
func (q *Queries) GetPublishedLegalDocument(ctx context.Context, kind, locale string) (LegalDocumentRow, error) {
	return scanLegalDocument(q.db.QueryRow(ctx, getPublishedLegalDocument, kind, locale))
}

// ─────────────────────────────────────────────────────────────────────────────
// Terms inputs
// ─────────────────────────────────────────────────────────────────────────────

// LegalTermsOwnerRow is what a public slug names: an organization, or one of
// its promoters (PromoterID set).
type LegalTermsOwnerRow struct {
	OrgID      uuid.UUID
	PromoterID *uuid.UUID
}

const resolveLegalTermsSlug = `-- name: ResolveLegalTermsSlug :one
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
LIMIT 1`

// ResolveLegalTermsSlug resolves a public page slug the way the hosted page
// does: an organization first, else a promoter that is not archived.
// pgx.ErrNoRows when neither matches.
func (q *Queries) ResolveLegalTermsSlug(ctx context.Context, slug string) (LegalTermsOwnerRow, error) {
	var r LegalTermsOwnerRow
	err := q.db.QueryRow(ctx, resolveLegalTermsSlug, slug).Scan(&r.OrgID, &r.PromoterID)
	return r, err
}

// LegalTermsSellerRow carries the seller inputs of the terms: the
// organization's legal data, its refund policy, and the channel the terms
// are read for (its payment mode and public contact). Deliberately no
// organizations.contact_email / contact_phone.
type LegalTermsSellerRow struct {
	OrgID              uuid.UUID
	OrgSlug            string
	OrgName            string
	LegalName          *string
	LegalForm          *string
	TaxID              *string
	TaxIDScheme        *string
	RegistrationNumber *string
	AddressLine1       *string
	AddressLine2       *string
	AddressPostalCode  *string
	AddressCity        *string
	AddressCountry     *string
	OrgCountry         string
	RefundPolicyMode   *string
	ChannelID          uuid.UUID
	PaymentMode        string
	PublicContact      []byte
}

const getLegalTermsSeller = `-- name: GetLegalTermsSeller :one
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
LIMIT 1`

// GetLegalTermsSeller reads the seller inputs of the organization orgID
// through the channel of feedToken (any active channel of THAT organization),
// or, with an empty token, through the organization's newest hosted-page
// channel. pgx.ErrNoRows when there is no such channel; a token of another
// organization finds nothing.
func (q *Queries) GetLegalTermsSeller(ctx context.Context, orgID uuid.UUID, feedToken string) (LegalTermsSellerRow, error) {
	var r LegalTermsSellerRow
	err := q.db.QueryRow(ctx, getLegalTermsSeller, orgID, feedToken).Scan(
		&r.OrgID, &r.OrgSlug, &r.OrgName, &r.LegalName, &r.LegalForm, &r.TaxID, &r.TaxIDScheme,
		&r.RegistrationNumber, &r.AddressLine1, &r.AddressLine2,
		&r.AddressPostalCode, &r.AddressCity, &r.AddressCountry,
		&r.OrgCountry, &r.RefundPolicyMode,
		&r.ChannelID, &r.PaymentMode, &r.PublicContact,
	)
	return r, err
}

// LegalTermsEventRow carries the event inputs of the terms: its refund
// policy and the promoter it is linked to (all promoter fields nil when the
// organization itself presents it).
type LegalTermsEventRow struct {
	EventID          uuid.UUID
	EventSlug        *string
	RefundPolicyMode *string
	PromoterID       *uuid.UUID
	PromoterName     *string
	PromoterLegalID  *string
	PromoterAddress  *string
	PromoterWebsite  *string
}

const legalTermsEventSelect = `SELECT
    e.id, e.slug, e.refund_policy_mode,
    p.id, p.name, p.legal_id, p.address, p.website
FROM events e
LEFT JOIN event_promoters epr ON epr.event_id = e.id AND epr.org_id = e.org_id
LEFT JOIN org_promoters p ON p.id = epr.promoter_id AND p.org_id = epr.org_id
`

func scanLegalTermsEvent(row interface{ Scan(...any) error }) (LegalTermsEventRow, error) {
	var r LegalTermsEventRow
	err := row.Scan(&r.EventID, &r.EventSlug, &r.RefundPolicyMode,
		&r.PromoterID, &r.PromoterName, &r.PromoterLegalID, &r.PromoterAddress, &r.PromoterWebsite)
	return r, err
}

const getLegalTermsEventBySlug = `-- name: GetLegalTermsEventBySlug :one
` + legalTermsEventSelect + `WHERE e.org_id = $1
  AND lower(e.slug) = lower($2)
  AND e.status = 'published'
  AND e.deleted_at IS NULL
  AND ($3::uuid IS NULL OR epr.promoter_id = $3::uuid)`

// GetLegalTermsEventBySlug finds a published event of the organization by
// slug, the way the hosted page does; when promoterID is set (the page slug
// named a promoter) the event must be linked to that promoter.
// pgx.ErrNoRows otherwise.
func (q *Queries) GetLegalTermsEventBySlug(ctx context.Context, orgID uuid.UUID, eventSlug string, promoterID *uuid.UUID) (LegalTermsEventRow, error) {
	return scanLegalTermsEvent(q.db.QueryRow(ctx, getLegalTermsEventBySlug, orgID, eventSlug, promoterID))
}

const getLegalTermsPublishedEventByID = `-- name: GetLegalTermsPublishedEventByID :one
` + legalTermsEventSelect + `WHERE e.org_id = $1
  AND e.id = $2
  AND e.status = 'published'
  AND e.deleted_at IS NULL
  AND ($3::uuid IS NULL OR epr.promoter_id = $3::uuid)`

// GetLegalTermsPublishedEventByID is GetLegalTermsEventBySlug by id: the
// public terms link names the event by id, which every event has.
func (q *Queries) GetLegalTermsPublishedEventByID(ctx context.Context, orgID, eventID uuid.UUID, promoterID *uuid.UUID) (LegalTermsEventRow, error) {
	return scanLegalTermsEvent(q.db.QueryRow(ctx, getLegalTermsPublishedEventByID, orgID, eventID, promoterID))
}

const getLegalTermsEventByID = `-- name: GetLegalTermsEventByID :one
` + legalTermsEventSelect + `WHERE e.org_id = $1
  AND e.id = $2
  AND e.deleted_at IS NULL`

// GetLegalTermsEventByID reads the terms inputs of one event of the
// organization (the checkout already proved it is published to the feed).
func (q *Queries) GetLegalTermsEventByID(ctx context.Context, orgID, eventID uuid.UUID) (LegalTermsEventRow, error) {
	return scanLegalTermsEvent(q.db.QueryRow(ctx, getLegalTermsEventByID, orgID, eventID))
}

// LegalTermsPromoterRow is a promoter named by the page slug.
type LegalTermsPromoterRow struct {
	ID      uuid.UUID
	Name    string
	LegalID *string
	Address *string
	Website *string
}

const getLegalTermsPromoter = `-- name: GetLegalTermsPromoter :one
SELECT id, name, legal_id, address, website
FROM org_promoters
WHERE org_id = $1 AND id = $2`

// GetLegalTermsPromoter reads a promoter of the organization.
func (q *Queries) GetLegalTermsPromoter(ctx context.Context, orgID, promoterID uuid.UUID) (LegalTermsPromoterRow, error) {
	var r LegalTermsPromoterRow
	err := q.db.QueryRow(ctx, getLegalTermsPromoter, orgID, promoterID).Scan(
		&r.ID, &r.Name, &r.LegalID, &r.Address, &r.Website)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Renderings
// ─────────────────────────────────────────────────────────────────────────────

// LegalRenderingRow is the terms HTML one organization's buyers were shown
// (migration 0163). Never updated; never deleted by code.
type LegalRenderingRow struct {
	ID              uuid.UUID
	OrgID           uuid.UUID
	ChannelID       *uuid.UUID
	EventID         *uuid.UUID
	DocumentID      uuid.UUID
	DocumentVersion int32
	Locale          string
	RefundMode      string
	Inputs          []byte
	InputsHash      string
	BodyHTML        string
	BodySHA256      string
	RenderedAt      time.Time
}

const legalRenderingColumns = `id, org_id, channel_id, event_id, document_id, document_version,
       locale, refund_mode, inputs, inputs_hash, body_html, body_sha256, rendered_at`

func scanLegalRendering(row interface{ Scan(...any) error }) (LegalRenderingRow, error) {
	var r LegalRenderingRow
	err := row.Scan(&r.ID, &r.OrgID, &r.ChannelID, &r.EventID, &r.DocumentID, &r.DocumentVersion,
		&r.Locale, &r.RefundMode, &r.Inputs, &r.InputsHash, &r.BodyHTML, &r.BodySHA256, &r.RenderedAt)
	return r, err
}

const getLegalRenderingByInputs = `-- name: GetLegalRenderingByInputs :one
SELECT ` + legalRenderingColumns + `
FROM legal_renderings
WHERE document_id = $1 AND inputs_hash = $2`

// GetLegalRenderingByInputs finds the rendering of a document version for
// one set of inputs. pgx.ErrNoRows when it was never rendered.
func (q *Queries) GetLegalRenderingByInputs(ctx context.Context, documentID uuid.UUID, inputsHash string) (LegalRenderingRow, error) {
	return scanLegalRendering(q.db.QueryRow(ctx, getLegalRenderingByInputs, documentID, inputsHash))
}

// InsertLegalRenderingParams carries one rendering.
type InsertLegalRenderingParams struct {
	OrgID           uuid.UUID
	ChannelID       *uuid.UUID
	EventID         *uuid.UUID
	DocumentID      uuid.UUID
	DocumentVersion int32
	Locale          string
	RefundMode      string
	Inputs          []byte
	InputsHash      string
	BodyHTML        string
	BodySHA256      string
}

const insertLegalRendering = `-- name: InsertLegalRendering :one
INSERT INTO legal_renderings (
    org_id, channel_id, event_id, document_id, document_version,
    locale, refund_mode, inputs, inputs_hash, body_html, body_sha256
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11)
ON CONFLICT (document_id, inputs_hash) DO NOTHING
RETURNING ` + legalRenderingColumns

// InsertLegalRendering stores a rendering. pgx.ErrNoRows when the same
// document and inputs were stored meanwhile (read that row instead): a
// rendering is content-addressed and never written twice.
func (q *Queries) InsertLegalRendering(ctx context.Context, p InsertLegalRenderingParams) (LegalRenderingRow, error) {
	return scanLegalRendering(q.db.QueryRow(ctx, insertLegalRendering,
		p.OrgID, p.ChannelID, p.EventID, p.DocumentID, p.DocumentVersion,
		p.Locale, p.RefundMode, p.Inputs, p.InputsHash, p.BodyHTML, p.BodySHA256))
}

// LegalTermsLinkRow carries the slugs the feed builds a terms link from.
type LegalTermsLinkRow struct {
	OrgSlug   string
	EventSlug *string
}

const getLegalTermsLink = `-- name: GetLegalTermsLink :one
SELECT o.slug, e.slug
FROM events e
JOIN organizations o ON o.id = e.org_id
WHERE e.id = $1`

// GetLegalTermsLink reads the organization slug and the event slug of an
// event, the two parts of its terms link.
func (q *Queries) GetLegalTermsLink(ctx context.Context, eventID uuid.UUID) (LegalTermsLinkRow, error) {
	var r LegalTermsLinkRow
	err := q.db.QueryRow(ctx, getLegalTermsLink, eventID).Scan(&r.OrgSlug, &r.EventSlug)
	return r, err
}
