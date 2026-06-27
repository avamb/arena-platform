/**
 * Operator Network detail surface (SAUI-08).
 *
 * Deep page for a single operator network, exposing the roster-management
 * endpoints introduced by features #209 (network users) and #210 (network
 * organizers/agents):
 *
 *   GET  /v1/operator-networks/{id}             — overview header
 *   GET  /v1/admin/networks/{id}/users          — Users tab
 *   POST /v1/admin/networks/{id}/users          — assign network_operator
 *   DEL  /v1/admin/networks/{id}/users/{userId} — soft-revoke
 *   GET  /v1/admin/networks/{id}/organizers
 *   POST /v1/admin/networks/{id}/organizers
 *   DEL  /v1/admin/networks/{id}/organizers/{orgId}
 *   GET  /v1/admin/networks/{id}/agents
 *   POST /v1/admin/networks/{id}/agents
 *   DEL  /v1/admin/networks/{id}/agents/{orgId}
 *
 * Permission gating is per-tab (mirrors the backend mount):
 *
 *   Overview     network.read              (route entry permission)
 *   Users        network.manage_users      (platform_operator only;
 *                                           network_operator NOT bound)
 *   Organizers   network.manage_organizers (platform_operator OR
 *                                           network_operator)
 *   Agents       network.manage_agents     (platform_operator OR
 *                                           network_operator)
 *   Audit        always visible — the tab itself documents the API gap
 *                because /v1/admin/networks/{id}/audit does not exist
 *                yet (see SAUI-09 backlog input below).
 *
 * Archived networks (status=archived) render the roster tabs read-only:
 * assign/detach controls are hidden across all three roster tabs because
 * the backend rejects every mutation with operator_network.archived (409)
 * once archived_at is set.
 *
 * Operator-type copy (acceptance criterion):
 *   - "Platform operators" (platform_superadmin role) administer THE
 *     PLATFORM globally; they can manage every network's roster.
 *   - "Network operators" (network_operator role; rows on the Users tab)
 *     administer ONE network's organizer/agent roster but cannot create
 *     new networks or edit the platform itself.
 *   - "Organizers" (organizations attached as kind=organizer) sell
 *     tickets for events on behalf of the network.
 *   - "Agents" (organizations attached as kind=agent) are external sales
 *     channels — they can resell but never publish.
 *
 * Backend gaps recorded for SAUI-09:
 *
 *   G1 The four mutation endpoints under /v1/admin/networks/{id}/ do
 *      NOT currently require X-Admin-Reason (see
 *      apps/admin-web/src/lib/api/reason.ts REASON_REQUIRED_PREFIXES;
 *      only /v1/admin/{organizations,orders,tickets,refunds,impersonate}
 *      are gated). The UI therefore cannot present a typed
 *      "missing_reason" prompt for these mutations. SAUI-09 must either
 *      extend the backend requireAdminReason allowlist to include
 *      /v1/admin/networks/* or formally exempt them with documented
 *      rationale.
 *
 *   G2 There is no lookup endpoint for "candidate users" or "candidate
 *      organizations" to assign. Today the operator must paste a UUID
 *      copied from another tool. SAUI-09 needs a typed search:
 *        GET /v1/admin/users?role=network_operator&q=...
 *        GET /v1/admin/organizations?q=...
 *      (the latter exists but does not return the org id alone for a
 *      free-text query).
 *
 *   G3 There is no audit/timeline endpoint scoped to a network. The
 *      backend writes audit_events rows under action=v1.network.* but
 *      exposes no /v1/admin/networks/{id}/audit reader; the Audit tab
 *      therefore states the gap explicitly instead of rendering empty.
 *
 *   G4 Soft-revoked roster rows (status=revoked) are not surfaced by the
 *      list endpoints — list returns only active rows. The UI cannot
 *      display the "previously assigned" history without a flag like
 *      ?include_revoked=true. Recorded for SAUI-09.
 *
 * Mock data: NONE. Every list / mutation hits the live backend through
 * authedFetch; no globalThis/devStore/mock placeholders.
 */
import {
  createRoute,
  Link,
  useParams,
} from "@tanstack/react-router";
import {
  useMutation,
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from "@tanstack/react-query";
import {
  useMemo,
  useState,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { useAuth } from "@/lib/auth/useAuth";
import type { NavEntry } from "@/lib/auth/navConfig";
import type { OperatorNetwork } from "./networks";

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/networks/$id",
  component: NetworkDetailRoute,
});

