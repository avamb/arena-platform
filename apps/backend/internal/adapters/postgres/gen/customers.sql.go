// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: customers.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// W1-A4a (feature #479): customers aggregate — customers, customer_identities
// and customer_org_links (spec §3.2 / §12.1).
// ─────────────────────────────────────────────────────────────────────────────

// CustomerRow mirrors the customers table (migration 0091). display_name and
// locale are nullable — the resolver populates them lazily from the first
// gateway session that supplies non-empty values. merged_into is set when
// the row was consolidated into another customer (see
// customer_merge_candidates); anonymized_at flags GDPR-erased rows.
type CustomerRow struct {
	ID           uuid.UUID  `json:"id"`
	SystemID     int64      `json:"system_id"`
	DisplayName  *string    `json:"display_name"`
	Locale       *string    `json:"locale"`
	MergedInto   *uuid.UUID `json:"merged_into"`
	AnonymizedAt *time.Time `json:"anonymized_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

func scanCustomerRow(row interface {
	Scan(dest ...any) error
}) (CustomerRow, error) {
	var c CustomerRow
	err := row.Scan(
		&c.ID,
		&c.SystemID,
		&c.DisplayName,
		&c.Locale,
		&c.MergedInto,
		&c.AnonymizedAt,
		&c.CreatedAt,
		&c.UpdatedAt,
	)
	return c, err
}

// ─────────────────────────────────────────────────────────────────────────────
// InsertCustomer
// ─────────────────────────────────────────────────────────────────────────────

const insertCustomer = `-- name: InsertCustomer :one
INSERT INTO customers (display_name, locale)
VALUES ($1, $2)
RETURNING id, system_id, display_name, locale, merged_into, anonymized_at, created_at, updated_at`

// InsertCustomer creates a customer row. system_id is auto-assigned from
// compatibility_system_id_seq (>= 1e9) and returned in the row so callers
// can immediately surface it as the Bil24 userId.
func (q *Queries) InsertCustomer(ctx context.Context, displayName *string, locale *string) (CustomerRow, error) {
	row := q.db.QueryRow(ctx, insertCustomer, displayName, locale)
	return scanCustomerRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetCustomerByID
// ─────────────────────────────────────────────────────────────────────────────

const getCustomerByID = `-- name: GetCustomerByID :one
SELECT id, system_id, display_name, locale, merged_into, anonymized_at, created_at, updated_at
FROM   customers
WHERE  id = $1`

// GetCustomerByID loads a customer by platform uuid. Returns pgx.ErrNoRows
// when absent.
func (q *Queries) GetCustomerByID(ctx context.Context, id uuid.UUID) (CustomerRow, error) {
	row := q.db.QueryRow(ctx, getCustomerByID, id)
	return scanCustomerRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// GetCustomerBySystemID
// ─────────────────────────────────────────────────────────────────────────────

const getCustomerBySystemID = `-- name: GetCustomerBySystemID :one
SELECT id, system_id, display_name, locale, merged_into, anonymized_at, created_at, updated_at
FROM   customers
WHERE  system_id = $1`

// GetCustomerBySystemID loads a customer by the bigint id exposed to Bil24
// clients as userId. Returns pgx.ErrNoRows when no such id exists — callers
// translate that to gateway result codes -3 / 101 per spec §6.
func (q *Queries) GetCustomerBySystemID(ctx context.Context, systemID int64) (CustomerRow, error) {
	row := q.db.QueryRow(ctx, getCustomerBySystemID, systemID)
	return scanCustomerRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// UpdateCustomerProfile
// ─────────────────────────────────────────────────────────────────────────────

const updateCustomerProfile = `-- name: UpdateCustomerProfile :exec
UPDATE customers
SET    display_name = $2,
       locale      = $3,
       updated_at  = now()
WHERE  id = $1`

// UpdateCustomerProfile refreshes display_name / locale and bumps
// updated_at. Both fields are nullable — pass nil to clear a value.
func (q *Queries) UpdateCustomerProfile(ctx context.Context, id uuid.UUID, displayName *string, locale *string) error {
	_, err := q.db.Exec(ctx, updateCustomerProfile, id, displayName, locale)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// CustomerIdentityRow + queries
// ─────────────────────────────────────────────────────────────────────────────

// CustomerIdentityRow mirrors the customer_identities table. channel_id is
// non-nil only for weak identities (device / wc_customer / bil24_user);
// verified_at is populated once the identity has been proven (e.g. an
// email OTP round-trip). value_normalized is the canonical form (lower/
// trim for email, E.164 for phone).
type CustomerIdentityRow struct {
	ID              uuid.UUID  `json:"id"`
	CustomerID      uuid.UUID  `json:"customer_id"`
	Kind            string     `json:"kind"`
	ValueNormalized string     `json:"value_normalized"`
	ChannelID       *uuid.UUID `json:"channel_id"`
	VerifiedAt      *time.Time `json:"verified_at"`
	FirstSeenAt     time.Time  `json:"first_seen_at"`
	LastSeenAt      time.Time  `json:"last_seen_at"`
	Source          string     `json:"source"`
}

func scanCustomerIdentityRow(row interface {
	Scan(dest ...any) error
}) (CustomerIdentityRow, error) {
	var r CustomerIdentityRow
	err := row.Scan(
		&r.ID,
		&r.CustomerID,
		&r.Kind,
		&r.ValueNormalized,
		&r.ChannelID,
		&r.VerifiedAt,
		&r.FirstSeenAt,
		&r.LastSeenAt,
		&r.Source,
	)
	return r, err
}

const insertCustomerIdentity = `-- name: InsertCustomerIdentity :one
INSERT INTO customer_identities
    (customer_id, kind, value_normalized, channel_id, verified_at, source)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, customer_id, kind, value_normalized, channel_id, verified_at,
          first_seen_at, last_seen_at, source`

// InsertCustomerIdentity attaches an identity to a customer. Uniqueness is
// enforced by the partial indexes customer_identities_strong_uq (kind ∈
// email/phone/telegram, platform-wide) and customer_identities_weak_uq
// (kind ∈ device/wc_customer/bil24_user, per channel). On 23505 conflict
// callers should fall back to Get*Identity* to resolve the winning row.
func (q *Queries) InsertCustomerIdentity(
	ctx context.Context,
	customerID uuid.UUID,
	kind string,
	valueNormalized string,
	channelID *uuid.UUID,
	verifiedAt *time.Time,
	source string,
) (CustomerIdentityRow, error) {
	row := q.db.QueryRow(ctx, insertCustomerIdentity,
		customerID, kind, valueNormalized, channelID, verifiedAt, source)
	return scanCustomerIdentityRow(row)
}

const getCustomerIdentityByStrongKey = `-- name: GetCustomerIdentityByStrongKey :one
SELECT id, customer_id, kind, value_normalized, channel_id, verified_at,
       first_seen_at, last_seen_at, source
FROM   customer_identities
WHERE  kind = $1
  AND  value_normalized = $2`

// GetCustomerIdentityByStrongKey resolves an identity by (kind, value) with
// no channel scope. Intended for strong identities (email / phone /
// telegram) that are unique platform-wide. Returns pgx.ErrNoRows when
// absent.
func (q *Queries) GetCustomerIdentityByStrongKey(ctx context.Context, kind string, valueNormalized string) (CustomerIdentityRow, error) {
	row := q.db.QueryRow(ctx, getCustomerIdentityByStrongKey, kind, valueNormalized)
	return scanCustomerIdentityRow(row)
}

const getCustomerIdentityByWeakKey = `-- name: GetCustomerIdentityByWeakKey :one
SELECT id, customer_id, kind, value_normalized, channel_id, verified_at,
       first_seen_at, last_seen_at, source
FROM   customer_identities
WHERE  kind = $1
  AND  value_normalized = $2
  AND  channel_id = $3`

// GetCustomerIdentityByWeakKey resolves a channel-scoped identity (device
// / wc_customer / bil24_user). Returns pgx.ErrNoRows when absent.
func (q *Queries) GetCustomerIdentityByWeakKey(ctx context.Context, kind string, valueNormalized string, channelID uuid.UUID) (CustomerIdentityRow, error) {
	row := q.db.QueryRow(ctx, getCustomerIdentityByWeakKey, kind, valueNormalized, channelID)
	return scanCustomerIdentityRow(row)
}

const listCustomerIdentities = `-- name: ListCustomerIdentities :many
SELECT id, customer_id, kind, value_normalized, channel_id, verified_at,
       first_seen_at, last_seen_at, source
FROM   customer_identities
WHERE  customer_id = $1
ORDER  BY last_seen_at DESC, id`

// ListCustomerIdentities enumerates every identity attached to a customer,
// most-recently-seen first. Used by the admin card and by the merge
// resolver (spec §12.2).
func (q *Queries) ListCustomerIdentities(ctx context.Context, customerID uuid.UUID) ([]CustomerIdentityRow, error) {
	rows, err := q.db.Query(ctx, listCustomerIdentities, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CustomerIdentityRow, 0)
	for rows.Next() {
		r, err := scanCustomerIdentityRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

const touchCustomerIdentityLastSeen = `-- name: TouchCustomerIdentityLastSeen :exec
UPDATE customer_identities
SET    last_seen_at = now()
WHERE  id = $1`

// TouchCustomerIdentityLastSeen bumps last_seen_at after a successful
// gateway resolution. Cheap enough to run on every resolver hit.
func (q *Queries) TouchCustomerIdentityLastSeen(ctx context.Context, id uuid.UUID) error {
	_, err := q.db.Exec(ctx, touchCustomerIdentityLastSeen, id)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// CustomerOrgLinkRow + queries
// ─────────────────────────────────────────────────────────────────────────────

// CustomerOrgLinkRow mirrors customer_org_links: the per-org rollup of a
// customer's activity. Counters are maintained by the orders write path;
// this file only ensures the row exists and reads it back.
type CustomerOrgLinkRow struct {
	CustomerID   uuid.UUID  `json:"customer_id"`
	OrgID        uuid.UUID  `json:"org_id"`
	FirstOrderAt *time.Time `json:"first_order_at"`
	LastOrderAt  *time.Time `json:"last_order_at"`
	OrdersCount  int32      `json:"orders_count"`
	TicketsCount int32      `json:"tickets_count"`
	Source       string     `json:"source"`
	Attributes   []byte     `json:"attributes"`
}

func scanCustomerOrgLinkRow(row interface {
	Scan(dest ...any) error
}) (CustomerOrgLinkRow, error) {
	var r CustomerOrgLinkRow
	err := row.Scan(
		&r.CustomerID,
		&r.OrgID,
		&r.FirstOrderAt,
		&r.LastOrderAt,
		&r.OrdersCount,
		&r.TicketsCount,
		&r.Source,
		&r.Attributes,
	)
	return r, err
}

const upsertCustomerOrgLink = `-- name: UpsertCustomerOrgLink :exec
INSERT INTO customer_org_links (customer_id, org_id, source)
VALUES ($1, $2, $3)
ON CONFLICT (customer_id, org_id) DO NOTHING`

// UpsertCustomerOrgLink ensures a (customer, org) rollup exists. Counter
// maintenance belongs to the orders write path (spec §3.3 / §12.1) and is
// deliberately NOT performed here — this query is idempotent and safe
// to call on every gateway session that resolves a customer against an org.
func (q *Queries) UpsertCustomerOrgLink(ctx context.Context, customerID uuid.UUID, orgID uuid.UUID, source string) error {
	_, err := q.db.Exec(ctx, upsertCustomerOrgLink, customerID, orgID, source)
	return err
}

const getCustomerOrgLink = `-- name: GetCustomerOrgLink :one
SELECT customer_id, org_id, first_order_at, last_order_at,
       orders_count, tickets_count, source, attributes
FROM   customer_org_links
WHERE  customer_id = $1
  AND  org_id = $2`

// GetCustomerOrgLink reads the (customer, org) rollup. Returns
// pgx.ErrNoRows when the customer has never been linked to the org.
func (q *Queries) GetCustomerOrgLink(ctx context.Context, customerID uuid.UUID, orgID uuid.UUID) (CustomerOrgLinkRow, error) {
	row := q.db.QueryRow(ctx, getCustomerOrgLink, customerID, orgID)
	return scanCustomerOrgLinkRow(row)
}

// ─────────────────────────────────────────────────────────────────────────────
// W1-A4b (feature #480): the resolver needs three small extra queries on top
// of the #479 gen wrappers — MarkCustomerIdentityVerified (PAY_ORDER sets
// verified_at on a strong identity), UpdateCustomerDisplayName (spec §12.2
// display_name rules) and InsertCustomerMergeCandidate (strong-key conflict
// never auto-merges — a candidate row is queued instead).
// ─────────────────────────────────────────────────────────────────────────────

const markCustomerIdentityVerified = `-- name: MarkCustomerIdentityVerified :exec
UPDATE customer_identities
SET    verified_at  = COALESCE(verified_at, $2),
       last_seen_at = $2
WHERE  id = $1`

// MarkCustomerIdentityVerified promotes an identity to verified status.
// verified_at is set only if currently NULL (idempotent — an identity
// stays verified once proven). last_seen_at is refreshed to the same
// timestamp so verification counts as a touch.
func (q *Queries) MarkCustomerIdentityVerified(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := q.db.Exec(ctx, markCustomerIdentityVerified, id, at)
	return err
}

const updateCustomerDisplayName = `-- name: UpdateCustomerDisplayName :exec
UPDATE customers
SET    display_name = $2,
       updated_at   = now()
WHERE  id = $1`

// UpdateCustomerDisplayName overwrites display_name only. Locale and other
// profile fields are left untouched — spec §12.2 "не перезаписывать
// непустое имя пустым; новое непустое — обновить".
func (q *Queries) UpdateCustomerDisplayName(ctx context.Context, id uuid.UUID, displayName string) error {
	_, err := q.db.Exec(ctx, updateCustomerDisplayName, id, displayName)
	return err
}

// CustomerMergeCandidateRow mirrors the customer_merge_candidates table.
// The resolver only writes rows; the admin UI consumes them (resolved_at,
// resolution are set by operator action).
type CustomerMergeCandidateRow struct {
	ID         uuid.UUID  `json:"id"`
	CustomerA  uuid.UUID  `json:"customer_a"`
	CustomerB  uuid.UUID  `json:"customer_b"`
	Reason     string     `json:"reason"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	Resolution *string    `json:"resolution"`
}

