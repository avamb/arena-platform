-- name: GetOrganizationArchived :one
-- Whether an organization was archived by a platform superadmin
-- (POST /v1/admin/organizations/{org_id}/archive sets deleted_at). One
-- primary-key lookup, read by the org-scoped route chain before a member or
-- an organization API key is served. pgx.ErrNoRows = no such organization.
SELECT deleted_at IS NOT NULL AS archived
FROM   organizations
WHERE  id = $1;
