// Hand-maintained typed query wrapper; follows sqlc output conventions.
// source: billing_foundation.sql
//
// Billing foundation (billing wave B-A, task B1-1, migration 0157): issuers,
// country routes, versioned price lists with rules and packs, billing
// accounts with their dated terms, and individual organization rates.
// Money is int64 minor units with the currency next to it, never a float.
// price_lists.bonus_share_cap (numeric) is read as integer basis points.

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ─────────────────────────────────────────────────────────────────────────────
// Row types and scanners
// ─────────────────────────────────────────────────────────────────────────────

// BillingIssuerRow is one of the platform's invoicing legal entities.
// Timezone defines the month and day boundaries of billing.
type BillingIssuerRow struct {
	ID            uuid.UUID `json:"id"`
	Code          string    `json:"code"`
	Name          string    `json:"name"`
	Country       string    `json:"country"`
	TaxID         *string   `json:"tax_id"`
	Address       []byte    `json:"address"`
	InvoiceSeries *string   `json:"invoice_series"`
	Timezone      string    `json:"timezone"`
	VatTexts      []byte    `json:"vat_texts"`
	CreatedAt     time.Time `json:"created_at"`
}

const billingIssuerColumns = `id, code, name, country, tax_id, address, invoice_series, timezone, vat_texts, created_at`

func scanBillingIssuerRow(row interface{ Scan(dest ...any) error }) (BillingIssuerRow, error) {
	var r BillingIssuerRow
	err := row.Scan(&r.ID, &r.Code, &r.Name, &r.Country, &r.TaxID, &r.Address,
		&r.InvoiceSeries, &r.Timezone, &r.VatTexts, &r.CreatedAt)
	return r, err
}

// BillingCountryRouteRow maps a venue country to the billing currency and
// the issuer of the account that sale is billed to.
type BillingCountryRouteRow struct {
	Country  string    `json:"country"`
	Currency string    `json:"currency"`
	IssuerID uuid.UUID `json:"issuer_id"`
}

func scanBillingCountryRouteRow(row interface{ Scan(dest ...any) error }) (BillingCountryRouteRow, error) {
	var r BillingCountryRouteRow
	err := row.Scan(&r.Country, &r.Currency, &r.IssuerID)
	return r, err
}

// PriceListRow is one version of a price list in one currency.
// BonusShareCapBP is price_lists.bonus_share_cap in basis points
// (0.300 = 3000).
type PriceListRow struct {
	ID                     uuid.UUID  `json:"id"`
	Code                   string     `json:"code"`
	Version                int32      `json:"version"`
	Currency               string     `json:"currency"`
	EffectiveFrom          time.Time  `json:"effective_from"`
	RevokedAt              *time.Time `json:"revoked_at"`
	Locked                 bool       `json:"locked"`
	FreeTicketsAnnualLimit *int32     `json:"free_tickets_annual_limit"`
	FloorPerTicketMinor    *int64     `json:"floor_per_ticket_minor"`
	BonusShareCapBP        int32      `json:"bonus_share_cap_bp"`
	BonusCoversSurcharge   bool       `json:"bonus_covers_surcharge"`
	CreatedBy              *string    `json:"created_by"`
	Reason                 *string    `json:"reason"`
	CreatedAt              time.Time  `json:"created_at"`
}

const priceListColumns = `id, code, version, currency, effective_from, revoked_at, locked,
       free_tickets_annual_limit, floor_per_ticket_minor,
       (bonus_share_cap * 10000)::int AS bonus_share_cap_bp,
       bonus_covers_surcharge, created_by, reason, created_at`

func scanPriceListRow(row interface{ Scan(dest ...any) error }) (PriceListRow, error) {
	var r PriceListRow
	err := row.Scan(&r.ID, &r.Code, &r.Version, &r.Currency, &r.EffectiveFrom,
		&r.RevokedAt, &r.Locked, &r.FreeTicketsAnnualLimit, &r.FloorPerTicketMinor,
		&r.BonusShareCapBP, &r.BonusCoversSurcharge, &r.CreatedBy, &r.Reason, &r.CreatedAt)
	return r, err
}

