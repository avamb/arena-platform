-- channels.sql — sqlc query definitions for the sales_channels table
-- (features #121 and #236). All queries filter WHERE deleted_at IS NULL
-- to respect the soft-delete policy.

-- name: InsertSalesChannel :one
INSERT INTO sales_channels (org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings)
VALUES ($1, $2, $3, $4, $5, $6, $7, COALESCE($8::jsonb, '{}'::jsonb))
RETURNING id, display_number, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at;

-- name: GetSalesChannelByID :one
SELECT id, display_number, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at
FROM   sales_channels
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL;

-- name: GetSalesChannelByDisplayNumber :one
-- Bil24 gateway (feature #471, W1-A1b): resolve the WordPress-side `fid`
-- credential — which the plugins cast to int and cannot carry as a UUID —
-- to the sales_channels row. display_number is a per-tenant human-facing
-- identifier assigned in migration 0072.
SELECT id, display_number, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at
FROM   sales_channels
WHERE  display_number = $1
  AND  deleted_at IS NULL;

-- name: ListSalesChannelsByOrg :many
SELECT id, display_number, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at
FROM   sales_channels
WHERE  org_id = $1
  AND  deleted_at IS NULL
ORDER  BY created_at ASC, id ASC;

-- name: UpdateSalesChannel :one
-- reservation_ttl_override is guarded by the explicit $9 boolean flag rather
-- than overloaded on NULL: unlike every other column here, NULL is a valid,
-- meaningful value for this one (org-level default), so it cannot double as
-- "caller didn't ask to change this". Pass set_reservation_ttl_override=false
-- (with $8 = NULL) to leave the stored value untouched; pass true with $8 =
-- NULL to explicitly clear it, or true with $8 = <n> to set it. Found live:
-- every partial update that passed nil (e.g. the gateway-credential PUT)
-- silently wiped a configured hold TTL back to the 20-minute default.
UPDATE sales_channels
SET    name                     = COALESCE(NULLIF($3, ''), name),
       payment_mode             = COALESCE(NULLIF($4, ''), payment_mode),
       provider                 = COALESCE(NULLIF($5, ''), provider),
       provider_account_id      = CASE WHEN $6::text IS NOT NULL THEN $6::text ELSE provider_account_id END,
       fee_percent              = CASE WHEN $7::numeric IS NOT NULL THEN $7::numeric ELSE fee_percent END,
       reservation_ttl_override = CASE WHEN $9::boolean THEN $8 ELSE reservation_ttl_override END,
       settings                 = CASE WHEN $10::jsonb IS NOT NULL THEN $10::jsonb ELSE settings END,
       updated_at               = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at;

-- name: SoftDeleteSalesChannel :one
UPDATE sales_channels
SET    deleted_at = now(),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
RETURNING id, display_number, org_id, name, payment_mode, provider, provider_account_id, fee_percent, reservation_ttl_override, settings, created_at, updated_at, deleted_at;

-- name: EnableChannelHostedPage :execrows
-- Turns on settings.hosted_page.enabled for a channel that publishes through
-- the event center (a human naming channelIds on the event-bundle). Only a
-- channel WITHOUT a gateway credential is touched: a WordPress site's channel
-- (settings.gateway.token_hash, or the legacy gateway_token_hash) keeps
-- whatever the operator decided, because its page is the site itself. An
-- already-enabled channel is left alone (0 rows), so a repeated import is a
-- no-op.
UPDATE sales_channels
SET    settings   = jsonb_set(COALESCE(settings, '{}'::jsonb), '{hosted_page}',
                              COALESCE(settings -> 'hosted_page', '{}'::jsonb) || '{"enabled": true}'::jsonb, true),
       updated_at = now()
WHERE  id = $1
  AND  org_id = $2
  AND  deleted_at IS NULL
  AND  COALESCE(settings #>> '{hosted_page,enabled}', '') <> 'true'
  AND  settings #>> '{gateway,token_hash}' IS NULL
  AND  settings ->> 'gateway_token_hash' IS NULL;