/**
 * Synthetic nav entry used by RequirePermission for the detail route. It is
 * NOT added to NAV_ENTRIES (no sidebar entry) — the parent /networks entry
 * already covers visibility; this exists purely so RequirePermission can
 * verify the caller still holds network.read on direct navigation.
 */
const NETWORK_DETAIL_NAV_ENTRY: NavEntry = {
  id: "networks-detail",
  label: "Operator Network detail",
  to: "/networks",
  permission: { anyOf: ["network.read"] },
  scopeKinds: ["global", "platform", "network"],
  purpose:
    "Inspect and manage a single operator network. Requires network.read.",
};

// ---------------------------------------------------------------------------
// Response envelopes
// ---------------------------------------------------------------------------

interface OperatorNetworkEnvelope {
  readonly operator_network: OperatorNetwork;
}

export interface NetworkUserRow {
  readonly id: string;
  readonly network_id: string;
  readonly user_id: string;
  readonly role: string;
  readonly status: string;
  readonly created_at: string;
  readonly updated_at: string;
}

interface NetworkUserListEnvelope {
  readonly network_id: string;
  readonly network_users: readonly NetworkUserRow[];
  readonly total: number;
}

export interface NetworkOrganizationRow {
  readonly id: string;
  readonly network_id: string;
  readonly organization_id: string;
  readonly assignment_kind: string;
  readonly status: string;
  readonly attached_at: string;
  readonly created_at: string;
  readonly updated_at: string;
}

interface NetworkOrganizationListEnvelope {
  readonly network_id: string;
  readonly assignment_kind: string;
  readonly organizers?: readonly NetworkOrganizationRow[];
  readonly agents?: readonly NetworkOrganizationRow[];
  readonly total: number;
}

// ---------------------------------------------------------------------------
// UUID validation (shared between assign forms)
// ---------------------------------------------------------------------------

const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function validateUuid(value: string, label = "value"): string | null {
  const trimmed = value.trim();
  if (trimmed === "") {
    return `${label} is required`;
  }
  if (!UUID_RE.test(trimmed)) {
    return `${label} must be a valid UUID (e.g. 0190a8b0-7d31-7a3c-9c4e-8c0c1d9d9c2a)`;
  }
  return null;
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

type TabId = "overview" | "users" | "organizers" | "agents" | "audit";

const TABS: ReadonlyArray<{ id: TabId; label: string; testid: string }> = [
  { id: "overview", label: "Overview", testid: "tab-overview" },
  { id: "users", label: "Users", testid: "tab-users" },
  { id: "organizers", label: "Organizers", testid: "tab-organizers" },
  { id: "agents", label: "Agents", testid: "tab-agents" },
  { id: "audit", label: "Audit", testid: "tab-audit" },
];

function NetworkDetailRoute() {
  return (
    <RequirePermission entry={NETWORK_DETAIL_NAV_ENTRY}>
      <NetworkDetailModule />
    </RequirePermission>
  );
}

function NetworkDetailModule() {
  const { id } = useParams({ from: "/networks/$id" });
  const [tab, setTab] = useState<TabId>("overview");

  const network = useQuery<OperatorNetworkEnvelope, ApiError>({
    queryKey: ["operator-networks", "detail", id],
    queryFn: () =>
      authedFetch<OperatorNetworkEnvelope>({
        method: "GET",
        path: `/v1/operator-networks/${id}`,
      }),
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (err.status === 401 || err.status === 403 || err.status === 404) {
          return false;
        }
        if (err.code === "permissions.denied") {
          return false;
        }
      }
      return failureCount < 2;
    },
    refetchOnWindowFocus: false,
  });

  const archived =
    network.data?.operator_network.status === "archived" ||
    network.data?.operator_network.archived_at !== null;

  return (
    <section aria-labelledby="network-detail-heading" style={pageStyle}>
      <Breadcrumb id={id} name={network.data?.operator_network.name ?? null} />
      <DetailHeader
        query={network}
        archived={archived}
      />
      <TabBar
        tab={tab}
        onChange={setTab}
        archived={archived}
      />
      <div style={tabPanelStyle}>
        {tab === "overview" ? (
          <OverviewTab query={network} />
        ) : null}
        {tab === "users" ? (
          <UsersTab networkId={id} archived={archived} />
        ) : null}
        {tab === "organizers" ? (
          <OrganizationsTab
            networkId={id}
            archived={archived}
            kind="organizer"
          />
        ) : null}
        {tab === "agents" ? (
          <OrganizationsTab
            networkId={id}
            archived={archived}
            kind="agent"
          />
        ) : null}
        {tab === "audit" ? <AuditTab /> : null}
      </div>
    </section>
  );
}

