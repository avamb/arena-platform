/**
 * SuperAdmin global dashboard (SAUI-05).
 *
 * The first authenticated landing surface for platform operators. It is
 * deliberately operational, not marketing: the page renders ONLY counts
 * derived from real backend reads, plus a shortcuts row to the modules
 * an operator typically reaches for next.
 *
 * Data sources (all behind `superadmin.read`):
 *   GET /v1/admin/organizations
 *   GET /v1/admin/orders   ?limit=200
 *   GET /v1/admin/tickets  ?limit=200
 *   GET /v1/admin/refunds  ?limit=200
 *
 * Backend semantics (see internal/platform/httpserver/superadmin.go):
 *   - Each list endpoint returns at most `limit` rows (max 200).
 *   - The `total` field in the JSON envelope is `len(rows)` -- i.e. it is
 *     the size of the page returned, NOT the size of the underlying
 *     collection across the whole platform. There is currently no
 *     backend-total count endpoint.
 *
 * UI consequence: every card label MUST be explicit that the number it
 * shows is page-local (capped at the request `limit`) and that no global
 * count is available yet. Showing a bare integer would be a regression
 * vector ("Operator believes 47 organizations exist when in reality the
 * page was just capped at 50"). The "backend-total" line per card reads
 * "not available" until a count endpoint ships.
 *
 * Permissions:
 *   - Users without `superadmin.read` see the cards in a "no access"
 *     state plus shortcuts to the surfaces their permissions DO unlock
 *     (driven by the same NAV_ENTRIES table that powers the sidebar).
 *   - Network/Org-scoped operators still get the shortcuts row so they
 *     can jump into the surfaces they own.
 *
 * Mock data: NONE. If a fetch fails, the card renders an error state
 * with the backend error code -- never a sample/placeholder integer.
 */
import { createRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import type { CSSProperties, ReactNode } from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { useAuth } from "@/lib/auth/useAuth";
import { useScope } from "@/lib/auth/ScopeContext";
import {
  NAV_ENTRIES,
  visibleNavEntries,
  type NavEntry,
  type NavRoutePath,
} from "@/lib/auth/navConfig";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/",
  component: DashboardRoute,
});

// ---------------------------------------------------------------------------
// Response types
//
// The /v1/admin/* responses are open-ended JSON shapes today (the
// backend builds them with map[string]any). We only need the shape's
// outer envelope for counts; row contents are not rendered here, so we
// model them as opaque arrays.
// ---------------------------------------------------------------------------

interface ListEnvelope {
  readonly total: number;
}

interface OrgsEnvelope extends ListEnvelope {
  readonly organizations: readonly unknown[];
}
interface OrdersEnvelope extends ListEnvelope {
  readonly orders: readonly unknown[];
  readonly limit: number;
  readonly offset: number;
}
interface TicketsEnvelope extends ListEnvelope {
  readonly tickets: readonly unknown[];
  readonly limit: number;
  readonly offset: number;
}
interface RefundsEnvelope extends ListEnvelope {
  readonly refunds: readonly unknown[];
  readonly limit: number;
  readonly offset: number;
}

// Page size the dashboard pulls. Matches the backend cap (200) so the
// page-local count reaches as close to a real total as the endpoint
// will report today.
const DASHBOARD_PAGE_SIZE = 200;

// ---------------------------------------------------------------------------
// Page component
// ---------------------------------------------------------------------------

