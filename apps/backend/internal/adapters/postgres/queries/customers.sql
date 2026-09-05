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
