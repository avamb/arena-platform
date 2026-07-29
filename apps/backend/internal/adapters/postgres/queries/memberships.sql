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

-- name: ListAdminUsers :many
-- Superadmin directory projection. JSON aggregates avoid N+1 role/membership
-- lookups while deliberately excluding password and token columns.
SELECT u.id, u.display_number, u.email, u.created_at, u.email_verified_at,
       COALESCE((SELECT jsonb_agg(x.role ORDER BY x.role)
                 FROM (SELECT DISTINCT r.name AS role FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                       WHERE ur.user_id = u.id AND ur.org_id IS NULL) x), '[]'::jsonb) AS global_roles,
       COALESCE((SELECT jsonb_agg(jsonb_build_object('id', m.id, 'org_id', o.id, 'name', o.name, 'slug', o.slug, 'role', m.role)
                                  ORDER BY o.name, m.role)
                 FROM memberships m JOIN organizations o ON o.id = m.org_id
                 WHERE m.user_id = u.id AND m.status = 'active'), '[]'::jsonb) AS memberships
FROM users u
WHERE lower(u.email) LIKE '%' || lower($1) || '%'
ORDER BY u.created_at DESC, u.id DESC
LIMIT $2 OFFSET $3;

-- name: CountAdminUsers :one
SELECT count(*) FROM users WHERE lower(email) LIKE '%' || lower($1) || '%';

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
-- Returns the distinct set of role names held by a user. Includes active
-- organization memberships plus global user_roles assignments (org_id IS
-- NULL). Scoped user_roles are intentionally excluded because the current
-- permission checker has no per-resource user_roles scope enforcement.
SELECT DISTINCT role
FROM (
    SELECT m.role
    FROM   memberships m
    WHERE  m.user_id = $1
      AND  m.status  = 'active'

    UNION

    SELECT r.name AS role
    FROM   user_roles ur
    JOIN   roles r ON r.id = ur.role_id
    WHERE  ur.user_id = $1
      AND  ur.org_id IS NULL
) effective_roles
ORDER BY role;

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

-- name: GetActiveRolesForUserInOrg :many
-- Returns distinct roles for a user scoped to a specific organization.
-- Includes active membership roles for the given org AND global user_roles
-- (org_id IS NULL — platform-wide roles that apply regardless of org context).
-- Used by the org-scoped permission enforcement layer (PR2-01).
SELECT DISTINCT role
FROM (
    SELECT m.role
    FROM   memberships m
    WHERE  m.user_id = $1
      AND  m.org_id  = $2
      AND  m.status  = 'active'

    UNION

    SELECT r.name AS role
    FROM   user_roles ur
    JOIN   roles r ON r.id = ur.role_id
    WHERE  ur.user_id = $1
      AND  ur.org_id IS NULL
) effective_roles
ORDER BY role;
