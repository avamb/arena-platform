/**
 * SuperAdmin Organizations cross-tenant explorer (SAUI-06).
 *
 * Backed by GET /v1/admin/organizations (see
 * apps/backend/internal/platform/httpserver/superadmin.go). The endpoint:
 *
 *   - requires `superadmin.read` permission;
 *   - requires the `X-Admin-Reason` header (cross-tenant read);
 *   - returns the *entire* organizations collection in one response,
 *     with no server-side pagination or search controls today.
 *
 * Because the endpoint has no server-side filter API, the search /
 * filter controls below are honestly labelled as *local*: they apply to
 * the rows already returned, and never re-issue a parameterised
 * request. Adding server-side query parameters is a backend change, not
 * a UI workaround — the worst regression we could ship here would be a
 * search box that quietly searches only the first page.
 *
 * The detail drawer exposes the metadata returned by the list endpoint
 * (id, name, slug, country, default locale, reservation TTL, created /
 * updated / deleted timestamps), plus cross-tenant filtered shortcuts
 * to related collections that DO support `?org_id=<uuid>` filtering:
 *
 *   ✓ /orders   — /v1/admin/orders?org_id=<uuid>
 *   ✓ /tickets  — /v1/admin/tickets?org_id=<uuid>
 *   ✓ /refunds  — /v1/admin/refunds?org_id=<uuid>
 *
 * The following related-data links are intentionally rendered as
 * *backend-gap* states, because the corresponding API surfaces do not
 * exist (or are not yet exposed under /v1/admin) at the time of
 * writing:
 *
 *   ✗ Networks-by-organization (no /v1/admin/organizations/{id}/networks)
 *   ✗ Events-by-organization   (no /v1/admin/organizations/{id}/events)
 *   ✗ Users-by-organization    (no /v1/admin/organizations/{id}/users)
 *
 * Showing a disabled tile with a clear "backend gap" explanation is the
 * honest UX: it tells the operator the surface is conceptually expected
 * but no API exists yet. Linking to a 404 would be a regression.
 *
 * Permissions / scope:
 *   - Wrapped in <RequirePermission /> using the `organizations` nav
 *     entry, so direct URL navigation by an operator without
 *     `superadmin.read` resolves to the canonical Forbidden surface.
 *   - The active-scope kind must be `global` or `platform` (enforced by
 *     the same nav entry).
 *
 * Audit reason:
 *   - The request is fired through `authedFetch`, which detects
 *     `requiresAdminReason('/v1/admin/organizations')` and waits on the
 *     reason resolver before sending the request. If the operator
 *     cancels the reason prompt the query rejects with
 *     `superadmin.reason_required` and the body renders an inline
 *     prompt explaining how to recover (re-enter a reason and retry).
 *
 * Mock data: NONE. The page renders only what the backend returns.
 */
import { createRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/organizations",
  component: OrganizationsRoute,
});

// ---------------------------------------------------------------------------
// Response shape
//
// The backend constructs the response with map[string]any (see
// handleSuperadminListOrganizations); we model the fields we display.
// Unknown extra fields are tolerated by the structural type.
// ---------------------------------------------------------------------------

export interface AdminOrganization {
  readonly id: string;
  readonly name: string;
  readonly slug: string;
  readonly country: string;
  readonly default_locale: string;
  readonly reservation_ttl_seconds: number;
  readonly created_at: string;
  readonly updated_at: string;
  readonly deleted_at: string | null;
}

interface OrganizationsEnvelope {
  readonly organizations: readonly AdminOrganization[];
  readonly total: number;
}

