-- memberships.sql — SQL queries for the memberships table (feature #120).
--
-- Memberships bind a user to an organization with a named role.
-- These queries are used by:
--   - POST /v1/organizations/{id}/members  → InsertMembership
--   - DELETE /v1/organizations/{id}/members/{user_id} → RevokeMembership
--   - GET /v1/organizations/{id}/members   → ListMembershipsByOrg
--
-- Permission resolution queries:
--   - GetActiveRolesForUser → called by permissions.DBChecker to union
--     membership-derived roles with JWT roles during permission checks.

-- name: InsertMembership :one
-- Inserts a new membership (user in org with role) and returns the created row.
-- Callers must handle the unique constraint violation (23505) when the user
-- already holds the same role in the same org.
INSERT INTO memberships (user_id, org_id, role)
VALUES ($1, $2, $3)
RETURNING id, user_id, org_id, role, status, joined_at;

-- name: RevokeMembership :one
-- Hard-deletes a membership row (role is fully removed from the user in the org).
-- Returns the deleted row so the handler can confirm what was removed.
-- Returns pgx.ErrNoRows when no matching active membership exists.
DELETE FROM memberships
WHERE  user_id = $1
  AND  org_id  = $2
  AND  role    = $3
  AND  status  = 'active'
RETURNING id, user_id, org_id, role, status, joined_at;

-- name: ListMembershipsByOrg :many
-- Returns all active memberships for an organization, ordered by joined_at ASC.
SELECT id, user_id, org_id, role, status, joined_at
FROM   memberships
WHERE  org_id = $1
  AND  status = 'active'
ORDER  BY joined_at ASC, id ASC;

-- name: GetActiveRolesForUser :many
-- Returns the distinct set of role names held by a user across ALL organizations
-- (active memberships only). Used by permissions.DBChecker to union JWT roles
-- with membership-derived roles during permission resolution.
SELECT DISTINCT role
FROM   memberships
WHERE  user_id = $1
  AND  status  = 'active'
ORDER  BY role;

-- name: ListMembershipsByUser :many
-- Returns all active memberships for a user across every organization they
-- belong to. Used by the GET /v1/me current-user context endpoint (feature #211)
-- so the response can enumerate organization_memberships and derive
-- organization-scoped entries in available_scopes.
SELECT id, user_id, org_id, role, status, joined_at
FROM   memberships
WHERE  user_id = $1
  AND  status  = 'active'
ORDER  BY joined_at ASC, id ASC;

-- name: GetMembershipByID :one
-- Looks up a single membership by its UUIDv7 primary key, scoped to the
-- supplied org_id so admin handlers cannot operate on a membership belonging
-- to a different organization than the one in the URL path. Used by the
-- /v1/admin/organizations/{org_id}/members/{membership_id} PATCH and DELETE
-- handlers (feature #234) for pre-flight resolution and 404 detection.
SELECT id, user_id, org_id, role, status, joined_at
FROM   memberships
WHERE  id     = $1
  AND  org_id = $2;

-- name: ChangeMembershipRole :one
-- Replaces the role of an existing membership identified by (id, org_id).
-- The new role must satisfy the memberships_role_check CHECK constraint
-- (validated at the API layer too). Only operates on rows whose status is
-- 'active'. Returns the updated row, or pgx.ErrNoRows if no matching active
-- membership exists. Hits a unique-constraint violation (23505) if the same
-- user already holds the target role in this organization.
UPDATE memberships
SET    role = $3
WHERE  id     = $1
  AND  org_id = $2
  AND  status = 'active'
RETURNING id, user_id, org_id, role, status, joined_at;

-- name: DeactivateMembership :one
-- Soft-removes a membership by setting status='revoked'. The row is preserved
-- so historic audit / reporting queries can still resolve it. Returns the
-- updated row, or pgx.ErrNoRows when no matching active membership exists.
-- Used by DELETE /v1/admin/organizations/{org_id}/members/{membership_id}
-- (feature #234).
UPDATE memberships
SET    status = 'revoked'
WHERE  id     = $1
  AND  org_id = $2
  AND  status = 'active'
RETURNING id, user_id, org_id, role, status, joined_at;
