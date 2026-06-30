/**
 * SAUI-12 -- legacy-derived module route placeholders.
 *
 * The single unified admin shell (apps/admin-web) is replacing the legacy
 * Bil24 four-application suite (TixManager, TixEditor, TixCassa,
 * TixReporter). The full module set is large and will not land in one
 * milestone; SAUI-12 ships honest placeholder routes for the remaining
 * legacy-derived modules so the navigation surface matches the unified
 * admin contract (one app, scope switcher, permission filter) instead
 * of pretending the modules do not exist.
 *
 * Each entry is the SHELL ONLY. We deliberately:
 *
 *   - render NO fake tables, NO synthetic rows, NO mock counts;
 *   - name the legacy reference (which legacy app/module the placeholder
 *     supersedes, plus the legacy_admin_reference_map.yaml module id);
 *   - name the EXPECTED future scope (the bullet list that the future
 *     real module is committed to delivering);
 *   - name the SPECIFIC deferral reason (why this milestone does NOT
 *     implement the module yet -- usually backend contract gap, or an
 *     explicit "do not overbuild" rule from the project guardrails).
 *
 * The data here is the single source of truth for:
 *
 *   - the placeholder route bodies (LegacyModulePlaceholder component),
 *   - the SAUI-03 navigation config (one NAV_ENTRIES row per module),
 *   - the unit tests that pin the contract.
 *
 * If a future SAUI-* task lands the real module, REPLACE the placeholder
 * route file with a real route module (mirroring SAUI-07 networks,
 * SAUI-10 orders/tickets/refunds, SAUI-11 audit/observability) and
 * remove the corresponding entry from LEGACY_MODULE_PLACEHOLDERS -- the
 * nav config will keep working because navConfig.ts derives entries
 * from this list at module load time.
 */
import type { NavRoutePath, PermissionRule, ScopeKind } from "@/lib/auth/navConfig";

/**
 * Per-placeholder metadata. Mirrors the keys in
 * 09_autoforge/admin_ui/legacy_admin_reference_map.yaml so an operator
 * can cross-reference the shipped route with the planning artifact.
 */
export interface LegacyModulePlaceholder {
  /** Stable id used by tests and React keys. Aligned with legacy map module id. */
  readonly id: string;
  /** Operator-visible label. Matches the legacy map target_name. */
  readonly label: string;
  /** TanStack Router path. Registered in routeTree.ts. */
  readonly path: NavRoutePath;
  /** SAUI-03 short purpose, also reused in <RequirePermission /> 403 surface. */
  readonly purpose: string;
  /** SAUI-03 permission rule. {anyOf} of representative families. */
  readonly permission: PermissionRule;
  /** SAUI-03 scope filter. */
  readonly scopeKinds: readonly ScopeKind[];
  /** Which legacy app this module supersedes, plus legacy map module id. */
  readonly sourceReference: {
    readonly legacyMapModuleId: string;
    readonly legacyApps: readonly string[];
    readonly legacyScreens: readonly string[];
  };
  /** What the FUTURE real module is committed to delivering (bullet list). */
  readonly futureScope: readonly string[];
  /** Why this milestone is NOT implementing the module yet. */
  readonly deferralReason: string;
  /** Workflow shape labels lifted from the legacy map. */
  readonly workflowShape: readonly string[];
  /** Legacy map mvp_priority bucket. */
  readonly mvpPriority: "P0" | "P1" | "P2";
}

