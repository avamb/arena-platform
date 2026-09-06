/**
 * Customer card (feature #482, W1-A4d part 3).
 *
 * Detail page for a single customer, backed by
 * apps/backend/internal/platform/httpserver/hcustomers/handler.go:
 *
 *   GET /v1/organizations/{org_id}/customers/{id}
 *
 * Requires `customer.read`; scoped via customer_org_links.org_id = org — a
 * customer never linked to the caller's org is invisible, even by direct
 * id lookup (404 customers.not_found).
 *
 * Org id is carried in the `?org=` search param (see the customers.tsx
 * list page, which links here with `search={{ org }}`) rather than a
 * second path segment, matching the payments.tsx / orders.tsx related-data
 * convention of passing org context via search rather than nested routes.
 *
 * Identity masking (email/phone/telegram, unless verified) is entirely
 * server-side (see maskStrongIdentity in handler.go) — this page renders
 * `identities[].value` verbatim, with no client-side masking logic.
 *
 * Mock data: NONE. The card hits the live backend. No globalThis /
 * devStore / mockDb.
 */
import { createRoute, Link, useParams, useSearch } from "@tanstack/react-router";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";
import { useState, type CSSProperties } from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import type { NavEntry } from "@/lib/auth/navConfig";

// ---------------------------------------------------------------------------
// Route registration
// ---------------------------------------------------------------------------

interface CustomerDetailSearch {
  readonly org?: string;
}

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/customers/$id",
  component: CustomerDetailRoute,
  validateSearch: (raw: Record<string, unknown>): CustomerDetailSearch => {
    const org = raw.org;
    return { org: typeof org === "string" && org.length > 0 ? org : undefined };
  },
});

/**
 * Synthetic nav entry used by RequirePermission for the detail route. It is
 * NOT added to NAV_ENTRIES (no sidebar entry) — the parent /customers entry
 * already covers visibility; this exists purely so RequirePermission can
 * verify the caller still holds customer.read on direct navigation.
 */
const CUSTOMER_DETAIL_NAV_ENTRY: NavEntry = {
  id: "customers-detail",
  label: "Customer detail",
  to: "/customers",
  permission: { anyOf: ["customer.read"] },
  scopeKinds: ["global", "platform", "network", "organization"],
  purpose: "Inspect a single customer's card. Requires customer.read.",
};

// ---------------------------------------------------------------------------
// Response shapes (mirrors HandleGet in hcustomers/handler.go)
// ---------------------------------------------------------------------------

export interface CustomerIdentity {
  readonly kind: string;
  readonly value: string;
  readonly verified: boolean;
}

export interface CustomerOrder {
  readonly id: string;
  readonly system_id: number;
  readonly status: string;
  readonly currency: string;
  readonly total: number;
  readonly created_at: string;
}

export interface CustomerAttribute {
  readonly key: string;
  readonly value: string;
  readonly org_scoped: boolean;
  readonly source: string;
}

export interface CustomerConsent {
  readonly kind: string;
  readonly given_at: string;
  readonly withdrawn_at: string | null;
}