// PriceListRuleRow is one rule of a price-list version. Params is the raw
// jsonb: amounts in minor units, percentages in basis points (pct_bp).
// SaleCurrency is set for a rule whose threshold is in the sale currency.
type PriceListRuleRow struct {
	ID           uuid.UUID `json:"id"`
	PriceListID  uuid.UUID `json:"price_list_id"`
	RuleType     string    `json:"rule_type"`
	Line         string    `json:"line"`
	EventKind    string    `json:"event_kind"`
	SaleCurrency *string   `json:"sale_currency"`
	Params       []byte    `json:"params"`
	SortOrder    int32     `json:"sort_order"`
	CreatedAt    time.Time `json:"created_at"`
}

func scanPriceListRuleRow(row interface{ Scan(dest ...any) error }) (PriceListRuleRow, error) {
	var r PriceListRuleRow
	err := row.Scan(&r.ID, &r.PriceListID, &r.RuleType, &r.Line, &r.EventKind,
		&r.SaleCurrency, &r.Params, &r.SortOrder, &r.CreatedAt)
	return r, err
}

// PriceListPackRow is one credit-pack tier of a price-list version.
type PriceListPackRow struct {
	ID          uuid.UUID `json:"id"`
	PriceListID uuid.UUID `json:"price_list_id"`
	// Currency is the price list's: a pack's money is in its list's currency.
	Currency             string `json:"currency"`
	Tier                 int16  `json:"tier"`
	FaceMinor            int64  `json:"face_minor"`
	UnseatedRateMinor    int64  `json:"unseated_rate_minor"`
	SeatedSurchargeMinor int64  `json:"seated_surcharge_minor"`
	Active               bool   `json:"active"`
	SortOrder            int32  `json:"sort_order"`
}

func scanPriceListPackRow(row interface{ Scan(dest ...any) error }) (PriceListPackRow, error) {
	var r PriceListPackRow
	err := row.Scan(&r.ID, &r.PriceListID, &r.Currency, &r.Tier, &r.FaceMinor, &r.UnseatedRateMinor,
		&r.SeatedSurchargeMinor, &r.Active, &r.SortOrder)
	return r, err
}

// BillingAccountRow is one billing account: (organization, currency, kind).
type BillingAccountRow struct {
	ID              uuid.UUID  `json:"id"`
	OrgID           uuid.UUID  `json:"org_id"`
	Currency        string     `json:"currency"`
	IssuerID        uuid.UUID  `json:"issuer_id"`
	AccountKind     string     `json:"account_kind"`
	Payer           []byte     `json:"payer"`
	TaxStatus       *string    `json:"tax_status"`
	TaxIDVerifiedAt *time.Time `json:"tax_id_verified_at"`
	AutoTopup       []byte     `json:"auto_topup"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

const billingAccountColumns = `id, org_id, currency, issuer_id, account_kind, payer, tax_status,
       tax_id_verified_at, auto_topup, created_at, updated_at`

func scanBillingAccountRow(row interface{ Scan(dest ...any) error }) (BillingAccountRow, error) {
	var r BillingAccountRow
	err := row.Scan(&r.ID, &r.OrgID, &r.Currency, &r.IssuerID, &r.AccountKind, &r.Payer,
		&r.TaxStatus, &r.TaxIDVerifiedAt, &r.AutoTopup, &r.CreatedAt, &r.UpdatedAt)
	return r, err
}

// BillingAccountTermsRow is one dated row of an account's conditions.
type BillingAccountTermsRow struct {
	ID               uuid.UUID `json:"id"`
	AccountID        uuid.UUID `json:"account_id"`
	EffectiveFrom    time.Time `json:"effective_from"`
	PriceListCode    string    `json:"price_list_code"`
	PlanKind         string    `json:"plan_kind"`
	BillingCycle     string    `json:"billing_cycle"`
	ShortfallPolicy  string    `json:"shortfall_policy"`
	IssuerID         uuid.UUID `json:"issuer_id"`
	InvoiceDelayDays *int32    `json:"invoice_delay_days"`
	DueDays          *int32    `json:"due_days"`
	Locked           bool      `json:"locked"`
	CreatedBy        *string   `json:"created_by"`
	Reason           *string   `json:"reason"`
	CreatedAt        time.Time `json:"created_at"`
}

const billingAccountTermsColumns = `id, account_id, effective_from, price_list_code, plan_kind, billing_cycle,
       shortfall_policy, issuer_id, invoice_delay_days, due_days, locked,
       created_by, reason, created_at`

func scanBillingAccountTermsRow(row interface{ Scan(dest ...any) error }) (BillingAccountTermsRow, error) {
	var r BillingAccountTermsRow
	err := row.Scan(&r.ID, &r.AccountID, &r.EffectiveFrom, &r.PriceListCode, &r.PlanKind,
		&r.BillingCycle, &r.ShortfallPolicy, &r.IssuerID, &r.InvoiceDelayDays, &r.DueDays,
		&r.Locked, &r.CreatedBy, &r.Reason, &r.CreatedAt)
	return r, err
}

// OrgRateOverrideRow is an individual per-ticket rate of an organization.
type OrgRateOverrideRow struct {
	ID            uuid.UUID  `json:"id"`
	OrgID         uuid.UUID  `json:"org_id"`
	Currency      string     `json:"currency"`
	UnseatedMinor int64      `json:"unseated_minor"`
	SeatedMinor   int64      `json:"seated_minor"`
	EffectiveFrom time.Time  `json:"effective_from"`
	EffectiveTo   *time.Time `json:"effective_to"`
	Note          *string    `json:"note"`
	CreatedBy     *string    `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
}

