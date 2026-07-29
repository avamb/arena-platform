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

## Role vocabulary

`memberships.role` is intentionally a narrow, organization-assignment list:
`organizer`, `agent`, `platform_operator`, `external_ticketing_operator`,
`platform_superadmin`, and `network_operator`. `org_admin` is an RBAC role,
not a legal value for `memberships.role`; assign it through the global RBAC
role mechanism rather than inserting it into `memberships`.
