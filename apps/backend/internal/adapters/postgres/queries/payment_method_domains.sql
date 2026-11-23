-- payment_method_domains.sql — wallet domains of a Stripe configuration
-- (migration 0218, spec 65 section 5). The Go wrappers are hand-maintained in
-- gen/payment_method_domains.sql.go.

-- name: ListPaymentMethodDomains :many
SELECT id, payment_config_id, org_id, domain, source, provider_domain_id, enabled,
       apple_pay_status, google_pay_status, link_status, status_detail, last_error,
       checked_at, created_at, updated_at
FROM   payment_method_domains
WHERE  payment_config_id = $1 AND org_id = $2
ORDER  BY source ASC, domain ASC;

-- name: UpsertPaymentMethodDomain :exec
INSERT INTO payment_method_domains (payment_config_id, org_id, domain, source, provider_domain_id, enabled,
    apple_pay_status, google_pay_status, link_status, status_detail, last_error, checked_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULL, now())
ON CONFLICT (payment_config_id, domain) DO UPDATE SET ...;

-- name: SetPaymentMethodDomainError :exec
INSERT ... ON CONFLICT ... SET last_error = $5;

-- name: DeleteStalePaymentMethodDomains :execrows
DELETE FROM payment_method_domains
WHERE payment_config_id = $1 AND source = 'site' AND NOT (domain = ANY($2::text[]));

-- name: ListSiteDomainsByChannel :many
SELECT domain FROM sales_channel_site_domains WHERE channel_id = $1 AND org_id = $2 ORDER BY domain;

-- name: ReplaceSalesChannelSiteDomains :exec
-- delete the channel's rows, insert the new set, in one transaction.

-- name: ListDesiredSiteDomainsForOrgStripe :many
SELECT DISTINCT d.domain
FROM   sales_channel_site_domains d
JOIN   sales_channels c ON c.id = d.channel_id
WHERE  d.org_id = $1 AND c.deleted_at IS NULL AND c.provider = 'stripe'
ORDER  BY d.domain;

-- name: ListActiveStripePaymentConfigs :many
SELECT <payment_provider_configs columns>
FROM   payment_provider_configs
WHERE  provider = 'stripe' AND is_active AND deleted_at IS NULL
  AND  verification_status <> 'failed'
  AND  ($1::uuid IS NULL OR org_id = $1)
ORDER  BY org_id, created_at, id;
