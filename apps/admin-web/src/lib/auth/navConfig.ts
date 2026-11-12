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
  | "/users"
  | "/organizations"
  | "/events"
  | "/venues"
  | "/orders"
  | "/tickets"
  | "/refunds"
  | "/channels"
  | "/payments"
  | "/customers"
  | "/org-orders"
  | "/reports"
  | "/content"
  | "/pos"
  | "/audit"
  | "/observability"
  | "/geo"
  | "/webhooks"
  | "/customer-imports"
  | "/onboarding"
  | "/billing/prices"
  | "/billing/invoices"
  | "/billing/payment-fees"
  | "/billing/bank"
  | "/analytics/flows"
  | "/analytics/errors"
  | "/organizations/$orgId/promo-codes";

export type PermissionRule =
  | "always"
  | { readonly anyOf: readonly string[] }
  | { readonly allOf: readonly string[] };

export interface NavEntry {
  /** Stable identifier used by tests and keyed renders. */
  readonly id: string;
  /**
   * Operator-visible label (fallback). Dense, factual; no marketing tone.
   * In the runtime UI, the i18n key `nav.<id>` is preferred when the
   * I18nProvider is mounted (Feature #251).
   */
  readonly label: string;
  /**
   * Optional i18n catalog key for the label. When set, the AppLayout
   * resolves the label via `t(labelKey)` and falls back to `label` if
   * the key is missing. Defaults to `nav.<id>` if omitted.
   */
  readonly labelKey?: string;
  /** TanStack Router path. MUST also exist in the route tree. */
  readonly to: NavRoutePath;
  /** Required permissions. */
  readonly permission: PermissionRule;
  /** When set, only show this entry under the listed scope kinds. */
  readonly scopeKinds?: readonly ScopeKind[];
  /** Short human-readable explanation, shown in the missing-permission UI. */
  readonly purpose: string;
  /**
   * PR-07: When true, this entry is a shell-only placeholder that has not yet
   * been implemented. In production builds (`!isDevelopment`) this entry is
   * hidden from the sidebar so operators are not presented with non-functional
   * navigation items. The route itself remains registered (permission-gated),
   * so direct-URL access still produces the correct 403/placeholder UI; only
   * the sidebar link is suppressed.
   *
   * Set this flag on Reports/Content/POS entries until their real
   * implementations land.
   */
  readonly productionPlaceholder?: boolean;
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
    purpose: "Start page for each user who is signed in.",
  },
  {
    id: "networks",
    label: "Operator Networks",
    to: "/networks",
    permission: { anyOf: ["network.read", "network.create"] },
    scopeKinds: ["global", "platform", "network"],
    purpose:
      "Examine and manage operator networks. Necessary permission: network.read or network.create.",
  },
  {
    id: "users",
    label: "Users",
    to: "/users",
    permission: { anyOf: ["superadmin.read", "membership.grant"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Make users and give each user a first platform or organization role. To view: superadmin.read. To submit: membership.grant.",
  },
  {
    id: "organizations",
    label: "Organizations",
    to: "/organizations",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "All organizations of all tenants. Necessary permission: superadmin.read.",
  },
  // ---- SAUI-12 legacy-derived module placeholders (events, venues) ----
  // These are catalog-shape modules and slot between organizations
  // (tenant view) and orders (commerce view) in the sidebar.
  {
    id: "events_sessions",
    label: "Events and Sessions",
    to: "/events",
    permission: {
      anyOf: [
        "superadmin.read",
        "event.read",
        "org.read",
        "network.view_sales",
      ],
    },
    scopeKinds: ["global", "platform", "network", "organization"],
    purpose:
      "Event organizer page (events, sessions, media, pricing, quotas, sync). This release has an empty page only. The backend contract does not have all necessary parts. Necessary permission: event.read, org.read, network.view_sales or superadmin.read.",
  },
  {
    id: "venues",
    label: "Venues",
    to: "/venues",
    permission: {
      anyOf: [
        "superadmin.read",
        "venue.read",
        "venue.create",
        "venue.update",
        "venue.delete",
      ],
    },
    scopeKinds: ["global", "platform", "network", "organization"],
    purpose:
      "Venue directory (name, city, address, capacity_default). Necessary permission: venue.read, venue.create, venue.update, venue.delete or superadmin.read. The visual seating editor is for a later release.",
  },
  {
    id: "orders",
    label: "Orders",
    to: "/orders",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose: "Orders of all tenants. Necessary permission: superadmin.read.",
  },
  {
    id: "tickets",
    label: "Tickets",
    to: "/tickets",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose: "Tickets of all tenants. Necessary permission: superadmin.read.",
  },
  {
    id: "refunds",
    label: "Refunds",
    to: "/refunds",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose: "Refunds of all tenants. Necessary permission: superadmin.read.",
  },
  // ---- SAUI-12 legacy-derived module placeholders (channels, payments,
  // reports, content, pos). Ordered to match the legacy_admin_reference_map
  // workflow grouping: sales surfaces -> money -> insight -> messaging ->
  // POS. Each is a SHELL ONLY (see LegacyModulePlaceholder.tsx).
  {
    id: "channels",
    label: "Sales Channels",
    to: "/channels",
    permission: {
      anyOf: [
        "superadmin.read",
        "channel.read",
        "channel.create",
        "channel.update",
        "channel.delete",
      ],
    },
    scopeKinds: ["global", "platform", "network", "organization"],
    purpose:
      "Sales channel directory (payment_mode, provider, hidden merchant credential, fee_percent, hold TTL override, settings). Necessary permission: channel.read, channel.create, channel.update, channel.delete or superadmin.read. Trusted agents, ETS connections, promotions and widgets are for a later release.",
  },
  {
    id: "payments",
    label: "Payment Configs",
    to: "/payments",
    permission: {
      anyOf: [
        "superadmin.read",
        "payment_config.read",
        "payment_config.write",
      ],
    },
    scopeKinds: ["global", "platform", "network", "organization"],
    purpose:
      "Payment provider settings for each organization (provider, mode, provider_account_id, status, hidden secrets). Necessary permission: payment_config.read, payment_config.write or superadmin.read. Fiscal and POS settings are for a later release.",
  },
  {
    id: "customers",
    label: "Buyers",
    to: "/customers",
    permission: { anyOf: ["customer.read"] },
    scopeKinds: ["global", "platform", "network", "organization"],
    purpose:
      "Buyer directory and card for one organization (identities, orders, attributes, consents). Necessary permission: customer.read.",
  },
  {
    id: "org-orders",
    label: "Orders",
    to: "/org-orders",
    permission: { anyOf: ["order.read"] },
    scopeKinds: ["global", "platform", "network", "organization"],
    purpose:
      "Order list for one organization (number, buyer, session, status, total). It has a detail drawer (items, timeline) and a cancel action. Necessary permission: order.read. To cancel: order.write.",
  },
  {
    id: "promo_codes",
    label: "Promo codes",
    to: "/organizations/$orgId/promo-codes",
    permission: { anyOf: ["promo.read"] },
    scopeKinds: ["global", "platform", "network", "organization"],
    purpose:
      "Discount vouchers for one organization (list, make, pause or activate, delete) and the report of their use. Necessary permission: promo.read. To make, change or delete: promo.create, promo.update or promo.delete. Open this page from an organization, for example with the \"Manage promo codes\" link of the session overview, because the URL has the organization ID.",
  },
  {
    id: "reports",
    label: "Reports",
    to: "/reports",
    permission: {
      anyOf: [
        "superadmin.read",
        "network.view_reports",
        "report.read",
      ],
    },
    scopeKinds: ["global", "platform", "network", "organization"],
    purpose:
      "One report for platform, network, organizer, agent, event and period. This release has an empty page only. Reports are not in this release. Necessary permission: network.view_reports, report.read or superadmin.read.",
    productionPlaceholder: true,
  },
  {
    id: "notifications_content",
    label: "Notifications and Content",
    to: "/content",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform", "network", "organization"],
    purpose:
      "Notifications, news, subscriptions, widget and content settings. This release has an empty page only. Necessary permission: superadmin.read.",
    productionPlaceholder: true,
  },
  {
    id: "pos",
    label: "POS Mode",
    to: "/pos",
    permission: { anyOf: ["superadmin.read", "pos.execute"] },
    scopeKinds: ["global", "platform", "network"],
    purpose:
      "Cash desk mode (work shifts, event selection, cart, payment, fiscal data, printing, returns). This release has an empty page only. POS work is not in this release. Necessary permission: pos.execute or superadmin.read.",
    productionPlaceholder: true,
  },
  {
    id: "audit",
    label: "Audit Log",
    to: "/audit",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Audit log shell for all tenants. Necessary permission: superadmin.read. The page shows only what is true until the backend has an audit reader.",
  },
  {
    id: "observability",
    label: "Observability",
    to: "/observability",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Platform health and observability shell. Necessary permission: superadmin.read. It has links to the operational probes (/healthz, /readyz, /metrics). It lists the dashboard endpoints that are not available.",
  },
  {
    id: "geo",
    label: "Geo Registry (advanced)",
    to: "/geo",
    permission: { anyOf: ["geo.admin"] },
    scopeKinds: ["global", "platform"],
    purpose: "Manage the catalog of countries and cities. Necessary permission: geo.admin.",
  },
  {
    id: "webhooks",
    label: "Webhook Subscribers",
    to: "/webhooks",
    permission: { anyOf: ["webhook.subscriber.manage"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Add, change and pause HTTP endpoints that receive signed outbox events. Necessary permission: webhook.subscriber.manage.",
  },
  {
    id: "customer_imports",
    label: "Buyer Imports",
    to: "/customer-imports",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Add, test and apply imports of buyer and order exports (feature #520). Necessary permission: superadmin.read.",
  },
  {
    id: "onboarding",
    label: "Onboarding",
    to: "/onboarding",
    permission: { anyOf: ["onboarding.review"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Organizer applications: the queue of sent forms, the checks of the system, approve, ask for details, reject, and the onboarding settings. Necessary permission: onboarding.review. For the settings tab: onboarding.settings.",
  },
  {
    id: "billing_prices",
    label: "Price lists and rates",
    to: "/billing/prices",
    permission: { anyOf: ["billing.admin"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Billing: versions of the price lists (a new price is a new version that starts in the future), the individual rates of organizations, and the terms of billing accounts. Necessary permission: billing.admin and a platform superadmin. Each change needs a reason, and the platform keeps an audit record.",
  },
  {
    id: "billing_invoices",
    label: "Invoices",
    to: "/billing/invoices",
    permission: { anyOf: ["billing.admin"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Billing: platform invoices to organizations. Make a draft usage invoice for a finished month. Issue it for a bank transfer or a card payment. Record money that came outside the feeds. Or void the invoice. Necessary permission: billing.admin and a platform superadmin. Each change needs a reason, and the platform keeps an audit record.",
  },
  {
    id: "billing_payment_fees",
    label: "Payment fees",
    to: "/billing/payment-fees",
    permission: { anyOf: ["billing.admin"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Billing: the average card fee (percentage and fixed part, for each currency) that a card payer adds to an invoice. A new version starts in the future. Necessary permission: billing.admin and a platform superadmin. Each change needs a reason, and the platform keeps an audit record.",
  },
  {
    id: "billing_bank",
    label: "Bank payments",
    to: "/billing/bank",
    permission: { anyOf: ["billing.admin"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Billing: bank payments that come in. Match to an invoice a payment that the bank feed did not match. Or ignore it and give a reason. Or import a statement by hand. Necessary permission: billing.admin and a platform superadmin. Each change needs a reason, and the platform keeps an audit record.",
  },
  {
    id: "analytics_flows",
    label: "Analytics: flows",
    to: "/analytics/flows",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Product analytics of the platform: the steps where people leave the bot wizard and the event center flows, with the previous period for comparison. Read-only. Necessary permission: superadmin.read.",
  },
  {
    id: "analytics_errors",
    label: "Analytics: errors",
    to: "/analytics/errors",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform"],
    purpose:
      "Product analytics of the platform: the most frequent screen errors, bot input errors and rage clicks, with the previous period for comparison. Read-only. Necessary permission: superadmin.read.",
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
 * PR-07: Filter out placeholder entries that should be hidden in production.
 *
 * Entries marked `productionPlaceholder: true` (Reports, Content, POS) are
 * shell-only routes with no real backend implementation. In production builds
 * (`isDevelopment === false`) they are suppressed from the sidebar so operators
 * are not presented with non-functional navigation items.
 *
 * The routes themselves remain registered and permission-gated; direct URL
 * access still produces the correct placeholder/403 UI. Only sidebar visibility
 * is controlled here.
 *
 * Pass `config.isDevelopment` from `@/lib/config` as the second argument.
 */
export function hideProductionPlaceholders(
  entries: readonly NavEntry[],
  isDevelopment: boolean,
): readonly NavEntry[] {
  if (isDevelopment) {
    return entries;
  }
  return entries.filter((entry) => !entry.productionPlaceholder);
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