// ---------------------------------------------------------------------------
// Header / Breadcrumb
// ---------------------------------------------------------------------------

function Breadcrumb({ id, name }: { id: string; name: string | null }) {
  return (
    <nav aria-label="Breadcrumb" style={breadcrumbStyle}>
      <Link to="/networks" style={breadcrumbLinkStyle}>
        Operator Networks
      </Link>
      <span style={breadcrumbSepStyle}>/</span>
      <span style={breadcrumbCurrentStyle} title={id}>
        {name ?? id}
      </span>
    </nav>
  );
}

function DetailHeader({
  query,
  archived,
}: {
  query: UseQueryResult<OperatorNetworkEnvelope, ApiError>;
  archived: boolean;
}) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading operator network…
      </div>
    );
  }
  if (query.isError) {
    return <DetailErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  const n = query.data.operator_network;
  return (
    <header style={headerStyle}>
      <div>
        <h1 id="network-detail-heading" style={headingStyle}>
          {n.name}
        </h1>
        <p style={subheadingStyle}>
          Slug <code style={monoStyle}>{n.slug}</code> · ID{" "}
          <code style={monoStyle}>{n.id}</code>
        </p>
        {archived ? (
          <p style={archivedNoticeStyle} role="status" data-testid="network-archived-banner">
            <strong>Archived.</strong> Roster mutations are disabled for this
            network. Existing roster rows remain visible for audit purposes.
          </p>
        ) : null}
      </div>
      <div style={refreshWrapStyle}>
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={() => query.refetch()}
          disabled={query.isFetching}
          data-testid="network-detail-refresh"
        >
          {query.isFetching ? "Refreshing…" : "Refresh"}
        </button>
      </div>
    </header>
  );
}

function DetailErrorState({
  error,
  onRetry,
}: {
  error: ApiError | null;
  onRetry: () => void;
}) {
  if (error instanceof ApiError && error.status === 404) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="network-detail-not-found">
        <strong>Network not found.</strong>
        <p style={errorParaStyle}>
          The operator network does not exist or has been hard-deleted. Use
          the <Link to="/networks" style={inlineLinkStyle}>networks list</Link>{" "}
          to pick another network.
        </p>
      </div>
    );
  }
  if (
    error instanceof ApiError &&
    (error.status === 403 || error.code === "permissions.denied")
  ) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="network-detail-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>network.read</code>.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="network-detail-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload the network.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="network-detail-error">
      <strong>Failed to load operator network.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Tab bar
// ---------------------------------------------------------------------------

