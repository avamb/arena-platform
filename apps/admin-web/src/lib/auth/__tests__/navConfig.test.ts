/**
 * SAUI-03 -- permission-driven nav and route-guard logic.
 *
 * These tests drive the nav filter and route-guard predicates with
 * realistic /v1/me fixtures for each role from the backend's 0044
 * permission matrix:
 *
 *   - platform_superadmin -- holds every permission
 *   - platform_operator    -- holds no network permissions, no superadmin.read
 *   - network_operator     -- holds the 11-permission network subset
 *   - "no-permission"      -- authenticated but empty permission set
 *
 * The fixtures shape mirrors components["schemas"]["MeResponse"]; we
 * only need the `permissions` and `available_scopes` subfields for the
 * predicates under test, so the fixtures construct just those plus the
 * minimal identity for clarity.
 */
import { describe, expect, it } from "vitest";
import {
  NAV_ENTRIES,
  describeRule,
  navEntryForPath,
  permissionRuleSatisfied,
  scopeRuleSatisfied,
  visibleNavEntries,
  type NavEntry,
  type ScopeKind,
} from "@/lib/auth/navConfig";

// ---- Fixtures -------------------------------------------------------------

const NETWORK_PERMS_FULL = [
  "network.read",
  "network.create",
  "network.update",
  "network.archive",
  "network.manage_users",
  "network.manage_organizers",
  "network.manage_agents",
  "network.manage_channels",
  "network.view_sales",
  "network.support_orders",
  "network.support_tickets",
  "network.support_refunds",
  "network.view_reports",
  "network.view_audit",
];
const NETWORK_PERMS_OPERATOR = NETWORK_PERMS_FULL.filter(
  (p) => p !== "network.create" && p !== "network.archive" && p !== "network.manage_users",
);

function fixture(permissions: readonly string[], scopes: readonly string[]) {
  return {
    permissions: new Set(permissions),
    scopes,
  };
}

const platformSuperadmin = fixture(
  [
    ...NETWORK_PERMS_FULL,
    "superadmin.read",
    "membership.grant",
    "geo.admin",
    // Feature #294 — S-3 added the /webhooks SuperAdmin route, which
    // requires `webhook.subscriber.manage` (matches the backend RBAC
    // seed in 0040_webhook_subscribers.sql).
    "webhook.subscriber.manage",
  ],
  ["global", "network:0193f01a-0001-7000-8000-000000000001"],
);

const platformOperator = fixture(["geo.admin"], ["platform"]);

const networkOperator = fixture(NETWORK_PERMS_OPERATOR, [
  "network:0193f01a-0001-7000-8000-000000000002",
  "network:0193f01a-0001-7000-8000-000000000003",
]);

const noPermission = fixture([], []);

// ---- permissionRuleSatisfied ---------------------------------------------

describe("permissionRuleSatisfied", () => {
  it("always-rule is satisfied for every caller", () => {
    expect(permissionRuleSatisfied("always", new Set())).toBe(true);
    expect(permissionRuleSatisfied("always", platformSuperadmin.permissions)).toBe(true);
  });

  it("anyOf-rule is satisfied when at least one permission is present", () => {
    const rule = { anyOf: ["network.read", "network.create"] } as const;
    expect(permissionRuleSatisfied(rule, networkOperator.permissions)).toBe(true);
    expect(permissionRuleSatisfied(rule, platformOperator.permissions)).toBe(false);
  });

  it("anyOf-rule is NOT satisfied with an empty permission set", () => {
    const rule = { anyOf: ["network.read"] } as const;
    expect(permissionRuleSatisfied(rule, noPermission.permissions)).toBe(false);
  });

  it("allOf-rule requires every listed permission", () => {
    const rule = { allOf: ["network.read", "network.create"] } as const;
    expect(permissionRuleSatisfied(rule, platformSuperadmin.permissions)).toBe(true);
    // network_operator lacks .create
    expect(permissionRuleSatisfied(rule, networkOperator.permissions)).toBe(false);
  });
});

// ---- scopeRuleSatisfied ---------------------------------------------------