export const LEGACY_MODULE_PLACEHOLDERS: readonly LegacyModulePlaceholder[] = [
  // events_sessions removed from placeholder list -- replaced by a real
  // route in src/routes/events.tsx (feature #281). The /events nav entry
  // in navConfig.ts now points at the real module (list with filters +
  // detail drawer + status transitions backed by
  // POST /v1/organizations/{org_id}/events/{id}/status). Full event /
  // session create-edit-delete and the media/pricing/sync surfaces
  // remain explicitly deferred -- they are separate concerns the E-3
  // scope does not cover.
  // venues_seating removed from placeholder list -- replaced by a real
  // CRUD route in src/routes/venues.tsx (feature #242). The /venues nav
  // entry in navConfig.ts now points at the real module; the visual
  // seating editor remains explicitly deferred and is not part of this
  // CRUD scope.
  // frontends_channels removed from placeholder list -- replaced by a
  // real CRUD route in src/routes/channels.tsx (feature #243). The
  // /channels nav entry in navConfig.ts now points at the real module
  // (Sales Channels CRUD against
  // /v1/organizations/{org_id}/channels). Trusted agents, ETS
  // connections, promotions, and widgets remain explicitly deferred --
  // they are separate legacy sub-tabs the channels CRUD scope does not
  // cover.
  // payments_fiscal removed from placeholder list -- replaced by a real
  // CRUD route in src/routes/payments.tsx (feature #244). The /payments
  // nav entry in navConfig.ts now points at the real module backed by
  // /v1/organizations/{org_id}/payment-configs (feature #237). Fiscal
  // printer templates and POS shift-side configuration remain explicitly
  // deferred -- they are separate concerns the payment-configs CRUD scope
  // does not cover.
  {
    id: "reports",
    label: "Reports",
    path: "/reports",
    purpose:
      "Unified reporting by platform, network, organizer, agent, event, and period. Requires network.view_reports or superadmin.read.",
    permission: {
      anyOf: [
        "superadmin.read",
        "network.view_reports",
        "report.read",
      ],
    },
    scopeKinds: ["global", "platform", "network", "organization"],
    sourceReference: {
      legacyMapModuleId: "reports",
      legacyApps: ["TixReporter", "TixManager"],
      legacyScreens: [
        "raw_misc/legacy_reporter_test_zone_2023.jpg",
        "raw_misc/legacy_subscriptions.jpg",
        "tix_manager/2026-06-11_manager_audit/30_tixreporter_auth.png",
      ],
    },
    futureScope: [
      "Saved report presets with role-scoped data visibility.",
      "Sales / refunds / reconciliation aggregates by period and scope.",
      "Export (CSV/JSON) when the backend exposes a query layer.",
    ],
    deferralReason:
      "Guardrail: this milestone explicitly skips full reports per the SAUI-12 description ('Do not implement full POS/seating/reports here'). Backend gap: the legacy TixReporter merged multiple sub-workflows (mailings, quotas, MACS, work shifts, statistics) without a coherent modern aggregation contract.",
    workflowShape: [
      "table/list view",
      "detail drawer",
      "deferred/later feature",
    ],
    mvpPriority: "P1",
  },
  {
    id: "notifications_content",
    label: "Notifications and Content",
    path: "/content",
    purpose:
      "Notifications, news, subscriptions, widget and content configuration. Requires superadmin.read.",
    permission: { anyOf: ["superadmin.read"] },
    scopeKinds: ["global", "platform", "network", "organization"],
    sourceReference: {
      legacyMapModuleId: "notifications_content",
      legacyApps: ["TixManager"],
      legacyScreens: [
        "tix_manager/2026-06-11_manager_audit/03_subscriptions.png",
        "tix_manager/2026-06-11_manager_audit/04_widget.png",
        "tix_manager/2026-06-11_manager_audit/05_notifications.png",
        "tix_manager/2026-06-11_manager_audit/06_news.png",
      ],
    },
    futureScope: [
      "Templates and campaigns by scope.",
      "Preview and delivery status.",
      "Subscriptions and widget settings as scoped submodules.",
    ],
    deferralReason:
      "Guardrail: the legacy map flags this as MVP priority P2 -- the screenshot evidence is thin and some areas overlap with widget/subscription configuration. Backend gap: no notifications/content configuration endpoint is in the admin contract yet.",
    workflowShape: ["settings page", "deferred/later feature"],
    mvpPriority: "P2",
  },
  {
    id: "pos",
    label: "POS Mode",
    path: "/pos",
    purpose:
      "Cash desk mode: shifts, event selection, cart, payment, fiscal, printing, returns. Shell only -- POS execution explicitly out of scope this milestone.",
    permission: { anyOf: ["superadmin.read", "pos.execute"] },
    scopeKinds: ["global", "platform", "network"],
    sourceReference: {
      legacyMapModuleId: "pos",
      legacyApps: ["TixCassa"],
      legacyScreens: [
        "tix_cassa/2026-06-21_cassa_audit/00_auth_open.png",
        "tix_cassa/2026-06-21_cassa_audit/02_current_root.png",
        "tix_cassa/2026-06-21_cassa_audit/08_need_shift_alert.png",
        "tix_cassa/2026-06-21_cassa_audit/10_menu_cash_shift_open.png",
        "tix_cassa/2026-06-21_cassa_audit/19_current_before_purchase.png",
        "tix_cassa/2026-06-21_cassa_audit/29_tab10_add_return.png",
      ],
    },
    futureScope: [
      "Separate POS workspace inside the same app shell (NOT a separate app).",
      "Keyboard-friendly dense layout.",
      "Shift state always visible; event select, cart, payment, fiscal, printing, returns.",
    ],
    deferralReason:
      "Guardrail: the SAUI-12 description explicitly excludes full POS ('Do not implement full POS/seating/reports here'). The legacy map also flags POS as MVP priority P2 and the project guardrails state 'Do not implement full POS/seating editor before RBAC and admin shell foundation' -- this placeholder satisfies the navigation contract without overbuilding.",
    workflowShape: ["pos mode", "table/list view", "detail drawer"],
    mvpPriority: "P2",
  },
];

/** Lookup table by route path. Used by the placeholder component + tests. */
export const LEGACY_MODULE_PLACEHOLDERS_BY_PATH: Readonly<
  Record<string, LegacyModulePlaceholder>
> = Object.freeze(
  LEGACY_MODULE_PLACEHOLDERS.reduce<Record<string, LegacyModulePlaceholder>>(
    (acc, mod) => {
      acc[mod.path] = mod;
      return acc;
    },
    {},
  ),
);

/**
 * Resolve the placeholder metadata for a given path, throwing if the
 * path is not registered. Used at route module load time so a typo
 * fails fast instead of rendering an empty placeholder.
 */
export function legacyModuleForPath(path: NavRoutePath): LegacyModulePlaceholder {
  const mod = LEGACY_MODULE_PLACEHOLDERS_BY_PATH[path];
  if (mod === undefined) {
    throw new Error(`legacyModuleForPath: no placeholder registered for ${path}`);
  }
  return mod;
}
