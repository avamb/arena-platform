/**
 * SuperAdmin Orders support console (SAUI-10).
 *
 * Backed by GET /v1/admin/orders (see
 * apps/backend/internal/platform/httpserver/superadmin.go). The endpoint:
 *
 *   - requires `superadmin.read` permission;
 *   - requires the `X-Admin-Reason` header (cross-tenant read);
 *   - accepts only `org_id`, `state`, `limit`, `offset` query parameters
 *     -- no free-text search, no created-at range, no channel filter;
 *   - returns the rows along with `total = len(rows)` (NOT a global
 *     count). The pagination UI is therefore offset-only with a
 *     "next available" inferred from a full page.
 *
 * Read-only. No write actions are exposed -- the SAUI-10 contract is
 * explicit that support consoles ship without destructive controls
 * until a richer permissions/audit contract lands.
 *
 * Filter contract (toolbar -> backend, exact mapping):
 *
 *   org_id UUID input        -> ?org_id=<uuid>
 *   state dropdown           -> ?state=<state>
 *   page size dropdown       -> ?limit=<n>
 *   prev/next pagination     -> ?offset=<n>
 *
 * Any future filters (channel_id, completed_at range, user_id) require
 * a corresponding backend change first; the toolbar deliberately does
 * NOT pretend to support them.
 *
 * The detail drawer exposes only the fields the list endpoint returns;
 * a richer single-order endpoint does not exist today and that gap is
 * documented inline so operators are not misled about availability.
 *
 * Mock data: NONE. The page renders only what the backend returns.
 */
import { createRoute, Link } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import {
  useEscapeClose,
  useFocusOnMount,
  useFocusRestore,
} from "@/lib/a11y";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { RequirePermission } from "@/components/RequirePermission";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { SupportErrorState } from "@/components/admin/SupportErrorState";
import {
  SUPPORT_LIMIT_CHOICES,
  buildSupportQuery,
  canGoNext,
  canGoPrev,
  clampLimit,
  clampOffset,
  currentPage,
  formatDateTime,
  formatMoneyMinor,
  isValidUuid,
  readSupportFiltersFromLocation,
  shortUuid,
  type SupportFilters,
} from "@/lib/admin/supportConsole";
import * as S from "@/lib/admin/supportStyles";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/orders",
  component: OrdersRoute,
});

/**
 * Known order states. Aligned with `checkout_sessions.state` in
 * apps/backend/internal/platform/persistence/migrations/*.sql.
 *
 * Keeping the dropdown values pinned here means the operator only ever
 * sees server-recognised values. Unknown values would be silently
 * rejected by the backend with `superadmin.invalid_state` (the backend
 * currently passes them through as-is, but pinning here keeps the UX
 * predictable). Adding a new state -> update both ends.
 */
export const ORDER_STATES: readonly string[] = [
  "created",
  "in_progress",
  "completed",
  "expired",
  "cancelled",
  "failed",
];

export interface AdminOrder {
  readonly id: string;
  readonly org_id: string;
  readonly channel_id: string;
  readonly reservation_id: string;
  readonly state: string;
  readonly user_id: string | null;
  readonly total: number | null;
  readonly currency: string | null;
  readonly completed_at: string | null;
  readonly created_at: string;
  readonly updated_at: string;
}

interface OrdersEnvelope {
  readonly orders: readonly AdminOrder[];
  readonly total: number;
  readonly limit: number;
  readonly offset: number;
}

const NAV_ENTRY = NAV_BY_PATH["/orders"];
if (NAV_ENTRY === undefined) {
  throw new Error("orders route: NAV_BY_PATH['/orders'] missing");
}

function OrdersRoute() {
  return (
    <RequirePermission entry={NAV_ENTRY}>
      <OrdersConsole />
    </RequirePermission>
  );
}