describe("scopeRuleSatisfied", () => {
  const networksEntry = navEntryForPath("/networks") as NavEntry;
  const workspaceEntry = navEntryForPath("/") as NavEntry;

  it("entries with no scope filter are scope-agnostic", () => {
    expect(scopeRuleSatisfied(workspaceEntry, null)).toBe(true);
    expect(scopeRuleSatisfied(workspaceEntry, "network")).toBe(true);
  });

  it("network-bearing entries hide under organization scope", () => {
    expect(scopeRuleSatisfied(networksEntry, "organization")).toBe(false);
  });

  it("network-bearing entries show under network scope", () => {
    expect(scopeRuleSatisfied(networksEntry, "network")).toBe(true);
  });

  it("falls back to global/platform allow when no scope is active", () => {
    // Networks entry includes "global" and "platform" in scopeKinds, so
    // null-scope (bootstrap) is permitted.
    expect(scopeRuleSatisfied(networksEntry, null)).toBe(true);
  });
});

// ---- visibleNavEntries (the integration the sidebar actually performs) ---

function ids(entries: readonly NavEntry[]): string[] {
  return entries.map((e) => e.id);
}

describe("visibleNavEntries -- /v1/me role fixtures", () => {
  it("platform_superadmin: sees every nav entry under global scope", () => {
    const out = visibleNavEntries(
      NAV_ENTRIES,
      platformSuperadmin.permissions,
      "global",
    );
    expect(ids(out)).toEqual([
      "workspace",
      "networks",
      "users",
      "organizations",
      "events_sessions",
      "venues",
      "orders",
      "tickets",
      "refunds",
      "channels",
      "payments",
      "reports",
      "notifications_content",
      "pos",
      "audit",
      "observability",
      "geo",
      "webhooks",
    ]);
  });

  it("superadmin.read alone shows the Users entry in the SuperAdmin sidebar", () => {
    const out = visibleNavEntries(
      NAV_ENTRIES,
      new Set(["superadmin.read"]),
      "global",
    );
    expect(ids(out)).toContain("users");
  });

  it("platform_operator: only workspace + geo (no superadmin.read, no network.*)", () => {
    const out = visibleNavEntries(
      NAV_ENTRIES,
      platformOperator.permissions,
      "platform",
    );
    // platform_operator fixture only holds geo.admin. None of the
    // SAUI-12 placeholders accept a bare geo.admin operator -- each
    // requires either superadmin.read or a scoped network/event/payment
    // permission. So the visible sidebar collapses to workspace + geo.
    expect(ids(out)).toEqual(["workspace", "geo"]);
  });

  it("network_operator: sees networks + SAUI-12 events/reports via network.* perms", () => {
    const out = visibleNavEntries(
      NAV_ENTRIES,
      networkOperator.permissions,
      "network",
    );
    // network_operator holds network.view_sales, network.manage_channels,
    // and network.view_reports. SAUI-12 placeholders use {anyOf} rules
    // that grant access from those families:
    //   events_sessions    -> network.view_sales
    //   reports            -> network.view_reports
    // The channels surface graduated to a real CRUD route (feature
    // #243) whose nav gate is keyed on channel.* (matching the backend
    // permission contract) plus superadmin.read; network.manage_channels
    // alone no longer unlocks it.
    // The payments surface graduated to a real CRUD route (feature
    // #244) whose nav gate is keyed on payment_config.* (matching the
    // backend permission contract) plus superadmin.read;
    // network.view_sales alone no longer unlocks it.
    // The operator still lacks superadmin.read, geo.admin, pos.execute,
    // org.read, channel.*, venue.*, payment_config.*, etc., so the
    // remaining sidebar collapses accordingly.
    expect(ids(out)).toEqual([
      "workspace",
      "networks",
      "events_sessions",
      "reports",
    ]);
  });

  it("network_operator under organization scope: only org-compatible entries remain", () => {
    const out = visibleNavEntries(
      NAV_ENTRIES,
      networkOperator.permissions,
      "organization",
    );
    // Networks hides (no "organization" in scopeKinds). Pos hides
    // (no organization scope AND missing pos.execute). Events / reports
    // include "organization" in scopeKinds and are unlocked by
    // network.view_sales / network.view_reports. Channels and payments
    // now require channel.* / payment_config.* (or superadmin.read)
    // which network_operator lacks.
    expect(ids(out)).toEqual([
      "workspace",
      "events_sessions",
      "reports",
    ]);
  });

  it("no-permission user: only the always-on workspace entry", () => {
    const out = visibleNavEntries(NAV_ENTRIES, noPermission.permissions, null);
    expect(ids(out)).toEqual(["workspace"]);
  });

  it("filter is permissions-only: changing role changes the sidebar", () => {
    const before = visibleNavEntries(NAV_ENTRIES, noPermission.permissions, "global");
    const after = visibleNavEntries(
      NAV_ENTRIES,
      platformSuperadmin.permissions,
      "global",
    );
    expect(before.length).toBeLessThan(after.length);
  });
});

