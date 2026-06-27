/**
 * Permission-driven navigation configuration (SAUI-03).
 *
 * The admin shell renders navigation by walking this list and filtering
 * each entry against the caller's permission set from /v1/me. Role names
 * (`platform_superadmin`, `network_operator`, ...) deliberately do not
 * appear here -- backend permissions are the source of truth. Role
 * presets influence only default scope selection (see scope.ts), not
 * the navigation surface.
 *
 * Permission family semantics:
 *
 *   "always"
 *       Visible to any authenticated user (e.g. Workspace).
 *
 *   { anyOf: [...] }
 *       Visible when the caller holds AT LEAST ONE of the listed
 *       permissions. Use for read surfaces whose backing endpoints
 *       accept either a broad or a narrow grant (e.g. network.read
 *       OR network.create).
 *
 *   { allOf: [...] }
 *       Visible only when the caller holds ALL listed permissions.
 *       Use for surfaces that fan out into multiple sub-operations and
 *       are unusable without each capability. Currently unused; kept
 *       for completeness.
 *
 *   { scope: "global" | "platform" | "network" | "organization" }
 *       Additional filter: the active scope must match this scope type.
 *       Network/organization-specific surfaces are hidden when the
 *       active scope is global/platform.
 *
 * Direct URL navigation to any guarded route MUST go through
 * <RequirePermission /> so the 403 UI is shown if the caller's
 * permission set has changed since the nav was rendered.
 */

export type ScopeKind = "global" | "platform" | "network" | "organization";

/**
 * Union of every registered admin route path. Keep in sync with the
 * route tree in `src/routeTree.ts`. Using a literal-union here gives
 * TanStack Router's typed <Link> a happy type when we pass `entry.to`.
 */
export type NavRoutePath =
  | "/"
  | "/networks"
  | "/organizations"
  | "/orders"
  | "/tickets"
  | "/refunds"
  | "/geo";

export type PermissionRule =
  | "always"
  | { readonly anyOf: readonly string[] }
  | { readonly allOf: readonly string[] };

export interface NavEntry {
  /** Stable identifier used by tests and keyed renders. */
  readonly id: string;
  /** Operator-visible label. Dense, factual; no marketing tone. */
  readonly label: string;
  /** TanStack Router path. MUST also exist in the route tree. */
  readonly to: NavRoutePath;
  /** Required permissions. */
  readonly permission: PermissionRule;
  /** When set, only show this entry under the listed scope kinds. */
  readonly scopeKinds?: readonly ScopeKind[];
  /** Short human-readable explanation, shown in the missing-permission UI. */
  readonly purpose: string;
}

/**
 * Canonical admin navigation. Keep ordering operationally sensible:
 * platform-wide surfaces first, then scoped surfaces, then settings.
 */
export const NAV_ENTRIES: readonly NavEntry[] = [
  {
    id: "workspace",
    label: "Workspace",
    to: "/",
    permission: "always",
    purpose: "Authenticated landing page; available to any signed-in user.",
  },
  {
    id: "networks",
    label: "Operator Networks",
    to: "/networks",
    permission: { anyOf: ["network.read", "network.create"] },
    scopeKinds: ["global", "platform", "network"],
    purpose:
      "Browse and manage operator networks. Requires network.read or network.create.",
  },
  {
    id: "organizations",
    label: "Organizations",
    to: "/organizations",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Cross-tenant organizations explorer. Requires superadmin.read.",
  },
  {
    id: "orders",
    label: "Orders",
    to: "/orders",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose: "Cross-tenant orders. Requires superadmin.read.",
  },
  {
    id: "tickets",
    label: "Tickets",
    to: "/tickets",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose: "Cross-tenant tickets. Requires superadmin.read.",
  },
  {
    id: "refunds",
    label: "Refunds",
    to: "/refunds",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose: "Cross-tenant refunds. Requires superadmin.read.",
  },
  {
    id: "geo",
    label: "Geo Registry",
    to: "/geo",
    permission: { anyOf: ["geo.admin"] },
    scopeKinds: ["global", "platform"],
    purpose: "Maintain countries/cities catalog. Requires geo.admin.",
  },
];

/** Index nav entries by route path for guard lookups. */
export const NAV_BY_PATH: Readonly<Record<string, NavEntry>> = Object.freeze(
  NAV_ENTRIES.reduce<Record<string, NavEntry>>((acc, entry) => {
    acc[entry.to] = entry;
    return acc;
  }, {}),
);

/** Lookup a nav entry by route path. Returns undefined if no rule defined. */
export function navEntryForPath(path: string): NavEntry | undefined {
  return NAV_BY_PATH[path];
}

/** True when the caller satisfies the entry's permission rule. */
export function permissionRuleSatisfied(
  rule: PermissionRule,
  permissions: ReadonlySet<string>,
): boolean {
  if (rule === "always") {
    return true;
  }
  if ("anyOf" in rule) {
    return rule.anyOf.some((p) => permissions.has(p));
  }
  return rule.allOf.every((p) => permissions.has(p));
}

/** True when the active scope satisfies the entry's scope filter (if any). */
export function scopeRuleSatisfied(
  entry: NavEntry,
  activeScopeKind: ScopeKind | null,
): boolean {
  if (entry.scopeKinds === undefined) {
    return true;
  }
  if (activeScopeKind === null) {
    // With no active scope, conservatively allow global/platform entries
    // (the bootstrap path). Network/org entries hide until a scope is set.
    return entry.scopeKinds.includes("global") || entry.scopeKinds.includes("platform");
  }
  return entry.scopeKinds.includes(activeScopeKind);
}

/**
 * Return the subset of NAV_ENTRIES the caller can SEE in the sidebar
 * given their current permissions and active scope.
 *
 * NOTE: this only filters visibility. Direct-URL navigation must still
 * be gated by <RequirePermission /> because permissions can change
 * between renders (refresh) and a stale nav cache must never bypass
 * authorization.
 */
export function visibleNavEntries(
  entries: readonly NavEntry[],
  permissions: ReadonlySet<string>,
  activeScopeKind: ScopeKind | null,
): readonly NavEntry[] {
  return entries.filter(
    (entry) =>
      permissionRuleSatisfied(entry.permission, permissions) &&
      scopeRuleSatisfied(entry, activeScopeKind),
  );
}

/** Render a permission rule into operator-readable text. */
export function describeRule(rule: PermissionRule): string {
  if (rule === "always") {
    return "available to any authenticated user";
  }
  if ("anyOf" in rule) {
    return `requires any of: ${rule.anyOf.join(", ")}`;
  }
  return `requires all of: ${rule.allOf.join(", ")}`;
}