const insertCustomerMergeCandidate = `-- name: InsertCustomerMergeCandidate :one
INSERT INTO customer_merge_candidates (customer_a, customer_b, reason)
VALUES ($1, $2, $3)
RETURNING id, customer_a, customer_b, reason, created_at, resolved_at, resolution`

// InsertCustomerMergeCandidate queues a suspected duplicate for operator
// review. The gateway NEVER auto-merges strong-key conflicts (spec §12.2
// + ADR-036); it emits one of these rows and returns the winning customer.
func (q *Queries) InsertCustomerMergeCandidate(ctx context.Context, customerA uuid.UUID, customerB uuid.UUID, reason string) (CustomerMergeCandidateRow, error) {
	row := q.db.QueryRow(ctx, insertCustomerMergeCandidate, customerA, customerB, reason)
	var r CustomerMergeCandidateRow
	err := row.Scan(&r.ID, &r.CustomerA, &r.CustomerB, &r.Reason, &r.CreatedAt, &r.ResolvedAt, &r.Resolution)
	return r, err
}

const insertCustomerAttribute = `-- name: InsertCustomerAttribute :exec
INSERT INTO customer_attributes (customer_id, org_id, key, value, source)
VALUES ($1, $2, $3, $4::jsonb, $5)
ON CONFLICT (customer_id, org_id, key) DO UPDATE
SET value = EXCLUDED.value, source = EXCLUDED.source`

