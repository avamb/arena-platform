# Superadmin organization access

Platform superadmins do not need a synthetic `memberships` row to bootstrap or
administer an organization. The API derives the exception per request only
when the authenticated actor both has the `platform_superadmin` role and is
authorized for `superadmin.read` by RBAC.

For an organization-scoped request where the actor is not an active member,
send a non-empty `X-Admin-Reason` header. The request is audit logged as
`superadmin.organization_access`, including the organization ID and reason.
Missing or blank reasons are rejected with `400 superadmin.missing_reason`.

Do not insert a `platform_superadmin` membership as a deployment workaround.
Memberships represent an active organization assignment and are still required
for normal users; suspended and revoked memberships do not grant access.

## Where the `platform_superadmin` role comes from (feature #531)

`POST /v1/auth/login` and `/v1/auth/refresh` issue JWTs with an empty `roles`
claim (see `hauth/login.go`) — role information is never embedded in the
token. The bypass therefore cannot rely on the JWT claim: `markSuperadminOrgAccess`
(`httpserver/mount_v1.go`) resolves the actor's effective role set server-side,
the same way every other permission check on this server does. When the
wired `permissions.Checker` is the production `DBChecker`
(`permissions.SuperadminBypassChecker`), it unions the JWT roles with the
membership-derived roles fetched fresh from the database
(`GetActiveRolesForUser`, which unions active `memberships` rows with
NULL-org_id `user_roles` rows) before checking both `platform_superadmin`
membership and the `superadmin.read` permission. This costs at most one
membership DB round trip per request — the same one `Check` would already
pay — never a second, separate lookup. Checkers that don't implement the
interface (`AllowAllChecker`/`DenyAllChecker`, used by simpler test wiring)
fall back to the historical JWT-claim-only check. Service actors (API keys)
never receive the bypass regardless of path.

## Role vocabulary

`memberships.role` is intentionally a narrow, organization-assignment list:
`organizer`, `agent`, `platform_operator`, `external_ticketing_operator`,
`platform_superadmin`, and `network_operator`. `org_admin` is an RBAC role,
not a legal value for `memberships.role`; assign it through the global RBAC
role mechanism rather than inserting it into `memberships`.