function TabBar({
  tab,
  onChange,
  archived,
}: {
  tab: TabId;
  onChange: (next: TabId) => void;
  archived: boolean;
}) {
  return (
    <div role="tablist" aria-label="Network sections" style={tabBarStyle}>
      {TABS.map((t) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          aria-selected={tab === t.id}
          onClick={() => onChange(t.id)}
          style={tab === t.id ? tabActiveStyle : tabStyle}
          data-testid={t.testid}
        >
          {t.label}
          {archived && (t.id === "users" || t.id === "organizers" || t.id === "agents") ? (
            <span style={tabBadgeStyle} aria-label="read only">read-only</span>
          ) : null}
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Overview tab
// ---------------------------------------------------------------------------

function OverviewTab({
  query,
}: {
  query: UseQueryResult<OperatorNetworkEnvelope, ApiError>;
}) {
  if (query.isPending) {
    return <div style={statusBoxStyle} role="status">Loading…</div>;
  }
  if (query.isError) {
    return (
      <div style={statusBoxStyle} role="status">
        Overview data is unavailable because the detail load failed; see the
        header for the typed error.
      </div>
    );
  }
  const n = query.data.operator_network;
  return (
    <div style={overviewGridStyle}>
      <MetaRow k="Name" v={n.name} />
      <MetaRow k="Slug" v={<code style={monoStyle}>{n.slug}</code>} />
      <MetaRow k="ID" v={<code style={monoStyle}>{n.id}</code>} />
      <MetaRow k="Status" v={n.status} />
      <MetaRow k="Created" v={formatDate(n.created_at)} />
      <MetaRow k="Updated" v={formatDate(n.updated_at)} />
      <MetaRow
        k="Archived at"
        v={n.archived_at !== null ? formatDate(n.archived_at) : "—"}
      />
      <div style={copyBoxStyle} data-testid="network-detail-roles-help">
        <h2 style={copyBoxHeadingStyle}>Operator role glossary</h2>
        <dl style={copyDlStyle}>
          <dt style={copyDtStyle}>Platform operators</dt>
          <dd style={copyDdStyle}>
            <code style={monoStyle}>platform_superadmin</code> role. Administer
            THE PLATFORM globally — can create / archive networks and edit
            every network's roster.
          </dd>
          <dt style={copyDtStyle}>Network operators</dt>
          <dd style={copyDdStyle}>
            <code style={monoStyle}>network_operator</code> role; rows on the{" "}
            <em>Users</em> tab. Administer ONE network's organizer / agent
            roster. Cannot create networks or edit the platform itself.
          </dd>
          <dt style={copyDtStyle}>Organizers</dt>
          <dd style={copyDdStyle}>
            Organizations attached as <code style={monoStyle}>kind=organizer</code>.
            Sell tickets for events on behalf of the network.
          </dd>
          <dt style={copyDtStyle}>Agents</dt>
          <dd style={copyDdStyle}>
            Organizations attached as <code style={monoStyle}>kind=agent</code>.
            External sales channels — can resell but never publish.
          </dd>
        </dl>
      </div>
    </div>
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
// Users tab
// ---------------------------------------------------------------------------

function UsersTab({
  networkId,
  archived,
}: {
  networkId: string;
  archived: boolean;
}) {
  const { permissions } = useAuth();
  const canManage = permissions.has("network.manage_users");
  const queryClient = useQueryClient();
  const [assignUuid, setAssignUuid] = useState("");
  const [assignError, setAssignError] = useState<ApiError | null>(null);

  const query = useQuery<NetworkUserListEnvelope, ApiError>({
    queryKey: ["operator-networks", networkId, "users"],
    queryFn: () =>
      authedFetch<NetworkUserListEnvelope>({
        method: "GET",
        path: `/v1/admin/networks/${networkId}/users`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const assign = useMutation<unknown, ApiError, string>({
    mutationFn: (userId: string) =>
      authedFetch({
        method: "POST",
        path: `/v1/admin/networks/${networkId}/users`,
        body: { user_id: userId },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["operator-networks", networkId, "users"],
      });
      setAssignUuid("");
      setAssignError(null);
    },
    onError: (err) => setAssignError(err),
  });

  const remove = useMutation<unknown, ApiError, string>({
    mutationFn: (userId: string) =>
      authedFetch({
        method: "DELETE",
        path: `/v1/admin/networks/${networkId}/users/${userId}`,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["operator-networks", networkId, "users"],
      });
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const err = validateUuid(assignUuid, "user_id");
    if (err !== null) {
      setAssignError(
        new ApiError(0, { code: "network_user.invalid_user_id", message: err }),
      );
      return;
    }
    assign.mutate(assignUuid.trim());
  }

  const rows = query.data?.network_users ?? [];

  return (
    <div style={tabContentStyle}>
      <h2 style={tabHeadingStyle}>Network operators</h2>
      <p style={tabHintStyle}>
        Users assigned here hold the <code style={monoStyle}>network_operator</code>{" "}
        role and may manage this network's organizer and agent roster.
        Per migration 0044_network_permissions.sql, only{" "}
        <strong>platform operators</strong> can change this list (
        <code style={monoStyle}>network.manage_users</code> is NOT bound to{" "}
        <code style={monoStyle}>network_operator</code>).
      </p>
      <RosterStatus query={query} entityLabel="network operators" />
      {query.isSuccess ? (
        <table style={tableStyle} data-testid="users-table">
          <thead>
            <tr>
              <th scope="col" style={thStyle}>User</th>
              <th scope="col" style={thStyle}>Role</th>
              <th scope="col" style={thStyle}>Status</th>
              <th scope="col" style={thStyle}>Assigned</th>
              <th scope="col" style={thStyle} aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={5} style={emptyCellStyle} data-testid="users-empty">
                  No network operators assigned yet.
                </td>
              </tr>
            ) : null}
            {rows.map((u) => (
              <tr key={u.id} data-testid={`users-row-${u.user_id}`}>
                <td style={tdMonoStyle}>{u.user_id}</td>
                <td style={tdStyle}>{u.role}</td>
                <td style={tdStyle}><StatusBadge status={u.status} /></td>
                <td style={tdStyle}>{formatDate(u.created_at)}</td>
                <td style={tdActionsStyle}>
                  {canManage && !archived ? (
                    <button
                      type="button"
                      style={rowDangerButtonStyle}
                      onClick={() => remove.mutate(u.user_id)}
                      disabled={remove.isPending}
                      data-testid={`users-remove-${u.user_id}`}
                    >
                      Detach
                    </button>
                  ) : (
                    <span style={mutedHintStyle}>
                      {archived ? "archived" : "read-only"}
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}

      <AssignSection
        title="Assign a network operator"
        canManage={canManage}
        archived={archived}
        permissionName="network.manage_users"
        canManageExplanation="Only platform operators may assign network operators (network.manage_users is bound to platform_superadmin and admin only)."
      >
        <form onSubmit={onSubmit} style={formInlineStyle} noValidate>
          <label htmlFor="assign-user-uuid" style={inlineLabelStyle}>
            User UUID
          </label>
          <input
            id="assign-user-uuid"
            type="text"
            value={assignUuid}
            onChange={(e) => setAssignUuid(e.target.value)}
            style={inputMonoStyle}
            placeholder="0190a8b0-7d31-7a3c-9c4e-8c0c1d9d9c2a"
            data-testid="users-assign-input"
            required
            disabled={!canManage || archived || assign.isPending}
          />
          <button
            type="submit"
            style={primaryButtonStyle}
            disabled={!canManage || archived || assign.isPending}
            data-testid="users-assign-submit"
          >
            {assign.isPending ? "Assigning…" : "Assign"}
          </button>
        </form>
        {assignError !== null ? (
          <div style={formErrorStyle} role="alert" data-testid="users-assign-error">
            {assignError.message} (<code style={monoStyle}>{assignError.code}</code>)
          </div>
        ) : null}
        {remove.isError && remove.error !== null ? (
          <div style={formErrorStyle} role="alert" data-testid="users-remove-error">
            {remove.error.message} (<code style={monoStyle}>{remove.error.code}</code>)
          </div>
        ) : null}
      </AssignSection>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Organizers / Agents tab
// ---------------------------------------------------------------------------

interface OrgsTabProps {
  networkId: string;
  archived: boolean;
  kind: "organizer" | "agent";
}

function OrganizationsTab({ networkId, archived, kind }: OrgsTabProps) {
  const { permissions } = useAuth();
  const permName = kind === "organizer"
    ? "network.manage_organizers"
    : "network.manage_agents";
  const canManage = permissions.has(permName);
  const queryClient = useQueryClient();
  const [assignUuid, setAssignUuid] = useState("");
  const [assignError, setAssignError] = useState<ApiError | null>(null);

  const collectionPath = kind === "organizer" ? "organizers" : "agents";

  const query = useQuery<NetworkOrganizationListEnvelope, ApiError>({
    queryKey: ["operator-networks", networkId, collectionPath],
    queryFn: () =>
      authedFetch<NetworkOrganizationListEnvelope>({
        method: "GET",
        path: `/v1/admin/networks/${networkId}/${collectionPath}`,
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });

  const assign = useMutation<unknown, ApiError, string>({
    mutationFn: (orgId: string) =>
      authedFetch({
        method: "POST",
        path: `/v1/admin/networks/${networkId}/${collectionPath}`,
        body: { organization_id: orgId },
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["operator-networks", networkId, collectionPath],
      });
      setAssignUuid("");
      setAssignError(null);
    },
    onError: (err) => setAssignError(err),
  });

  const remove = useMutation<unknown, ApiError, string>({
    mutationFn: (orgId: string) =>
      authedFetch({
        method: "DELETE",
        path: `/v1/admin/networks/${networkId}/${collectionPath}/${orgId}`,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["operator-networks", networkId, collectionPath],
      });
    },
  });

  function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const err = validateUuid(assignUuid, "organization_id");
    if (err !== null) {
      setAssignError(
        new ApiError(0, {
          code: "network_org.invalid_organization_id",
          message: err,
        }),
      );
      return;
    }
    assign.mutate(assignUuid.trim());
  }

  const rows = useMemo<readonly NetworkOrganizationRow[]>(() => {
    if (query.data === undefined) {
      return [];
    }
    return kind === "organizer"
      ? query.data.organizers ?? []
      : query.data.agents ?? [];
  }, [query.data, kind]);

  const heading = kind === "organizer" ? "Organizers" : "Agents";
  const copy = kind === "organizer"
    ? "Organizations attached as kind=organizer sell tickets for events on behalf of the network."
    : "Organizations attached as kind=agent act as external sales channels — they can resell but never publish.";

  return (
    <div style={tabContentStyle}>
      <h2 style={tabHeadingStyle}>{heading}</h2>
      <p style={tabHintStyle}>
        {copy} Network operators and platform operators may manage this
        list (<code style={monoStyle}>{permName}</code>).
      </p>
      <RosterStatus
        query={query}
        entityLabel={kind === "organizer" ? "organizers" : "agents"}
      />
      {query.isSuccess ? (
        <table style={tableStyle} data-testid={`${collectionPath}-table`}>
          <thead>
            <tr>
              <th scope="col" style={thStyle}>Organization</th>
              <th scope="col" style={thStyle}>Status</th>
              <th scope="col" style={thStyle}>Attached</th>
              <th scope="col" style={thStyle} aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={4} style={emptyCellStyle} data-testid={`${collectionPath}-empty`}>
                  No {kind === "organizer" ? "organizers" : "agents"} attached yet.
                </td>
              </tr>
            ) : null}
            {rows.map((row) => (
              <tr key={row.id} data-testid={`${collectionPath}-row-${row.organization_id}`}>
                <td style={tdMonoStyle}>{row.organization_id}</td>
                <td style={tdStyle}><StatusBadge status={row.status} /></td>
                <td style={tdStyle}>{formatDate(row.attached_at)}</td>
                <td style={tdActionsStyle}>
                  {canManage && !archived ? (
                    <button
                      type="button"
                      style={rowDangerButtonStyle}
                      onClick={() => remove.mutate(row.organization_id)}
                      disabled={remove.isPending}
                      data-testid={`${collectionPath}-detach-${row.organization_id}`}
                    >
                      Detach
                    </button>
                  ) : (
                    <span style={mutedHintStyle}>
                      {archived ? "archived" : "read-only"}
                    </span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : null}

      <AssignSection
        title={kind === "organizer" ? "Attach an organizer" : "Attach an agent"}
        canManage={canManage}
        archived={archived}
        permissionName={permName}
        canManageExplanation={
          `Requires ${permName}. Bound to platform_superadmin, network_operator, and admin per migration 0044.`
        }
      >
        <form onSubmit={onSubmit} style={formInlineStyle} noValidate>
          <label
            htmlFor={`assign-${collectionPath}-uuid`}
            style={inlineLabelStyle}
          >
            Organization UUID
          </label>
          <input
            id={`assign-${collectionPath}-uuid`}
            type="text"
            value={assignUuid}
            onChange={(e) => setAssignUuid(e.target.value)}
            style={inputMonoStyle}
            placeholder="0190a8b0-7d31-7a3c-9c4e-8c0c1d9d9c2a"
            data-testid={`${collectionPath}-assign-input`}
            required
            disabled={!canManage || archived || assign.isPending}
          />
          <button
            type="submit"
            style={primaryButtonStyle}
            disabled={!canManage || archived || assign.isPending}
            data-testid={`${collectionPath}-assign-submit`}
          >
            {assign.isPending ? "Attaching…" : "Attach"}
          </button>
        </form>
        {assignError !== null ? (
          <div
            style={formErrorStyle}
            role="alert"
            data-testid={`${collectionPath}-assign-error`}
          >
            {assignError.message} (<code style={monoStyle}>{assignError.code}</code>)
          </div>
        ) : null}
        {remove.isError && remove.error !== null ? (
          <div
            style={formErrorStyle}
            role="alert"
            data-testid={`${collectionPath}-remove-error`}
          >
            {remove.error.message} (<code style={monoStyle}>{remove.error.code}</code>)
          </div>
        ) : null}
      </AssignSection>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Shared roster pieces
// ---------------------------------------------------------------------------

function RosterStatus<T>({
  query,
  entityLabel,
}: {
  query: UseQueryResult<T, ApiError>;
  entityLabel: string;
}) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status">
        Loading {entityLabel}…
      </div>
    );
  }
  if (query.isError) {
    const error = query.error;
    if (
      error instanceof ApiError &&
      (error.status === 403 || error.code === "permissions.denied")
    ) {
      return (
        <div style={errorBoxStyle} role="alert" data-testid="roster-forbidden">
          <strong>Forbidden.</strong>
          <p style={errorParaStyle}>
            Your account is missing the read permission for {entityLabel}.
          </p>
        </div>
      );
    }
    return (
      <div style={errorBoxStyle} role="alert" data-testid="roster-error">
        <strong>Failed to load {entityLabel}.</strong>
        <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
        {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
        <button
          type="button"
          style={errorRetryStyle}
          onClick={() => query.refetch()}
        >
          Retry
        </button>
      </div>
    );
  }
  return null;
}

interface AssignSectionProps {
  title: string;
  canManage: boolean;
  archived: boolean;
  permissionName: string;
  canManageExplanation: string;
  children: ReactNode;
}

function AssignSection({
  title,
  canManage,
  archived,
  permissionName,
  canManageExplanation,
  children,
}: AssignSectionProps) {
  return (
    <section style={assignBoxStyle} aria-label={title}>
      <h3 style={assignHeadingStyle}>{title}</h3>
      {archived ? (
        <p style={mutedHintStyle} data-testid="assign-archived-explain">
          Roster mutations are disabled because this network is archived.
        </p>
      ) : !canManage ? (
        <p style={mutedHintStyle} data-testid="assign-permission-explain">
          You lack <code style={monoStyle}>{permissionName}</code>.{" "}
          {canManageExplanation}
        </p>
      ) : null}
      {children}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Audit tab
// ---------------------------------------------------------------------------

function AuditTab() {
  return (
    <div style={tabContentStyle}>
      <h2 style={tabHeadingStyle}>Audit</h2>
      <div style={gapBoxStyle} data-testid="audit-gap">
        <strong>Backend gap (SAUI-09 G3).</strong>
        <p style={errorParaStyle}>
          The backend writes <code style={monoStyle}>v1.network.*</code>{" "}
          audit_events rows for every roster mutation (see{" "}
          <code style={monoStyle}>network_users.go</code> /{" "}
          <code style={monoStyle}>network_orgs.go</code>), but does not yet
          expose a scoped reader endpoint such as{" "}
          <code style={monoStyle}>GET /v1/admin/networks/{`{id}`}/audit</code>.
          Until that endpoint exists this tab cannot render a real timeline.
        </p>
        <p style={errorParaStyle}>
          Other documented gaps for SAUI-09:
        </p>
        <ul style={gapListStyle}>
          <li>
            <strong>G1.</strong> <code style={monoStyle}>/v1/admin/networks/*</code>{" "}
            mutations are NOT in the X-Admin-Reason allowlist
            (see <code style={monoStyle}>REASON_REQUIRED_PREFIXES</code> in{" "}
            <code style={monoStyle}>lib/api/reason.ts</code>). Either extend
            the backend allowlist or document the exemption.
          </li>
          <li>
            <strong>G2.</strong> No lookup endpoints for candidate users /
            organizations — operators must paste UUIDs.
          </li>
          <li>
            <strong>G4.</strong> List endpoints omit revoked rows. Add a
            <code style={monoStyle}>?include_revoked=true</code> flag to
            surface roster history.
          </li>
        </ul>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Helpers / format
// ---------------------------------------------------------------------------

export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return d.toISOString().slice(0, 16).replace("T", " ");
}

function StatusBadge({ status }: { status: string }) {
  const style = status === "active"
    ? badgeActiveStyle
    : status === "archived"
      ? badgeArchivedStyle
      : status === "revoked"
        ? badgeArchivedStyle
        : badgeNeutralStyle;
  return <span style={style}>{status}</span>;
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const pageStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
};

const breadcrumbStyle: CSSProperties = {
  display: "flex",
  gap: 6,
  fontSize: 12,
  color: "#64748b",
  alignItems: "center",
};
const breadcrumbLinkStyle: CSSProperties = {
  color: "#0369a1",
  textDecoration: "none",
};
const breadcrumbSepStyle: CSSProperties = { color: "#94a3b8" };
const breadcrumbCurrentStyle: CSSProperties = {
  color: "#0f172a",
  fontWeight: 600,
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
};
const archivedNoticeStyle: CSSProperties = {
  margin: "8px 0 0 0",
  padding: 8,
  background: "#fef3c7",
  border: "1px solid #fcd34d",
  borderRadius: 4,
  color: "#78350f",
  fontSize: 12,
  maxWidth: 640,
};
const refreshWrapStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "center",
};
const refreshButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const tabBarStyle: CSSProperties = {
  display: "flex",
  gap: 4,
  borderBottom: "1px solid #e2e8f0",
};
const tabStyle: CSSProperties = {
  background: "transparent",
  border: "none",
  borderBottom: "2px solid transparent",
  padding: "8px 12px",
  fontSize: 13,
  color: "#475569",
  cursor: "pointer",
  display: "inline-flex",
  alignItems: "center",
  gap: 6,
};
const tabActiveStyle: CSSProperties = {
  ...tabStyle,
  color: "#0f172a",
  borderBottomColor: "#0369a1",
  fontWeight: 600,
};
const tabBadgeStyle: CSSProperties = {
  fontSize: 10,
  padding: "2px 6px",
  background: "#fef3c7",
  color: "#78350f",
  borderRadius: 999,
  fontWeight: 500,
};

const tabPanelStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
};

const tabContentStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 12,
};
const tabHeadingStyle: CSSProperties = {
  margin: 0,
  fontSize: 16,
  fontWeight: 600,
  color: "#0f172a",
};
const tabHintStyle: CSSProperties = {
  margin: 0,
  fontSize: 12,
  color: "#475569",
  lineHeight: 1.5,
  maxWidth: 720,
};

const overviewGridStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "1fr",
  gap: 4,
};
const metaRowStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "180px 1fr",
  gap: 12,
  padding: "6px 0",
  borderBottom: "1px solid #f1f5f9",
  fontSize: 13,
};
const metaKeyStyle: CSSProperties = {
  margin: 0,
  color: "#475569",
  fontWeight: 600,
};
const metaValStyle: CSSProperties = { margin: 0, color: "#0f172a" };

const copyBoxStyle: CSSProperties = {
  marginTop: 16,
  padding: 12,
  background: "#f8fafc",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
};
const copyBoxHeadingStyle: CSSProperties = {
  margin: "0 0 8px 0",
  fontSize: 13,
  fontWeight: 600,
};
const copyDlStyle: CSSProperties = { margin: 0 };
const copyDtStyle: CSSProperties = {
  fontWeight: 600,
  fontSize: 12,
  marginTop: 6,
  color: "#0f172a",
};
const copyDdStyle: CSSProperties = {
  margin: "2px 0 0 0",
  fontSize: 12,
  color: "#475569",
  lineHeight: 1.5,
};

const tableStyle: CSSProperties = {
  width: "100%",
  borderCollapse: "collapse",
  fontSize: 13,
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  overflow: "hidden",
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
const tdStyle: CSSProperties = {
  padding: "10px 12px",
  borderBottom: "1px solid #f1f5f9",
  color: "#0f172a",
  verticalAlign: "middle",
};
const tdMonoStyle: CSSProperties = {
  ...tdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
  color: "#334155",
};
const tdActionsStyle: CSSProperties = {
  ...tdStyle,
  display: "flex",
  gap: 6,
  flexWrap: "wrap",
};
const emptyCellStyle: CSSProperties = {
  padding: 16,
  textAlign: "center",
  fontSize: 12,
  color: "#94a3b8",
  fontStyle: "italic",
  borderBottom: "1px solid #f1f5f9",
};

const badgeBaseStyle: CSSProperties = {
  fontSize: 10,
  padding: "2px 6px",
  borderRadius: 999,
  fontWeight: 600,
  textTransform: "uppercase",
  letterSpacing: 0.4,
  display: "inline-block",
};
const badgeActiveStyle: CSSProperties = {
  ...badgeBaseStyle,
  background: "#dcfce7",
  color: "#166534",
};
const badgeArchivedStyle: CSSProperties = {
  ...badgeBaseStyle,
  background: "#fee2e2",
  color: "#7f1d1d",
};
const badgeNeutralStyle: CSSProperties = {
  ...badgeBaseStyle,
  background: "#e2e8f0",
  color: "#334155",
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

const assignBoxStyle: CSSProperties = {
  marginTop: 8,
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
  display: "flex",
  flexDirection: "column",
  gap: 8,
};
const assignHeadingStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  fontWeight: 600,
  color: "#0f172a",
};
const formInlineStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "center",
  flexWrap: "wrap",
};
const inlineLabelStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  color: "#334155",
};
const inputMonoStyle: CSSProperties = {
  fontSize: 12,
  padding: "8px 10px",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  background: "#ffffff",
  color: "#0f172a",
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  minWidth: 320,
};
const primaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "8px 14px",
  background: "#0369a1",
  border: "1px solid #0369a1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
};
const rowDangerButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#ffffff",
  border: "1px solid #fca5a5",
  borderRadius: 4,
  cursor: "pointer",
  color: "#7f1d1d",
};
const mutedHintStyle: CSSProperties = {
  fontSize: 11,
  color: "#94a3b8",
  fontStyle: "italic",
};
const formErrorStyle: CSSProperties = {
  fontSize: 12,
  padding: 8,
  background: "#fef2f2",
  border: "1px solid #fca5a5",
  color: "#7f1d1d",
  borderRadius: 4,
};

const gapBoxStyle: CSSProperties = {
  padding: 12,
  border: "1px solid #fcd34d",
  background: "#fffbeb",
  borderRadius: 6,
  color: "#78350f",
  fontSize: 12,
  display: "flex",
  flexDirection: "column",
  gap: 6,
};
const gapListStyle: CSSProperties = {
  margin: 0,
  paddingLeft: 18,
  fontSize: 12,
  display: "flex",
  flexDirection: "column",
  gap: 4,
};

const monoStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};
const inlineLinkStyle: CSSProperties = {
  color: "#0369a1",
  textDecoration: "underline",
};
