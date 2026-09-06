/**
 * Customers directory (feature #482, W1-A4d part 3).
 *
 * Org-scoped read surface backed by
 * apps/backend/internal/platform/httpserver/hcustomers/handler.go:
 *
 *   GET /v1/organizations/{org_id}/customers?q=&limit=&offset= — search/list
 *   GET /v1/organizations/{org_id}/customers/{id}             — card (see
 *                                                                customerDetail.tsx)
 *
 * Both routes require `customer.read` and are scoped via
 * customer_org_links.org_id = org — a customer never linked to the
 * caller's org is invisible, even by direct id lookup.
 *
 * Org scoping follows the payments.tsx convention: the org id is read
 * from the `?org=` query param (see `@/lib/routing/orgContext`), falling
 * back to the active scope when it is an organization scope, with a
 * manual UUID-paste override and an org `<select>` picker sourced from
 * GET /v1/organizations.
 *
 * Mock data: NONE. The list hits the live backend. No globalThis /
 * devStore / mockDb.
 */
import { createRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type CSSProperties, type FormEvent } from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { useScope } from "@/lib/auth/ScopeContext";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { orgContextFromLocation } from "@/lib/routing/orgContext";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/customers",
  component: CustomersRoute,
});

// ---------------------------------------------------------------------------
// Backend response shapes
// ---------------------------------------------------------------------------

interface OrganizationSummary {
  readonly id: string;
  readonly name: string;
  readonly slug?: string;
}

interface OrganizationListEnvelope {
  readonly organizations: readonly OrganizationSummary[];
}

export interface CustomerSummary {
  readonly id: string;
  readonly system_id: number;
  readonly display_name: string | null;
  readonly locale: string | null;
  readonly created_at: string;
  readonly updated_at: string;
}

interface CustomerListEnvelope {
  readonly customers: readonly CustomerSummary[];
  readonly limit: number;
  readonly offset: number;
}

// ---------------------------------------------------------------------------
// Validators (mirror backend contracts, apps/backend/.../hcustomers/handler.go)
// ---------------------------------------------------------------------------

export const UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export function validateOrgID(orgID: string): string | null {
  if (orgID.trim() === "") {
    return "Organization ID is required";
  }
  if (!UUID_RE.test(orgID.trim())) {
    return "Organization ID must be a UUID";
  }
  return null;
}

/**
 * Mirrors the `len(q) > 200` rejection in HandleList: 200 chars is the
 * largest valid query, 201 is rejected with customers.invalid_query.
 */
