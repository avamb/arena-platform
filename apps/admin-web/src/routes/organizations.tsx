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
 * The detail drawer (feature #240) exposes the metadata returned by the
 * list endpoint inside an OVERVIEW tab, and pivots all per-organization
 * related-data surfaces into real tabs scoped on `org_id`:
 *
 *   ✓ Overview — metadata (id, name, slug, country, default locale,
 *                reservation TTL, created / updated / deleted timestamps)
 *   ✓ Users    — GET /v1/admin/organizations/{org_id}/members
 *   ✓ Venues   — GET /v1/venues (client-filtered by org_id)
 *   ✓ Channels — GET /v1/organizations/{org_id}/channels
 *   ✓ Payments — GET /v1/organizations/{org_id}/payment-configs
 *
 * The previous "backend gap" tiles (Networks, Events, Users) and the
 * cross-tenant shortcut tiles (Orders, Tickets, Refunds) were honest
 * but read-only links; tabs let the operator stay inside the
 * organization context. Cross-tenant collections (Orders / Tickets /
 * Refunds) remain reachable through the top-level nav with an
 * `?org_id=` filter and are intentionally not duplicated here.
 *
 * The active drawer tab is part of the page state and reflected to the
 * URL hash (e.g. `#org=<uuid>&tab=users`) so a refresh restores the
 * same drawer + tab the operator was looking at.
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
import { createRoute } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { useAuth } from "@/lib/auth/useAuth";
import {
  useEscapeClose,
  useFocusOnMount,
  useFocusRestore,
} from "@/lib/a11y";

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
  const { permissions } = useAuth();
  const canCreate = permissions.has("org.create");
  const canUpdate = permissions.has("org.update");
  const canArchive = permissions.has("org.delete");
  const [filter, setFilter] = useState("");
  const [editOrgId, setEditOrgId] = useState<string | null>(null);
  const [archiveOrgId, setArchiveOrgId] = useState<string | null>(null);
  // SAUI-#240: activeOrgId + activeTab are reflected to the URL hash so a
  // refresh restores the same drawer + tab the operator was looking at.
  const initialHash = parseDrawerHash(
    typeof window === "undefined" ? "" : window.location.hash,
  );
  const [activeOrgId, setActiveOrgId] = useState<string | null>(initialHash.org);
  const [activeTab, setActiveTab] = useState<DrawerTabKey>(initialHash.tab);
  const [includeDeleted, setIncludeDeleted] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);

  useEffect(() => {
    if (typeof window === "undefined") {
      return;
    }
    const next = serializeDrawerHash(activeOrgId, activeTab);
    const current = window.location.hash;
    if (next !== current) {
      window.history.replaceState(null, "", `${window.location.pathname}${window.location.search}${next}`);
    }
  }, [activeOrgId, activeTab]);

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
  const editOrg = useMemo(
    () => (editOrgId === null ? null : rows.find((o) => o.id === editOrgId) ?? null),
    [editOrgId, rows],
  );
  const archiveOrg = useMemo(
    () => (archiveOrgId === null ? null : rows.find((o) => o.id === archiveOrgId) ?? null),
    [archiveOrgId, rows],
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
          {canCreate ? (
            <button
              type="button"
              onClick={() => setCreateOpen(true)}
              style={primaryButtonStyle}
              data-testid="orgs-create-open"
            >
              Create organization
            </button>
          ) : (
            <span style={mutedHintStyle} title="Requires org.create">
              Create requires org.create
            </span>
          )}
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
        canUpdate={canUpdate}
        canArchive={canArchive}
        onEdit={setEditOrgId}
        onArchive={setArchiveOrgId}
      />

      {activeOrg !== null ? (
        <OrganizationDrawer
          org={activeOrg}
          activeTab={activeTab}
          onTabChange={setActiveTab}
          onClose={() => setActiveOrgId(null)}
        />
      ) : null}

      {createOpen ? (
        <CreateOrganizationDialog onClose={() => setCreateOpen(false)} />
      ) : null}

      {editOrg !== null ? (
        <EditOrganizationDialog
          org={editOrg}
          onClose={() => setEditOrgId(null)}
        />
      ) : null}

      {archiveOrg !== null ? (
        <ArchiveOrganizationDialog
          org={archiveOrg}
          onClose={() => setArchiveOrgId(null)}
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
  canUpdate: boolean;
  canArchive: boolean;
  onEdit: (id: string) => void;
  onArchive: (id: string) => void;
}

function OrganizationsBody({
  query,
  rows,
  activeOrgId,
  onOpen,
  canUpdate,
  canArchive,
  onEdit,
  onArchive,
}: BodyProps) {
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
  return (
    <OrganizationsTable
      rows={rows}
      activeOrgId={activeOrgId}
      onOpen={onOpen}
      canUpdate={canUpdate}
      canArchive={canArchive}
      onEdit={onEdit}
      onArchive={onArchive}
    />
  );
}

function OrganizationsTable({
  rows,
  activeOrgId,
  onOpen,
  canUpdate,
  canArchive,
  onEdit,
  onArchive,
}: {
  rows: readonly AdminOrganization[];
  activeOrgId: string | null;
  onOpen: (id: string) => void;
  canUpdate: boolean;
  canArchive: boolean;
  onEdit: (id: string) => void;
  onArchive: (id: string) => void;
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
                  <div style={rowActionsCellStyle}>
                    <button
                      type="button"
                      style={rowActionButtonStyle}
                      onClick={() => onOpen(o.id)}
                      data-testid={`orgs-open-${o.slug}`}
                    >
                      Details
                    </button>
                    {canUpdate ? (
                      <button
                        type="button"
                        style={rowActionButtonStyle}
                        onClick={() => onEdit(o.id)}
                        disabled={o.deleted_at !== null}
                        title={
                          o.deleted_at !== null
                            ? "Soft-deleted organizations cannot be edited"
                            : "Edit organization"
                        }
                        data-testid={`orgs-edit-${o.slug}`}
                      >
                        Edit
                      </button>
                    ) : null}
                    {canArchive ? (
                      <button
                        type="button"
                        style={rowActionDangerStyle}
                        onClick={() => onArchive(o.id)}
                        disabled={o.deleted_at !== null}
                        title={
                          o.deleted_at !== null
                            ? "Organization is already archived"
                            : "Archive organization (soft-delete)"
                        }
                        data-testid={`orgs-archive-${o.slug}`}
                      >
                        Archive
                      </button>
                    ) : null}
                  </div>
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

// ---------------------------------------------------------------------------
// Drawer tab model (feature #240)
//
// The drawer pivots from per-row tiles to real tabs scoped on org_id.
// Each tab is rendered by a small panel component below that fetches
// the tab-specific data when (and only when) it becomes active.
// ---------------------------------------------------------------------------

export const DRAWER_TAB_KEYS = [
  "overview",
  "users",
  "venues",
  "channels",
  "payments",
] as const;

export type DrawerTabKey = (typeof DRAWER_TAB_KEYS)[number];

const DRAWER_TAB_LABELS: Record<DrawerTabKey, string> = {
  overview: "Overview",
  users: "Users",
  venues: "Venues",
  channels: "Channels",
  payments: "Payments",
};

/**
 * Coerce an unknown value (URL hash, query param, persisted state) into a
 * legal DrawerTabKey. Falls back to "overview" on any unknown / missing
 * input so a stale link can never crash the drawer.
 */
export function parseDrawerTab(raw: unknown): DrawerTabKey {
  if (typeof raw !== "string") {
    return "overview";
  }
  const lc = raw.toLowerCase();
  return (DRAWER_TAB_KEYS as readonly string[]).includes(lc)
    ? (lc as DrawerTabKey)
    : "overview";
}

/**
 * Parse a `#org=<uuid>&tab=<key>` style URL hash into a structured
 * `{org, tab}` pair. Unknown fields are tolerated; missing org → null,
 * missing/unknown tab → "overview".
 */
export function parseDrawerHash(rawHash: string): {
  org: string | null;
  tab: DrawerTabKey;
} {
  const trimmed = rawHash.startsWith("#") ? rawHash.slice(1) : rawHash;
  if (trimmed === "") {
    return { org: null, tab: "overview" };
  }
  const params = new URLSearchParams(trimmed);
  const orgRaw = params.get("org");
  const tabRaw = params.get("tab");
  // UUID-ish: alphanumerics + dashes, 8..64 chars. We don't strictly
  // validate UUIDv7 here — the explorer simply ignores an unknown id
  // when it doesn't match a fetched row.
  const org =
    orgRaw !== null && /^[0-9a-fA-F-]{8,64}$/.test(orgRaw) ? orgRaw : null;
  return { org, tab: parseDrawerTab(tabRaw) };
}

/**
 * Inverse of parseDrawerHash. Returns an empty string when there is
 * nothing to record (no drawer open and the default tab is selected) so
 * the URL stays clean.
 */
export function serializeDrawerHash(
  orgId: string | null,
  tab: DrawerTabKey,
): string {
  if (orgId === null) {
    return "";
  }
  const params = new URLSearchParams();
  params.set("org", orgId);
  if (tab !== "overview") {
    params.set("tab", tab);
  }
  return `#${params.toString()}`;
}

function OrganizationDrawer({
  org,
  activeTab,
  onTabChange,
  onClose,
}: {
  org: AdminOrganization;
  activeTab: DrawerTabKey;
  onTabChange: (tab: DrawerTabKey) => void;
  onClose: () => void;
}) {
  // SAUI-13: Escape closes, focus lands on close, focus restores on unmount.
  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, closeRef);
  useFocusRestore(true);
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
          ref={closeRef}
          onClick={onClose}
          style={drawerCloseStyle}
          aria-label="Close organization details"
          data-testid="orgs-drawer-close"
          title="Close (Esc)"
        >
          ×
        </button>
      </header>

      <div
        role="tablist"
        aria-label="Organization sections"
        style={tabListStyle}
        data-testid="orgs-drawer-tablist"
      >
        {DRAWER_TAB_KEYS.map((key) => {
          const selected = key === activeTab;
          return (
            <button
              type="button"
              key={key}
              role="tab"
              id={`orgs-drawer-tab-${key}`}
              aria-selected={selected}
              aria-controls={`orgs-drawer-panel-${key}`}
              tabIndex={selected ? 0 : -1}
              onClick={() => onTabChange(key)}
              style={selected ? tabButtonActiveStyle : tabButtonStyle}
              data-testid={`orgs-drawer-tab-${key}`}
            >
              {DRAWER_TAB_LABELS[key]}
            </button>
          );
        })}
      </div>

      <section
        role="tabpanel"
        id={`orgs-drawer-panel-${activeTab}`}
        aria-labelledby={`orgs-drawer-tab-${activeTab}`}
        style={drawerSectionStyle}
        data-testid={`orgs-drawer-panel-${activeTab}`}
      >
        {activeTab === "overview" ? <OverviewTab org={org} /> : null}
        {activeTab === "users" ? <UsersTab org={org} /> : null}
        {activeTab === "venues" ? <VenuesTab org={org} /> : null}
        {activeTab === "channels" ? <ChannelsTab org={org} /> : null}
        {activeTab === "payments" ? <PaymentsTab org={org} /> : null}
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

// ---------------------------------------------------------------------------
// Tab panels (feature #240)
//
// Each panel renders only when its tab is active. We use react-query so
// the fetch is cached, deduplicated, and survives tab switches without
// re-issuing the request.
// ---------------------------------------------------------------------------

function OverviewTab({ org }: { org: AdminOrganization }) {
  return (
    <>
      <h3 style={drawerSectionTitleStyle}>Metadata</h3>
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
            org.deleted_at === null ? (
              <span style={mutedStyle}>—</span>
            ) : (
              formatDateTime(org.deleted_at)
            )
          }
        />
      </dl>
    </>
  );
}

interface MembershipResponse {
  readonly id: string;
  readonly user_id: string;
  readonly org_id: string;
  readonly role: string;
  readonly status: string;
  readonly joined_at: string;
}
interface MembershipsEnvelope {
  readonly memberships: readonly MembershipResponse[];
}
interface MembershipEnvelope {
  readonly membership: MembershipResponse;
}

/**
 * Canonical list of membership roles surfaced by the admin Users tab
 * (feature #241). Mirrors the `enum` published by the backend OpenAPI
 * contract for AdminAddMemberRequest / AdminChangeMemberRoleRequest
 * (apps/backend/openapi/openapi.yaml) which is itself a mirror of the
 * memberships_role_check CHECK constraint in migration 0011_memberships.sql
 * as extended by 0042_network_operator_role.sql.
 *
 * The list is intentionally hard-coded rather than fetched at runtime so
 * the dropdown renders synchronously and the type checker can guarantee
 * exhaustive switch coverage on the union. If the backend adds a new
 * role, this list and the OpenAPI enum must be updated together.
 */
export const MEMBERSHIP_ROLES = [
  "organizer",
  "agent",
  "platform_operator",
  "external_ticketing_operator",
  "platform_superadmin",
  "network_operator",
] as const;

export type MembershipRole = (typeof MEMBERSHIP_ROLES)[number];

export function isMembershipRole(value: unknown): value is MembershipRole {
  return (
    typeof value === "string" &&
    (MEMBERSHIP_ROLES as readonly string[]).includes(value)
  );
}

const MEMBERSHIP_ROLE_LABELS: Record<MembershipRole, string> = {
  organizer: "Organizer",
  agent: "Agent",
  platform_operator: "Platform operator",
  external_ticketing_operator: "External ticketing operator",
  platform_superadmin: "Platform superadmin",
  network_operator: "Network operator",
};

export function formatMembershipRole(role: string): string {
  return isMembershipRole(role) ? MEMBERSHIP_ROLE_LABELS[role] : role;
}

const UUID_RE =
  /^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/;
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/**
 * Validate the "user" field of the add-member form. The operator may
 * type either a UUIDv7 user_id OR an email address; the backend resolves
 * email -> user_id via GetUserByEmail. Whitespace is ignored.
 */
export function validateMemberUserInput(raw: string): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return "Enter a user_id (UUID) or email address";
  }
  if (UUID_RE.test(trimmed)) {
    return null;
  }
  if (EMAIL_RE.test(trimmed)) {
    return null;
  }
  return "Must be a UUID or a valid email address";
}

/**
 * Decide whether the operator typed a UUID or an email. Returns the
 * canonical request body shape for
 * POST /v1/admin/organizations/{org_id}/members.
 */
export function buildAddMemberBody(
  userInput: string,
  role: MembershipRole,
):
  | { user_id: string; role: MembershipRole }
  | { email: string; role: MembershipRole } {
  const trimmed = userInput.trim();
  if (UUID_RE.test(trimmed)) {
    return { user_id: trimmed, role };
  }
  return { email: trimmed.toLowerCase(), role };
}

export interface AddMemberFieldErrors {
  user?: string;
  role?: string;
  form?: string;
}

/**
 * Map an error envelope from admin_memberships.go onto field-level
 * errors. The backend emits `details.field` for invalid_role /
 * user_not_found / invalid_user_id; missing_user / ambiguous_user use
 * `details.fields`. Duplicate / reference errors land on the form
 * surface so the operator sees a single coherent message.
 */
export function mapAddMemberServerError(err: ApiError): AddMemberFieldErrors {
  const details = err.details ?? {};
  const field = typeof details.field === "string" ? details.field : undefined;
  switch (err.code) {
    case "admin_membership.invalid_role":
      return { role: err.message };
    case "admin_membership.user_not_found":
      return { user: err.message };
    case "admin_membership.invalid_user_id":
      return { user: err.message };
    case "admin_membership.missing_user":
    case "admin_membership.ambiguous_user":
      return { user: err.message };
    case "admin_membership.duplicate":
      return { form: err.message };
    case "admin_membership.invalid_reference":
      return { user: err.message };
    case "admin_membership.empty_body":
    case "admin_membership.invalid_body":
    case "admin_membership.invalid_json":
      return { form: err.message };
    case "permissions.denied":
      return {
        form: "Your account is missing membership.grant. Ask a platform administrator.",
      };
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      return {
        form: "An audit reason (X-Admin-Reason) is required. Submit a reason and retry.",
      };
    default:
      if (field === "role") {
        return { role: err.message };
      }
      if (field === "user_id" || field === "email" || field === "user") {
        return { user: err.message };
      }
      return { form: `${err.message} (${err.code})` };
  }
}

/**
 * Map errors from PATCH (change role) / DELETE (deactivate) onto a
 * single string. These flows are inline (no dedicated dialog), so we
 * collapse the envelope into the row-level toast surface.
 */
export function mapMembershipMutationError(err: ApiError): string {
  switch (err.code) {
    case "admin_membership.invalid_role":
      return err.message;
    case "admin_membership.duplicate":
      return "User already holds the requested role.";
    case "admin_membership.not_found":
      return "Membership not found or already revoked.";
    case "permissions.denied":
      return "Permission denied (membership.grant / membership.revoke).";
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      return "An audit reason (X-Admin-Reason) is required. Submit a reason and retry.";
    default:
      return `${err.message} (${err.code})`;
  }
}

function UsersTab({ org }: { org: AdminOrganization }) {
  const { permissions } = useAuth();
  const queryClient = useQueryClient();
  const canRead =
    permissions.has("membership.read") || permissions.has("superadmin.read");
  const canGrant =
    permissions.has("membership.grant") || permissions.has("superadmin.read");
  const canRevoke =
    permissions.has("membership.revoke") || permissions.has("superadmin.read");

  const [addOpen, setAddOpen] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [rowError, setRowError] = useState<{ id: string; message: string } | null>(null);

  const query = useQuery<MembershipsEnvelope, ApiError>({
    queryKey: ["admin", "organizations", org.id, "members"],
    queryFn: () =>
      authedFetch<MembershipsEnvelope>({
        method: "GET",
        path: `/v1/admin/organizations/${org.id}/members`,
      }),
    enabled: canRead,
    retry: (count, err) =>
      err instanceof ApiError && (err.status === 401 || err.status === 403)
        ? false
        : count < 2,
    refetchOnWindowFocus: false,
  });

  function invalidate() {
    queryClient.invalidateQueries({
      queryKey: ["admin", "organizations", org.id, "members"],
    });
  }

  const changeRole = useMutation<
    MembershipEnvelope,
    ApiError,
    { id: string; role: MembershipRole }
  >({
    mutationFn: ({ id, role }) =>
      authedFetch<MembershipEnvelope>({
        method: "PATCH",
        path: `/v1/admin/organizations/${org.id}/members/${id}`,
        body: { role },
      }),
    onSuccess: () => {
      setEditingId(null);
      setRowError(null);
      invalidate();
    },
    onError: (err, vars) => {
      setRowError({ id: vars.id, message: mapMembershipMutationError(err) });
    },
  });

  const deactivate = useMutation<unknown, ApiError, { id: string }>({
    mutationFn: ({ id }) =>
      authedFetch<unknown>({
        method: "DELETE",
        path: `/v1/admin/organizations/${org.id}/members/${id}`,
      }),
    onSuccess: () => {
      setRowError(null);
      invalidate();
    },
    onError: (err, vars) => {
      setRowError({ id: vars.id, message: mapMembershipMutationError(err) });
    },
  });

  return (
    <>
      <div style={tabHeaderStyle}>
        <h3 style={drawerSectionTitleStyle}>Users (memberships)</h3>
        {canGrant ? (
          <button
            type="button"
            style={smallPrimaryButtonStyle}
            onClick={() => setAddOpen(true)}
            data-testid="orgs-drawer-users-add-open"
          >
            Add member
          </button>
        ) : (
          <span style={mutedHintStyle} title="Requires membership.grant">
            Add requires membership.grant
          </span>
        )}
      </div>
      <p style={drawerHelpStyle}>
        <code>GET /v1/admin/organizations/{org.id}/members</code>
      </p>
      {!canRead ? (
        <TabForbidden
          missing="membership.read"
          testid="orgs-drawer-users-forbidden"
        />
      ) : query.isPending ? (
        <div style={tabStatusStyle} role="status">Loading memberships…</div>
      ) : query.isError ? (
        <TabError
          error={query.error}
          retry={() => query.refetch()}
          testid="orgs-drawer-users-error"
        />
      ) : query.data.memberships.length === 0 ? (
        <div style={tabStatusStyle} data-testid="orgs-drawer-users-empty">
          No active memberships for this organization.
        </div>
      ) : (
        <div style={tabTableWrapStyle} data-testid="orgs-drawer-users-table">
          <table style={tabTableStyle}>
            <thead>
              <tr>
                <th scope="col" style={tabThStyle}>User</th>
                <th scope="col" style={tabThStyle}>Role</th>
                <th scope="col" style={tabThStyle}>Status</th>
                <th scope="col" style={tabThStyle}>Joined</th>
                <th scope="col" style={tabThStyle} aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {query.data.memberships.map((m) => {
                const isEditing = editingId === m.id;
                const rowErr = rowError?.id === m.id ? rowError.message : null;
                return (
                  <tr key={m.id} data-testid={`orgs-drawer-users-row-${m.id}`}>
                    <td style={tabTdMonoStyle}>{m.user_id}</td>
                    <td style={tabTdStyle}>
                      {isEditing && canGrant ? (
                        <RoleEditor
                          initial={m.role}
                          disabled={changeRole.isPending}
                          onSave={(role) => changeRole.mutate({ id: m.id, role })}
                          onCancel={() => {
                            setEditingId(null);
                            setRowError(null);
                          }}
                        />
                      ) : (
                        <span data-testid={`orgs-drawer-users-role-${m.id}`}>
                          {formatMembershipRole(m.role)}
                        </span>
                      )}
                      {rowErr !== null ? (
                        <div
                          style={fieldErrorStyle}
                          role="alert"
                          data-testid={`orgs-drawer-users-row-error-${m.id}`}
                        >
                          {rowErr}
                        </div>
                      ) : null}
                    </td>
                    <td style={tabTdStyle}>{m.status}</td>
                    <td style={tabTdStyle}>{formatDateTime(m.joined_at)}</td>
                    <td style={tabTdStyle}>
                      <div style={rowActionsStyle}>
                        {canGrant && !isEditing ? (
                          <button
                            type="button"
                            style={tabRowButtonStyle}
                            onClick={() => {
                              setEditingId(m.id);
                              setRowError(null);
                            }}
                            data-testid={`orgs-drawer-users-edit-${m.id}`}
                            disabled={
                              changeRole.isPending || deactivate.isPending
                            }
                          >
                            Change role
                          </button>
                        ) : null}
                        {canRevoke && m.status === "active" ? (
                          <button
                            type="button"
                            style={tabRowDangerStyle}
                            onClick={() => {
                              if (
                                typeof window !== "undefined" &&
                                window.confirm(
                                  `Revoke ${formatMembershipRole(m.role)} membership for user ${m.user_id}?`,
                                )
                              ) {
                                deactivate.mutate({ id: m.id });
                              }
                            }}
                            data-testid={`orgs-drawer-users-revoke-${m.id}`}
                            disabled={deactivate.isPending}
                          >
                            {deactivate.isPending &&
                            deactivate.variables?.id === m.id
                              ? "Revoking…"
                              : "Revoke"}
                          </button>
                        ) : null}
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {addOpen ? (
        <AddMemberDialog
          orgId={org.id}
          onClose={() => setAddOpen(false)}
          onCreated={() => {
            invalidate();
            setAddOpen(false);
          }}
        />
      ) : null}
    </>
  );
}

function RoleEditor({
  initial,
  disabled,
  onSave,
  onCancel,
}: {
  initial: string;
  disabled: boolean;
  onSave: (role: MembershipRole) => void;
  onCancel: () => void;
}) {
  const [value, setValue] = useState<MembershipRole>(
    isMembershipRole(initial) ? initial : "organizer",
  );
  const dirty = value !== initial;
  return (
    <div style={roleEditorStyle}>
      <select
        value={value}
        onChange={(e) => setValue(e.target.value as MembershipRole)}
        disabled={disabled}
        style={inputStyle}
        data-testid="orgs-drawer-users-role-select"
        aria-label="Membership role"
      >
        {MEMBERSHIP_ROLES.map((r) => (
          <option key={r} value={r}>
            {MEMBERSHIP_ROLE_LABELS[r]}
          </option>
        ))}
      </select>
      <button
        type="button"
        style={smallPrimaryButtonStyle}
        onClick={() => onSave(value)}
        disabled={disabled || !dirty}
        data-testid="orgs-drawer-users-role-save"
      >
        {disabled ? "Saving…" : "Save"}
      </button>
      <button
        type="button"
        style={tabRowButtonStyle}
        onClick={onCancel}
        disabled={disabled}
        data-testid="orgs-drawer-users-role-cancel"
      >
        Cancel
      </button>
    </div>
  );
}

function AddMemberDialog({
  orgId,
  onClose,
  onCreated,
}: {
  orgId: string;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [userInput, setUserInput] = useState("");
  const [role, setRole] = useState<MembershipRole>("organizer");
  const [serverErrors, setServerErrors] = useState<AddMemberFieldErrors>({});

  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, closeRef);
  useFocusRestore(true);

  const userErr = userInput.length > 0 ? validateMemberUserInput(userInput) : null;
  const localValid = validateMemberUserInput(userInput) === null;

  const mutation = useMutation<MembershipEnvelope, ApiError, void>({
    mutationFn: () =>
      authedFetch<MembershipEnvelope>({
        method: "POST",
        path: `/v1/admin/organizations/${orgId}/members`,
        body: buildAddMemberBody(userInput, role),
      }),
    onSuccess: () => {
      onCreated();
    },
    onError: (err) => {
      setServerErrors(mapAddMemberServerError(err));
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerErrors({});
    if (!localValid) {
      return;
    }
    mutation.mutate();
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="orgs-add-member-title"
      style={dialogBackdropStyle}
      data-testid="orgs-drawer-users-add-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="orgs-add-member-title" style={dialogTitleStyle}>
            Add member
          </h2>
          <button
            type="button"
            ref={closeRef}
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="orgs-drawer-users-add-close"
          >
            ×
          </button>
        </header>
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          <FieldRow
            label="User (user_id or email)"
            htmlFor="orgs-add-member-user"
            error={serverErrors.user ?? null}
            localError={userErr}
            hint="Enter a UUIDv7 user_id, or an email address — backend resolves email → user via GetUserByEmail."
          >
            <input
              id="orgs-add-member-user"
              type="text"
              value={userInput}
              onChange={(e) => {
                setUserInput(e.target.value);
                if (serverErrors.user !== undefined) {
                  setServerErrors({ ...serverErrors, user: undefined });
                }
              }}
              style={inputMonoStyle}
              required
              autoFocus
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="orgs-drawer-users-add-user"
            />
          </FieldRow>
          <FieldRow
            label="Role"
            htmlFor="orgs-add-member-role"
            error={serverErrors.role ?? null}
            localError={null}
            hint="Roles are enforced by the memberships_role_check CHECK constraint."
          >
            <select
              id="orgs-add-member-role"
              value={role}
              onChange={(e) => {
                setRole(e.target.value as MembershipRole);
                if (serverErrors.role !== undefined) {
                  setServerErrors({ ...serverErrors, role: undefined });
                }
              }}
              style={inputStyle}
              data-testid="orgs-drawer-users-add-role"
            >
              {MEMBERSHIP_ROLES.map((r) => (
                <option key={r} value={r}>
                  {MEMBERSHIP_ROLE_LABELS[r]}
                </option>
              ))}
            </select>
          </FieldRow>

          {serverErrors.form !== undefined ? (
            <div
              style={formErrorStyle}
              role="alert"
              data-testid="orgs-drawer-users-add-error"
            >
              {serverErrors.form}
            </div>
          ) : null}

          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="orgs-drawer-users-add-cancel"
            >
              Cancel
            </button>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={!localValid || mutation.isPending}
              data-testid="orgs-drawer-users-add-submit"
            >
              {mutation.isPending ? "Adding…" : "Add member"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

interface VenueResponse {
  readonly id: string;
  readonly org_id: string;
  readonly name: string;
  readonly slug?: string;
  readonly timezone?: string;
  readonly deleted_at?: string | null;
}
interface VenuesEnvelope {
  readonly venues: readonly VenueResponse[];
}

function VenuesTab({ org }: { org: AdminOrganization }) {
  const { permissions } = useAuth();
  const canRead = permissions.has("venue.read") || permissions.has("superadmin.read");
  const query = useQuery<VenuesEnvelope, ApiError>({
    queryKey: ["admin", "organizations", org.id, "venues"],
    queryFn: () =>
      authedFetch<VenuesEnvelope>({ method: "GET", path: "/v1/venues" }),
    enabled: canRead,
    retry: (count, err) =>
      err instanceof ApiError && (err.status === 401 || err.status === 403)
        ? false
        : count < 2,
    refetchOnWindowFocus: false,
  });
  const venues = (query.data?.venues ?? []).filter((v) => v.org_id === org.id);

  return (
    <>
      <h3 style={drawerSectionTitleStyle}>Venues</h3>
      <p style={drawerHelpStyle}>
        <code>GET /v1/venues</code> — filtered to org_id <code>{org.id}</code>{" "}
        client-side.
      </p>
      {!canRead ? (
        <TabForbidden missing="venue.read" testid="orgs-drawer-venues-forbidden" />
      ) : query.isPending ? (
        <div style={tabStatusStyle} role="status">Loading venues…</div>
      ) : query.isError ? (
        <TabError
          error={query.error}
          retry={() => query.refetch()}
          testid="orgs-drawer-venues-error"
        />
      ) : venues.length === 0 ? (
        <div style={tabStatusStyle} data-testid="orgs-drawer-venues-empty">
          No venues registered for this organization.
        </div>
      ) : (
        <div style={tabTableWrapStyle} data-testid="orgs-drawer-venues-table">
          <table style={tabTableStyle}>
            <thead>
              <tr>
                <th scope="col" style={tabThStyle}>Name</th>
                <th scope="col" style={tabThStyle}>Slug</th>
                <th scope="col" style={tabThStyle}>Timezone</th>
                <th scope="col" style={tabThStyle}>Status</th>
              </tr>
            </thead>
            <tbody>
              {venues.map((v) => (
                <tr key={v.id}>
                  <td style={tabTdStyle}>{v.name}</td>
                  <td style={tabTdMonoStyle}>{v.slug ?? "—"}</td>
                  <td style={tabTdStyle}>{v.timezone ?? "—"}</td>
                  <td style={tabTdStyle}>
                    {v.deleted_at === null || v.deleted_at === undefined
                      ? "active"
                      : "soft-deleted"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

interface ChannelResponse {
  readonly id: string;
  readonly org_id: string;
  readonly kind: string;
  readonly name: string;
  readonly status?: string;
  readonly enabled?: boolean;
}
interface ChannelsEnvelope {
  readonly channels: readonly ChannelResponse[];
}

function ChannelsTab({ org }: { org: AdminOrganization }) {
  const { permissions } = useAuth();
  const canRead = permissions.has("channel.read") || permissions.has("superadmin.read");
  const query = useQuery<ChannelsEnvelope, ApiError>({
    queryKey: ["admin", "organizations", org.id, "channels"],
    queryFn: () =>
      authedFetch<ChannelsEnvelope>({
        method: "GET",
        path: `/v1/organizations/${org.id}/channels`,
      }),
    enabled: canRead,
    retry: (count, err) =>
      err instanceof ApiError && (err.status === 401 || err.status === 403)
        ? false
        : count < 2,
    refetchOnWindowFocus: false,
  });

  return (
    <>
      <h3 style={drawerSectionTitleStyle}>Sales channels</h3>
      <p style={drawerHelpStyle}>
        <code>GET /v1/organizations/{org.id}/channels</code>
      </p>
      {!canRead ? (
        <TabForbidden missing="channel.read" testid="orgs-drawer-channels-forbidden" />
      ) : query.isPending ? (
        <div style={tabStatusStyle} role="status">Loading channels…</div>
      ) : query.isError ? (
        <TabError
          error={query.error}
          retry={() => query.refetch()}
          testid="orgs-drawer-channels-error"
        />
      ) : query.data.channels.length === 0 ? (
        <div style={tabStatusStyle} data-testid="orgs-drawer-channels-empty">
          No sales channels configured for this organization.
        </div>
      ) : (
        <div style={tabTableWrapStyle} data-testid="orgs-drawer-channels-table">
          <table style={tabTableStyle}>
            <thead>
              <tr>
                <th scope="col" style={tabThStyle}>Name</th>
                <th scope="col" style={tabThStyle}>Kind</th>
                <th scope="col" style={tabThStyle}>Status</th>
              </tr>
            </thead>
            <tbody>
              {query.data.channels.map((c) => (
                <tr key={c.id}>
                  <td style={tabTdStyle}>{c.name}</td>
                  <td style={tabTdMonoStyle}>{c.kind}</td>
                  <td style={tabTdStyle}>
                    {c.status ?? (c.enabled === false ? "disabled" : "enabled")}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

interface PaymentConfigResponse {
  readonly id: string;
  readonly org_id: string;
  readonly provider: string;
  readonly mode: string;
  readonly provider_account_id?: string;
  readonly is_active?: boolean;
  readonly updated_at?: string;
}
interface PaymentConfigsEnvelope {
  readonly payment_configs: readonly PaymentConfigResponse[];
}

function PaymentsTab({ org }: { org: AdminOrganization }) {
  const { permissions } = useAuth();
  const canRead =
    permissions.has("payment_config.read") ||
    permissions.has("payment_config.write") ||
    permissions.has("superadmin.read");
  const query = useQuery<PaymentConfigsEnvelope, ApiError>({
    queryKey: ["admin", "organizations", org.id, "payment-configs"],
    queryFn: () =>
      authedFetch<PaymentConfigsEnvelope>({
        method: "GET",
        path: `/v1/organizations/${org.id}/payment-configs`,
      }),
    enabled: canRead,
    retry: (count, err) =>
      err instanceof ApiError && (err.status === 401 || err.status === 403)
        ? false
        : count < 2,
    refetchOnWindowFocus: false,
  });

  return (
    <>
      <h3 style={drawerSectionTitleStyle}>Payment providers</h3>
      <p style={drawerHelpStyle}>
        <code>GET /v1/organizations/{org.id}/payment-configs</code>
      </p>
      {!canRead ? (
        <TabForbidden
          missing="payment_config.read"
          testid="orgs-drawer-payments-forbidden"
        />
      ) : query.isPending ? (
        <div style={tabStatusStyle} role="status">Loading payment configs…</div>
      ) : query.isError ? (
        <TabError
          error={query.error}
          retry={() => query.refetch()}
          testid="orgs-drawer-payments-error"
        />
      ) : query.data.payment_configs.length === 0 ? (
        <div style={tabStatusStyle} data-testid="orgs-drawer-payments-empty">
          No payment providers configured for this organization.
        </div>
      ) : (
        <div style={tabTableWrapStyle} data-testid="orgs-drawer-payments-table">
          <table style={tabTableStyle}>
            <thead>
              <tr>
                <th scope="col" style={tabThStyle}>Provider</th>
                <th scope="col" style={tabThStyle}>Mode</th>
                <th scope="col" style={tabThStyle}>Account</th>
                <th scope="col" style={tabThStyle}>Active</th>
              </tr>
            </thead>
            <tbody>
              {query.data.payment_configs.map((c) => (
                <tr key={c.id}>
                  <td style={tabTdStyle}>{c.provider}</td>
                  <td style={tabTdMonoStyle}>{c.mode}</td>
                  <td style={tabTdMonoStyle}>{c.provider_account_id ?? "—"}</td>
                  <td style={tabTdStyle}>
                    {c.is_active === false ? "no" : "yes"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

function TabForbidden({ missing, testid }: { missing: string; testid: string }) {
  return (
    <div style={tabStatusStyle} role="status" data-testid={testid}>
      Your account is missing <code>{missing}</code>. Ask a platform
      administrator to grant the permission.
    </div>
  );
}

function TabError({
  error,
  retry,
  testid,
}: {
  error: ApiError | null;
  retry: () => void;
  testid: string;
}) {
  if (error instanceof ApiError && error.status === 403) {
    return (
      <div style={tabStatusStyle} role="alert" data-testid={testid}>
        Forbidden ({error.code}). {error.message}
      </div>
    );
  }
  return (
    <div style={tabStatusStyle} role="alert" data-testid={testid}>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={retry}>
        Retry
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Create-organization dialog (feature #238)
// ---------------------------------------------------------------------------

/**
 * Local validation mirrors the backend (admin_orgs.go::handleAdminCreateOrg):
 *
 *   name  — trimmed, required, <= 200 chars
 *   slug  — trimmed + lowercased, required, <= 100 chars, [a-z0-9-]
 *   country — optional, 2-letter ISO when present (free-text on the wire)
 *   default_locale — optional, defaults to "en" server-side
 *   reservation_ttl_seconds — optional positive integer (server defaults
 *                              to 1200 when missing or non-positive),
 *                              capped at 86400 (24h) by the UI.
 *
 * Empty `country` / `default_locale` / `reservation_ttl_seconds` are
 * tolerated so the operator can rely on backend defaults.
 */
export function validateOrgName(raw: string): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return "Name is required";
  }
  if (trimmed.length > 200) {
    return "Name must be at most 200 characters";
  }
  return null;
}

const SLUG_RE = /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/;

export function validateOrgSlug(raw: string): string | null {
  const trimmed = raw.trim().toLowerCase();
  if (trimmed === "") {
    return "Slug is required";
  }
  if (trimmed.length > 100) {
    return "Slug must be at most 100 characters";
  }
  if (!SLUG_RE.test(trimmed)) {
    return "Slug must contain only lowercase letters, digits, and dashes";
  }
  return null;
}

export function validateOrgCountry(raw: string): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return null;
  }
  if (trimmed.length < 2 || trimmed.length > 3) {
    return "Country must be a 2- or 3-letter ISO code";
  }
  if (!/^[A-Za-z]+$/.test(trimmed)) {
    return "Country must be alphabetic";
  }
  return null;
}

export function validateOrgLocale(raw: string): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return null;
  }
  // BCP-47 lite: language[-REGION]
  if (!/^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})?$/.test(trimmed)) {
    return "Locale must look like 'en' or 'en-US'";
  }
  return null;
}

export function validateOrgReservationTTL(raw: string): string | null {
  const trimmed = raw.trim();
  if (trimmed === "") {
    return null;
  }
  const parsed = Number(trimmed);
  if (!Number.isInteger(parsed)) {
    return "Reservation TTL must be a whole number of seconds";
  }
  if (parsed <= 0) {
    return "Reservation TTL must be positive";
  }
  if (parsed > 86_400) {
    return "Reservation TTL must be at most 86400 (24h)";
  }
  return null;
}

interface CreateOrgFieldErrors {
  name?: string;
  slug?: string;
  country?: string;
  default_locale?: string;
  reservation_ttl_seconds?: string;
  form?: string;
}

interface CreateOrgEnvelope {
  readonly organization: AdminOrganization;
}

/**
 * Map an error envelope from admin_orgs.go::handleAdminCreateOrg onto
 * field-level errors. The backend emits `details.field = "name" | "slug"`
 * for admin_org.invalid_name / admin_org.invalid_slug; duplicates are
 * reported against the slug field (uniqueness is per slug AND name).
 */
export function mapCreateOrgServerError(err: ApiError): CreateOrgFieldErrors {
  const out: CreateOrgFieldErrors = {};
  const field =
    err.details !== undefined && typeof err.details.field === "string"
      ? err.details.field
      : undefined;
  switch (err.code) {
    case "admin_org.invalid_name":
      out.name = err.message;
      return out;
    case "admin_org.invalid_slug":
      out.slug = err.message;
      return out;
    case "admin_org.duplicate":
      out.slug = err.message;
      return out;
    case "admin_org.empty_body":
    case "admin_org.invalid_body":
    case "admin_org.invalid_json":
      out.form = err.message;
      return out;
    case "permissions.denied":
      out.form =
        "Your account is missing org.create. Ask a platform administrator.";
      return out;
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      out.form =
        "An audit reason (X-Admin-Reason) is required. Submit a reason and retry.";
      return out;
    default:
      if (field === "name") {
        out.name = err.message;
      } else if (field === "slug") {
        out.slug = err.message;
      } else {
        out.form = `${err.message} (${err.code})`;
      }
      return out;
  }
}

function CreateOrganizationDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [country, setCountry] = useState("");
  const [locale, setLocale] = useState("");
  const [ttl, setTtl] = useState("");
  const [serverErrors, setServerErrors] = useState<CreateOrgFieldErrors>({});
  const [success, setSuccess] = useState<AdminOrganization | null>(null);

  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, closeRef);
  useFocusRestore(true);

  const nameErr = validateOrgName(name);
  const slugErr = validateOrgSlug(slug);
  const countryErr = validateOrgCountry(country);
  const localeErr = validateOrgLocale(locale);
  const ttlErr = validateOrgReservationTTL(ttl);
  const localValid =
    nameErr === null &&
    slugErr === null &&
    countryErr === null &&
    localeErr === null &&
    ttlErr === null;

  const mutation = useMutation<CreateOrgEnvelope, ApiError, void>({
    mutationFn: () => {
      const body: Record<string, unknown> = {
        name: name.trim(),
        slug: slug.trim().toLowerCase(),
        country: country.trim(),
      };
      if (locale.trim() !== "") {
        body.default_locale = locale.trim();
      }
      if (ttl.trim() !== "") {
        body.reservation_ttl_seconds = Number(ttl);
      }
      return authedFetch<CreateOrgEnvelope>({
        method: "POST",
        path: "/v1/admin/organizations",
        body,
      });
    },
    onSuccess: (data) => {
      // Invalidate the list query so the new row appears immediately.
      queryClient.invalidateQueries({ queryKey: ["admin", "organizations"] });
      setSuccess(data.organization);
    },
    onError: (err) => {
      setServerErrors(mapCreateOrgServerError(err));
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerErrors({});
    if (!localValid) {
      return;
    }
    mutation.mutate();
  }

  if (success !== null) {
    return (
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="orgs-create-success-title"
        style={dialogBackdropStyle}
        data-testid="orgs-create-success"
      >
        <div style={dialogStyle}>
          <header style={dialogHeaderStyle}>
            <h2 id="orgs-create-success-title" style={dialogTitleStyle}>
              Organization created
            </h2>
            <button
              type="button"
              ref={closeRef}
              onClick={onClose}
              style={dialogCloseStyle}
              aria-label="Close"
              data-testid="orgs-create-close"
            >
              ×
            </button>
          </header>
          <div style={successBodyStyle}>
            <p style={successParaStyle}>
              <strong>{success.name}</strong> (
              <code style={monoStyle}>{success.slug}</code>) was created and is
              now visible in the table.
            </p>
            <dl style={metaListStyle}>
              <MetaRow k="ID" v={<code style={monoStyle}>{success.id}</code>} />
              <MetaRow k="Country" v={success.country || "—"} />
              <MetaRow k="Default locale" v={success.default_locale} />
              <MetaRow
                k="Reservation TTL"
                v={`${formatDurationSeconds(success.reservation_ttl_seconds)} (${success.reservation_ttl_seconds.toLocaleString()}s)`}
              />
            </dl>
            <div style={formActionsStyle}>
              <button
                type="button"
                onClick={onClose}
                style={primaryButtonStyle}
                data-testid="orgs-create-done"
              >
                Done
              </button>
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="orgs-create-title"
      style={dialogBackdropStyle}
      data-testid="orgs-create-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="orgs-create-title" style={dialogTitleStyle}>
            Create organization
          </h2>
          <button
            type="button"
            ref={closeRef}
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="orgs-create-close"
          >
            ×
          </button>
        </header>
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          <FieldRow
            label="Name"
            htmlFor="orgs-create-name"
            error={serverErrors.name ?? null}
            localError={name.length > 0 ? nameErr : null}
            hint="Operator-visible organization name. Required."
          >
            <input
              id="orgs-create-name"
              type="text"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (serverErrors.name !== undefined) {
                  setServerErrors({ ...serverErrors, name: undefined });
                }
              }}
              style={inputStyle}
              required
              maxLength={200}
              autoFocus
              data-testid="orgs-create-name"
            />
          </FieldRow>
          <FieldRow
            label="Slug"
            htmlFor="orgs-create-slug"
            error={serverErrors.slug ?? null}
            localError={slug.length > 0 ? slugErr : null}
            hint="Lowercase, URL-safe identifier. Required and unique."
          >
            <input
              id="orgs-create-slug"
              type="text"
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value);
                if (serverErrors.slug !== undefined) {
                  setServerErrors({ ...serverErrors, slug: undefined });
                }
              }}
              style={inputMonoStyle}
              required
              maxLength={100}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="orgs-create-slug"
            />
          </FieldRow>
          <FieldRow
            label="Country"
            htmlFor="orgs-create-country"
            error={serverErrors.country ?? null}
            localError={country.length > 0 ? countryErr : null}
            hint="2-letter ISO 3166-1 country code (e.g. US, GB). Optional."
          >
            <input
              id="orgs-create-country"
              type="text"
              value={country}
              onChange={(e) => setCountry(e.target.value.toUpperCase())}
              style={inputMonoStyle}
              maxLength={3}
              autoCapitalize="characters"
              autoCorrect="off"
              spellCheck={false}
              data-testid="orgs-create-country"
            />
          </FieldRow>
          <FieldRow
            label="Default locale"
            htmlFor="orgs-create-locale"
            error={serverErrors.default_locale ?? null}
            localError={locale.length > 0 ? localeErr : null}
            hint="BCP-47 locale tag. Server defaults to 'en' if blank."
          >
            <input
              id="orgs-create-locale"
              type="text"
              value={locale}
              onChange={(e) => setLocale(e.target.value)}
              style={inputMonoStyle}
              maxLength={20}
              placeholder="en"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="orgs-create-locale"
            />
          </FieldRow>
          <FieldRow
            label="Reservation TTL (seconds)"
            htmlFor="orgs-create-ttl"
            error={serverErrors.reservation_ttl_seconds ?? null}
            localError={ttl.length > 0 ? ttlErr : null}
            hint="Cart-hold timeout. Server defaults to 1200 (20m). Max 86400 (24h)."
          >
            <input
              id="orgs-create-ttl"
              type="number"
              value={ttl}
              onChange={(e) => setTtl(e.target.value)}
              style={inputStyle}
              min={1}
              max={86400}
              step={1}
              placeholder="1200"
              data-testid="orgs-create-ttl"
            />
          </FieldRow>

          {serverErrors.form !== undefined ? (
            <div style={formErrorStyle} role="alert" data-testid="orgs-create-error">
              {serverErrors.form}
            </div>
          ) : null}

          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="orgs-create-cancel"
            >
              Cancel
            </button>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={!localValid || mutation.isPending}
              data-testid="orgs-create-submit"
            >
              {mutation.isPending ? "Creating…" : "Create"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Edit-organization dialog (feature #239)
//
// Mirrors the create dialog but pre-fills every field from the row and
// PATCHes /v1/admin/organizations/{id}. The X-Admin-Reason header is
// auto-prompted by authedFetch (requiresAdminReason matches
// /v1/admin/*). On success we invalidate the ["admin","organizations"]
// query so the list reflects the edit.
// ---------------------------------------------------------------------------

interface UpdateOrgFieldErrors {
  name?: string;
  slug?: string;
  country?: string;
  default_locale?: string;
  reservation_ttl_seconds?: string;
  form?: string;
}

interface UpdateOrgEnvelope {
  readonly organization: AdminOrganization;
}

/**
 * Map an error envelope from admin_orgs.go::handleAdminUpdateOrg onto
 * field-level errors. Codes mirror the create path:
 *   - admin_org.invalid_name / invalid_slug -> field-scoped
 *   - admin_org.duplicate -> slug (uniqueness is per slug AND name)
 *   - admin_org.not_found -> form-level (the row may have been archived
 *     in another tab between table load and submit)
 *   - admin_org.{empty,invalid}_body / invalid_json -> form-level
 *   - permissions.denied -> form-level, scoped to org.update
 *   - superadmin.{missing,required}_reason -> form-level prompt
 *
 * Unknown codes fall back to a generic surface, honouring
 * details.field when present (forwards compat).
 */
export function mapUpdateOrgServerError(err: ApiError): UpdateOrgFieldErrors {
  const out: UpdateOrgFieldErrors = {};
  const field =
    err.details !== undefined && typeof err.details.field === "string"
      ? err.details.field
      : undefined;
  switch (err.code) {
    case "admin_org.invalid_name":
      out.name = err.message;
      return out;
    case "admin_org.invalid_slug":
      out.slug = err.message;
      return out;
    case "admin_org.duplicate":
      out.slug = err.message;
      return out;
    case "admin_org.not_found":
      out.form =
        "Organization no longer exists. Refresh the list and try again.";
      return out;
    case "admin_org.empty_body":
    case "admin_org.invalid_body":
    case "admin_org.invalid_json":
      out.form = err.message;
      return out;
    case "permissions.denied":
      out.form =
        "Your account is missing org.update. Ask a platform administrator.";
      return out;
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      out.form =
        "An audit reason (X-Admin-Reason) is required. Submit a reason and retry.";
      return out;
    default:
      if (field === "name") {
        out.name = err.message;
      } else if (field === "slug") {
        out.slug = err.message;
      } else {
        out.form = `${err.message} (${err.code})`;
      }
      return out;
  }
}

function EditOrganizationDialog({
  org,
  onClose,
}: {
  org: AdminOrganization;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [name, setName] = useState(org.name);
  const [slug, setSlug] = useState(org.slug);
  const [country, setCountry] = useState(org.country);
  const [locale, setLocale] = useState(org.default_locale);
  const [ttl, setTtl] = useState(String(org.reservation_ttl_seconds));
  const [serverErrors, setServerErrors] = useState<UpdateOrgFieldErrors>({});
  const [success, setSuccess] = useState<AdminOrganization | null>(null);

  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, closeRef);
  useFocusRestore(true);

  const nameErr = validateOrgName(name);
  const slugErr = validateOrgSlug(slug);
  const countryErr = validateOrgCountry(country);
  const localeErr = validateOrgLocale(locale);
  const ttlErr = validateOrgReservationTTL(ttl);
  // Edit semantics: reservation TTL is required on the wire (PATCH does
  // not partial-update), so blank is rejected here even though create
  // tolerated blank (server default = 1200).
  const ttlBlankErr = ttl.trim() === "" ? "Reservation TTL is required" : null;
  const localValid =
    nameErr === null &&
    slugErr === null &&
    countryErr === null &&
    localeErr === null &&
    ttlErr === null &&
    ttlBlankErr === null;

  const isDirty =
    name.trim() !== org.name ||
    slug.trim().toLowerCase() !== org.slug ||
    country.trim() !== org.country ||
    locale.trim() !== org.default_locale ||
    (ttl.trim() !== "" && Number(ttl) !== org.reservation_ttl_seconds);

  const mutation = useMutation<UpdateOrgEnvelope, ApiError, void>({
    mutationFn: () => {
      const body: Record<string, unknown> = {
        name: name.trim(),
        slug: slug.trim().toLowerCase(),
        country: country.trim(),
        default_locale: locale.trim(),
        reservation_ttl_seconds: Number(ttl),
      };
      return authedFetch<UpdateOrgEnvelope>({
        method: "PATCH",
        path: `/v1/admin/organizations/${encodeURIComponent(org.id)}`,
        body,
      });
    },
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ["admin", "organizations"] });
      setSuccess(data.organization);
    },
    onError: (err) => {
      setServerErrors(mapUpdateOrgServerError(err));
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerErrors({});
    if (!localValid || !isDirty) {
      return;
    }
    mutation.mutate();
  }

  if (success !== null) {
    return (
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="orgs-edit-success-title"
        style={dialogBackdropStyle}
        data-testid="orgs-edit-success"
      >
        <div style={dialogStyle}>
          <header style={dialogHeaderStyle}>
            <h2 id="orgs-edit-success-title" style={dialogTitleStyle}>
              Organization updated
            </h2>
            <button
              type="button"
              ref={closeRef}
              onClick={onClose}
              style={dialogCloseStyle}
              aria-label="Close"
              data-testid="orgs-edit-close"
            >
              ×
            </button>
          </header>
          <div style={successBodyStyle}>
            <p style={successParaStyle}>
              <strong>{success.name}</strong> (
              <code style={monoStyle}>{success.slug}</code>) was updated.
            </p>
            <div style={formActionsStyle}>
              <button
                type="button"
                onClick={onClose}
                style={primaryButtonStyle}
                data-testid="orgs-edit-done"
              >
                Done
              </button>
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="orgs-edit-title"
      style={dialogBackdropStyle}
      data-testid="orgs-edit-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="orgs-edit-title" style={dialogTitleStyle}>
            Edit organization
          </h2>
          <button
            type="button"
            ref={closeRef}
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="orgs-edit-close"
          >
            ×
          </button>
        </header>
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          <FieldRow
            label="Name"
            htmlFor="orgs-edit-name"
            error={serverErrors.name ?? null}
            localError={name.length > 0 ? nameErr : null}
            hint="Operator-visible organization name. Required."
          >
            <input
              id="orgs-edit-name"
              type="text"
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                if (serverErrors.name !== undefined) {
                  setServerErrors({ ...serverErrors, name: undefined });
                }
              }}
              style={inputStyle}
              required
              maxLength={200}
              autoFocus
              data-testid="orgs-edit-name"
            />
          </FieldRow>
          <FieldRow
            label="Slug"
            htmlFor="orgs-edit-slug"
            error={serverErrors.slug ?? null}
            localError={slug.length > 0 ? slugErr : null}
            hint="Lowercase, URL-safe identifier. Required and unique."
          >
            <input
              id="orgs-edit-slug"
              type="text"
              value={slug}
              onChange={(e) => {
                setSlug(e.target.value);
                if (serverErrors.slug !== undefined) {
                  setServerErrors({ ...serverErrors, slug: undefined });
                }
              }}
              style={inputMonoStyle}
              required
              maxLength={100}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="orgs-edit-slug"
            />
          </FieldRow>
          <FieldRow
            label="Country"
            htmlFor="orgs-edit-country"
            error={serverErrors.country ?? null}
            localError={country.length > 0 ? countryErr : null}
            hint="2-letter ISO 3166-1 country code (e.g. US, GB). Optional."
          >
            <input
              id="orgs-edit-country"
              type="text"
              value={country}
              onChange={(e) => setCountry(e.target.value.toUpperCase())}
              style={inputMonoStyle}
              maxLength={3}
              autoCapitalize="characters"
              autoCorrect="off"
              spellCheck={false}
              data-testid="orgs-edit-country"
            />
          </FieldRow>
          <FieldRow
            label="Default locale"
            htmlFor="orgs-edit-locale"
            error={serverErrors.default_locale ?? null}
            localError={locale.length > 0 ? localeErr : null}
            hint="BCP-47 locale tag (e.g. en, en-US). Required."
          >
            <input
              id="orgs-edit-locale"
              type="text"
              value={locale}
              onChange={(e) => setLocale(e.target.value)}
              style={inputMonoStyle}
              maxLength={20}
              placeholder="en"
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              data-testid="orgs-edit-locale"
            />
          </FieldRow>
          <FieldRow
            label="Reservation TTL (seconds)"
            htmlFor="orgs-edit-ttl"
            error={serverErrors.reservation_ttl_seconds ?? null}
            localError={ttl.length > 0 ? ttlErr : ttlBlankErr}
            hint="Cart-hold timeout in seconds. Max 86400 (24h)."
          >
            <input
              id="orgs-edit-ttl"
              type="number"
              value={ttl}
              onChange={(e) => setTtl(e.target.value)}
              style={inputStyle}
              min={1}
              max={86400}
              step={1}
              required
              data-testid="orgs-edit-ttl"
            />
          </FieldRow>

          {serverErrors.form !== undefined ? (
            <div style={formErrorStyle} role="alert" data-testid="orgs-edit-error">
              {serverErrors.form}
            </div>
          ) : null}

          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="orgs-edit-cancel"
            >
              Cancel
            </button>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={!localValid || !isDirty || mutation.isPending}
              data-testid="orgs-edit-submit"
            >
              {mutation.isPending ? "Saving…" : "Save changes"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Archive-organization confirmation dialog (feature #239)
//
// POST /v1/admin/organizations/{id}/archive performs a soft-delete in a
// single transaction with an audit event. The confirmation step requires
// the operator to retype the slug — this matches the destructive-action
// pattern used elsewhere in the admin console and protects against
// fat-fingered archives on the wrong row.
// ---------------------------------------------------------------------------

interface ArchiveOrgFieldErrors {
  form?: string;
}

interface ArchiveOrgEnvelope {
  readonly organization: AdminOrganization;
  readonly archived: boolean;
}

/**
 * Map an error envelope from admin_orgs.go::handleAdminArchiveOrg onto
 * a form-level error surface. Archive has no field-scoped errors today;
 * everything routes through the single `form` slot.
 */
export function mapArchiveOrgServerError(err: ApiError): ArchiveOrgFieldErrors {
  switch (err.code) {
    case "admin_org.not_found":
      return {
        form: "Organization no longer exists. Refresh the list and close this dialog.",
      };
    case "permissions.denied":
      return {
        form: "Your account is missing org.delete. Ask a platform administrator.",
      };
    case "superadmin.missing_reason":
    case "superadmin.reason_required":
      return {
        form: "An audit reason (X-Admin-Reason) is required. Submit a reason and retry.",
      };
    case "dependency.database_unavailable":
      return { form: "Database is unavailable. Try again shortly." };
    default:
      return { form: `${err.message} (${err.code})` };
  }
}

function ArchiveOrganizationDialog({
  org,
  onClose,
}: {
  org: AdminOrganization;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [confirmSlug, setConfirmSlug] = useState("");
  const [serverErrors, setServerErrors] = useState<ArchiveOrgFieldErrors>({});
  const [success, setSuccess] = useState<AdminOrganization | null>(null);

  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, closeRef);
  useFocusRestore(true);

  const slugMatches = confirmSlug.trim().toLowerCase() === org.slug;
  // Defence-in-depth: the row-level button already disables on archived
  // rows, but a stale `?org=` link could open this dialog on a soft-
  // deleted row. The submit guard treats that as a no-op.
  const alreadyArchived = org.deleted_at !== null;

  const mutation = useMutation<ArchiveOrgEnvelope, ApiError, void>({
    mutationFn: () =>
      authedFetch<ArchiveOrgEnvelope>({
        method: "POST",
        path: `/v1/admin/organizations/${encodeURIComponent(org.id)}/archive`,
      }),
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ["admin", "organizations"] });
      setSuccess(data.organization);
    },
    onError: (err) => {
      setServerErrors(mapArchiveOrgServerError(err));
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerErrors({});
    if (!slugMatches || alreadyArchived) {
      return;
    }
    mutation.mutate();
  }

  if (success !== null) {
    return (
      <div
        role="dialog"
        aria-modal="true"
        aria-labelledby="orgs-archive-success-title"
        style={dialogBackdropStyle}
        data-testid="orgs-archive-success"
      >
        <div style={dialogStyle}>
          <header style={dialogHeaderStyle}>
            <h2 id="orgs-archive-success-title" style={dialogTitleStyle}>
              Organization archived
            </h2>
            <button
              type="button"
              ref={closeRef}
              onClick={onClose}
              style={dialogCloseStyle}
              aria-label="Close"
              data-testid="orgs-archive-close"
            >
              ×
            </button>
          </header>
          <div style={successBodyStyle}>
            <p style={successParaStyle}>
              <strong>{success.name}</strong> (
              <code style={monoStyle}>{success.slug}</code>) has been
              soft-deleted. It remains visible when the "Show soft-deleted"
              toggle is enabled.
            </p>
            <div style={formActionsStyle}>
              <button
                type="button"
                onClick={onClose}
                style={primaryButtonStyle}
                data-testid="orgs-archive-done"
              >
                Done
              </button>
            </div>
          </div>
        </div>
      </div>
    );
  }

  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-labelledby="orgs-archive-title"
      style={dialogBackdropStyle}
      data-testid="orgs-archive-dialog"
    >
      <div style={dialogStyle}>
        <header style={dialogHeaderStyle}>
          <h2 id="orgs-archive-title" style={dialogTitleStyle}>
            Archive organization
          </h2>
          <button
            type="button"
            ref={closeRef}
            onClick={onClose}
            style={dialogCloseStyle}
            aria-label="Close"
            data-testid="orgs-archive-close"
          >
            ×
          </button>
        </header>
        <form onSubmit={onSubmit} style={formStyle} noValidate>
          <p style={successParaStyle}>
            This will soft-delete <strong>{org.name}</strong> (
            <code style={monoStyle}>{org.slug}</code>). The organization
            and all its data become hidden from operators by default;
            superadmins can still see archived rows via the
            "Show soft-deleted" toggle. An audit event is written with the
            <code style={monoStyle}> X-Admin-Reason </code> header.
          </p>
          <FieldRow
            label={`Type the slug "${org.slug}" to confirm`}
            htmlFor="orgs-archive-confirm"
            error={null}
            localError={
              confirmSlug.length > 0 && !slugMatches
                ? "Slug does not match"
                : null
            }
            hint="The slug is case-insensitive."
          >
            <input
              id="orgs-archive-confirm"
              type="text"
              value={confirmSlug}
              onChange={(e) => setConfirmSlug(e.target.value)}
              style={inputMonoStyle}
              autoCapitalize="off"
              autoCorrect="off"
              spellCheck={false}
              autoComplete="off"
              data-testid="orgs-archive-confirm"
            />
          </FieldRow>

          {alreadyArchived ? (
            <div style={formErrorStyle} role="alert" data-testid="orgs-archive-already">
              This organization is already archived. Close this dialog.
            </div>
          ) : null}

          {serverErrors.form !== undefined ? (
            <div style={formErrorStyle} role="alert" data-testid="orgs-archive-error">
              {serverErrors.form}
            </div>
          ) : null}

          <div style={formActionsStyle}>
            <button
              type="button"
              onClick={onClose}
              style={secondaryButtonStyle}
              data-testid="orgs-archive-cancel"
            >
              Cancel
            </button>
            <button
              type="submit"
              style={dangerButtonStyle}
              disabled={
                !slugMatches || alreadyArchived || mutation.isPending
              }
              data-testid="orgs-archive-submit"
            >
              {mutation.isPending ? "Archiving…" : "Archive organization"}
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

function FieldRow({
  label,
  htmlFor,
  error,
  localError,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  error: string | null;
  localError: string | null;
  hint: ReactNode;
  children: ReactNode;
}) {
  const visibleError = error ?? localError;
  return (
    <div style={fieldRowStyle}>
      <label htmlFor={htmlFor} style={fieldLabelStyle}>
        {label}
      </label>
      {children}
      {visibleError !== null ? (
        <div style={fieldErrorStyle} role="alert" data-testid={`${htmlFor}-error`}>
          {visibleError}
        </div>
      ) : (
        <div style={fieldHintStyle}>{hint}</div>
      )}
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

const rowActionDangerStyle: CSSProperties = {
  ...rowActionButtonStyle,
  borderColor: "#fca5a5",
  color: "#b91c1c",
};

const rowActionsCellStyle: CSSProperties = {
  display: "flex",
  gap: 6,
  flexWrap: "wrap",
};

const dangerButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#b91c1c",
  border: "1px solid #b91c1c",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
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

// Drawer tab styles (feature #240).
const tabListStyle: CSSProperties = {
  display: "flex",
  flexWrap: "wrap",
  gap: 4,
  borderBottom: "1px solid #e2e8f0",
  marginTop: -8,
};
const tabButtonStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 500,
  padding: "8px 12px",
  background: "transparent",
  border: "none",
  borderBottom: "2px solid transparent",
  color: "#475569",
  cursor: "pointer",
};
const tabButtonActiveStyle: CSSProperties = {
  ...tabButtonStyle,
  color: "#0369a1",
  borderBottomColor: "#0369a1",
  fontWeight: 600,
};
const tabStatusStyle: CSSProperties = {
  padding: 12,
  border: "1px dashed #cbd5e1",
  borderRadius: 6,
  background: "#f8fafc",
  fontSize: 12,
  color: "#475569",
  display: "flex",
  flexDirection: "column",
  gap: 8,
};
const tabTableWrapStyle: CSSProperties = {
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  overflowX: "auto",
};
const tabTableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 12,
};
const tabThStyle: CSSProperties = {
  textAlign: "left",
  padding: "6px 10px",
  borderBottom: "1px solid #e2e8f0",
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  background: "#f8fafc",
};
const tabTdStyle: CSSProperties = {
  padding: "6px 10px",
  borderBottom: "1px solid #f1f5f9",
  color: "#0f172a",
};
const tabTdMonoStyle: CSSProperties = {
  ...tabTdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 11,
};

// Users tab styles (feature #241).
const tabHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
  gap: 8,
};
const smallPrimaryButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};
const tabRowButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "3px 8px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};
const tabRowDangerStyle: CSSProperties = {
  ...tabRowButtonStyle,
  borderColor: "#fca5a5",
  color: "#7f1d1d",
  background: "#fef2f2",
};
const rowActionsStyle: CSSProperties = {
  display: "flex",
  gap: 6,
  flexWrap: "wrap",
};
const roleEditorStyle: CSSProperties = {
  display: "flex",
  gap: 6,
  alignItems: "center",
  flexWrap: "wrap",
};

// Create-organization dialog styles (feature #238).
const primaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};

const secondaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const mutedHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#94a3b8",
  fontStyle: "italic",
};

const dialogBackdropStyle: CSSProperties = {
  position: "fixed",
  inset: 0,
  background: "rgba(15, 23, 42, 0.4)",
  display: "flex",
  alignItems: "center",
  justifyContent: "center",
  padding: 16,
  zIndex: 100,
};

const dialogStyle: CSSProperties = {
  background: "#ffffff",
  borderRadius: 8,
  border: "1px solid #e2e8f0",
  boxShadow: "0 10px 25px rgba(15, 23, 42, 0.2)",
  width: "min(520px, 100%)",
  maxHeight: "90vh",
  overflowY: "auto",
};

const dialogHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "center",
  justifyContent: "space-between",
  padding: "12px 16px",
  borderBottom: "1px solid #e2e8f0",
};

const dialogTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 16,
  fontWeight: 600,
  color: "#0f172a",
};

const dialogCloseStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  fontSize: 22,
  lineHeight: 1,
  cursor: "pointer",
  color: "#64748b",
  padding: "0 4px",
};

const formStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
  padding: 16,
};

const fieldRowStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const fieldLabelStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  color: "#334155",
};

const inputStyle: CSSProperties = {
  fontSize: 13,
  padding: "8px 10px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
};

const inputMonoStyle: CSSProperties = {
  ...inputStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const fieldHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
  lineHeight: 1.4,
};

const fieldErrorStyle: CSSProperties = {
  fontSize: 11,
  color: "#b91c1c",
  fontWeight: 500,
};

const formErrorStyle: CSSProperties = {
  fontSize: 12,
  padding: 8,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#7f1d1d",
  borderRadius: 4,
};

const formActionsStyle: CSSProperties = {
  display: "flex",
  justifyContent: "flex-end",
  gap: 8,
};

const successBodyStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
  padding: 16,
};

const successParaStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  color: "#334155",
  lineHeight: 1.5,
};