// ---- Configuration invariants --------------------------------------------

describe("NAV_ENTRIES invariants", () => {
  it("every entry has a unique id", () => {
    const ids2 = NAV_ENTRIES.map((e) => e.id);
    expect(new Set(ids2).size).toBe(ids2.length);
  });

  it("every entry has a unique route path", () => {
    const paths = NAV_ENTRIES.map((e) => e.to);
    expect(new Set(paths).size).toBe(paths.length);
  });

  it("navEntryForPath round-trips every nav entry", () => {
    for (const entry of NAV_ENTRIES) {
      expect(navEntryForPath(entry.to)).toBe(entry);
    }
  });

  it("describeRule renders human-readable text for each shape", () => {
    expect(describeRule("always")).toMatch(/authenticated/);
    expect(describeRule({ anyOf: ["a", "b"] })).toMatch(/any of/);
    expect(describeRule({ allOf: ["a", "b"] })).toMatch(/all of/);
  });

  it("all entries with a scopeKinds list reference only known scope kinds", () => {
    const knownKinds: ReadonlySet<ScopeKind> = new Set<ScopeKind>([
      "global",
      "platform",
      "network",
      "organization",
    ]);
    for (const entry of NAV_ENTRIES) {
      if (entry.scopeKinds === undefined) {
        continue;
      }
      for (const k of entry.scopeKinds) {
        expect(knownKinds.has(k)).toBe(true);
      }
    }
  });
});

// ---- Route-level guard symmetry ------------------------------------------
//
// These cases reflect the exact predicate <RequirePermission /> uses at
// render time. If they pass, the route guard will pass; if not, it will
// render the 403 surface. Kept in sync with RequirePermission.tsx.

describe("route guard predicate (sidebar parity)", () => {
  function canAccess(
    path: string,
    perms: ReadonlySet<string>,
    scope: ScopeKind | null,
  ): boolean {
    const entry = navEntryForPath(path);
    if (entry === undefined) {
      throw new Error(`unknown nav path: ${path}`);
    }
    return (
      permissionRuleSatisfied(entry.permission, perms) &&
      scopeRuleSatisfied(entry, scope)
    );
  }

  it("platform_superadmin can access every guarded route under global scope", () => {
    for (const e of NAV_ENTRIES) {
      expect(canAccess(e.to, platformSuperadmin.permissions, "global")).toBe(true);
    }
  });

  it("superadmin.read can access /users even before membership.grant is present", () => {
    expect(canAccess("/users", new Set(["superadmin.read"]), "global")).toBe(true);
  });

  it("network_operator cannot access /organizations or /geo", () => {
    expect(canAccess("/organizations", networkOperator.permissions, "network")).toBe(false);
    expect(canAccess("/geo", networkOperator.permissions, "network")).toBe(false);
  });

  it("no-permission user is locked out of everything except workspace", () => {
    for (const e of NAV_ENTRIES) {
      const ok = canAccess(e.to, noPermission.permissions, null);
      expect(ok).toBe(e.id === "workspace");
    }
  });

  it("platform_operator hits 403 for network entity surface", () => {
    expect(canAccess("/networks", platformOperator.permissions, "platform")).toBe(false);
  });
});