const ORG_NAV_ENTRY = NAV_BY_PATH["/organizations"];
if (ORG_NAV_ENTRY === undefined) {
  throw new Error("organizations route: NAV_BY_PATH['/organizations'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function OrganizationsRoute() {
  return (
    <RequirePermission entry={ORG_NAV_ENTRY}>
      <OrganizationsExplorer />
    </RequirePermission>
  );
}

function OrganizationsExplorer() {
  const [filter, setFilter] = useState("");
  const [activeOrgId, setActiveOrgId] = useState<string | null>(null);
  const [includeDeleted, setIncludeDeleted] = useState(false);

  const query = useQuery<OrganizationsEnvelope, ApiError>({
    queryKey: ["admin", "organizations"],
    queryFn: () =>
      authedFetch<OrganizationsEnvelope>({
        method: "GET",
        path: "/v1/admin/organizations",
      }),
    // 401/403/reason-required must surface as states, not retry storms.
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (err.status === 401 || err.status === 403 || err.status === 0) {
          return false;
        }
        if (
          err.code === "superadmin.reason_required" ||
          err.code === "permissions.denied"
        ) {
          return false;
        }
      }
      return failureCount < 2;
    },
    refetchOnWindowFocus: false,
  });

  const rows = query.data?.organizations ?? [];
  const filtered = useMemo(
    () => filterRows(rows, filter, includeDeleted),
    [rows, filter, includeDeleted],
  );
  const activeOrg = useMemo(
    () => (activeOrgId === null ? null : rows.find((o) => o.id === activeOrgId) ?? null),
    [activeOrgId, rows],
  );

  return (
    <section aria-labelledby="orgs-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="orgs-heading" style={headingStyle}>
            Organizations
          </h1>
          <p style={subheadingStyle}>
            Cross-tenant directory of organizations. The list endpoint
            returns every organization in one response; the controls
            below filter <strong>locally</strong> — there is no server-side
            search API today.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={refreshButtonStyle}
            disabled={query.isFetching}
            data-testid="orgs-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </header>

      <div style={toolbarStyle}>
        <label style={searchLabelStyle}>
          <span style={visuallyHiddenStyle}>Filter organizations</span>
          <input
            type="search"
            placeholder="Filter by name, slug, country, locale, or id (local)"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            style={searchInputStyle}
            data-testid="orgs-filter"
            aria-label="Filter organizations locally"
          />
        </label>
        <label style={checkboxLabelStyle}>
          <input
            type="checkbox"
            checked={includeDeleted}
            onChange={(e) => setIncludeDeleted(e.target.checked)}
            data-testid="orgs-include-deleted"
          />
          <span>Show soft-deleted</span>
        </label>
        <div style={countStyle} data-testid="orgs-count" aria-live="polite">
          {renderCount(rows.length, filtered.length, query.isPending)}
        </div>
      </div>

      <OrganizationsBody
        query={query}
        rows={filtered}
        activeOrgId={activeOrgId}
        onOpen={setActiveOrgId}
      />

      {activeOrg !== null ? (
        <OrganizationDrawer
          org={activeOrg}
          onClose={() => setActiveOrgId(null)}
        />
      ) : null}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Local filter helpers
// ---------------------------------------------------------------------------

export function filterRows(
  rows: readonly AdminOrganization[],
  rawFilter: string,
  includeDeleted: boolean,
): readonly AdminOrganization[] {
  const visible = includeDeleted ? rows : rows.filter((o) => o.deleted_at === null);
  const needle = rawFilter.trim().toLowerCase();
  if (needle === "") {
    return visible;
  }
  return visible.filter((o) => {
    return (
      o.name.toLowerCase().includes(needle) ||
      o.slug.toLowerCase().includes(needle) ||
      o.country.toLowerCase().includes(needle) ||
      o.default_locale.toLowerCase().includes(needle) ||
      o.id.toLowerCase().includes(needle)
    );
  });
}

function renderCount(total: number, shown: number, pending: boolean): string {
  if (pending) {
    return "Loading…";
  }
  if (shown === total) {
    return `${total.toLocaleString()} organization${total === 1 ? "" : "s"}`;
  }
  return `${shown.toLocaleString()} of ${total.toLocaleString()} (local filter)`;
}

// ---------------------------------------------------------------------------
// Table body and states
// ---------------------------------------------------------------------------

interface BodyProps {
  query: ReturnType<typeof useQuery<OrganizationsEnvelope, ApiError>>;
  rows: readonly AdminOrganization[];
  activeOrgId: string | null;
  onOpen: (id: string) => void;
}

function OrganizationsBody({ query, rows, activeOrgId, onOpen }: BodyProps) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading organizations from /v1/admin/organizations…
      </div>
    );
  }
  if (query.isError) {
    return <OrgErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="orgs-empty">
        No organizations match the current filter.
      </div>
    );
  }
  return <OrganizationsTable rows={rows} activeOrgId={activeOrgId} onOpen={onOpen} />;
}