// InsertCustomerAttribute writes (or overwrites) a platform-scoped or org-
// scoped free-form attribute. Used by the resolver to stash an invalid raw
// phone as an attribute rather than an identity (spec §3.2 last paragraph:
// "невалидный телефон — не идентичность, а атрибут"). value is a JSON
// document supplied as a string; the query casts it to jsonb.
func (q *Queries) InsertCustomerAttribute(ctx context.Context, customerID uuid.UUID, orgID *uuid.UUID, key string, valueJSON string, source string) error {
	_, err := q.db.Exec(ctx, insertCustomerAttribute, customerID, orgID, key, valueJSON, source)
	return err
}

// ─────────────────────────────────────────────────────────────────────────────
// W1-A4d (feature #482): org-scoped read endpoints — SearchCustomersByOrg,
// ListCustomerAttributesForOrg, ListCustomerConsentsForOrg.
// ─────────────────────────────────────────────────────────────────────────────

const searchCustomersByOrg = `-- name: SearchCustomersByOrg :many
SELECT DISTINCT c.id, c.system_id, c.display_name, c.locale, c.merged_into,
       c.anonymized_at, c.created_at, c.updated_at
FROM   customers c
JOIN   customer_org_links l ON l.customer_id = c.id
LEFT JOIN customer_identities i ON i.customer_id = c.id
WHERE  l.org_id = $1
  AND  ($2 = ''
        OR i.value_normalized = $2
        OR c.display_name ILIKE '%' || $2 || '%')
ORDER  BY c.created_at DESC, c.id DESC
LIMIT  $3 OFFSET $4`