export function validateCustomerQuery(q: string): string | null {
  if (q.trim().length > 200) {
    return "Search must be at most 200 characters";
  }
  return null;
}

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const CUSTOMERS_NAV_ENTRY = NAV_BY_PATH["/customers"];
if (CUSTOMERS_NAV_ENTRY === undefined) {
  throw new Error("customers route: NAV_BY_PATH['/customers'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function CustomersRoute() {
  return (
    <RequirePermission entry={CUSTOMERS_NAV_ENTRY}>
      <CustomersModule />
    </RequirePermission>
  );
}

function CustomersModule() {
  const { activeScope } = useScope();

  const scopeOrgID =
    orgContextFromLocation() ||
    (activeScope?.kind === "organization" && activeScope.id !== null
      ? activeScope.id
      : "");

  const [orgID, setOrgID] = useState(scopeOrgID);
  const trimmedOrgID = orgID.trim();
  const orgIDError = orgID === "" ? null : validateOrgID(orgID);
  const orgReady = trimmedOrgID !== "" && orgIDError === null;

  const orgsQuery = useQuery<OrganizationListEnvelope, ApiError>({
    queryKey: ["organizations", "customers-picker"],
    queryFn: () =>
      authedFetch<OrganizationListEnvelope>({
        method: "GET",
        path: "/v1/organizations",
      }),
    retry: false,
    refetchOnWindowFocus: false,
  });
  const orgsSorted = useMemo(
    () =>
      [...(orgsQuery.data?.organizations ?? [])].sort((a, b) =>
        a.name.localeCompare(b.name),
      ),
    [orgsQuery.data],
  );

  const [qInput, setQInput] = useState("");
  const [q, setQ] = useState("");
  const qError = validateCustomerQuery(qInput);

  const query = useQuery<CustomerListEnvelope, ApiError>({
    queryKey: ["customers", "list", trimmedOrgID, q],
    enabled: orgReady,
    queryFn: () =>
      authedFetch<CustomerListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${trimmedOrgID}/customers?q=${encodeURIComponent(q)}`,
      }),
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (err.status === 401 || err.status === 403 || err.status === 0) {
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

  const rows = query.data?.customers ?? [];

  function onSearchSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (qError !== null) {
      return;
    }
    setQ(qInput.trim());
  }

  return (
    <section aria-labelledby="customers-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="customers-heading" style={headingStyle}>
            Customers
          </h1>
          <p style={subheadingStyle}>
            Org-scoped customer directory. Strong identities (email, phone,
            telegram) are masked by the backend unless verified — this UI
            renders whatever the API returns without additional masking.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={refreshButtonStyle}
            disabled={!orgReady || query.isFetching}
            data-testid="customers-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </header>

      <div style={orgPickerStyle}>
        <label htmlFor="customers-org-id" style={fieldLabelStyle}>
          Organization
        </label>
        {orgsSorted.length > 0 ? (
          <select
            id="customers-org-select"
            value={orgID}
            onChange={(e) => setOrgID(e.target.value)}
            style={inputStyle}
            data-testid="customers-org-select"
          >
            <option value="">Select an organization…</option>
            {orgsSorted.map((o) => (
              <option key={o.id} value={o.id}>
                {o.name}
              </option>
            ))}
          </select>
        ) : null}
        <input
          id="customers-org-id"
          type="text"
          value={orgID}
          onChange={(e) => setOrgID(e.target.value)}
          style={inputMonoStyle}
          placeholder="0190a8b0-7d31-7a3c-9c4e-8c0c1d9d9c2a"
          data-testid="customers-org-input"
        />
        {orgIDError !== null ? (
          <div style={fieldErrorStyle} data-testid="customers-org-error">
            {orgIDError}
          </div>
        ) : (
          <div style={fieldHintStyle}>
            Paste an organization UUID, or pick one from the list above.
          </div>
        )}
      </div>

      {orgReady ? (
        <>
          <form onSubmit={onSearchSubmit} style={searchFormStyle} noValidate>
            <label htmlFor="customers-search" style={fieldLabelStyle}>
              Search
            </label>
            <input
              id="customers-search"
              type="text"
              value={qInput}
              onChange={(e) => setQInput(e.target.value)}
              style={inputStyle}
              maxLength={201}
              placeholder="Name, email, phone…"
              data-testid="customers-search-input"
            />
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={qError !== null}
              data-testid="customers-search-submit"
            >
              Search
            </button>
            {qError !== null ? (
              <div style={fieldErrorStyle} data-testid="customers-search-error">
                {qError}
              </div>
            ) : null}
          </form>

          <CustomersBody
            query={query}
            rows={rows}
            orgID={trimmedOrgID}
          />
        </>
      ) : (
        <div style={statusBoxStyle} role="status" data-testid="customers-org-required">
          Select or paste an organization to load its customers.
        </div>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Table body and states
// ---------------------------------------------------------------------------

interface BodyProps {
  query: ReturnType<typeof useQuery<CustomerListEnvelope, ApiError>>;
  rows: readonly CustomerSummary[];
  orgID: string;
}

function CustomersBody({ query, rows, orgID }: BodyProps) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading customers…
      </div>
    );
  }
  if (query.isError) {
    return (
      <CustomersErrorState error={query.error} onRetry={() => query.refetch()} />
    );
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="customers-empty">
        No customers found for this organization and search.
      </div>
    );
  }
  return (
    <div style={tableWrapStyle} role="region" aria-label="Customers">
      <table style={tableStyle} data-testid="customers-table">
        <thead>
          <tr>
            <th scope="col" style={thStyle}>System ID</th>
            <th scope="col" style={thStyle}>Display name</th>
            <th scope="col" style={thStyle}>Locale</th>
            <th scope="col" style={thStyle}>Created</th>
            <th scope="col" style={thStyle}>Updated</th>
            <th scope="col" style={thStyle} aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {rows.map((c) => (
            <tr key={c.id} data-testid={`customers-row-${c.id}`}>
              <td style={tdMonoStyle}>{c.system_id}</td>
              <td style={tdStyle}>{c.display_name ?? "—"}</td>
              <td style={tdStyle}>{c.locale ?? "—"}</td>
              <td style={tdStyle}>{formatDate(c.created_at)}</td>
              <td style={tdStyle}>{formatDate(c.updated_at)}</td>
              <td style={tdActionsStyle}>
                <Link
                  to={`/customers/${c.id}` as "/"}
                  search={{ org: orgID } as unknown as Record<string, never>}
                  style={rowActionButtonStyle}
                  data-testid={`customers-view-${c.id}`}
                >
                  View
                </Link>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function CustomersErrorState({
  error,
  onRetry,
}: {
  error: ApiError | null;
  onRetry: () => void;
}) {
  if (
    error instanceof ApiError &&
    (error.status === 403 || error.code === "permissions.denied")
  ) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="customers-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>customer.read</code>.
          Ask a platform administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="customers-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload customers.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="customers-error">
      <strong>Failed to load customers.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Server error mapping (exported per SAUI convention, used by callers
// that need field-level copy for hcustomers error codes)
// ---------------------------------------------------------------------------

export interface ServerFieldErrors {
  q?: string;
  org?: string;
  form?: string;
}

export function mapServerError(err: ApiError): ServerFieldErrors {
  const out: ServerFieldErrors = {};
  switch (err.code) {
    case "customers.invalid_query":
      out.q = err.message;
      return out;
    case "customers.invalid_limit":
    case "customers.invalid_offset":
      out.form = err.message;
      return out;
    case "customers.not_found":
      out.form = err.message;
      return out;
    case "dependency.database_unavailable":
      out.form = err.message;
      return out;
    case "permissions.denied":
      out.form =
        "Your account is missing the required permission. Ask a platform administrator.";
      return out;
    default:
      out.form = `${err.message} (${err.code})`;
      return out;
  }
}

// ---------------------------------------------------------------------------
// Format helpers
// ---------------------------------------------------------------------------

function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) {
    return iso;
  }
  return d.toISOString().slice(0, 10);
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

const refreshWrapStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "center",
  flexWrap: "wrap",
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

const rowActionButtonStyle: CSSProperties = {
  fontSize: 11,
  padding: "4px 10px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
  textDecoration: "none",
};

const orgPickerStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 6,
  padding: 12,
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  background: "#ffffff",
  maxWidth: 480,
};

const searchFormStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "flex-end",
  flexWrap: "wrap",
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
