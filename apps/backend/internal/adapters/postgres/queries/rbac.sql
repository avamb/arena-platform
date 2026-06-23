-- RBAC queries — feature #117
-- Provides the DB-level read operations for the permission engine.

-- name: HasPermissionForRoles :one
-- Returns true when at least one of the supplied role names holds the
-- named permission. Designed for the hot-path permission check in DBChecker.
SELECT EXISTS(
    SELECT 1
    FROM   role_permissions rp
    JOIN   roles       r ON r.id = rp.role_id
    JOIN   permissions p ON p.id = rp.permission_id
    WHERE  r.name = ANY($1::text[])
      AND  p.name = $2
) AS has_permission;

-- name: GetPermissionsForRoles :many
-- Returns the distinct set of permission names held by any of the supplied
-- role names. Used by DBChecker to load the full permission set for a role
-- combination and cache it in memory.
SELECT   p.name
FROM     permissions p
JOIN     role_permissions rp ON rp.permission_id = p.id
JOIN     roles r              ON r.id = rp.role_id
WHERE    r.name = ANY($1::text[])
GROUP BY p.name
ORDER BY p.name;
