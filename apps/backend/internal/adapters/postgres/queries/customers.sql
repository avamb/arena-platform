-- customers.sql — sqlc query definitions for the customer aggregate
-- (customers, customer_identities, customer_org_links) introduced by
-- migration 0091 (W1-A4a, feature #479).
--
-- Model overview (spec §3.2 / §12.1): the platform holds ONE global
-- customer entity; identities and per-org rollups attach to it. Strong
-- identity keys (email/phone/telegram) are unique platform-wide; weak
-- identity keys (device/wc_customer/bil24_user) are unique per sales
-- channel. Consents, attributes and merge candidates get their own query
-- files as they land in later sub-features of the W1-A wave.

-- name: InsertCustomer :one
-- Creates a new customer row. system_id defaults to the next value of
-- compatibility_system_id_seq (>= 1e9) so Bil24 wire responses can surface
-- the id as userId without colliding with externally-registered ids.
INSERT INTO customers (display_name, locale)
VALUES ($1, $2)
RETURNING id, system_id, display_name, locale, merged_into, anonymized_at, created_at, updated_at;

-- name: GetCustomerByID :one
-- Loads a customer by platform uuid. Returns pgx.ErrNoRows when absent.
SELECT id, system_id, display_name, locale, merged_into, anonymized_at, created_at, updated_at
FROM   customers
WHERE  id = $1;

-- name: GetCustomerBySystemID :one
-- Loads a customer by the bigint system_id exposed to Bil24 clients.
-- Returns pgx.ErrNoRows when absent (translated by callers to gateway
-- result codes -3 / 101 per spec §6).
SELECT id, system_id, display_name, locale, merged_into, anonymized_at, created_at, updated_at
FROM   customers
WHERE  system_id = $1;

-- name: UpdateCustomerProfile :exec
-- Refreshes display_name / locale (nullable) and bumps updated_at. Used
-- by the resolver once a live gateway session provides fresh values.
UPDATE customers
SET    display_name = $2,
       locale      = $3,
       updated_at  = now()
WHERE  id = $1;

-- name: InsertCustomerIdentity :one
-- Attaches an identity to a customer. Uniqueness is enforced by the
-- partial indexes customer_identities_strong_uq and
-- customer_identities_weak_uq — callers should detect 23505 conflicts and
-- fall back to Get*Identity* to resolve the winning row.
INSERT INTO customer_identities
    (customer_id, kind, value_normalized, channel_id, verified_at, source)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, customer_id, kind, value_normalized, channel_id, verified_at,
          first_seen_at, last_seen_at, source;

-- name: GetCustomerIdentityByStrongKey :one
-- Resolves a strong identity (email / phone / telegram) globally.
-- Callers MUST pass a kind from the strong set — the query does not
-- constrain kind so it can double as a "look up any identity by value"
-- probe when the caller already knows the kind.
SELECT id, customer_id, kind, value_normalized, channel_id, verified_at,
       first_seen_at, last_seen_at, source
FROM   customer_identities
WHERE  kind = $1
  AND  value_normalized = $2;

-- name: GetCustomerIdentityByWeakKey :one
-- Resolves a weak identity scoped to a sales channel (device / wc_customer
-- / bil24_user). Returns pgx.ErrNoRows when absent.
SELECT id, customer_id, kind, value_normalized, channel_id, verified_at,
       first_seen_at, last_seen_at, source
FROM   customer_identities
WHERE  kind = $1
  AND  value_normalized = $2
  AND  channel_id = $3;

-- name: ListCustomerIdentities :many
-- Enumerates every identity attached to a customer, most recently seen
-- first. Used by the admin card and by the merge resolver.
SELECT id, customer_id, kind, value_normalized, channel_id, verified_at,
       first_seen_at, last_seen_at, source
FROM   customer_identities
WHERE  customer_id = $1
ORDER  BY last_seen_at DESC, id;

-- name: TouchCustomerIdentityLastSeen :exec
-- Bumps last_seen_at on an identity row after a successful gateway
-- resolution. Cheap enough to run on every resolver hit.
UPDATE customer_identities
SET    last_seen_at = now()
WHERE  id = $1;

-- name: UpsertCustomerOrgLink :exec
-- Ensures a (customer_id, org_id) rollup row exists. Counter maintenance
-- (orders_count, tickets_count, first_order_at, last_order_at) belongs to
-- the orders write path (§3.3 / §12.1) and is not touched here.
INSERT INTO customer_org_links (customer_id, org_id, source)
VALUES ($1, $2, $3)
ON CONFLICT (customer_id, org_id) DO NOTHING;

-- name: GetCustomerOrgLink :one
-- Reads the rollup row for a (customer_id, org_id) pair. Returns
-- pgx.ErrNoRows when the customer has never been linked to the org.
SELECT customer_id, org_id, first_order_at, last_order_at,
       orders_count, tickets_count, source, attributes
FROM   customer_org_links
WHERE  customer_id = $1
  AND  org_id = $2;

-- ─── W1-A4b (feature #480): resolver extras ─────────────────────────────────
-- The four queries below back the platform/customers.Resolve helper. They
-- are hand-added rather than sqlc-generated for symmetry with the other
-- wrappers in gen/customers.sql.go.

-- name: MarkCustomerIdentityVerified :exec
-- Promotes an identity to verified. verified_at is only set when currently
-- NULL (idempotent — the first verifier wins). last_seen_at is bumped so
-- verification counts as a fresh touch.
UPDATE customer_identities
SET    verified_at  = COALESCE(verified_at, $2),
       last_seen_at = $2
WHERE  id = $1;

-- name: UpdateCustomerDisplayName :exec
-- Overwrites display_name only. Locale is untouched. Callers implement the
-- §12.2 rules (never overwrite non-empty with empty).
UPDATE customers
SET    display_name = $2,
       updated_at   = now()
WHERE  id = $1;

-- name: InsertCustomerMergeCandidate :one
-- Queues a suspected duplicate for operator review. Strong-key conflicts
-- (spec §12.2, ADR-036) are NEVER auto-merged; the gateway keeps both
-- customers and emits this row instead.
INSERT INTO customer_merge_candidates (customer_a, customer_b, reason)
VALUES ($1, $2, $3)
RETURNING id, customer_a, customer_b, reason, created_at, resolved_at, resolution;

-- name: InsertCustomerAttribute :exec
-- Writes a customer attribute (platform-scoped when org_id IS NULL). The
-- resolver uses this path to stash an invalid raw phone number so the data
-- is not lost even though it cannot become an identity (spec §3.2).
INSERT INTO customer_attributes (customer_id, org_id, key, value, source)
VALUES ($1, $2, $3, $4::jsonb, $5)
ON CONFLICT (customer_id, org_id, key) DO UPDATE
SET value = EXCLUDED.value, source = EXCLUDED.source;

-- ─── W1-A4d (feature #482): org-scoped read endpoints ───────────────────────
-- Backs GET /v1/organizations/{org_id}/customers?q= and the customer card
-- (spec §12.3). None of these existed before this feature.

-- name: SearchCustomersByOrg :many
-- Org-scoped customer search: only customers with a customer_org_links row
-- for this org are visible. Matches an exact normalized email/phone
-- (customer_identities.value_normalized) OR an ILIKE substring against
-- display_name. Pass '' for q to list every customer linked to the org.
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
LIMIT  $3 OFFSET $4;

-- name: ListCustomerAttributesForOrg :many
-- Lists a customer's attributes visible from one org: platform-scoped rows
-- (org_id IS NULL) plus this org's own rows (spec §12.3 card: "org +
-- platform attributes").
SELECT id, customer_id, org_id, key, value, source, imported_at, created_at
FROM   customer_attributes
WHERE  customer_id = $1
  AND  (org_id IS NULL OR org_id = $2)
ORDER  BY created_at DESC, id;

-- name: ListCustomerConsentsForOrg :many
-- Lists a customer's consent records within one org (spec §12.3 card: "org
-- consents"). First use of customer_consents in the codebase.
SELECT customer_id, org_id, kind, given_at, withdrawn_at, source
FROM   customer_consents
WHERE  customer_id = $1
  AND  org_id = $2
ORDER  BY kind;
