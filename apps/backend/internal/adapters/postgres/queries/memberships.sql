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