function OrganizationsTable({
  rows,
  activeOrgId,
  onOpen,
}: {
  rows: readonly AdminOrganization[];
  activeOrgId: string | null;
  onOpen: (id: string) => void;
}) {
  return (
    <div style={tableWrapStyle} role="region" aria-label="Organizations table">
      <table style={tableStyle} data-testid="orgs-table">
        <thead>
          <tr>
            <th scope="col" style={thStyle}>Name</th>
            <th scope="col" style={thStyle}>Slug</th>
            <th scope="col" style={thStyle}>Country</th>
            <th scope="col" style={thStyle}>Locale</th>
            <th scope="col" style={thStyle}>Reservation TTL</th>
            <th scope="col" style={thStyle}>Created</th>
            <th scope="col" style={thStyle}>Status</th>
            <th scope="col" style={thStyle} aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {rows.map((o) => {
            const isActive = o.id === activeOrgId;
            return (
              <tr
                key={o.id}
                style={isActive ? trActiveStyle : trStyle}
                data-testid={`orgs-row-${o.slug}`}
              >
                <td style={tdStyle}>
                  <button
                    type="button"
                    style={rowNameButtonStyle}
                    onClick={() => onOpen(o.id)}
                    aria-label={`Open details for ${o.name}`}
                  >
                    {o.name}
                  </button>
                </td>
                <td style={tdMonoStyle}>{o.slug}</td>
                <td style={tdStyle}>{o.country}</td>
                <td style={tdStyle}>{o.default_locale}</td>
                <td style={tdStyle}>
                  {formatDurationSeconds(o.reservation_ttl_seconds)}
                </td>
                <td style={tdStyle}>{formatDate(o.created_at)}</td>
                <td style={tdStyle}>
                  {o.deleted_at === null ? (
                    <span style={badgeActiveStyle}>active</span>
                  ) : (
                    <span style={badgeDeletedStyle}>soft-deleted</span>
                  )}
                </td>
                <td style={tdStyle}>
                  <button
                    type="button"
                    style={rowActionButtonStyle}
                    onClick={() => onOpen(o.id)}
                    data-testid={`orgs-open-${o.slug}`}
                  >
                    Details
                  </button>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function OrgErrorState({
  error,
  onRetry,
}: {
  error: ApiError | null;
  onRetry: () => void;
}) {
  if (error instanceof ApiError && error.code === "superadmin.reason_required") {
    return (
      <div style={errorBoxStyle} role="status" data-testid="orgs-reason-required">
        <strong>Audit reason required.</strong>
        <p style={errorParaStyle}>
          Cross-tenant reads require an <code>X-Admin-Reason</code>. Submit a
          reason in the prompt at the top of the screen and then retry.
        </p>
        <button type="button" style={errorRetryStyle} onClick={onRetry}>
          Retry
        </button>
      </div>
    );
  }
  if (
    error instanceof ApiError &&
    (error.status === 403 || error.code === "permissions.denied")
  ) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="orgs-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code>superadmin.read</code>. Ask a platform
          administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="orgs-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload organizations.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="orgs-error">
      <strong>Failed to load organizations.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Detail drawer
// ---------------------------------------------------------------------------

function OrganizationDrawer({
  org,
  onClose,
}: {
  org: AdminOrganization;
  onClose: () => void;
}) {
  return (
    <aside
      style={drawerWrapStyle}
      role="dialog"
      aria-modal="false"
      aria-labelledby="orgs-drawer-title"
      data-testid="orgs-drawer"
    >
      <header style={drawerHeaderStyle}>
        <div>
          <div style={drawerEyebrowStyle}>Organization</div>
          <h2 id="orgs-drawer-title" style={drawerTitleStyle}>
            {org.name}
          </h2>
        </div>
        <button
          type="button"
          onClick={onClose}
          style={drawerCloseStyle}
          aria-label="Close organization details"
          data-testid="orgs-drawer-close"
        >
          ×
        </button>
      </header>

      <section style={drawerSectionStyle} aria-labelledby="orgs-drawer-meta">
        <h3 id="orgs-drawer-meta" style={drawerSectionTitleStyle}>
          Metadata
        </h3>
        <dl style={metaListStyle}>
          <MetaRow k="ID" v={<code style={monoStyle}>{org.id}</code>} />
          <MetaRow k="Slug" v={<code style={monoStyle}>{org.slug}</code>} />
          <MetaRow k="Country" v={org.country} />
          <MetaRow k="Default locale" v={org.default_locale} />
          <MetaRow
            k="Reservation TTL"
            v={`${formatDurationSeconds(org.reservation_ttl_seconds)} (${org.reservation_ttl_seconds.toLocaleString()}s)`}
          />
          <MetaRow k="Created" v={formatDateTime(org.created_at)} />
          <MetaRow k="Updated" v={formatDateTime(org.updated_at)} />
          <MetaRow
            k="Deleted"
            v={
              org.deleted_at === null
                ? <span style={mutedStyle}>—</span>
                : formatDateTime(org.deleted_at)
            }
          />
        </dl>
      </section>

      <section style={drawerSectionStyle} aria-labelledby="orgs-drawer-related">
        <h3 id="orgs-drawer-related" style={drawerSectionTitleStyle}>
          Related data
        </h3>
        <p style={drawerHelpStyle}>
          Cross-tenant filtered shortcuts. Endpoints that support
          <code> ?org_id=&lt;uuid&gt; </code>
          filtering are linkable; collections without an admin endpoint
          are rendered as <em>backend gap</em> tiles.
        </p>
        <div style={relatedGridStyle}>
          <RelatedLink
            id="orders"
            label="Orders"
            to="/orders"
            search={{ org_id: org.id }}
            hint="GET /v1/admin/orders?org_id=…"
          />
          <RelatedLink
            id="tickets"
            label="Tickets"
            to="/tickets"
            search={{ org_id: org.id }}
            hint="GET /v1/admin/tickets?org_id=…"
          />
          <RelatedLink
            id="refunds"
            label="Refunds"
            to="/refunds"
            search={{ org_id: org.id }}
            hint="GET /v1/admin/refunds?org_id=…"
          />
          <BackendGapTile
            id="networks"
            label="Networks"
            reason="No /v1/admin/organizations/{id}/networks endpoint yet."
          />
          <BackendGapTile
            id="events"
            label="Events"
            reason="No /v1/admin/organizations/{id}/events endpoint yet."
          />
          <BackendGapTile
            id="users"
            label="Users"
            reason="No /v1/admin/organizations/{id}/users endpoint yet."
          />
        </div>
      </section>
    </aside>
  );
}

function MetaRow({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div style={metaRowStyle}>
      <dt style={metaKeyStyle}>{k}</dt>
      <dd style={metaValStyle}>{v}</dd>
    </div>
  );
}

interface RelatedLinkProps {
  id: string;
  label: string;
  to: "/orders" | "/tickets" | "/refunds";
  search: Record<string, string>;
  hint: string;
}

function RelatedLink({ id, label, to, search, hint }: RelatedLinkProps) {
  // TanStack Router only types the routes that have a dedicated `Route`
  // export; the guarded placeholder routes (/orders /tickets /refunds)
  // are generated dynamically in `routes/guarded.tsx` and so are absent
  // from the typed `to` union. We narrow with `as "/"` so the typed
  // <Link> still works, mirroring the pattern used in routes/index.tsx
  // for the dashboard shortcut tiles. Search params are forwarded as a
  // structural record.
  return (
    <Link
      to={to as "/"}
      search={search as unknown as Record<string, never>}
      style={relatedTileStyle}
      data-testid={`orgs-related-${id}`}
      title={hint}
    >
      <span style={relatedTileLabelStyle}>{label}</span>
      <span style={relatedTileHintStyle}>{hint}</span>
    </Link>
  );
}

function BackendGapTile({
  id,
  label,
  reason,
}: {
  id: string;
  label: string;
  reason: string;
}) {
  return (
    <div
      style={relatedTileDisabledStyle}
      role="note"
      aria-disabled="true"
      data-testid={`orgs-related-gap-${id}`}
      title={reason}
    >
      <span style={relatedTileLabelStyle}>{label}</span>
      <span style={relatedTileGapBadgeStyle}>backend gap</span>
      <span style={relatedTileHintStyle}>{reason}</span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Format helpers
// ---------------------------------------------------------------------------

export function formatDurationSeconds(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return "—";
  }
  if (seconds < 60) {
    return `${seconds}s`;
  }
  if (seconds < 3600) {
    const m = Math.floor(seconds / 60);
    const s = seconds % 60;
    return s === 0 ? `${m}m` : `${m}m ${s}s`;
  }
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return m === 0 ? `${h}h` : `${h}h ${m}m`;
}

function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return d.toISOString().slice(0, 10);
}

function formatDateTime(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  // YYYY-MM-DD HH:MMZ — short, sortable, unambiguous.
  return `${d.toISOString().slice(0, 16).replace("T", " ")}Z`;
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
};

const headerStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 16,
  flexWrap: "wrap",
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

const refreshWrapStyle: CSSProperties = { display: "flex", gap: 8 };
const refreshButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const toolbarStyle: CSSProperties = {
  display: "flex",
  gap: 12,
  alignItems: "center",
  flexWrap: "wrap",
  padding: "8px 12px",
  background: "#f8fafc",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
};

const searchLabelStyle: CSSProperties = { flex: "1 1 280px" };
const searchInputStyle: CSSProperties = {
  width: "100%",
  fontSize: 13,
  padding: "8px 10px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};
const visuallyHiddenStyle: CSSProperties = {
  position: "absolute",
  width: 1,
  height: 1,
  overflow: "hidden",
  clip: "rect(0 0 0 0)",
  whiteSpace: "nowrap",
};

const checkboxLabelStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  gap: 6,
  fontSize: 12,
  color: "#475569",
};

const countStyle: CSSProperties = {
  marginLeft: "auto",
  fontSize: 12,
  color: "#475569",
  fontVariantNumeric: "tabular-nums",
};

const tableWrapStyle: CSSProperties = {
  overflowX: "auto",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
};

const tableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 13,
};

const thStyle: CSSProperties = {
  textAlign: "left",
  padding: "10px 12px",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const trStyle: CSSProperties = {};
const trActiveStyle: CSSProperties = { background: "#eff6ff" };

const tdStyle: CSSProperties = {
  padding: "10px 12px",
  borderBottom: "1px solid #f1f5f9",
  color: "#0f172a",
  verticalAlign: "top",
};
const tdMonoStyle: CSSProperties = {
  ...tdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
  color: "#334155",
};

const rowNameButtonStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  padding: 0,
  color: "#0369a1",
  fontSize: 13,
  fontWeight: 500,
  cursor: "pointer",
  textAlign: "left",
};

const rowActionButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const badgeActiveStyle: CSSProperties = {
  fontSize: 10,
  padding: "2px 6px",
  borderRadius: 999,
  background: "#dcfce7",
  color: "#166534",
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
};
const badgeDeletedStyle: CSSProperties = {
  fontSize: 10,
  padding: "2px 6px",
  borderRadius: 999,
  background: "#fee2e2",
  color: "#7f1d1d",
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const statusBoxStyle: CSSProperties = {
  padding: 16,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
};

const errorBoxStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  padding: 16,
  border: "1px solid #fca5a5",
  borderRadius: 6,
  background: "#fef2f2",
  color: "#7f1d1d",
  fontSize: 12,
};
const errorParaStyle: CSSProperties = { margin: 0, fontSize: 12 };
const errorCodeStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};
const errorRetryStyle: CSSProperties = {
  alignSelf: "flex-start",
  fontSize: 12,
  padding: "6px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const drawerWrapStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
  padding: 16,
  border: "1px solid #e2e8f0",
  borderRadius: 8,
  background: "#ffffff",
};

const drawerHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 12,
};

const drawerEyebrowStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  textTransform: "uppercase",
  letterSpacing: 0.5,
};
const drawerTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 18,
  fontWeight: 600,
  color: "#0f172a",
};
const drawerCloseStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  fontSize: 24,
  lineHeight: 1,
  cursor: "pointer",
  color: "#64748b",
  padding: "0 4px",
};

const drawerSectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
};

const drawerSectionTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  fontWeight: 600,
  color: "#334155",
  textTransform: "uppercase",
  letterSpacing: 0.5,
};

const drawerHelpStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#475569",
  lineHeight: 1.45,
};

const metaListStyle: CSSProperties = {
  margin: 0,
  display: "grid",
  gridTemplateColumns: "minmax(140px, max-content) 1fr",
  rowGap: 6,
  columnGap: 12,
  fontSize: 12,
};
const metaRowStyle: CSSProperties = { display: "contents" };
const metaKeyStyle: CSSProperties = { margin: 0, color: "#64748b" };
const metaValStyle: CSSProperties = { margin: 0, color: "#0f172a", wordBreak: "break-word" };
const monoStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};
const mutedStyle: CSSProperties = { color: "#94a3b8" };

const relatedGridStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))",
  gap: 8,
};
const relatedTileStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: "10px 12px",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
  textDecoration: "none",
  color: "#0f172a",
};
const relatedTileDisabledStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
  padding: "10px 12px",
  borderRadius: 6,
  background: "#f8fafc",
  border: "1px dashed #cbd5e1",
  color: "#475569",
};
const relatedTileLabelStyle: CSSProperties = { fontSize: 13, fontWeight: 600 };
const relatedTileHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  lineHeight: 1.4,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
};
const relatedTileGapBadgeStyle: CSSProperties = {
  alignSelf: "flex-start",
  fontSize: 10,
  padding: "2px 6px",
  borderRadius: 999,
  background: "#fef3c7",
  color: "#78350f",
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
};