function DashboardRoute() {
  const { hasPermission, permissions, me } = useAuth();
  const { activeScopeKind } = useScope();

  const canRead = hasPermission("superadmin.read");

  // Sidebar uses the same data; we surface a SUBSET as shortcut tiles.
  // Workspace itself ("/") is intentionally excluded — no self-link.
  const shortcuts = visibleNavEntries(NAV_ENTRIES, permissions, activeScopeKind)
    .filter((e) => e.to !== "/");

  return (
    <section aria-labelledby="dash-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="dash-heading" style={headingStyle}>
            SuperAdmin Dashboard
          </h1>
          <p style={subheadingStyle}>
            Cross-tenant operational overview. All counts shown below are
            <strong> page-local </strong>
            (capped at {DASHBOARD_PAGE_SIZE} rows per endpoint); the backend
            does not currently expose a global total endpoint.
          </p>
        </div>
        {me !== null ? (
          <div style={signedInStyle} aria-label="Signed-in operator">
            <div style={signedInLabelStyle}>Signed in</div>
            <div style={signedInRolesStyle}>
              {me.roles.length === 0 ? "(no roles)" : me.roles.join(", ")}
            </div>
          </div>
        ) : null}
      </header>

      <section aria-labelledby="metrics-heading" style={sectionStyle}>
        <h2 id="metrics-heading" style={sectionHeadingStyle}>
          Operational counts
        </h2>
        {canRead ? (
          <MetricsGrid />
        ) : (
          <MissingPermissionNotice
            required="superadmin.read"
            held={Array.from(permissions)}
          />
        )}
      </section>

      <section aria-labelledby="shortcuts-heading" style={sectionStyle}>
        <h2 id="shortcuts-heading" style={sectionHeadingStyle}>
          Shortcuts
        </h2>
        {shortcuts.length === 0 ? (
          <p style={emptyShortcutsStyle} role="status">
            No modules available for your current permissions and scope. Ask
            a platform administrator to grant access, or switch to a scope
            that exposes operational surfaces.
          </p>
        ) : (
          <div style={shortcutGridStyle} role="list">
            {shortcuts.map((entry) => (
              <ShortcutTile key={entry.id} entry={entry} />
            ))}
            <AuditObservabilityTile />
          </div>
        )}
      </section>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

function MetricsGrid() {
  return (
    <div style={cardGridStyle} role="list">
      <MetricCard<OrgsEnvelope>
        title="Organizations"
        queryKey={["dashboard", "organizations"]}
        path="/v1/admin/organizations"
        countSelector={(env) => env.organizations.length}
        linkTo="/organizations"
        linkLabel="Open Organizations"
      />
      <MetricCard<OrdersEnvelope>
        title="Orders"
        queryKey={["dashboard", "orders", DASHBOARD_PAGE_SIZE]}
        path={`/v1/admin/orders?limit=${DASHBOARD_PAGE_SIZE}`}
        countSelector={(env) => env.orders.length}
        linkTo="/orders"
        linkLabel="Open Orders"
      />
      <MetricCard<TicketsEnvelope>
        title="Tickets"
        queryKey={["dashboard", "tickets", DASHBOARD_PAGE_SIZE]}
        path={`/v1/admin/tickets?limit=${DASHBOARD_PAGE_SIZE}`}
        countSelector={(env) => env.tickets.length}
        linkTo="/tickets"
        linkLabel="Open Tickets"
      />
      <MetricCard<RefundsEnvelope>
        title="Refunds"
        queryKey={["dashboard", "refunds", DASHBOARD_PAGE_SIZE]}
        path={`/v1/admin/refunds?limit=${DASHBOARD_PAGE_SIZE}`}
        countSelector={(env) => env.refunds.length}
        linkTo="/refunds"
        linkLabel="Open Refunds"
      />
    </div>
  );
}

interface MetricCardProps<E> {
  title: string;
  queryKey: readonly unknown[];
  path: string;
  countSelector: (env: E) => number;
  linkTo: NavRoutePath;
  linkLabel: string;
}

function MetricCard<E>({
  title,
  queryKey,
  path,
  countSelector,
  linkTo,
  linkLabel,
}: MetricCardProps<E>) {
  const q = useQuery<E, ApiError>({
    queryKey,
    queryFn: () => authedFetch<E>({ method: "GET", path }),
    // Cross-tenant lists are heavy; don't auto-refetch on focus for the
    // dashboard. Operators can navigate into the module for fresh data.
    refetchOnWindowFocus: false,
    // A 403 or reason-required failure must not be retried — those are
    // user-facing states, not transient backend hiccups.
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (err.status === 401 || err.status === 403 || err.status === 0) {
          return false;
        }
        if (err.code === "superadmin.reason_required") {
          return false;
        }
      }
      return failureCount < 2;
    },
  });

  return (
    <article
      style={cardStyle}
      aria-labelledby={`mc-${title}`}
      role="listitem"
      data-testid={`metric-${title.toLowerCase()}`}
    >
      <div style={cardHeaderStyle}>
        <h3 id={`mc-${title}`} style={cardTitleStyle}>
          {title}
        </h3>
        <Link
          to={linkTo as "/"}
          style={cardLinkStyle}
          aria-label={linkLabel}
        >
          Open →
        </Link>
      </div>
      <MetricBody
        title={title}
        status={q.status}
        fetchStatus={q.fetchStatus}
        error={q.error}
        data={q.data}
        countSelector={countSelector}
      />
    </article>
  );
}

interface MetricBodyProps<E> {
  title: string;
  status: "pending" | "error" | "success";
  fetchStatus: "fetching" | "paused" | "idle";
  error: ApiError | null;
  data: E | undefined;
  countSelector: (env: E) => number;
}