function OrdersConsole() {
  const initial = useMemo<SupportFilters>(() => {
    if (typeof window === "undefined") {
      return { orgId: "", statusValue: "", limit: 50, offset: 0 };
    }
    return readSupportFiltersFromLocation(window.location.search, "state");
  }, []);
  const [orgIdInput, setOrgIdInput] = useState(initial.orgId);
  const [state, setState] = useState(initial.statusValue);
  const [limit, setLimit] = useState<number>(initial.limit);
  const [offset, setOffset] = useState<number>(initial.offset);
  const [activeOrderId, setActiveOrderId] = useState<string | null>(null);

  // Validity gate: empty org_id is fine (no filter); non-empty must be UUID.
  const orgIdInvalid =
    orgIdInput.trim() !== "" && !isValidUuid(orgIdInput.trim());

  const filters: SupportFilters = {
    orgId: orgIdInvalid ? "" : orgIdInput,
    statusValue: state,
    limit,
    offset,
  };

  const query = useQuery<OrdersEnvelope, ApiError>({
    queryKey: ["admin", "orders", filters],
    queryFn: () =>
      authedFetch<OrdersEnvelope>({
        method: "GET",
        path: `/v1/admin/orders?${buildSupportQuery(filters, "state")}`,
      }),
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (err.status === 401 || err.status === 403 || err.status === 0) {
          return false;
        }
        if (
          err.code === "superadmin.reason_required" ||
          err.code === "superadmin.missing_reason" ||
          err.code === "permissions.denied"
        ) {
          return false;
        }
      }
      return failureCount < 2;
    },
    refetchOnWindowFocus: false,
  });

  const rows = query.data?.orders ?? [];
  const activeOrder = useMemo(
    () =>
      activeOrderId === null
        ? null
        : rows.find((o) => o.id === activeOrderId) ?? null,
    [activeOrderId, rows],
  );

  // Reset pagination when filter inputs change.
  useEffect(() => {
    setOffset(0);
    setActiveOrderId(null);
  }, [orgIdInput, state, limit]);

  return (
    <section aria-labelledby="orders-heading" style={S.pageStyle}>
      <header style={S.headerStyle}>
        <div>
          <h1 id="orders-heading" style={S.headingStyle}>
            Orders
          </h1>
          <p style={S.subheadingStyle}>
            Cross-tenant checkout sessions. Filters map directly to the
            backend's <code>org_id</code>, <code>state</code>,{" "}
            <code>limit</code>, <code>offset</code> query parameters;
            free-text search is unavailable until the backend exposes
            it. Read-only; no support write actions are wired here.
          </p>
        </div>
        <div style={S.refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={S.refreshButtonStyle}
            disabled={query.isFetching}
            data-testid="orders-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </header>

      <div style={S.toolbarStyle}>
        <label style={S.fieldGroupStyle}>
          <span style={S.fieldLabelStyle}>Organization ID</span>
          <input
            type="text"
            inputMode="text"
            placeholder="UUID (optional)"
            value={orgIdInput}
            onChange={(e) => setOrgIdInput(e.target.value)}
            style={orgIdInvalid ? S.inputInvalidStyle : S.inputStyle}
            data-testid="orders-org-id"
            aria-invalid={orgIdInvalid}
            aria-describedby={orgIdInvalid ? "orders-org-id-err" : undefined}
          />
          {orgIdInvalid ? (
            <span
              id="orders-org-id-err"
              style={{ color: "#7f1d1d", fontSize: 11 }}
              data-testid="orders-org-id-error"
            >
              Must be a valid UUID — filter not applied.
            </span>
          ) : null}
        </label>
        <label style={S.fieldGroupStyle}>
          <span style={S.fieldLabelStyle}>State</span>
          <select
            value={state}
            onChange={(e) => setState(e.target.value)}
            style={S.selectStyle}
            data-testid="orders-state"
          >
            <option value="">Any state</option>
            {ORDER_STATES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </label>
        <label style={S.fieldGroupStyle}>
          <span style={S.fieldLabelStyle}>Page size</span>
          <select
            value={String(limit)}
            onChange={(e) => setLimit(clampLimit(Number(e.target.value)))}
            style={S.selectStyle}
            data-testid="orders-limit"
          >
            {SUPPORT_LIMIT_CHOICES.map((n) => (
              <option key={n} value={String(n)}>
                {n} / page
              </option>
            ))}
          </select>
        </label>
        <div style={S.pageNavStyle} aria-live="polite">
          <button
            type="button"
            style={S.buttonStyle}
            disabled={!canGoPrev(offset) || query.isFetching}
            onClick={() => setOffset(clampOffset(offset - limit))}
            data-testid="orders-prev"
          >
            Prev
          </button>
          <span data-testid="orders-page-caption">
            Page {currentPage(offset, limit)} · rows {rows.length}
          </span>
          <button
            type="button"
            style={S.buttonStyle}
            disabled={!canGoNext(rows.length, limit) || query.isFetching}
            onClick={() => setOffset(offset + limit)}
            data-testid="orders-next"
          >
            Next
          </button>
        </div>
      </div>

      <Body
        query={query}
        rows={rows}
        activeOrderId={activeOrderId}
        onOpen={setActiveOrderId}
      />

      {activeOrder !== null ? (
        <OrderDrawer
          order={activeOrder}
          onClose={() => setActiveOrderId(null)}
        />
      ) : null}
    </section>
  );
}

interface BodyProps {
  query: ReturnType<typeof useQuery<OrdersEnvelope, ApiError>>;
  rows: readonly AdminOrder[];
  activeOrderId: string | null;
  onOpen: (id: string) => void;
}

function Body({ query, rows, activeOrderId, onOpen }: BodyProps) {
  if (query.isPending) {
    return (
      <div style={S.statusBoxStyle} role="status" aria-live="polite">
        Loading orders from /v1/admin/orders…
      </div>
    );
  }
  if (query.isError) {
    return (
      <SupportErrorState
        testIdPrefix="orders"
        error={query.error}
        onRetry={() => query.refetch()}
      />
    );
  }
  if (rows.length === 0) {
    return (
      <div style={S.statusBoxStyle} role="status" data-testid="orders-empty">
        No orders match the current filters.
      </div>
    );
  }
  return (
    <div style={S.tableWrapStyle} role="region" aria-label="Orders table">
      <table style={S.tableStyle} data-testid="orders-table">
        <thead>
          <tr>
            <th scope="col" style={S.thStyle}>ID</th>
            <th scope="col" style={S.thStyle}>Org</th>
            <th scope="col" style={S.thStyle}>State</th>
            <th scope="col" style={S.thStyle}>Total</th>
            <th scope="col" style={S.thStyle}>Created</th>
            <th scope="col" style={S.thStyle}>Completed</th>
            <th scope="col" style={S.thStyle} aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {rows.map((o) => {
            const isActive = o.id === activeOrderId;
            return (
              <tr
                key={o.id}
                style={isActive ? S.trActiveStyle : S.trStyle}
                data-testid={`orders-row-${o.id}`}
              >
                <td style={S.tdMonoStyle}>
                  <button
                    type="button"
                    style={S.rowNameButtonStyle}
                    onClick={() => onOpen(o.id)}
                    aria-label={`Open details for order ${o.id}`}
                    title={o.id}
                  >
                    {shortUuid(o.id)}
                  </button>
                </td>
                <td style={S.tdMonoStyle} title={o.org_id}>{shortUuid(o.org_id)}</td>
                <td style={S.tdStyle}>
                  <span style={badgeForState(o.state)}>{o.state}</span>
                </td>
                <td style={S.tdStyle}>{formatMoneyMinor(o.total, o.currency)}</td>
                <td style={S.tdStyle}>{formatDateTime(o.created_at)}</td>
                <td style={S.tdStyle}>{formatDateTime(o.completed_at)}</td>
                <td style={S.tdStyle}>
                  <button
                    type="button"
                    style={S.rowActionButtonStyle}
                    onClick={() => onOpen(o.id)}
                    data-testid={`orders-open-${o.id}`}
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

/**
 * Pick a colour badge appropriate to the lifecycle state.
 * Exported for unit testing.
 */
export function badgeForState(state: string): CSSProperties {
  if (state === "completed") {
    return S.successBadgeStyle;
  }
  if (state === "failed" || state === "cancelled" || state === "expired") {
    return S.errorBadgeStyle;
  }
  if (state === "in_progress" || state === "created") {
    return S.warnBadgeStyle;
  }
  return S.statusBadgeStyle;
}

function OrderDrawer({ order, onClose }: { order: AdminOrder; onClose: () => void }) {
  // SAUI-13 accessibility: Escape closes, focus lands on close button,
  // focus returns to the row's "Details" button on unmount.
  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, closeRef);
  useFocusRestore(true);
  return (
    <aside
      style={S.drawerWrapStyle}
      role="dialog"
      aria-modal="false"
      aria-labelledby="orders-drawer-title"
      data-testid="orders-drawer"
    >
      <header style={S.drawerHeaderStyle}>
        <div>
          <div style={S.drawerEyebrowStyle}>Order</div>
          <h2 id="orders-drawer-title" style={S.drawerTitleStyle}>
            <code style={S.monoStyle}>{order.id}</code>
          </h2>
        </div>
        <button
          type="button"
          ref={closeRef}
          onClick={onClose}
          style={S.drawerCloseStyle}
          aria-label="Close order details"
          data-testid="orders-drawer-close"
          title="Close (Esc)"
        >
          ×
        </button>
      </header>

      <section style={S.drawerSectionStyle} aria-labelledby="orders-drawer-meta">
        <h3 id="orders-drawer-meta" style={S.drawerSectionTitleStyle}>
          Fields
        </h3>
        <dl style={S.metaListStyle}>
          <MetaRow k="State" v={<span style={badgeForState(order.state)}>{order.state}</span>} />
          <MetaRow k="Organization" v={<code style={S.monoStyle}>{order.org_id}</code>} />
          <MetaRow k="Channel" v={<code style={S.monoStyle}>{order.channel_id}</code>} />
          <MetaRow k="Reservation" v={<code style={S.monoStyle}>{order.reservation_id}</code>} />
          <MetaRow
            k="User"
            v={
              order.user_id === null ? (
                <span style={S.mutedStyle}>—</span>
              ) : (
                <code style={S.monoStyle}>{order.user_id}</code>
              )
            }
          />
          <MetaRow k="Total" v={formatMoneyMinor(order.total, order.currency)} />
          <MetaRow k="Created" v={formatDateTime(order.created_at)} />
          <MetaRow k="Updated" v={formatDateTime(order.updated_at)} />
          <MetaRow k="Completed" v={formatDateTime(order.completed_at)} />
        </dl>
      </section>

      <section style={S.drawerSectionStyle} aria-labelledby="orders-drawer-related">
        <h3 id="orders-drawer-related" style={S.drawerSectionTitleStyle}>
          Related data
        </h3>
        <div style={S.relatedGridStyle}>
          <Link
            to={"/tickets" as "/"}
            search={{ org_id: order.org_id } as unknown as Record<string, never>}
            style={S.relatedTileStyle}
            data-testid="orders-related-tickets-org"
          >
            <span style={S.relatedTileLabelStyle}>Tickets in this org</span>
            <span style={S.relatedTileHintStyle}>
              GET /v1/admin/tickets?org_id={shortUuid(order.org_id)}
            </span>
          </Link>
          <Link
            to={"/refunds" as "/"}
            search={{ org_id: order.org_id } as unknown as Record<string, never>}
            style={S.relatedTileStyle}
            data-testid="orders-related-refunds-org"
          >
            <span style={S.relatedTileLabelStyle}>Refunds in this org</span>
            <span style={S.relatedTileHintStyle}>
              GET /v1/admin/refunds?org_id={shortUuid(order.org_id)}
            </span>
          </Link>
          <BackendGapTile
            id="ticket-by-order"
            label="Tickets for this order"
            reason="No /v1/admin/orders/{id}/tickets endpoint yet; ticket list is filterable only by org_id."
          />
          <BackendGapTile
            id="refund-by-order"
            label="Refunds for this order"
            reason="No /v1/admin/orders/{id}/refunds endpoint yet; refund list is filterable only by org_id."
          />
          <BackendGapTile
            id="payment"
            label="Payment intent"
            reason="No /v1/admin/payments endpoint exposed; payment_intent_id is referenced from refunds only."
          />
          <BackendGapTile
            id="line-items"
            label="Line items"
            reason="List endpoint does not return seat/tier breakdown; richer detail endpoint not exposed."
          />
        </div>
      </section>
    </aside>
  );
}

function MetaRow({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div style={S.metaRowStyle}>
      <dt style={S.metaKeyStyle}>{k}</dt>
      <dd style={S.metaValStyle}>{v}</dd>
    </div>
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
      style={S.relatedTileDisabledStyle}
      role="note"
      aria-disabled="true"
      data-testid={`orders-related-gap-${id}`}
      title={reason}
    >
      <span style={S.relatedTileLabelStyle}>{label}</span>
      <span style={S.relatedTileGapBadgeStyle}>backend gap</span>
      <span style={S.relatedTileHintStyle}>{reason}</span>
    </div>
  );
}