// SearchCustomersByOrg is the org-scoped customer search backing
// GET /v1/organizations/{org_id}/customers?q= (spec §12.3). Only customers
// linked to this org (customer_org_links) are visible. q matches an exact
// normalized email/phone or an ILIKE substring of display_name; pass "" to
// list every customer linked to the org.
func (q *Queries) SearchCustomersByOrg(ctx context.Context, orgID uuid.UUID, q2 string, limit, offset int32) ([]CustomerRow, error) {
	rows, err := q.db.Query(ctx, searchCustomersByOrg, orgID, q2, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CustomerRow, 0)
	for rows.Next() {
		c, err := scanCustomerRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CustomerAttributeRow mirrors the customer_attributes table. OrgID is nil
// for platform-scoped attributes.
type CustomerAttributeRow struct {
	ID         uuid.UUID  `json:"id"`
	CustomerID uuid.UUID  `json:"customer_id"`
	OrgID      *uuid.UUID `json:"org_id"`
	Key        string     `json:"key"`
	Value      []byte     `json:"value"`
	Source     string     `json:"source"`
	ImportedAt *time.Time `json:"imported_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

const listCustomerAttributesForOrg = `-- name: ListCustomerAttributesForOrg :many
SELECT id, customer_id, org_id, key, value, source, imported_at, created_at
FROM   customer_attributes
WHERE  customer_id = $1
  AND  (org_id IS NULL OR org_id = $2)
ORDER  BY created_at DESC, id`

// ListCustomerAttributesForOrg lists a customer's attributes visible from
// one org: platform-scoped rows (org_id IS NULL) plus this org's own rows
// (spec §12.3 card: "org + platform attributes").
func (q *Queries) ListCustomerAttributesForOrg(ctx context.Context, customerID uuid.UUID, orgID uuid.UUID) ([]CustomerAttributeRow, error) {
	rows, err := q.db.Query(ctx, listCustomerAttributesForOrg, customerID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CustomerAttributeRow, 0)
	for rows.Next() {
		var a CustomerAttributeRow
		if err := rows.Scan(&a.ID, &a.CustomerID, &a.OrgID, &a.Key, &a.Value, &a.Source, &a.ImportedAt, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CustomerConsentRow mirrors the customer_consents table.
type CustomerConsentRow struct {
	CustomerID  uuid.UUID  `json:"customer_id"`
	OrgID       uuid.UUID  `json:"org_id"`
	Kind        string     `json:"kind"`
	GivenAt     time.Time  `json:"given_at"`
	WithdrawnAt *time.Time `json:"withdrawn_at"`
	Source      string     `json:"source"`
}

const listCustomerConsentsForOrg = `-- name: ListCustomerConsentsForOrg :many
SELECT customer_id, org_id, kind, given_at, withdrawn_at, source
FROM   customer_consents
WHERE  customer_id = $1
  AND  org_id = $2
ORDER  BY kind`

// ListCustomerConsentsForOrg lists a customer's consent records within one
// org (spec §12.3 card: "org consents").
func (q *Queries) ListCustomerConsentsForOrg(ctx context.Context, customerID uuid.UUID, orgID uuid.UUID) ([]CustomerConsentRow, error) {
	rows, err := q.db.Query(ctx, listCustomerConsentsForOrg, customerID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]CustomerConsentRow, 0)
	for rows.Next() {
		var c CustomerConsentRow
		if err := rows.Scan(&c.CustomerID, &c.OrgID, &c.Kind, &c.GivenAt, &c.WithdrawnAt, &c.Source); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
