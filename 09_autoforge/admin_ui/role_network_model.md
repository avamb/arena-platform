# Role and network model update

## Why this layer exists

The platform needs one more business layer between `platform_superadmin` and ordinary organizers/agents.

Bil24/Arena can have an operator that manages its own internal network. This network contains a specific set of organizers and agents. The platform SuperAdmin creates the network, assigns the operator, and binds organizers and agents to that operator's network.

This is not the same role as `platform_operator`.

## Terms

### `platform_superadmin`

Top-level platform owner/admin.

Can:

- create operator networks;
- assign network operators;
- attach/detach organizers and agents to/from a network;
- see all tenants, networks, organizers, agents, events, orders, tickets, refunds, billing, audit, and observability;
- perform controlled cross-tenant support actions with audit reason and step-up where required.

### `platform_operator`

Internal platform operations/support staff.

Can:

- moderate and support platform objects according to permissions;
- work across the platform only where explicitly granted;
- handle support, verification, reconciliation, and operational exceptions.

Must not be confused with a business operator that owns a managed network.

### `operator_network`

A business network created by `platform_superadmin`.

Contains:

- one or more network operator users;
- assigned organizer organizations;
- assigned agents/sales partners;
- network-level sales channels/frontends/integrations where needed;
- scoped reporting and support surface.

The data model should allow future many-to-many membership if business rules require it, but the first UI can default to one primary network per organizer/agent.

### `network_operator`

A user role scoped to one or more `operator_network` records.

Can manage only the organizers, agents, events, orders, and operational data inside assigned networks.

Can:

- view network dashboard;
- manage assigned organizers and agents;
- invite or manage users within the network if granted;
- view network events/sessions and sales;
- perform support actions for network orders/tickets/refunds;
- configure network-level frontends/channels where granted.

Cannot:

- see unrelated organizers or agents;
- manage platform-wide roles;
- access platform-wide observability by default;
- change global credentials, feature flags, or system settings.

### `organizer`

Owns or manages its own events, sessions, venues, ticket inventory, quotas, media, reports, complimentary tickets, and support cases.

If attached to an `operator_network`, the organizer is visible to that network's operator according to scoped permissions.

### `agent`

Sells tickets through allowed sales surfaces and sees only assigned catalog, reservations, orders, refunds, and reports.

If attached to an `operator_network`, the agent is visible to that network's operator according to scoped permissions.

### `external_ticketing_operator`

Manages external ticketing allocations, barcode import, quota synchronization, reconciliation, and integration-specific operations.

This role can be platform-scoped, network-scoped, or organization-scoped depending on the integration contract.

## Suggested permission groups

Use permissions, not hardcoded roles, as the source of truth.

Network-level permissions:

- `network.read`
- `network.create`
- `network.update`
- `network.archive`
- `network.manage_users`
- `network.manage_organizers`
- `network.manage_agents`
- `network.manage_channels`
- `network.view_sales`
- `network.support_orders`
- `network.support_tickets`
- `network.support_refunds`
- `network.view_reports`
- `network.view_audit`

Platform-level permissions:

- `platform.superadmin.read_all`
- `platform.superadmin.write_all`
- `platform.superadmin.manage_networks`
- `platform.superadmin.manage_roles`
- `platform.superadmin.manage_credentials`
- `platform.superadmin.view_logs`
- `platform.superadmin.view_sensitive_logs`
- `platform.superadmin.impersonate_readonly`
- `platform.superadmin.break_glass`

Organization-level permissions remain separate from network permissions.

## Suggested data model additions

The exact schema should be designed by the implementation agent after inspecting current migrations and RBAC tables.

Minimum entities:

- `operator_networks`
  - `id`
  - `name`
  - `status`
  - `created_by_user_id`
  - `created_at`
  - `updated_at`

- `operator_network_users`
  - `network_id`
  - `user_id`
  - `role`
  - `status`

- `operator_network_organizations`
  - `network_id`
  - `organization_id`
  - `relationship_type`
  - `status`

- `operator_network_agents`
  - `network_id`
  - `agent_id` or `organization_id`
  - `relationship_type`
  - `status`

If agents are represented as organizations in the current domain model, prefer a single network-to-organization join with organization type metadata instead of a parallel agent table.

## Admin UI implications

The admin UI needs an explicit scope model:

- platform scope;
- network scope;
- organization scope;
- agent/channel scope where applicable.

Every route should know:

- current actor;
- active scope;
- effective permissions;
- visible navigation items;
- allowed actions;
- audit context.

SuperAdmin must be able to switch into a network context. Network operators must start directly inside their assigned network context and must not see global platform data.

