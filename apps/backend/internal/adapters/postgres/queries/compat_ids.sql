-- compat_ids.sql — sqlc query definitions for the compatibility_id_map
-- table introduced by migration 0090 (W1-A2a, feature #475).
--
-- These queries back internal/platform/compatids. They intentionally live
-- in the general gen/ package (like macs_system_ids.sql.go) so that the
-- bil24compat surface can share a single *gen.Queries with the rest of
-- the backend and remain transactional across mint + read.
--
-- Invariants (spec §3.1 and §4):
--   * kind ∈ {action, action_event, category_price, venue, city, country}
--   * arena-minted system_id always >= 1e9 (guaranteed by
--     compatibility_system_id_seq START WITH 1000000000)
--   * externally-registered system_id must be < 1e9 (enforced in Go by
--     compatids.RegisterExternal → compat.external_id_out_of_range)
--   * one platform_id ↔ one system_id per kind, forever
--
-- All queries are org-agnostic on purpose: the map is a platform-wide
-- identity registry (like tickets.system_ticket_id in 0088).

-- name: EnsureCompatibilityID :one
-- Lazily mints an arena-owned system_id for (kind, platform_id) if none
-- exists, and returns the row (freshly minted or pre-existing). The
-- INSERT ... ON CONFLICT DO NOTHING branch relies on the unique index on
-- (kind, platform_id) and is safe under concurrent callers.
--
-- Callers MUST run this inside a transaction or on the same connection as
-- the follow-up SELECT (compatids.Ensure does the follow-up SELECT when
-- ON CONFLICT swallows the INSERT).
INSERT INTO compatibility_id_map (kind, platform_id, system_id, source)
VALUES ($1, $2, nextval('compatibility_system_id_seq'), 'arena')
ON CONFLICT (kind, platform_id) DO NOTHING
RETURNING kind, system_id, platform_id, source, created_at;

-- name: GetCompatibilityIDByPlatformID :one
-- Reads an existing (kind, platform_id) row. Returns pgx.ErrNoRows when
-- absent. Used both by Resolve and by Ensure's fallback path.
SELECT kind, system_id, platform_id, source, created_at
FROM   compatibility_id_map
WHERE  kind = $1
  AND  platform_id = $2;

-- name: GetCompatibilityIDBySystemID :one
-- Reverse lookup: (kind, system_id) → platform_id. Returns pgx.ErrNoRows
-- when absent (which the gateway must translate to result code -3/101 per
-- spec §6).
SELECT kind, system_id, platform_id, source, created_at
FROM   compatibility_id_map
WHERE  kind = $1
  AND  system_id = $2;

-- name: RegisterExternalCompatibilityID :one
-- Records an externally-assigned (Bil24) system_id. Callers MUST reject
-- values >= 1e9 in Go before invoking this query — the shared sequence
-- lives above that boundary and a bil24-source row with system_id >= 1e9
-- would silently collide with a locally-minted arena id in the future.
--
-- On (kind, system_id) conflict returns pgx.ErrNoRows so the caller can
-- differentiate a real duplicate from an idempotent re-registration.
INSERT INTO compatibility_id_map (kind, platform_id, system_id, source)
VALUES ($1, $2, $3, 'bil24')
ON CONFLICT DO NOTHING
RETURNING kind, system_id, platform_id, source, created_at;
