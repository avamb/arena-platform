# Admin CRUD / Discovery — Gap Inventory (Feature #232)

Date: 2026-06-27
Author: Autoforge agent (feature #232)
Status: Discovery artifact (read-only audit). Drives downstream
implementation tasks for the Admin / SuperAdmin UI workstream.

## 0. Scope and method

This audit answers feature #232:

> Inventory existing admin endpoints and RBAC for orgs, memberships,
> venues, channels, payments. Audit existing backend endpoints, sqlc,
> migrations, and permissions for organizations, memberships, venues,
> channels, and payment configs. Produce a gap-list.

Sources inspected (HEAD on 2026-06-27):

- `apps/backend/internal/platform/httpserver/mount_v1.go`
  (router wire-up and `applyAuth` helper)
- `apps/backend/internal/platform/httpserver/mount_iam.go`
  (orgs, memberships, venues, channels)
- `apps/backend/internal/platform/httpserver/mount_admin.go`
  (reports, billing, Stripe billing, superadmin, impersonation,
  webhook subscribers)
- `apps/backend/internal/platform/httpserver/mount_commerce.go`
  (promo codes, payment intents)
- `apps/backend/internal/platform/httpserver/mount_catalog.go`
  (events / sessions / tiers — referenced for shape only)
- `apps/backend/internal/platform/httpserver/mount_networks.go`
  (operator networks)
- `apps/backend/internal/migrations/sql/0008_rbac.sql`
- `apps/backend/internal/migrations/sql/0009_organizations.sql`
- `apps/backend/internal/migrations/sql/0010_sales_channels.sql`
- `apps/backend/internal/migrations/sql/0011_memberships.sql`
- `apps/backend/internal/migrations/sql/0012_venues.sql`
- `apps/backend/internal/migrations/sql/0025_payment_intents.sql`
- `apps/backend/internal/migrations/sql/0034_superadmin.sql`
- `apps/backend/internal/migrations/sql/0042_network_operator_role.sql`
- `apps/backend/internal/migrations/sql/0044_network_permissions.sql`
- `apps/backend/internal/platform/permissions/checker.go` and
  `rbac_checker.go`

Cross-checked against `apps/backend/openapi/openapi.yaml` for OpenAPI
parity (see §5).

All RBAC wiring goes through the `Server.applyAuth` helper at
`apps/backend/internal/platform/httpserver/mount_v1.go:65`:

```go
func (s *Server) applyAuth(pr chi.Router, perm, scope string) {
    pr.Use(auth.Middleware(s.stub, auth.MiddlewareOptions{Logger: s.logger}))
    pr.Use(permissions.RequirePermission(s.perms, perm, scope))
}
```

The checker resolves a caller's roles → permissions through the
`RbacChecker` (`permissions/rbac_checker.go`).

---

## 1. Built-in roles inventory

Source: rows seeded by migrations 0008, 0034, 0042. All have
`org_id IS NULL` (global / built-in).

| Role                  | Seeded in     | Intent                                                                                                                                   |
|-----------------------|---------------|------------------------------------------------------------------------------------------------------------------------------------------|
| `admin`               | 0008          | Legacy "god-mode" role; broad seed grants *every* permission via `INSERT … FROM roles r, permissions p WHERE r.name='admin'` patterns.   |
| `geo_admin`           | 0008          | Geo reference data only (`geo.admin`).                                                                                                   |
| `scaffold_user`       | 0008          | Historical; scaffold echo endpoint. Currently has no live route (scaffold removed in 0031).                                              |
| `platform_superadmin` | 0034          | Cross-tenant read-only platform staff. Holds `superadmin.read` and the full `network.*` set.                                             |
| `network_operator`    | 0042          | External operator running a carrier organization across a network. Operational subset of `network.*` (no create/archive/manage_users).   |

### Roles **referenced in the task statement** but **NOT yet seeded as
distinct rows**

These names appear in `app_spec.txt` and architecture docs as the
canonical RBAC vocabulary but are **not** seeded in
`apps/backend/internal/migrations/sql/`. They are currently fulfilled
implicitly by the broad `admin` seed:

- `org_admin`  — organization-scoped administrator (per-org `roles.org_id`).
- `organizer`  — organization that owns events; runs catalog/inventory/checkout.
- `agent`      — organization that resells inventory belonging to organizers.
- `viewer` / `support` — read-only support persona.

**Gap G-R1 (Missing role rows).** No migration seeds `org_admin`,
`organizer`, `agent`, `viewer`, or `support`. Per-org `roles`
(non-null `org_id`) work in principle (column exists since 0008), but
no migration creates them. Today every "admin" caller in dev/tests is
mapped onto the global `admin` role.

---

## 2. Endpoint × permission matrix

### 2.1 Organizations  (`mount_iam.go::mountOrgRoutes`, file `orgs.go`)

| Method | Path                              | Permission   | Resource scope | Notes                       |
|--------|-----------------------------------|--------------|----------------|-----------------------------|
| GET    | `/v1/organizations`               | `org.read`   | organizations  | List, paginated.            |
| GET    | `/v1/organizations/{id}`          | `org.read`   | organizations  | Single org by UUID.         |
| POST   | `/v1/organizations`               | `org.create` | organizations  | Body: name, slug, etc.      |
| PATCH  | `/v1/organizations/{id}`          | `org.update` | organizations  | Partial update.             |
| DELETE | `/v1/organizations/{id}`          | `org.delete` | organizations  | Soft-delete.                |

Permissions seeded by 0009. All four bound to `admin`.

### 2.2 Memberships  (`mount_iam.go::mountMembershipRoutes`, file `memberships.go`)

| Method | Path                                                  | Permission           |
|--------|-------------------------------------------------------|----------------------|
| GET    | `/v1/organizations/{org_id}/members`                  | `membership.read`    |
| POST   | `/v1/organizations/{org_id}/members`                  | `membership.grant`   |
| DELETE | `/v1/organizations/{org_id}/members/{user_id}`        | `membership.revoke`  |

Permissions seeded by 0011. Bound to `admin`. The
`network_operator_203` test confirms `network_operator` may *read* its
own org's roster but cannot grant/revoke.

### 2.3 Venues  (`mount_iam.go::mountVenueRoutes`, file `venues.go`)

| Method | Path                                              | Permission     |
|--------|---------------------------------------------------|----------------|
| GET    | `/v1/venues`                                      | `venue.read`   |
| GET    | `/v1/venues/{id}`                                 | `venue.read`   |
| GET    | `/v1/organizations/{org_id}/venues`               | `venue.read`   |
| POST   | `/v1/organizations/{org_id}/venues`               | `venue.create` |
| PATCH  | `/v1/organizations/{org_id}/venues/{id}`          | `venue.update` |
| DELETE | `/v1/organizations/{org_id}/venues/{id}`          | `venue.delete` |

Permissions seeded by 0012. Bound to `admin`.

### 2.4 Sales channels  (`mount_iam.go::mountChannelRoutes`, file `channels.go`)

| Method | Path                                                   | Permission        |
|--------|--------------------------------------------------------|-------------------|
| GET    | `/v1/organizations/{org_id}/channels`                  | `channel.read`    |
| GET    | `/v1/organizations/{org_id}/channels/{id}`             | `channel.read`    |
| POST   | `/v1/organizations/{org_id}/channels`                  | `channel.create`  |
| PATCH  | `/v1/organizations/{org_id}/channels/{id}`             | `channel.update`  |
| DELETE | `/v1/organizations/{org_id}/channels/{id}`             | `channel.delete`  |

Permissions seeded by 0010. Bound to `admin`.

### 2.5 Payment intents and payment configuration

#### 2.5.1 Payment-intent CRUD (commerce surface — `mount_commerce.go`)

| Method | Path                                          | Permission                | Notes                                                                                  |
|--------|-----------------------------------------------|---------------------------|----------------------------------------------------------------------------------------|
| GET    | `/v1/payment-intents/{id}`                    | `payment_intent.read`     | Customer/admin read; ownership check inside handler.                                   |
| POST   | `/v1/payment-intents`                         | `payment_intent.create`   | Created for a checkout session.                                                        |
| POST   | `/v1/payment-intents/{id}/transition`         | `payment_intent.update`   | Adapter callbacks transition state machine.                                            |
| POST   | `/v1/payment-intents/webhook`                 | (none — public)           | Adapter webhook; verified by signature in handler. NOT under `applyAuth`.              |

Permissions seeded by 0025. Bound to `admin`.

#### 2.5.2 Platform billing (Stripe Billing for platform fees) — `mount_admin.go`

| Method | Path                                                       | Permission       |
|--------|------------------------------------------------------------|------------------|
| GET    | `/v1/billing/tariffs/active`                               | `billing.read`   |
| GET    | `/v1/billing/invoices/{id}`                                | `billing.read`   |
| GET    | `/v1/organizations/{org_id}/billing/usage`                 | `billing.read`   |
| GET    | `/v1/organizations/{org_id}/billing/invoices`              | `billing.read`   |
| POST   | `/v1/billing/tariffs`                                      | `billing.admin`  |
| POST   | `/v1/billing/invoices/generate`                            | `billing.admin`  |
| POST   | `/v1/billing/invoices/{id}/issue`                          | `billing.admin`  |
| POST   | `/v1/billing/invoices/{id}/pay`                            | `billing.admin`  |
| POST   | `/v1/billing/invoices/{id}/void`                           | `billing.admin`  |
| POST   | `/v1/billing/stripe/push-invoice/{id}`                     | `billing.admin`  |
| POST   | `/v1/billing/stripe/webhook`                               | (none — public)  |

Permissions seeded under feature #161 / #162 migrations and `billing`
domain. Bound to `admin`. There is **no** organization-scoped
`billing_admin` role today; all writes are global-admin only.

#### 2.5.3 Payment **provider configuration** per org/channel

**Gap G-PC1 (Missing endpoints).** There are no CRUD endpoints today
for managing per-organization or per-channel payment **provider
configuration** (e.g. attaching Stripe / AllPay credentials, choosing
the active acquirer, rotating webhook secrets). The Stripe and AllPay
adapters under `apps/backend/internal/adapters/stripe/` and
`adapters/allpay/` exist but are wired by environment / static config,
not by an admin-mutable row.

There is also **no** `payment_provider.read` / `payment_provider.write`
permission seeded.

### 2.6 Superadmin read-only surface  (`mount_admin.go::mountSuperadminRoutes`)

| Method | Path                          | Permission         |
|--------|-------------------------------|--------------------|
| GET    | `/v1/admin/organizations`     | `superadmin.read`  |
| GET    | `/v1/admin/orders`            | `superadmin.read`  |
| GET    | `/v1/admin/tickets`           | `superadmin.read`  |
| GET    | `/v1/admin/refunds`           | `superadmin.read`  |
| POST   | `/v1/admin/impersonate`       | `superadmin.read`  |

`superadmin.read` is seeded in 0034 and bound to both `admin` and
`platform_superadmin`.

### 2.7 Operator networks  (`mount_networks.go`)

Network CRUD and roster management are bound on top of the
`network.*` set (14 permissions, migration 0044). Already documented
in `09_autoforge/admin_ui/operator_network_design_note.md`; included
here only for completeness:

- `/v1/operator-networks{,/...}` — `network.read|create|update|archive`
- `/v1/admin/networks/{id}/users` — `network.manage_users`
- `/v1/admin/networks/{id}/organizers` — `network.manage_organizers`
- `/v1/admin/networks/{id}/agents` — `network.manage_agents`

### 2.8 Webhook subscribers  (`mount_admin.go::mountWebhookSubscriberRoutes`)

| Method | Path                                | Permission                  |
|--------|-------------------------------------|-----------------------------|
| GET    | `/v1/webhooks/subscribers`          | `webhook.subscriber.manage` |
| GET    | `/v1/webhooks/subscribers/{id}`     | `webhook.subscriber.manage` |
| POST   | `/v1/webhooks/subscribers`          | `webhook.subscriber.manage` |
| DELETE | `/v1/webhooks/subscribers/{id}`     | `webhook.subscriber.manage` |

---

## 3. sqlc / queries inventory

| Domain        | Queries file (relative to `apps/backend/internal/adapters/postgres/queries/`) |
|---------------|--------------------------------------------------------------------------------|
| Organizations | `organizations.sql`                                                            |
| Memberships   | `memberships.sql`                                                              |
| Venues        | `venues.sql`                                                                   |
| Channels      | `sales_channels.sql`                                                           |
| Payments      | `payment_intents.sql`                                                          |
| Billing       | `billing.sql`, `stripe_billing.sql`                                            |
| Superadmin    | `superadmin.sql`                                                               |
| Networks      | `operator_networks.sql`, `network_assignments.sql`                             |

Each file has a matching `*.sql.go` under
`apps/backend/internal/adapters/postgres/gen/`. The handlers in
`mount_iam.go` consume the `Queries` aggregate produced by sqlc.

No sqlc gaps were found for the five domains in scope: every endpoint
listed above maps to a generated method.

---

## 4. RBAC consistency observations

1. **Per-org roles are unseeded.** The `roles.org_id` column exists
   (0008) but no migration inserts org-scoped rows. Every membership
   today links a user to a *global* role. Until `org_admin`,
   `organizer`, `agent`, `viewer/support` are seeded, the per-org
   distinction collapses onto a single "is in org" boolean signal.
2. **`admin` role is broad-grant by default.** Every domain migration
   contains
   `INSERT INTO role_permissions SELECT … WHERE r.name = 'admin'`
   patterns that grant *all* of its permissions to `admin`. This
   keeps dev/CI green but means revoking a single capability requires
   either a new role or a future migration that deletes the broad
   grant — there is no negative grant primitive.
3. **No org-scoped `billing` role.** Per-org users cannot read their
   own invoices today unless they have global `billing.read`.
4. **No payment provider permission set.** As noted under §2.5.3.
5. **No `viewer` / `support` read-only role.** The closest is
   `platform_superadmin` (cross-tenant) and the operational subset of
   `network_operator` (within a network). A per-org support persona
   that can read tickets/orders/refunds without write access does not
   exist.
6. **Network-vs-platform terminology.** `platform_superadmin`,
   `platform_operator`, and `network_operator` are *distinct*; the
   audit confirms only `platform_superadmin` and `network_operator`
   have role rows. References to `platform_operator` in
   `0044_network_permissions.sql` are deliberate no-ops.

---

## 5. OpenAPI alignment

`apps/backend/openapi/openapi.yaml` documents the org / membership /
venue / channel / payment-intent / billing / superadmin endpoints
matching §2. No drift was found in this audit (each `mount_*.go`
handler ID matches the `operationId` in `openapi.yaml`). The known
oapi-codegen v2.4.1 warning about partial OpenAPI 3.1 support is
unrelated to this surface.

---

## 6. Gap list (consolidated)

| ID    | Severity | Title                                                                                       | Suggested follow-up                                                                                                                            |
|-------|----------|---------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------|
| G-R1  | High     | Per-org roles `org_admin`, `organizer`, `agent`, `viewer`, `support` are not seeded.        | Add a new migration that seeds these as `org_id IS NULL` defaults *and* defines the per-org templating rule. Update permission bindings.       |
| G-R2  | Med      | No org-scoped `billing` role; only global `billing.read` / `billing.admin` exist.           | Bind `billing.read` to `org_admin` once G-R1 lands; consider an `org_billing_admin` per-org role for write actions.                            |
| G-PC1 | High     | No CRUD endpoints, sqlc queries, migrations, or permissions for **payment provider config** per org/channel. | New domain: `payment_configs` table + sqlc, REST surface `/v1/organizations/{org_id}/payment-configs`, perms `payment_config.{read,write}`.   |
| G-R3  | Med      | Broad `admin` grant cannot be narrowed without further migrations.                          | Introduce a "least-privilege" role-binding strategy in future migrations; treat `admin` as legacy and deprecate over time.                     |
| G-R4  | Low      | `viewer` / `support` read-only persona does not exist anywhere in the role table.           | Seed `support` (or `viewer`) with the `*.read` subset and bind to the Admin UI's support views.                                                |
| G-D1  | Low      | `scaffold_user` role still seeded in 0008 although the scaffold endpoint was removed in 0031.| Optional cleanup migration to remove the dead role rows.                                                                                       |
| G-E1  | Low      | `/v1/payment-intents/webhook` and `/v1/billing/stripe/webhook` are intentionally public.    | Document this in OpenAPI security stanza so the Admin UI never tries to attach a bearer token.                                                 |

Severity legend:
- **High** — blocks the upcoming Admin UI permission model.
- **Med**  — degraded UX or weaker tenant isolation.
- **Low**  — cosmetic or documentation only.

---

## 7. Alignment with reference documents

- `08_architecture/03_platform_management_api_and_permissions_ru.md`
  enumerates the RBAC vocabulary used here (`org_admin`, `organizer`,
  `agent`, `viewer/support`). Confirming that #232's RBAC step is
  satisfied by listing roles in §1 and gaps in §4.
- `09_autoforge/admin_ui/role_network_model.md` covers the network
  role split; §1 cross-references it.
- `09_autoforge/admin_ui/contract_snapshot_216.md` pins the
  superadmin surface; §2.6 reuses its conclusions verbatim.

---

## 8. Recommended downstream features

1. **Seed per-org roles** — migration 0045 creates `org_admin`,
   `organizer`, `agent`, `support` and rebinds existing per-domain
   permissions. (Resolves G-R1, G-R4.)
2. **Payment provider configuration domain** — migration + sqlc +
   handler + OpenAPI + perms. (Resolves G-PC1.)
3. **`org_billing` permission set** — split `billing.read` into
   `billing.read.global` and `billing.read.own_org`. (Resolves G-R2.)
4. **Documentation pass** — flag webhook endpoints in OpenAPI
   `security: []` and remove obsolete `scaffold_user` row.
   (Resolves G-E1, G-D1.)

These are recorded as *recommendations only* — feature #232 is the
discovery artifact and does not itself implement the fixes.
