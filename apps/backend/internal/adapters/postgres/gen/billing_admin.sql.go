// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: billing_admin.sql
//
// Queries of the superadmin screen "Price lists and rates" (billing wave B-A,
// task B1-5). They reuse the row types and scanners of billing_foundation.sql.go.
// Money is int64 minor units with the currency next to it.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Price list versions
// ─────────────────────────────────────────────────────────────────────────────

// PriceListSummaryRow is a price-list version with the size of its content.
type PriceListSummaryRow struct {
	PriceListRow
	RulesCount int32 `json:"rules_count"`
	PacksCount int32 `json:"packs_count"`
}

const listPriceListVersions = `-- name: ListPriceListVersions :many
SELECT ` + priceListColumns + `,
       (SELECT count(*) FROM price_list_rules r WHERE r.price_list_id = price_lists.id)::int AS rules_count,
       (SELECT count(*) FROM price_list_packs p WHERE p.price_list_id = price_lists.id)::int AS packs_count
FROM   price_lists
WHERE  ($1::text = '' OR code = $1) AND ($2::text = '' OR currency = $2)
ORDER BY code, currency, effective_from, version`

// ListPriceListVersions returns every version, oldest first within a
// (code, currency) series. An empty code or currency does not narrow.
func (q *Queries) ListPriceListVersions(ctx context.Context, code, currency string) ([]PriceListSummaryRow, error) {
	rows, err := q.db.Query(ctx, listPriceListVersions, code, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PriceListSummaryRow
	for rows.Next() {
		var r PriceListSummaryRow
		if err := rows.Scan(&r.ID, &r.Code, &r.Version, &r.Currency, &r.EffectiveFrom,
			&r.RevokedAt, &r.Locked, &r.FreeTicketsAnnualLimit, &r.FloorPerTicketMinor,
			&r.BonusShareCapBP, &r.BonusCoversSurcharge, &r.CreatedBy, &r.Reason, &r.CreatedAt,
			&r.RulesCount, &r.PacksCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getPriceListByID = `-- name: GetPriceListByID :one
SELECT ` + priceListColumns + `
FROM   price_lists
WHERE  id = $1`

// GetPriceListByID returns one version, pgx.ErrNoRows when it does not exist.
func (q *Queries) GetPriceListByID(ctx context.Context, id uuid.UUID) (PriceListRow, error) {
	return scanPriceListRow(q.db.QueryRow(ctx, getPriceListByID, id))
}

const lockPriceListSeries = `-- name: LockPriceListSeries :exec
SELECT pg_advisory_xact_lock(hashtextextended('price_list:' || $1::text || ':' || $2::text, 0))`

// LockPriceListSeries serializes the writers of one (code, currency) series
// until the end of the transaction.
func (q *Queries) LockPriceListSeries(ctx context.Context, code, currency string) error {
	_, err := q.db.Exec(ctx, lockPriceListSeries, code, currency)
	return err
}

const getPriceListSeriesState = `-- name: GetPriceListSeriesState :one
SELECT (COALESCE(max(version), 0) + 1)::int AS next_version,
       max(effective_from) FILTER (WHERE revoked_at IS NULL) AS latest_effective_from
FROM   price_lists
WHERE  code = $1 AND currency = $2`

// PriceListSeriesState is what a new version needs to know about its series.
type PriceListSeriesState struct {
	// NextVersion counts every version, revoked ones included.
	NextVersion int32
	// LatestEffectiveFrom is the latest start among versions not revoked,
	// nil when the series has none.
	LatestEffectiveFrom *time.Time
}

// GetPriceListSeriesState reads the next version number and the latest start
// of the (code, currency) series.
func (q *Queries) GetPriceListSeriesState(ctx context.Context, code, currency string) (PriceListSeriesState, error) {
	var s PriceListSeriesState
	err := q.db.QueryRow(ctx, getPriceListSeriesState, code, currency).Scan(&s.NextVersion, &s.LatestEffectiveFrom)
	return s, err
}

// InsertPriceListVersionParams are the columns of a new version. The numbers
// are the operator's: nothing here has a default.
type InsertPriceListVersionParams struct {
	Code                   string
	Version                int32
	Currency               string
	EffectiveFrom          time.Time
	Locked                 bool
	FreeTicketsAnnualLimit *int32
	FloorPerTicketMinor    *int64
	// BonusShareCapBP is the cap in basis points, a multiple of 10.
	BonusShareCapBP      int32
	BonusCoversSurcharge bool
	CreatedBy            *string
	Reason               *string
}

const insertPriceListVersion = `-- name: InsertPriceListVersion :one
INSERT INTO price_lists (
    code, version, currency, effective_from, locked, free_tickets_annual_limit,
    floor_per_ticket_minor, bonus_share_cap, bonus_covers_surcharge, created_by, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, ($8::int / 10000.0)::numeric(4,3), $9, $10, $11)
RETURNING ` + priceListColumns

// InsertPriceListVersion writes a new version. A second row with the same
// (code, currency, version) fails with 23505.
func (q *Queries) InsertPriceListVersion(ctx context.Context, p InsertPriceListVersionParams) (PriceListRow, error) {
	return scanPriceListRow(q.db.QueryRow(ctx, insertPriceListVersion,
		p.Code, p.Version, p.Currency, p.EffectiveFrom, p.Locked, p.FreeTicketsAnnualLimit,
		p.FloorPerTicketMinor, p.BonusShareCapBP, p.BonusCoversSurcharge, p.CreatedBy, p.Reason))
}

const insertPriceListRule = `-- name: InsertPriceListRule :one
INSERT INTO price_list_rules (price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order, created_at`

// InsertPriceListRule writes one rule of a new version. params is the raw
// JSON object, amounts in minor units.
func (q *Queries) InsertPriceListRule(ctx context.Context, priceListID uuid.UUID, ruleType, line, eventKind string,
	saleCurrency *string, params []byte, sortOrder int32,
) (PriceListRuleRow, error) {
	return scanPriceListRuleRow(q.db.QueryRow(ctx, insertPriceListRule,
		priceListID, ruleType, line, eventKind, saleCurrency, params, sortOrder))
}

const insertPriceListPack = `-- name: InsertPriceListPack :one
INSERT INTO price_list_packs (
    price_list_id, tier, face_minor, unseated_rate_minor, seated_surcharge_minor, active, sort_order)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, price_list_id, tier, face_minor, unseated_rate_minor, seated_surcharge_minor, active, sort_order`

// InsertPriceListPack writes one credit-pack tier of a new version. currency
// is the price list's, set on the returned row.
func (q *Queries) InsertPriceListPack(ctx context.Context, priceListID uuid.UUID, currency string, tier int16,
	faceMinor, unseatedRateMinor, seatedSurchargeMinor int64, active bool, sortOrder int32,
) (PriceListPackRow, error) {
	var r PriceListPackRow
	err := q.db.QueryRow(ctx, insertPriceListPack, priceListID, tier, faceMinor, unseatedRateMinor,
		seatedSurchargeMinor, active, sortOrder).Scan(&r.ID, &r.PriceListID, &r.Tier, &r.FaceMinor,
		&r.UnseatedRateMinor, &r.SeatedSurchargeMinor, &r.Active, &r.SortOrder)
	r.Currency = currency
	return r, err
}

const lockPriceListByID = `-- name: LockPriceListByID :one
SELECT ` + priceListColumns + `
FROM   price_lists
WHERE  id = $1
FOR UPDATE`

// LockPriceListByID reads a version and locks its row until the end of the
// transaction. pgx.ErrNoRows when it does not exist.
func (q *Queries) LockPriceListByID(ctx context.Context, id uuid.UUID) (PriceListRow, error) {
	return scanPriceListRow(q.db.QueryRow(ctx, lockPriceListByID, id))
}

const revokePriceListVersion = `-- name: RevokePriceListVersion :one
UPDATE price_lists
SET    revoked_at = $2::timestamptz
WHERE  id = $1 AND revoked_at IS NULL AND effective_from > $2::timestamptz
RETURNING ` + priceListColumns

// RevokePriceListVersion marks a version revoked at now. It is the only
// UPDATE of a price-list version, and it repeats the rule "only before the
// version's effective_from" in its WHERE: pgx.ErrNoRows means the version
// does not exist, is already revoked, or has already started.
func (q *Queries) RevokePriceListVersion(ctx context.Context, id uuid.UUID, now time.Time) (PriceListRow, error) {
	return scanPriceListRow(q.db.QueryRow(ctx, revokePriceListVersion, id, now))
}

const priceListRevokeLeavesAccountsUncovered = `-- name: PriceListRevokeLeavesAccountsUncovered :one
SELECT EXISTS (
    SELECT 1 FROM price_lists self
    WHERE  self.id = $1
      AND  NOT EXISTS (SELECT 1 FROM price_lists o
                       WHERE  o.code = self.code AND o.currency = self.currency
                         AND  o.id <> self.id AND o.revoked_at IS NULL
                         AND  o.effective_from <= self.effective_from)
      AND  EXISTS (SELECT 1 FROM billing_account_terms t
                   JOIN   billing_accounts a ON a.id = t.account_id
                   WHERE  t.price_list_code = self.code AND a.currency = self.currency)
)`

// PriceListRevokeLeavesAccountsUncovered reports whether revoking the version
// would leave accounts that name its code with no version in force.
func (q *Queries) PriceListRevokeLeavesAccountsUncovered(ctx context.Context, id uuid.UUID) (bool, error) {
	var b bool
	err := q.db.QueryRow(ctx, priceListRevokeLeavesAccountsUncovered, id).Scan(&b)
	return b, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Individual organization rates
// ─────────────────────────────────────────────────────────────────────────────

// OrgRateOverrideAdminRow is an individual rate with its organization's name.
type OrgRateOverrideAdminRow struct {
	OrgRateOverrideRow
	OrgName string `json:"org_name"`
}

const listOrgRateOverridesAdmin = `-- name: ListOrgRateOverridesAdmin :many
SELECT r.id, r.org_id, r.currency, r.unseated_minor, r.seated_minor, r.effective_from,
       r.effective_to, r.note, r.created_by, r.created_at, o.name AS org_name
FROM   org_rate_overrides r
JOIN   organizations o ON o.id = r.org_id
WHERE  ($1::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR r.org_id = $1)
  AND  ($2::text = '' OR r.currency = $2)
ORDER BY o.name, r.currency, r.effective_from DESC`

// ListOrgRateOverridesAdmin returns individual rates, every period of them.
// uuid.Nil for orgID and "" for currency do not narrow.
func (q *Queries) ListOrgRateOverridesAdmin(ctx context.Context, orgID uuid.UUID, currency string) ([]OrgRateOverrideAdminRow, error) {
	rows, err := q.db.Query(ctx, listOrgRateOverridesAdmin, orgID, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OrgRateOverrideAdminRow
	for rows.Next() {
		var r OrgRateOverrideAdminRow
		if err := rows.Scan(&r.ID, &r.OrgID, &r.Currency, &r.UnseatedMinor, &r.SeatedMinor,
			&r.EffectiveFrom, &r.EffectiveTo, &r.Note, &r.CreatedBy, &r.CreatedAt, &r.OrgName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const lockOrgRateSeries = `-- name: LockOrgRateSeries :exec
SELECT pg_advisory_xact_lock(hashtextextended('org_rate:' || $1::text || ':' || $2::text, 0))`

// LockOrgRateSeries serializes the writers of one (organization, currency)
// series until the end of the transaction.
func (q *Queries) LockOrgRateSeries(ctx context.Context, orgID uuid.UUID, currency string) error {
	_, err := q.db.Exec(ctx, lockOrgRateSeries, orgID.String(), currency)
	return err
}

const getOrgRateSeriesLatestFrom = `-- name: GetOrgRateSeriesLatestFrom :one
SELECT max(effective_from) AS latest_effective_from
FROM   org_rate_overrides
WHERE  org_id = $1 AND currency = $2`

// GetOrgRateSeriesLatestFrom returns the latest start of the series, nil when
// it has no rows.
func (q *Queries) GetOrgRateSeriesLatestFrom(ctx context.Context, orgID uuid.UUID, currency string) (*time.Time, error) {
	var t *time.Time
	err := q.db.QueryRow(ctx, getOrgRateSeriesLatestFrom, orgID, currency).Scan(&t)
	return t, err
}

const closeOrgRateOverridesAt = `-- name: CloseOrgRateOverridesAt :many
UPDATE org_rate_overrides
SET    effective_to = $3::timestamptz
WHERE  org_id = $1 AND currency = $2 AND effective_from < $3::timestamptz
  AND  (effective_to IS NULL OR effective_to > $3::timestamptz)
RETURNING id`

// CloseOrgRateOverridesAt ends the individual rates that are still open at
// at (the start of a new one) and returns their ids. The caller passes a
// moment in the future.
func (q *Queries) CloseOrgRateOverridesAt(ctx context.Context, orgID uuid.UUID, currency string, at time.Time) ([]uuid.UUID, error) {
	rows, err := q.db.Query(ctx, closeOrgRateOverridesAt, orgID, currency, at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// InsertOrgRateOverrideParams are the columns of a new individual rate.
type InsertOrgRateOverrideParams struct {
	OrgID         uuid.UUID
	Currency      string
	UnseatedMinor int64
	SeatedMinor   int64
	EffectiveFrom time.Time
	EffectiveTo   *time.Time
	Note          *string
	CreatedBy     *string
}

const insertOrgRateOverride = `-- name: InsertOrgRateOverride :one
INSERT INTO org_rate_overrides (
    org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, note, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, note, created_by, created_at`

// InsertOrgRateOverride writes an individual rate. A second row with the same
// (organization, currency, effective_from) fails with 23505.
func (q *Queries) InsertOrgRateOverride(ctx context.Context, p InsertOrgRateOverrideParams) (OrgRateOverrideRow, error) {
	return scanOrgRateOverrideRow(q.db.QueryRow(ctx, insertOrgRateOverride,
		p.OrgID, p.Currency, p.UnseatedMinor, p.SeatedMinor, p.EffectiveFrom, p.EffectiveTo, p.Note, p.CreatedBy))
}

const lockOrgRateOverrideByID = `-- name: LockOrgRateOverrideByID :one
SELECT id, org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, note, created_by, created_at
FROM   org_rate_overrides
WHERE  id = $1
FOR UPDATE`

// LockOrgRateOverrideByID reads an individual rate and locks its row until the
// end of the transaction. pgx.ErrNoRows when it does not exist.
func (q *Queries) LockOrgRateOverrideByID(ctx context.Context, id uuid.UUID) (OrgRateOverrideRow, error) {
	return scanOrgRateOverrideRow(q.db.QueryRow(ctx, lockOrgRateOverrideByID, id))
}

const nextOrgRateOverrideFrom = `-- name: NextOrgRateOverrideFrom :one
SELECT min(effective_from) AS next_effective_from
FROM   org_rate_overrides
WHERE  org_id = $1 AND currency = $2 AND effective_from > $3`

// NextOrgRateOverrideFrom returns the start of the next individual rate of the
// series after after, nil when there is none.
func (q *Queries) NextOrgRateOverrideFrom(ctx context.Context, orgID uuid.UUID, currency string, after time.Time) (*time.Time, error) {
	var t *time.Time
	err := q.db.QueryRow(ctx, nextOrgRateOverrideFrom, orgID, currency, after).Scan(&t)
	return t, err
}

const setOrgRateOverrideEnd = `-- name: SetOrgRateOverrideEnd :one
UPDATE org_rate_overrides
SET    effective_to = $2::timestamptz
WHERE  id = $1 AND $2::timestamptz > effective_from AND $2::timestamptz > $3::timestamptz
  AND  (effective_to IS NULL OR (effective_to > $3::timestamptz AND $2::timestamptz < effective_to))
RETURNING id, org_id, currency, unseated_minor, seated_minor, effective_from, effective_to, note, created_by, created_at`

// SetOrgRateOverrideEnd closes an individual rate for the future: the new end
// must be after the start and after now, earlier than the current end (closing
// never extends a period), and the rate must not have ended.
// pgx.ErrNoRows means one of those did not hold.
func (q *Queries) SetOrgRateOverrideEnd(ctx context.Context, id uuid.UUID, end, now time.Time) (OrgRateOverrideRow, error) {
	return scanOrgRateOverrideRow(q.db.QueryRow(ctx, setOrgRateOverrideEnd, id, end, now))
}

const deleteNotStartedOrgRateOverride = `-- name: DeleteNotStartedOrgRateOverride :execrows
DELETE FROM org_rate_overrides
WHERE  id = $1 AND effective_from > $2`

// DeleteNotStartedOrgRateOverride removes an individual rate that has not
// started yet. A rate in force or in the past is never deleted: 0 rows.
func (q *Queries) DeleteNotStartedOrgRateOverride(ctx context.Context, id uuid.UUID, now time.Time) (int64, error) {
	ct, err := q.db.Exec(ctx, deleteNotStartedOrgRateOverride, id, now)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Accounts and their terms
// ─────────────────────────────────────────────────────────────────────────────

// BillingAccountAdminRow is a billing account with its organization's name.
type BillingAccountAdminRow struct {
	BillingAccountRow
	OrgName string `json:"org_name"`
}

const listBillingAccountsAdmin = `-- name: ListBillingAccountsAdmin :many
SELECT a.id, a.org_id, a.currency, a.issuer_id, a.account_kind, a.payer, a.tax_status,
       a.tax_id_verified_at, a.auto_topup, a.created_at, a.updated_at, o.name AS org_name
FROM   billing_accounts a
JOIN   organizations o ON o.id = a.org_id
WHERE  ($1::uuid = '00000000-0000-0000-0000-000000000000'::uuid OR a.org_id = $1)
ORDER BY o.name, a.account_kind, a.currency`

// ListBillingAccountsAdmin returns billing accounts with the organization's
// name. uuid.Nil does not narrow.
func (q *Queries) ListBillingAccountsAdmin(ctx context.Context, orgID uuid.UUID) ([]BillingAccountAdminRow, error) {
	rows, err := q.db.Query(ctx, listBillingAccountsAdmin, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BillingAccountAdminRow
	for rows.Next() {
		var r BillingAccountAdminRow
		if err := rows.Scan(&r.ID, &r.OrgID, &r.Currency, &r.IssuerID, &r.AccountKind, &r.Payer,
			&r.TaxStatus, &r.TaxIDVerifiedAt, &r.AutoTopup, &r.CreatedAt, &r.UpdatedAt, &r.OrgName); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listBillingAccountTermsForAccounts = `-- name: ListBillingAccountTermsForAccounts :many
SELECT ` + billingAccountTermsColumns + `
FROM   billing_account_terms
WHERE  account_id = ANY($1::uuid[])
ORDER BY account_id, effective_from`

// ListBillingAccountTermsForAccounts returns the whole terms history of the
// accounts, oldest first.
func (q *Queries) ListBillingAccountTermsForAccounts(ctx context.Context, accountIDs []uuid.UUID) ([]BillingAccountTermsRow, error) {
	rows, err := q.db.Query(ctx, listBillingAccountTermsForAccounts, accountIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BillingAccountTermsRow
	for rows.Next() {
		r, err := scanBillingAccountTermsRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const lockBillingAccountTermsSeries = `-- name: LockBillingAccountTermsSeries :exec
SELECT pg_advisory_xact_lock(hashtextextended('account_terms:' || $1::text, 0))`

// LockBillingAccountTermsSeries serializes the writers of one account's terms
// until the end of the transaction.
func (q *Queries) LockBillingAccountTermsSeries(ctx context.Context, accountID uuid.UUID) error {
	_, err := q.db.Exec(ctx, lockBillingAccountTermsSeries, accountID.String())
	return err
}

const getBillingAccountTermsLatestFrom = `-- name: GetBillingAccountTermsLatestFrom :one
SELECT max(effective_from) AS latest_effective_from
FROM   billing_account_terms
WHERE  account_id = $1`

// GetBillingAccountTermsLatestFrom returns the latest start among the
// account's terms, nil when it has none.
func (q *Queries) GetBillingAccountTermsLatestFrom(ctx context.Context, accountID uuid.UUID) (*time.Time, error) {
	var t *time.Time
	err := q.db.QueryRow(ctx, getBillingAccountTermsLatestFrom, accountID).Scan(&t)
	return t, err
}

const organizationExistsForBilling = `-- name: OrganizationExistsForBilling :one
SELECT EXISTS (SELECT 1 FROM organizations WHERE id = $1 AND deleted_at IS NULL)`

// OrganizationExistsForBilling reports whether the organization exists and is
// not deleted.
func (q *Queries) OrganizationExistsForBilling(ctx context.Context, id uuid.UUID) (bool, error) {
	var b bool
	err := q.db.QueryRow(ctx, organizationExistsForBilling, id).Scan(&b)
	return b, err
}
