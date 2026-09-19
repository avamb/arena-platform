/**
 * SuperAdmin Orders support console (SAUI-10).
 *
 * Backed by GET /v1/admin/orders (see
 * apps/backend/internal/platform/httpserver/superadmin.go). The endpoint:
 *
 *   - requires `superadmin.read` permission;
 *   - requires the `X-Admin-Reason` header (cross-tenant read);
 *   - accepts `org_id`, `state`, `q`, `limit`, `offset` query parameters.
 *     `q` finds an order by its number (system_id) or the selling site's
 *     reference exactly, or by part of the buyer's email, name or phone
 *     (functional run 2026-09-19, F-35); there is no created-at range
 *     and no channel filter;
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
 *   search box (on submit)   -> ?q=<text>
 *   page size dropdown       -> ?limit=<n>
 *   prev/next pagination     -> ?offset=<n>
 *
 * Any future filters (channel_id, completed_at range) require
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
  ResponsiveTable,
  ResponsiveDrawer,
  useIsDesktop,
  type ResponsiveTableColumn,
} from "@/components/layout";
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
import { buildOrgContextHref } from "@/lib/routing/orgContext";

/** Query string for GET /v1/admin/orders. Exported for unit testing. */
export function buildOrdersQuery(filters: SupportFilters, q: string): string {
  const base = buildSupportQuery(filters, "state");
  const trimmed = q.trim();
  return trimmed === "" ? base : `${base}&q=${encodeURIComponent(trimmed)}`;
}

/** An order's display name: its number, or the short UUID for a row without one. */
export function orderLabel(o: Pick<AdminOrder, "id" | "system_id">): string {
  return typeof o.system_id === "number" && o.system_id > 0
    ? `#${o.system_id}`
    : shortUuid(o.id);
}

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/orders",
  component: OrdersRoute,
});

/**
 * Known order states. Aligned with the `orders.status` CHECK constraint
 * (apps/backend/internal/migrations/sql/0092_orders.sql); the endpoint reads
 * the `orders` table, not checkout sessions.
 *
 * Keeping the dropdown values pinned here means the operator only ever
 * sees server-recognised values. Unknown values would be silently
 * rejected by the backend with `superadmin.invalid_state` (the backend
 * currently passes them through as-is, but pinning here keeps the UX
 * predictable). Adding a new state -> update both ends.
 */
export const ORDER_STATES: readonly string[] = [
  "pending_payment",
  "paid",
  "cancelled",
  "expired",
  "abandoned",
  "refunded",
  "partially_refunded",
  "manual_review",
];

export interface AdminOrder {
  readonly id: string;
  /** Order number the buyer and the selling site know the order by. */
  readonly system_id: number;
  readonly org_id: string;
  readonly org_name: string;
  readonly event_id: string;
  readonly event_name: string;
  readonly source: string;
  readonly external_ref: string | null;
  readonly buyer_name: string | null;
  readonly buyer_email: string | null;
  readonly buyer_phone: string | null;
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
  const [qInput, setQInput] = useState("");
  const [q, setQ] = useState("");
  const [limit, setLimit] = useState<number>(initial.limit);
  const [offset, setOffset] = useState<number>(initial.offset);
  const [activeOrderId, setActiveOrderId] = useState<string | null>(null);
  const isDesktop = useIsDesktop(true);
  const [filtersOpen, setFiltersOpen] = useState<boolean>(false);

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
    queryKey: ["admin", "orders", filters, q],
    queryFn: () =>
      authedFetch<OrdersEnvelope>({
        method: "GET",
        path: `/v1/admin/orders?${buildOrdersQuery(filters, q)}`,
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
  }, [orgIdInput, state, limit, q]);

