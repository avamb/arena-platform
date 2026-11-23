// Hand-maintained typed query wrapper; follows sqlc output conventions.
// Run `make sqlc-generate` (requires sqlc >= v1.26) to regenerate from source.
// source: payment_method_domains.sql

package gen

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PaymentMethodDomainRow is one wallet domain of a payment configuration
// (migration 0218). LastError is the provider's own wording: operator-facing.
type PaymentMethodDomainRow struct {
	ID               uuid.UUID  `json:"id"`
	PaymentConfigID  uuid.UUID  `json:"payment_config_id"`
	OrgID            uuid.UUID  `json:"org_id"`
	Domain           string     `json:"domain"`
	Source           string     `json:"source"`
	ProviderDomainID *string    `json:"provider_domain_id"`
	Enabled          *bool      `json:"enabled"`
	ApplePayStatus   *string    `json:"apple_pay_status"`
	GooglePayStatus  *string    `json:"google_pay_status"`
	LinkStatus       *string    `json:"link_status"`
	StatusDetail     *string    `json:"status_detail"`
	LastError        *string    `json:"last_error"`
	CheckedAt        *time.Time `json:"checked_at"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// UpsertPaymentMethodDomainParams are the arguments of UpsertPaymentMethodDomain.
type UpsertPaymentMethodDomainParams struct {
	PaymentConfigID  uuid.UUID
	OrgID            uuid.UUID
	Domain           string
	Source           string
	ProviderDomainID *string
	Enabled          *bool
	ApplePayStatus   *string
	GooglePayStatus  *string
	LinkStatus       *string
	StatusDetail     *string
}

const listPaymentMethodDomains = `-- name: ListPaymentMethodDomains :many
SELECT id, payment_config_id, org_id, domain, source, provider_domain_id, enabled,
       apple_pay_status, google_pay_status, link_status, status_detail, last_error,
       checked_at, created_at, updated_at
FROM   payment_method_domains
WHERE  payment_config_id = $1 AND org_id = $2
ORDER  BY source ASC, domain ASC`

// ListPaymentMethodDomains lists the wallet domains of one configuration,
// scoped to the organization.
func (q *Queries) ListPaymentMethodDomains(ctx context.Context, configID, orgID uuid.UUID) ([]PaymentMethodDomainRow, error) {
	rows, err := q.db.Query(ctx, listPaymentMethodDomains, configID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentMethodDomainRow
	for rows.Next() {
		var p PaymentMethodDomainRow
		if err := rows.Scan(&p.ID, &p.PaymentConfigID, &p.OrgID, &p.Domain, &p.Source, &p.ProviderDomainID, &p.Enabled,
			&p.ApplePayStatus, &p.GooglePayStatus, &p.LinkStatus, &p.StatusDetail, &p.LastError,
			&p.CheckedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

const upsertPaymentMethodDomain = `-- name: UpsertPaymentMethodDomain :exec
INSERT INTO payment_method_domains (payment_config_id, org_id, domain, source, provider_domain_id, enabled,
    apple_pay_status, google_pay_status, link_status, status_detail, last_error, checked_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, now())
ON CONFLICT (payment_config_id, domain) DO UPDATE SET
    source = EXCLUDED.source, provider_domain_id = EXCLUDED.provider_domain_id, enabled = EXCLUDED.enabled,
    apple_pay_status = EXCLUDED.apple_pay_status, google_pay_status = EXCLUDED.google_pay_status,
    link_status = EXCLUDED.link_status, status_detail = EXCLUDED.status_detail,
    last_error = NULL, checked_at = now(), updated_at = now()`

// UpsertPaymentMethodDomain stores what the provider answered and clears last_error.
func (q *Queries) UpsertPaymentMethodDomain(ctx context.Context, p UpsertPaymentMethodDomainParams) error {
	_, err := q.db.Exec(ctx, upsertPaymentMethodDomain, p.PaymentConfigID, p.OrgID, p.Domain, p.Source,
		p.ProviderDomainID, p.Enabled, p.ApplePayStatus, p.GooglePayStatus, p.LinkStatus, p.StatusDetail)
	return err
}

const setPaymentMethodDomainError = `-- name: SetPaymentMethodDomainError :exec
INSERT INTO payment_method_domains (payment_config_id, org_id, domain, source, last_error, checked_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (payment_config_id, domain) DO UPDATE SET
    source = EXCLUDED.source, last_error = EXCLUDED.last_error, checked_at = now(), updated_at = now()`

// SetPaymentMethodDomainError stores a provider failure and keeps any earlier statuses.
func (q *Queries) SetPaymentMethodDomainError(ctx context.Context, configID, orgID uuid.UUID, domain, source, lastError string) error {
	_, err := q.db.Exec(ctx, setPaymentMethodDomainError, configID, orgID, domain, source, lastError)
	return err
}

const deleteStalePaymentMethodDomains = `-- name: DeleteStalePaymentMethodDomains :execrows
DELETE FROM payment_method_domains
WHERE payment_config_id = $1 AND source = 'site' AND NOT (domain = ANY($2::text[]))`

// DeleteStalePaymentMethodDomains deletes the 'site' rows of a configuration
// whose domain is not in keep. Platform rows are never touched.
func (q *Queries) DeleteStalePaymentMethodDomains(ctx context.Context, configID uuid.UUID, keep []string) (int64, error) {
	if keep == nil {
		keep = []string{}
	}
	tag, err := q.db.Exec(ctx, deleteStalePaymentMethodDomains, configID, keep)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const listSalesChannelSiteDomains = `-- name: ListSalesChannelSiteDomains :many
SELECT domain FROM sales_channel_site_domains WHERE channel_id = $1 AND org_id = $2 ORDER BY domain`

// ListSalesChannelSiteDomains lists the site hosts of one channel.
func (q *Queries) ListSalesChannelSiteDomains(ctx context.Context, channelID, orgID uuid.UUID) ([]string, error) {
	return queryDomainStrings(ctx, q.db, listSalesChannelSiteDomains, channelID, orgID)
}

const deleteSalesChannelSiteDomains = `DELETE FROM sales_channel_site_domains WHERE channel_id = $1 AND org_id = $2`

const insertSalesChannelSiteDomain = `INSERT INTO sales_channel_site_domains (channel_id, org_id, domain) VALUES ($1, $2, $3)
ON CONFLICT (channel_id, domain) DO NOTHING`

// ReplaceSalesChannelSiteDomains replaces the channel's site hosts with
// domains in one transaction (a savepoint when q is already in one). Domains
// must be lower-case hosts already validated by the caller.
func (q *Queries) ReplaceSalesChannelSiteDomains(ctx context.Context, channelID, orgID uuid.UUID, domains []string) error {
	b, ok := q.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return pgx.ErrTxClosed
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, deleteSalesChannelSiteDomains, channelID, orgID); err != nil {
		return err
	}
	for _, d := range domains {
		if _, err := tx.Exec(ctx, insertSalesChannelSiteDomain, channelID, orgID, d); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

const listDesiredSiteDomainsForOrgStripe = `-- name: ListDesiredSiteDomainsForOrgStripe :many
SELECT DISTINCT d.domain
FROM   sales_channel_site_domains d
JOIN   sales_channels c ON c.id = d.channel_id
WHERE  d.org_id = $1 AND c.deleted_at IS NULL AND c.provider = 'stripe'
ORDER  BY d.domain`

// ListDesiredSiteDomainsForOrgStripe lists the site hosts of every live
// Stripe sales channel of the organization.
func (q *Queries) ListDesiredSiteDomainsForOrgStripe(ctx context.Context, orgID uuid.UUID) ([]string, error) {
	return queryDomainStrings(ctx, q.db, listDesiredSiteDomainsForOrgStripe, orgID)
}

const listActiveStripePaymentConfigs = `-- name: ListActiveStripePaymentConfigs :many
SELECT id, org_id, provider, mode, provider_account_id, public_config, secrets, status, is_active, created_at, updated_at, deleted_at, verification_status, verified_at, verification_error
FROM   payment_provider_configs
WHERE  provider = 'stripe' AND is_active AND deleted_at IS NULL
  AND  verification_status <> 'failed'
  AND  ($1::uuid IS NULL OR org_id = $1)
ORDER  BY org_id, created_at, id`

// ListActiveStripePaymentConfigs lists active, not deleted, not failed Stripe
// configurations. A nil orgID lists every organization.
func (q *Queries) ListActiveStripePaymentConfigs(ctx context.Context, orgID *uuid.UUID) ([]PaymentProviderConfigRow, error) {
	rows, err := q.db.Query(ctx, listActiveStripePaymentConfigs, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentProviderConfigRow
	for rows.Next() {
		p, err := scanPaymentProviderConfigRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func queryDomainStrings(ctx context.Context, db DBTX, sql string, args ...any) ([]string, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