function MetricBody<E>({
  title,
  status,
  fetchStatus,
  error,
  data,
  countSelector,
}: MetricBodyProps<E>) {
  if (status === "pending") {
    return (
      <div style={metricLoadingStyle} role="status" aria-live="polite">
        Loading {title.toLowerCase()}…
        {fetchStatus === "paused" ? " (paused — offline)" : null}
      </div>
    );
  }
  if (status === "error" || data === undefined) {
    return <MetricErrorBody title={title} error={error} />;
  }
  const count = countSelector(data);
  const empty = count === 0;
  return (
    <div style={metricBodyStyle}>
      <div
        style={empty ? metricNumberEmptyStyle : metricNumberStyle}
        aria-label={`${count} ${title.toLowerCase()} loaded`}
      >
        {count.toLocaleString()}
      </div>
      <dl style={metricMetaStyle}>
        <div style={metricMetaRowStyle}>
          <dt style={metricMetaKeyStyle}>Shown</dt>
          <dd style={metricMetaValStyle}>
            {empty
              ? "No rows returned"
              : `${count.toLocaleString()} (page-local, ≤${DASHBOARD_PAGE_SIZE})`}
          </dd>
        </div>
        <div style={metricMetaRowStyle}>
          <dt style={metricMetaKeyStyle}>Backend total</dt>
          <dd style={metricMetaValMutedStyle}>
            not available (no global count endpoint)
          </dd>
        </div>
      </dl>
    </div>
  );
}

function MetricErrorBody({
  title,
  error,
}: {
  title: string;
  error: ApiError | null;
}) {
  // Specific copy for the most common operator-recoverable failures.
  if (error instanceof ApiError) {
    if (error.code === "superadmin.reason_required") {
      return (
        <div style={metricErrorStyle} role="status">
          Audit reason required. Submit one in the prompt to load{" "}
          {title.toLowerCase()}.
        </div>
      );
    }
    if (error.status === 401) {
      return (
        <div style={metricErrorStyle} role="status">
          Session expired. Sign in again to load {title.toLowerCase()}.
        </div>
      );
    }
    if (error.status === 403 || error.code === "permissions.denied") {
      return (
        <div style={metricErrorStyle} role="status">
          Forbidden. Your account is missing the permission required to
          read {title.toLowerCase()}.
        </div>
      );
    }
  }
  return (
    <div style={metricErrorStyle} role="alert">
      <div>Failed to load {title.toLowerCase()}.</div>
      <code style={metricErrorCodeStyle}>
        {error?.code ?? "unknown.error"}
      </code>
      {error?.message ? (
        <div style={metricErrorMessageStyle}>{error.message}</div>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Missing permission notice
// ---------------------------------------------------------------------------

function MissingPermissionNotice({
  required,
  held,
}: {
  required: string;
  held: readonly string[];
}) {
  return (
    <div
      style={missingPermissionStyle}
      role="status"
      data-testid="dashboard-missing-permission"
    >
      <div style={missingPermissionTitleStyle}>
        Operational counts hidden — missing <code>{required}</code>.
      </div>
      <p style={missingPermissionBodyStyle}>
        Cross-tenant counts (organizations, orders, tickets, refunds) are only
        available to operators holding <code>{required}</code>. Your account
        currently holds {held.length} permission
        {held.length === 1 ? "" : "s"}; ask a platform administrator to grant
        the missing permission, or use the shortcuts below to reach the
        modules you do have access to.
      </p>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Shortcuts
// ---------------------------------------------------------------------------

function ShortcutTile({ entry }: { entry: NavEntry }): ReactNode {
  return (
    <Link
      to={entry.to as "/"}
      style={shortcutTileStyle}
      role="listitem"
      data-testid={`shortcut-${entry.id}`}
      title={entry.purpose}
    >
      <span style={shortcutLabelStyle}>{entry.label}</span>
      <span style={shortcutHintStyle}>{entry.purpose}</span>
    </Link>
  );
}

function AuditObservabilityTile(): ReactNode {
  // Audit / observability does not have a UI route yet. The feature
  // requirement calls for a shortcut to it; rendering a disabled tile is
  // honest -- it tells the operator the surface exists conceptually but
  // is not yet implemented, rather than linking to a 404.
  return (
    <div
      style={shortcutTileDisabledStyle}
      role="listitem"
      aria-disabled="true"
      data-testid="shortcut-audit-observability"
      title="Audit / Observability surface is not yet implemented."
    >
      <span style={shortcutLabelStyle}>Audit / Observability</span>
      <span style={shortcutHintStyle}>
        Pending follow-up SAUI-* task; backend audit_events table is already
        populated by superadmin reads.
      </span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 24,
};

const headerStyle: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 16,
};

const headingStyle: CSSProperties = {
  margin: 0,
  fontSize: 22,
  fontWeight: 600,
  letterSpacing: -0.2,
};

const subheadingStyle: CSSProperties = {
  margin: "4px 0 0 0",
  fontSize: 13,
  color: "#475569",
  maxWidth: 720,
  lineHeight: 1.45,
};

const signedInStyle: CSSProperties = {
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  padding: "8px 12px",
  display: "flex",
  flexDirection: "column",
  gap: 2,
  minWidth: 180,
};

const signedInLabelStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  textTransform: "uppercase",
  letterSpacing: 0.5,
};

const signedInRolesStyle: CSSProperties = {
  fontSize: 12,
  color: "#0f172a",
  fontWeight: 500,
  wordBreak: "break-word",
};

const sectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 10,
};

const sectionHeadingStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  fontWeight: 600,
  color: "#334155",
  textTransform: "uppercase",
  letterSpacing: 0.5,
};

const cardGridStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(240px, 1fr))",
  gap: 12,
};

const cardStyle: CSSProperties = {
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  padding: 16,
  display: "flex",
  flexDirection: "column",
  gap: 10,
  minHeight: 140,
};

const cardHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
  gap: 8,
};

const cardTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  fontWeight: 600,
  color: "#0f172a",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const cardLinkStyle: CSSProperties = {
  fontSize: 12,
  color: "#0369a1",
  textDecoration: "none",
  fontWeight: 500,
};

const metricBodyStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
};

const metricNumberStyle: CSSProperties = {
  fontSize: 32,
  fontWeight: 600,
  lineHeight: 1.1,
  color: "#0f172a",
  fontVariantNumeric: "tabular-nums",
};

const metricNumberEmptyStyle: CSSProperties = {
  fontSize: 32,
  fontWeight: 600,
  lineHeight: 1.1,
  fontVariantNumeric: "tabular-nums",
  color: "#94a3b8",
};

const metricLoadingStyle: CSSProperties = {
  fontSize: 12,
  color: "#64748b",
  padding: "16px 0",
};

const metricErrorStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: 12,
  border: "1px dashed #fca5a5",
  borderRadius: 4,
  background: "#fef2f2",
  fontSize: 12,
  color: "#7f1d1d",
};

const metricErrorCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
  color: "#7f1d1d",
};

const metricErrorMessageStyle: CSSProperties = {
  fontSize: 11,
  color: "#7f1d1d",
};

const metricMetaStyle: CSSProperties = {
  margin: 0,
  display: "flex",
  flexDirection: "column",
  gap: 2,
};

const metricMetaRowStyle: CSSProperties = {
  display: "flex",
  gap: 6,
  fontSize: 11,
};

const metricMetaKeyStyle: CSSProperties = {
  color: "#64748b",
  minWidth: 90,
  margin: 0,
};

const metricMetaValStyle: CSSProperties = {
  color: "#0f172a",
  margin: 0,
};

const metricMetaValMutedStyle: CSSProperties = {
  color: "#94a3b8",
  margin: 0,
  fontStyle: "italic",
};

const missingPermissionStyle: CSSProperties = {
  background: "#fffbeb",
  border: "1px solid #fde68a",
  borderRadius: 6,
  padding: 16,
  display: "flex",
  flexDirection: "column",
  gap: 6,
};

const missingPermissionTitleStyle: CSSProperties = {
  fontSize: 13,
  fontWeight: 600,
  color: "#78350f",
};

const missingPermissionBodyStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#78350f",
  lineHeight: 1.5,
};

const shortcutGridStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))",
  gap: 12,
};

const shortcutTileStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: "12px 14px",
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  textDecoration: "none",
  color: "#0f172a",
};

const shortcutTileDisabledStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: "12px 14px",
  borderRadius: 6,
  background: "#f8fafc",
  border: "1px dashed #cbd5e1",
  color: "#475569",
};

const shortcutLabelStyle: CSSProperties = {
  fontSize: 13,
  fontWeight: 600,
};

const shortcutHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  lineHeight: 1.4,
};

const emptyShortcutsStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#64748b",
  padding: 16,
  border: "1px dashed #cbd5e1",
  borderRadius: 4,
  background: "#f8fafc",
  lineHeight: 1.45,
};