  return (
    <section aria-labelledby="orders-heading" style={S.pageStyle}>
      <header style={S.headerStyle}>
        <div>
          <h1 id="orders-heading" style={S.headingStyle}>
            Orders
          </h1>
          <p style={S.subheadingStyle}>
            Orders of every organization. Search by order number, the
            selling site's reference, or the buyer's email, name or phone.
            Read-only here; cancel from the organization's
            orders page.
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

      {(() => {
        const toolbar = (
          <div style={S.toolbarStyle}>
            <form
              style={S.fieldGroupStyle}
              role="search"
              onSubmit={(e) => {
                e.preventDefault();
                setQ(qInput.trim());
              }}
            >
              <span style={S.fieldLabelStyle}>Search</span>
              <span style={{ display: "flex", gap: 8 }}>
                <input
                  type="search"
                  placeholder="Order #, email, name, phone"
                  value={qInput}
                  maxLength={100}
                  onChange={(e) => {
                    setQInput(e.target.value);
                    if (e.target.value === "") setQ("");
                  }}
                  style={S.inputStyle}
                  aria-label="Search orders"
                  data-testid="orders-search-input"
                />
                <button type="submit" style={S.buttonStyle} data-testid="orders-search-submit">
                  Search
                </button>
              </span>
            </form>
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
        );
        if (isDesktop) {
          return toolbar;
        }
        return (
          <>
            <button
              type="button"
              style={S.buttonStyle}
              onClick={() => setFiltersOpen(true)}
              data-testid="orders-filters-open"
            >
              Filters
            </button>
            <ResponsiveDrawer
              id="orders-filters-drawer"
              open={filtersOpen}
              onClose={() => setFiltersOpen(false)}
              title="Filters"
            >
              {toolbar}
            </ResponsiveDrawer>
          </>
        );
      })()}

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
  const columns: ResponsiveTableColumn<AdminOrder>[] = [
    {
      id: "id",
      header: "Order",
      primary: true,
      renderCell: (o) => (
        <span data-testid={`orders-row-${o.id}`}>
          <button
            type="button"
            style={S.rowNameButtonStyle}
            onClick={() => onOpen(o.id)}
            aria-label={`Open details for order ${orderLabel(o)}`}
            title={o.id}
          >
            {orderLabel(o)}
          </button>
        </span>
      ),
    },
    {
      id: "org",
      header: "Organization",
      renderCell: (o) => (
        <span title={o.org_id}>{o.org_name || shortUuid(o.org_id)}</span>
      ),
    },
    {
      id: "event",
      header: "Event",
      renderCell: (o) => o.event_name || <span style={S.mutedStyle}>—</span>,
    },
    {
      id: "buyer",
      header: "Buyer",
      renderCell: (o) =>
        o.buyer_email || o.buyer_name ? (
          <span title={o.buyer_phone ?? undefined}>
            {o.buyer_name ? <div>{o.buyer_name}</div> : null}
            {o.buyer_email ? <div style={S.mutedStyle}>{o.buyer_email}</div> : null}
          </span>
        ) : (
          <span style={S.mutedStyle}>—</span>
        ),
    },
    {
      id: "state",
      header: "State",
      renderCell: (o) => <span style={badgeForState(o.state)}>{o.state}</span>,
    },
    {
      id: "total",
      header: "Total",
      renderCell: (o) => formatMoneyMinor(o.total, o.currency),
    },
    {
      id: "created",
      header: "Created",
      renderCell: (o) => formatDateTime(o.created_at),
    },
    {
      id: "completed",
      header: "Paid",
      renderCell: (o) => formatDateTime(o.completed_at),
    },
    {
      id: "actions",
      header: "Actions",
      hideOnMobile: true,
      renderCell: (o) => (
        <button
          type="button"
          style={S.rowActionButtonStyle}
          onClick={() => onOpen(o.id)}
          data-testid={`orders-open-${o.id}`}
        >
          Details
        </button>
      ),
    },
  ];
  // Suppress unused-var warning for activeOrderId in this scope; row
  // highlighting is omitted from the responsive table primitive.
  void activeOrderId;
  return (
    <div style={S.tableWrapStyle} role="region" aria-label="Orders table">
      <ResponsiveTable<AdminOrder>
        id="orders-table"
        columns={columns}
        rows={rows}
        rowKey={(o) => o.id}
      />
    </div>
  );
}

/**
 * Pick a colour badge appropriate to the lifecycle state.
 * Exported for unit testing.
 */
export function badgeForState(state: string): CSSProperties {
  if (state === "paid") {
    return S.successBadgeStyle;
  }
  if (
    state === "cancelled" ||
    state === "expired" ||
    state === "abandoned" ||
    state === "refunded"
  ) {
    return S.errorBadgeStyle;
  }
  if (
    state === "pending_payment" ||
    state === "partially_refunded" ||
    state === "manual_review"
  ) {
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
            {orderLabel(order)}
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
          <MetaRow k="Order ID" v={<code style={S.monoStyle}>{order.id}</code>} />
          <MetaRow
            k="Organization"
            v={
              <>
                {order.org_name ? <div>{order.org_name}</div> : null}
                <code style={S.monoStyle}>{order.org_id}</code>
              </>
            }
          />
          <MetaRow k="Event" v={order.event_name || <span style={S.mutedStyle}>—</span>} />
          <MetaRow k="Buyer" v={order.buyer_name || <span style={S.mutedStyle}>—</span>} />
          <MetaRow k="Email" v={order.buyer_email || <span style={S.mutedStyle}>—</span>} />
          <MetaRow k="Phone" v={order.buyer_phone || <span style={S.mutedStyle}>—</span>} />
          <MetaRow k="Sales path" v={order.source} />
          <MetaRow
            k="Site reference"
            v={
              order.external_ref ? (
                <code style={S.monoStyle}>{order.external_ref}</code>
              ) : (
                <span style={S.mutedStyle}>—</span>
              )
            }
          />
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
          <MetaRow k="Paid" v={formatDateTime(order.completed_at)} />
        </dl>
      </section>

      <section style={S.drawerSectionStyle} aria-labelledby="orders-drawer-related">
        <h3 id="orders-drawer-related" style={S.drawerSectionTitleStyle}>
          Related data
        </h3>
        <div style={S.relatedGridStyle}>
          <a
            href={buildOrgContextHref("/org-orders", order.org_id)}
            style={S.relatedTileStyle}
            data-testid="orders-related-org-orders"
          >
            <span style={S.relatedTileLabelStyle}>Organization orders</span>
            <span style={S.relatedTileHintStyle}>
              Full order card with tickets and cancellation
            </span>
          </a>
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
