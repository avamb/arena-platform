# Admin UI planning artifacts

This folder contains structured planning inputs for the future unified admin UI.

The goal is to prevent the implementation agent from recreating the legacy Bil24 split into four separate Java applications. The target product is one modern role-aware web backoffice where navigation, data scope, and actions are filtered by permissions.

## Files

- `role_network_model.md` - role model update with the additional internal operator network layer.
- `legacy_admin_reference_map.yaml` - structured mapping from legacy Manager / Editor / Reporter / Cassa screens to modern unified modules.
- `autoforge_admin_task_statement.md` - updated AutoForge task statement for the unified admin platform.

## Core rule

Do not build separate admin applications for SuperAdmin, Operator, Organizer, Agent, and POS. Build one admin shell with:

- permission-driven routing;
- scoped data access;
- role-specific navigation presets;
- audited cross-tenant and cross-network actions;
- contextual UI hints and disabled-action explanations.