func scanOrgRateOverrideRow(row interface{ Scan(dest ...any) error }) (OrgRateOverrideRow, error) {
	var r OrgRateOverrideRow
	err := row.Scan(&r.ID, &r.OrgID, &r.Currency, &r.UnseatedMinor, &r.SeatedMinor,
		&r.EffectiveFrom, &r.EffectiveTo, &r.Note, &r.CreatedBy, &r.CreatedAt)
	return r, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Issuers and country routes
// ─────────────────────────────────────────────────────────────────────────────

const getBillingIssuerByCode = `-- name: GetBillingIssuerByCode :one
SELECT ` + billingIssuerColumns + `
FROM   billing_issuers
WHERE  code = $1`

// GetBillingIssuerByCode returns the issuer with that code, pgx.ErrNoRows when
// there is none.
func (q *Queries) GetBillingIssuerByCode(ctx context.Context, code string) (BillingIssuerRow, error) {
	return scanBillingIssuerRow(q.db.QueryRow(ctx, getBillingIssuerByCode, code))
}

const listBillingCountryRoutes = `-- name: ListBillingCountryRoutes :many
SELECT country, currency, issuer_id
FROM   billing_country_routes
ORDER BY country`

// ListBillingCountryRoutes returns every country route, by country.
func (q *Queries) ListBillingCountryRoutes(ctx context.Context) ([]BillingCountryRouteRow, error) {
	rows, err := q.db.Query(ctx, listBillingCountryRoutes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BillingCountryRouteRow
	for rows.Next() {
		r, err := scanBillingCountryRouteRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const getBillingCountryRoute = `-- name: GetBillingCountryRoute :one
SELECT country, currency, issuer_id
FROM   billing_country_routes
WHERE  country = $1`

// GetBillingCountryRoute returns the route of an ISO alpha-2 country,
// pgx.ErrNoRows when the country has none (it then bills in EUR, see
// billing.RouteCurrency).
func (q *Queries) GetBillingCountryRoute(ctx context.Context, country string) (BillingCountryRouteRow, error) {
	return scanBillingCountryRouteRow(q.db.QueryRow(ctx, getBillingCountryRoute, country))
}

// ─────────────────────────────────────────────────────────────────────────────
// Price lists
// ─────────────────────────────────────────────────────────────────────────────

const getPriceListVersionAt = `-- name: GetPriceListVersionAt :one
SELECT ` + priceListColumns + `
FROM   price_lists
WHERE  code = $1 AND currency = $2 AND effective_from <= $3 AND revoked_at IS NULL
ORDER BY effective_from DESC, version DESC
LIMIT  1`

// GetPriceListVersionAt returns the version of price list code in currency
// that was in force at at: the latest effective_from <= at that was not
// revoked. pgx.ErrNoRows when none was in force yet.
func (q *Queries) GetPriceListVersionAt(ctx context.Context, code, currency string, at time.Time) (PriceListRow, error) {
	return scanPriceListRow(q.db.QueryRow(ctx, getPriceListVersionAt, code, currency, at))
}

const listPriceListRules = `-- name: ListPriceListRules :many
SELECT id, price_list_id, rule_type, line, event_kind, sale_currency, params, sort_order, created_at
FROM   price_list_rules
WHERE  price_list_id = $1
ORDER BY line, sort_order, id`

// ListPriceListRules returns the rules of one price-list version, by line and
// sort order.
func (q *Queries) ListPriceListRules(ctx context.Context, priceListID uuid.UUID) ([]PriceListRuleRow, error) {
	rows, err := q.db.Query(ctx, listPriceListRules, priceListID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PriceListRuleRow
	for rows.Next() {
		r, err := scanPriceListRuleRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const listPriceListPacks = `-- name: ListPriceListPacks :many
SELECT p.id, p.price_list_id, pl.currency, p.tier, p.face_minor, p.unseated_rate_minor, p.seated_surcharge_minor, p.active, p.sort_order
FROM   price_list_packs p
JOIN   price_lists pl ON pl.id = p.price_list_id
WHERE  p.price_list_id = $1
ORDER BY p.sort_order, p.tier`

// ListPriceListPacks returns the credit-pack tiers of one price-list version,
// inactive ones included.
func (q *Queries) ListPriceListPacks(ctx context.Context, priceListID uuid.UUID) ([]PriceListPackRow, error) {
	rows, err := q.db.Query(ctx, listPriceListPacks, priceListID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PriceListPackRow
	for rows.Next() {
		r, err := scanPriceListPackRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Billing accounts
// ─────────────────────────────────────────────────────────────────────────────

const getBillingAccount = `-- name: GetBillingAccount :one
SELECT ` + billingAccountColumns + `
FROM   billing_accounts
WHERE  org_id = $1 AND currency = $2 AND account_kind = $3`

// GetBillingAccount returns the account of an organization in one currency
// and kind, pgx.ErrNoRows when it does not exist.
func (q *Queries) GetBillingAccount(ctx context.Context, orgID uuid.UUID, currency, accountKind string) (BillingAccountRow, error) {
	return scanBillingAccountRow(q.db.QueryRow(ctx, getBillingAccount, orgID, currency, accountKind))
}

const insertBillingAccountIfMissing = `-- name: InsertBillingAccountIfMissing :exec
INSERT INTO billing_accounts (org_id, currency, issuer_id, account_kind)
VALUES ($1, $2, $3, $4)
ON CONFLICT (org_id, currency, account_kind) DO NOTHING`

// InsertBillingAccountIfMissing creates the account unless one already
// exists for (orgID, currency, accountKind), then reads it back in a second
// statement, so a row committed meanwhile by a concurrent caller is returned
// instead of a duplicate. An existing account keeps its issuer.
func (q *Queries) InsertBillingAccountIfMissing(ctx context.Context, orgID uuid.UUID, currency string, issuerID uuid.UUID, accountKind string) (BillingAccountRow, error) {
	if _, err := q.db.Exec(ctx, insertBillingAccountIfMissing, orgID, currency, issuerID, accountKind); err != nil {
		return BillingAccountRow{}, err
	}
	return q.GetBillingAccount(ctx, orgID, currency, accountKind)
}

const listBillingAccountsByOrg = `-- name: ListBillingAccountsByOrg :many
SELECT ` + billingAccountColumns + `
FROM   billing_accounts
WHERE  org_id = $1
ORDER BY account_kind, currency`

// ListBillingAccountsByOrg returns every billing account of an organization.
func (q *Queries) ListBillingAccountsByOrg(ctx context.Context, orgID uuid.UUID) ([]BillingAccountRow, error) {
	rows, err := q.db.Query(ctx, listBillingAccountsByOrg, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BillingAccountRow
	for rows.Next() {
		r, err := scanBillingAccountRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ─────────────────────────────────────────────────────────────────────────────
// Account terms
// ─────────────────────────────────────────────────────────────────────────────

// InsertBillingAccountTermsParams are the columns of a new terms row. Empty
// PlanKind, BillingCycle and ShortfallPolicy are refused by the table's
// CHECK constraints: the caller names them.
type InsertBillingAccountTermsParams struct {
	AccountID        uuid.UUID
	EffectiveFrom    time.Time
	PriceListCode    string
	PlanKind         string
	BillingCycle     string
	ShortfallPolicy  string
	IssuerID         uuid.UUID
	InvoiceDelayDays *int32
	DueDays          *int32
	Locked           bool
	CreatedBy        *string
	Reason           *string
}

const insertBillingAccountTerms = `-- name: InsertBillingAccountTerms :one
INSERT INTO billing_account_terms (
    account_id, effective_from, price_list_code, plan_kind, billing_cycle,
    shortfall_policy, issuer_id, invoice_delay_days, due_days, locked,
    created_by, reason)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING ` + billingAccountTermsColumns

// InsertBillingAccountTerms writes a new dated terms row. A second row with
// the same (account, effective_from) fails with 23505.
func (q *Queries) InsertBillingAccountTerms(ctx context.Context, p InsertBillingAccountTermsParams) (BillingAccountTermsRow, error) {
	return scanBillingAccountTermsRow(q.db.QueryRow(ctx, insertBillingAccountTerms,
		p.AccountID, p.EffectiveFrom, p.PriceListCode, p.PlanKind, p.BillingCycle,
		p.ShortfallPolicy, p.IssuerID, p.InvoiceDelayDays, p.DueDays, p.Locked,
		p.CreatedBy, p.Reason))
}

const insertBillingAccountTermsIfNone = `-- name: InsertBillingAccountTermsIfNone :exec
INSERT INTO billing_account_terms (
    account_id, effective_from, price_list_code, plan_kind, billing_cycle,
    shortfall_policy, issuer_id, created_by, reason)
SELECT $1::uuid, $2::timestamptz, $3::text, 'standard', 'monthly', 'invoice_postpaid', $4::uuid, $5::text, $6::text
WHERE  NOT EXISTS (SELECT 1 FROM billing_account_terms WHERE account_id = $1::uuid)
ON CONFLICT (account_id, effective_from) DO NOTHING`

// InsertBillingAccountTermsIfNone writes the FIRST terms of an account (the
// standard plan, monthly cycle, invoice_postpaid) and only when the account
// has no terms at all, so it never shadows terms an operator set.
func (q *Queries) InsertBillingAccountTermsIfNone(ctx context.Context, accountID uuid.UUID, effectiveFrom time.Time, priceListCode string, issuerID uuid.UUID, createdBy, reason string) error {
	_, err := q.db.Exec(ctx, insertBillingAccountTermsIfNone, accountID, effectiveFrom, priceListCode, issuerID, createdBy, reason)
	return err
}

const getBillingAccountTermsAt = `-- name: GetBillingAccountTermsAt :one
SELECT ` + billingAccountTermsColumns + `
FROM   billing_account_terms
WHERE  account_id = $1 AND effective_from <= $2
ORDER BY effective_from DESC
LIMIT  1`

// GetBillingAccountTermsAt returns the terms of an account in force at at:
// the last row with effective_from <= at. pgx.ErrNoRows when none.
func (q *Queries) GetBillingAccountTermsAt(ctx context.Context, accountID uuid.UUID, at time.Time) (BillingAccountTermsRow, error) {
	return scanBillingAccountTermsRow(q.db.QueryRow(ctx, getBillingAccountTermsAt, accountID, at))
}

// ─────────────────────────────────────────────────────────────────────────────
// Individual organization rates
// ─────────────────────────────────────────────────────────────────────────────

const getOrgRateOverrideAt = `-- name: GetOrgRateOverrideAt :one
SELECT id, org_id, currency, unseated_minor, seated_minor, effective_from,
       effective_to, note, created_by, created_at
FROM   org_rate_overrides
WHERE  org_id = $1 AND currency = $2 AND effective_from <= $3
  AND  (effective_to IS NULL OR effective_to > $3)
ORDER BY effective_from DESC
LIMIT  1`

// GetOrgRateOverrideAt returns the individual rate of an organization in one
// currency in force at at, pgx.ErrNoRows when the organization has none (the
// price-list rate applies).
func (q *Queries) GetOrgRateOverrideAt(ctx context.Context, orgID uuid.UUID, currency string, at time.Time) (OrgRateOverrideRow, error) {
	return scanOrgRateOverrideRow(q.db.QueryRow(ctx, getOrgRateOverrideAt, orgID, currency, at))
}