export interface CustomerCard {
  readonly id: string;
  readonly system_id: number;
  readonly display_name: string | null;
  readonly locale: string | null;
  readonly created_at: string;
  readonly updated_at: string;
  readonly identities: readonly CustomerIdentity[];
  readonly orders: readonly CustomerOrder[];
  readonly attributes: readonly CustomerAttribute[];
  readonly consents: readonly CustomerConsent[];
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

type TabId = "identities" | "orders" | "attributes" | "consents";

const TABS: ReadonlyArray<{ id: TabId; label: string; testid: string }> = [
  { id: "identities", label: "Identities", testid: "tab-identities" },
  { id: "orders", label: "Orders", testid: "tab-orders" },
  { id: "attributes", label: "Attributes", testid: "tab-attributes" },
  { id: "consents", label: "Consents", testid: "tab-consents" },
];

function CustomerDetailRoute() {
  return (
    <RequirePermission entry={CUSTOMER_DETAIL_NAV_ENTRY}>
      <CustomerDetailModule />
    </RequirePermission>
  );
}

function CustomerDetailModule() {
  const { id } = useParams({ from: "/customers/$id" });
  const { org } = useSearch({ from: "/customers/$id" });
  const orgID = org ?? "";
  const [tab, setTab] = useState<TabId>("identities");

  const query = useQuery<CustomerCard, ApiError>({
    queryKey: ["customers", "detail", orgID, id],
    enabled: orgID !== "",
    queryFn: () =>
      authedFetch<CustomerCard>({
        method: "GET",
        path: `/v1/organizations/${orgID}/customers/${id}`,
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

  return (
    <section aria-labelledby="customer-detail-heading" style={pageStyle}>
      <Breadcrumb id={id} name={query.data?.display_name ?? null} orgID={orgID} />

      {orgID === "" ? (
        <div style={statusBoxStyle} role="status" data-testid="customer-detail-org-required">
          No organization context. Open this page via the Customers list so
          the <code style={monoStyle}>org</code> id is carried in the URL.
        </div>
      ) : (
        <>
          <DetailHeader query={query} />
          {query.isSuccess ? (
            <>
              <TabBar tab={tab} onChange={setTab} />
              <div style={tabPanelStyle}>
                {tab === "identities" ? (
                  <IdentitiesTab identities={query.data.identities} />
                ) : null}
                {tab === "orders" ? <OrdersTab orders={query.data.orders} /> : null}
                {tab === "attributes" ? (
                  <AttributesTab attributes={query.data.attributes} />
                ) : null}
                {tab === "consents" ? (
                  <ConsentsTab consents={query.data.consents} />
                ) : null}
              </div>
            </>
          ) : null}
        </>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Header / Breadcrumb
// ---------------------------------------------------------------------------

function Breadcrumb({
  id,
  name,
  orgID,
}: {
  id: string;
  name: string | null;
  orgID: string;
}) {
  return (
    <nav aria-label="Breadcrumb" style={breadcrumbStyle}>
      <Link
        to={orgID === "" ? "/customers" : (`/customers?org=${orgID}` as "/customers")}
        style={breadcrumbLinkStyle}
      >
        Customers
      </Link>
      <span style={breadcrumbSepStyle}>/</span>
      <span style={breadcrumbCurrentStyle} title={id}>
        {name ?? id}
      </span>
    </nav>
  );
}

function DetailHeader({ query }: { query: UseQueryResult<CustomerCard, ApiError> }) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading customer…
      </div>
    );
  }
  if (query.isError) {
    return <DetailErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  const c = query.data;
  return (
    <header style={headerStyle}>
      <div>
        <h1 id="customer-detail-heading" style={headingStyle}>
          {c.display_name ?? `Customer #${c.system_id}`}
        </h1>
        <p style={subheadingStyle}>
          System ID <code style={monoStyle}>{c.system_id}</code> · ID{" "}
          <code style={monoStyle}>{c.id}</code>
          {c.locale ? (
            <>
              {" "}
              · Locale <code style={monoStyle}>{c.locale}</code>
            </>
          ) : null}
        </p>
      </div>
      <div style={refreshWrapStyle}>
        <button
          type="button"
          style={refreshButtonStyle}
          onClick={() => query.refetch()}
          disabled={query.isFetching}
          data-testid="customer-detail-refresh"
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
      <div style={errorBoxStyle} role="alert" data-testid="customer-detail-not-found">
        <strong>Customer not found.</strong>
        <p style={errorParaStyle}>
          {error.message || "This customer does not exist, or is not linked to this organization."}{" "}
          Use the <Link to="/customers" style={inlineLinkStyle}>customers list</Link>{" "}
          to pick another customer.
        </p>
      </div>
    );
  }
  if (
    error instanceof ApiError &&
    (error.status === 403 || error.code === "permissions.denied")
  ) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="customer-detail-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>customer.read</code>.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="customer-detail-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload this customer.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="customer-detail-error">
      <strong>Failed to load customer.</strong>
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

function TabBar({ tab, onChange }: { tab: TabId; onChange: (next: TabId) => void }) {
  return (
    <div role="tablist" aria-label="Customer sections" style={tabBarStyle}>
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
        </button>
      ))}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Identities tab
// ---------------------------------------------------------------------------

function IdentitiesTab({ identities }: { identities: readonly CustomerIdentity[] }) {
  if (identities.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="identities-empty">
        No identities on record for this customer.
      </div>
    );
  }
  return (
    <table style={tableStyle} data-testid="identities-table">
      <thead>
        <tr>
          <th scope="col" style={thStyle}>Kind</th>
          <th scope="col" style={thStyle}>Value</th>
          <th scope="col" style={thStyle}>Verified</th>
        </tr>
      </thead>
      <tbody>
        {identities.map((ident, i) => (
          <tr key={`${ident.kind}-${i}`} data-testid={`identities-row-${i}`}>
            <td style={tdStyle}>{ident.kind}</td>
            <td style={tdMonoStyle}>{ident.value}</td>
            <td style={tdStyle}>
              <StatusBadge status={ident.verified ? "verified" : "unverified"} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ---------------------------------------------------------------------------
// Orders tab
// ---------------------------------------------------------------------------

function OrdersTab({ orders }: { orders: readonly CustomerOrder[] }) {
  if (orders.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="orders-empty">
        No orders for this customer in this organization.
      </div>
    );
  }
  return (
    <table style={tableStyle} data-testid="customer-orders-table">
      <thead>
        <tr>
          <th scope="col" style={thStyle}>System ID</th>
          <th scope="col" style={thStyle}>Status</th>
          <th scope="col" style={thStyle}>Total</th>
          <th scope="col" style={thStyle}>Created</th>
        </tr>
      </thead>
      <tbody>
        {orders.map((o) => (
          <tr key={o.id} data-testid={`customer-orders-row-${o.id}`}>
            <td style={tdMonoStyle}>{o.system_id}</td>
            <td style={tdStyle}><StatusBadge status={o.status} /></td>
            <td style={tdStyle}>{formatMoney(o.total, o.currency)}</td>
            <td style={tdStyle}>{formatDate(o.created_at)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ---------------------------------------------------------------------------
// Attributes tab
// ---------------------------------------------------------------------------

function AttributesTab({ attributes }: { attributes: readonly CustomerAttribute[] }) {
  if (attributes.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="attributes-empty">
        No attributes on record for this customer.
      </div>
    );
  }
  return (
    <table style={tableStyle} data-testid="attributes-table">
      <thead>
        <tr>
          <th scope="col" style={thStyle}>Key</th>
          <th scope="col" style={thStyle}>Value</th>
          <th scope="col" style={thStyle}>Scope</th>
          <th scope="col" style={thStyle}>Source</th>
        </tr>
      </thead>
      <tbody>
        {attributes.map((a, i) => (
          <tr key={`${a.key}-${i}`} data-testid={`attributes-row-${i}`}>
            <td style={tdStyle}>{a.key}</td>
            <td style={tdMonoStyle}>{a.value}</td>
            <td style={tdStyle}>{a.org_scoped ? "org" : "platform"}</td>
            <td style={tdStyle}>{a.source}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ---------------------------------------------------------------------------
// Consents tab
// ---------------------------------------------------------------------------

function ConsentsTab({ consents }: { consents: readonly CustomerConsent[] }) {
  if (consents.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="consents-empty">
        No consents on record for this customer in this organization.
      </div>
    );
  }
  return (
    <table style={tableStyle} data-testid="consents-table">
      <thead>
        <tr>
          <th scope="col" style={thStyle}>Kind</th>
          <th scope="col" style={thStyle}>Given</th>
          <th scope="col" style={thStyle}>Withdrawn</th>
        </tr>
      </thead>
      <tbody>
        {consents.map((c, i) => (
          <tr key={`${c.kind}-${i}`} data-testid={`consents-row-${i}`}>
            <td style={tdStyle}>{c.kind}</td>
            <td style={tdStyle}>{formatDate(c.given_at)}</td>
            <td style={tdStyle}>
              {c.withdrawn_at !== null ? formatDate(c.withdrawn_at) : "—"}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

// ---------------------------------------------------------------------------
// Helpers / format
// ---------------------------------------------------------------------------

function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return d.toISOString().slice(0, 16).replace("T", " ");
}

function formatMoney(minor: number, currency: string): string {
  return `${(minor / 100).toFixed(2)} ${currency}`;
}

function StatusBadge({ status }: { status: string }) {
  const style =
    status === "verified" || status === "active" || status === "paid" || status === "completed"
      ? badgeActiveStyle
      : status === "unverified" || status === "cancelled" || status === "refunded"
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
};
const tabActiveStyle: CSSProperties = {
  ...tabStyle,
  color: "#0f172a",
  borderBottomColor: "#0369a1",
  fontWeight: 600,
};

const tabPanelStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 16,
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

const monoStyle: CSSProperties = {
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};
const inlineLinkStyle: CSSProperties = {
  color: "#0369a1",
  textDecoration: "underline",
};
