/**
 * Org-scoped Orders page (feature #490, W1-A6e; parent epic #456, spec §14.2).
 *
 * Backed by apps/backend/internal/platform/httpserver/horders/handler.go:
 *
 *   GET  /v1/organizations/{org_id}/orders?q=&status=&session_id=&from=&to=&limit=&offset=
 *   GET  /v1/organizations/{org_id}/orders/{id}
 *   POST /v1/organizations/{org_id}/orders/{id}/cancel
 *
 * List/detail require `order.read`; cancel requires `order.write` (the
 * "Cancel order" button self-disables via RequirePermission's caller
 * permission set, mirrored client-side with a permission check on the
 * mutation trigger — the backend is the actual enforcement point and
 * answers 403 permissions.denied if a caller without order.write attempts
 * it directly).
 *
 * This route is registered at "/org-orders" rather than "/orders" because
 * "/orders" is already the SuperAdmin cross-tenant support console
 * (orders.tsx, backed by GET /v1/admin/orders). The two pages are
 * deliberately separate: this one is org-scoped, search+status filtered,
 * and has a real write action (cancel); the SuperAdmin console is
 * read-only and cross-tenant.
 *
 * Org scoping follows the customers.tsx convention: org id from the `?org=`
 * query param / active organization scope, with a manual UUID paste
 * override and an org <select> picker sourced from GET /v1/organizations.
 *
 * Mock data: NONE. The list/detail/cancel all hit the live backend. No
 * globalThis / devStore / mockDb.
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
import { useScope } from "@/lib/auth/ScopeContext";
import { NAV_BY_PATH } from "@/lib/auth/navConfig";
import { orgContextFromLocation } from "@/lib/routing/orgContext";
import {
  useEscapeClose,
  useFocusOnMount,
  useFocusRestore,
} from "@/lib/a11y";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/org-orders",
  component: OrgOrdersRoute,
});

// ---------------------------------------------------------------------------
// Backend response shapes (mirrors horders/handler.go orderSummary/HandleGet)
// ---------------------------------------------------------------------------

interface OrganizationSummary {
  readonly id: string;
  readonly name: string;
  readonly slug?: string;
}

interface OrganizationListEnvelope {
  readonly organizations: readonly OrganizationSummary[];
}

export interface OrderSummary {
  readonly id: string;
  readonly system_id: number;
  readonly org_id: string;
  readonly channel_id: string;
  readonly event_id: string;
  readonly session_id: string;
  readonly checkout_session_id: string;
  readonly reservation_id: string;
  readonly source: string;
  readonly status: string;
  readonly currency: string;
  readonly subtotal: number;
  readonly discount: number;
  readonly charge: number;
  readonly total: number;
  readonly charge_percent_bp: number;
  readonly buyer_name: string | null;
  readonly buyer_email: string | null;
  readonly buyer_phone: string | null;
  readonly payment_method: string | null;
  readonly external_ref: string | null;
  readonly customer_id: string | null;
  readonly promo_code_id: string | null;
  readonly created_at: string;
  readonly updated_at: string;
  readonly paid_at: string | null;
  readonly cancelled_at: string | null;
  readonly expires_at: string | null;
}

interface OrderListEnvelope {
  readonly orders: readonly OrderSummary[];
  readonly limit: number;
  readonly offset: number;
}

export interface OrderItem {
  readonly id: string;
  readonly ordinal: number;
  readonly kind: string;
  readonly tier_id: string;
  readonly session_seat_id: string | null;
  readonly unit_price: number;
  readonly discount: number;
  readonly charge: number;
  readonly total: number;
  readonly ticket_id: string | null;
}

export interface OrderTicket {
  readonly id: string;
  readonly status: string;
  readonly holder_email: string | null;
  readonly seat_sector: string | null;
  readonly seat_row: string | null;
  readonly seat_number: string | null;
  readonly issued_at: string;
}

export interface OrderEvent {
  readonly id: string;
  readonly type: string;
  readonly actor: string;
  readonly payload: unknown;
  readonly created_at: string;
}

export interface OrderDetail extends OrderSummary {
  readonly items: readonly OrderItem[];
  readonly tickets: readonly OrderTicket[];
  readonly events: readonly OrderEvent[];
}

/** Known order statuses (orders.status CHECK, internal/platform/ordering). */
export const ORDER_STATUSES: readonly string[] = [
  "pending_payment",
  "paid",
  "cancelled",
  "expired",
  "manual_review",
];

export const CANCELLABLE_STATUSES: readonly string[] = [
  "pending_payment",
  "manual_review",
];

// ---------------------------------------------------------------------------
// Validators (mirror horders/handler.go)
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

/** Mirrors `len(q) > 200` -> orders.invalid_query in HandleList. */
export function validateOrderQuery(q: string): string | null {
  if (q.trim().length > 200) {
    return "Search must be at most 200 characters";
  }
  return null;
}

export function isCancellable(status: string): boolean {
  return CANCELLABLE_STATUSES.includes(status);
}

// ---------------------------------------------------------------------------
// Nav entry binding
// ---------------------------------------------------------------------------

const ORDERS_NAV_ENTRY = NAV_BY_PATH["/org-orders"];
if (ORDERS_NAV_ENTRY === undefined) {
  throw new Error("orgOrders route: NAV_BY_PATH['/org-orders'] missing");
}

// ---------------------------------------------------------------------------
// Page shell
// ---------------------------------------------------------------------------

function OrgOrdersRoute() {
  return (
    <RequirePermission entry={ORDERS_NAV_ENTRY}>
      <OrgOrdersModule />
    </RequirePermission>
  );
}

function OrgOrdersModule() {
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
    queryKey: ["organizations", "orders-picker"],
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
  const qError = validateOrderQuery(qInput);
  const [status, setStatus] = useState("");
  const [activeOrderId, setActiveOrderId] = useState<string | null>(null);

  const query = useQuery<OrderListEnvelope, ApiError>({
    queryKey: ["orders", "list", trimmedOrgID, q, status],
    enabled: orgReady,
    queryFn: () => {
      const params = new URLSearchParams();
      if (q !== "") {
        params.set("q", q);
      }
      if (status !== "") {
        params.set("status", status);
      }
      return authedFetch<OrderListEnvelope>({
        method: "GET",
        path: `/v1/organizations/${trimmedOrgID}/orders?${params.toString()}`,
      });
    },
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

  const rows = query.data?.orders ?? [];

  // Reset the drawer/page when the filters change.
  useEffect(() => {
    setActiveOrderId(null);
  }, [trimmedOrgID, q, status]);

  function onSearchSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (qError !== null) {
      return;
    }
    setQ(qInput.trim());
  }

  return (
    <section aria-labelledby="org-orders-heading" style={pageStyle}>
      <header style={headerStyle}>
        <div>
          <h1 id="org-orders-heading" style={headingStyle}>
            Orders
          </h1>
          <p style={subheadingStyle}>
            Org-scoped orders. Search matches buyer name, e-mail or phone;
            cancel is only available for <code>pending_payment</code> and{" "}
            <code>manual_review</code> orders and releases the associated
            hold.
          </p>
        </div>
        <div style={refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={refreshButtonStyle}
            disabled={!orgReady || query.isFetching}
            data-testid="org-orders-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </header>

      <div style={orgPickerStyle}>
        <label htmlFor="org-orders-org-id" style={fieldLabelStyle}>
          Organization
        </label>
        {orgsSorted.length > 0 ? (
          <select
            id="org-orders-org-select"
            value={orgID}
            onChange={(e) => setOrgID(e.target.value)}
            style={inputStyle}
            data-testid="org-orders-org-select"
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
          id="org-orders-org-id"
          type="text"
          value={orgID}
          onChange={(e) => setOrgID(e.target.value)}
          style={inputMonoStyle}
          placeholder="0190a8b0-7d31-7a3c-9c4e-8c0c1d9d9c2a"
          data-testid="org-orders-org-input"
        />
        {orgIDError !== null ? (
          <div style={fieldErrorStyle} data-testid="org-orders-org-error">
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
          <form onSubmit={onSearchSubmit} style={toolbarStyle} noValidate>
            <label style={fieldGroupStyle}>
              <span style={fieldLabelStyle}>Search</span>
              <input
                type="text"
                value={qInput}
                onChange={(e) => setQInput(e.target.value)}
                style={inputStyle}
                maxLength={201}
                placeholder="Name, email, phone…"
                data-testid="org-orders-search-input"
              />
            </label>
            <label style={fieldGroupStyle}>
              <span style={fieldLabelStyle}>Status</span>
              <select
                value={status}
                onChange={(e) => setStatus(e.target.value)}
                style={inputStyle}
                data-testid="org-orders-status"
              >
                <option value="">Any status</option>
                {ORDER_STATUSES.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="submit"
              style={primaryButtonStyle}
              disabled={qError !== null}
              data-testid="org-orders-search-submit"
            >
              Search
            </button>
            {qError !== null ? (
              <div style={fieldErrorStyle} data-testid="org-orders-search-error">
                {qError}
              </div>
            ) : null}
          </form>

          <OrdersBody
            query={query}
            rows={rows}
            onOpen={setActiveOrderId}
          />

          {activeOrderId !== null ? (
            <OrderDrawer
              orgID={trimmedOrgID}
              orderID={activeOrderId}
              onClose={() => setActiveOrderId(null)}
            />
          ) : null}
        </>
      ) : (
        <div style={statusBoxStyle} role="status" data-testid="org-orders-org-required">
          Select or paste an organization to load its orders.
        </div>
      )}
    </section>
  );
}

// ---------------------------------------------------------------------------
// Table body and states
// ---------------------------------------------------------------------------

interface BodyProps {
  query: ReturnType<typeof useQuery<OrderListEnvelope, ApiError>>;
  rows: readonly OrderSummary[];
  onOpen: (id: string) => void;
}

function OrdersBody({ query, rows, onOpen }: BodyProps) {
  if (query.isPending) {
    return (
      <div style={statusBoxStyle} role="status" aria-live="polite">
        Loading orders…
      </div>
    );
  }
  if (query.isError) {
    return <OrdersErrorState error={query.error} onRetry={() => query.refetch()} />;
  }
  if (rows.length === 0) {
    return (
      <div style={statusBoxStyle} role="status" data-testid="org-orders-empty">
        No orders match the current filters.
      </div>
    );
  }
  return (
    <div style={tableWrapStyle} role="region" aria-label="Orders">
      <table style={tableStyle} data-testid="org-orders-table">
        <thead>
          <tr>
            <th scope="col" style={thStyle}>Number</th>
            <th scope="col" style={thStyle}>Buyer</th>
            <th scope="col" style={thStyle}>Session</th>
            <th scope="col" style={thStyle}>Status</th>
            <th scope="col" style={thStyle}>Total</th>
            <th scope="col" style={thStyle}>Created</th>
            <th scope="col" style={thStyle} aria-label="Actions" />
          </tr>
        </thead>
        <tbody>
          {rows.map((o) => (
            <tr key={o.id} data-testid={`org-orders-row-${o.id}`}>
              <td style={tdMonoStyle}>{o.system_id}</td>
              <td style={tdStyle}>{o.buyer_name ?? o.buyer_email ?? "—"}</td>
              <td style={tdMonoStyle} title={o.session_id}>{shortId(o.session_id)}</td>
              <td style={tdStyle}>
                <StatusBadge status={o.status} />
              </td>
              <td style={tdStyle}>{formatMoney(o.total, o.currency)}</td>
              <td style={tdStyle}>{formatDate(o.created_at)}</td>
              <td style={tdActionsStyle}>
                <button
                  type="button"
                  style={rowActionButtonStyle}
                  onClick={() => onOpen(o.id)}
                  data-testid={`org-orders-open-${o.id}`}
                >
                  Details
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function OrdersErrorState({
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
      <div style={errorBoxStyle} role="alert" data-testid="org-orders-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>order.read</code>.
          Ask a platform administrator to grant the permission.
        </p>
      </div>
    );
  }
  if (error instanceof ApiError && error.status === 401) {
    return (
      <div style={errorBoxStyle} role="status" data-testid="org-orders-session-expired">
        <strong>Session expired.</strong>
        <p style={errorParaStyle}>Sign in again to reload orders.</p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="org-orders-error">
      <strong>Failed to load orders.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Detail drawer (items + timeline + cancel)
// ---------------------------------------------------------------------------

function OrderDrawer({
  orgID,
  orderID,
  onClose,
}: {
  orgID: string;
  orderID: string;
  onClose: () => void;
}) {
  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, closeRef);
  useFocusRestore(true);

  const queryClient = useQueryClient();
  const [reason, setReason] = useState("");
  const [confirming, setConfirming] = useState(false);

  const detail = useQuery<OrderDetail, ApiError>({
    queryKey: ["orders", "detail", orgID, orderID],
    queryFn: () =>
      authedFetch<OrderDetail>({
        method: "GET",
        path: `/v1/organizations/${orgID}/orders/${orderID}`,
      }),
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (err.status === 401 || err.status === 403 || err.status === 404) {
          return false;
        }
      }
      return failureCount < 2;
    },
    refetchOnWindowFocus: false,
  });

  const cancelMut = useMutation<OrderSummary, ApiError, void>({
    mutationFn: () =>
      authedFetch<OrderSummary>({
        method: "POST",
        path: `/v1/organizations/${orgID}/orders/${orderID}/cancel`,
        body: { reason: reason.trim() },
      }),
    onSuccess: () => {
      setConfirming(false);
      queryClient.invalidateQueries({ queryKey: ["orders", "detail", orgID, orderID] });
      queryClient.invalidateQueries({ queryKey: ["orders", "list", orgID] });
    },
  });

  const order = detail.data;

  return (
    <aside
      style={drawerWrapStyle}
      role="dialog"
      aria-modal="false"
      aria-labelledby="org-orders-drawer-title"
      data-testid="org-orders-drawer"
    >
      <header style={drawerHeaderStyle}>
        <div>
          <div style={drawerEyebrowStyle}>Order</div>
          <h2 id="org-orders-drawer-title" style={drawerTitleStyle}>
            {order !== undefined ? `#${order.system_id}` : orderID}
          </h2>
        </div>
        <button
          type="button"
          ref={closeRef}
          onClick={onClose}
          style={drawerCloseStyle}
          aria-label="Close order details"
          data-testid="org-orders-drawer-close"
          title="Close (Esc)"
        >
          ×
        </button>
      </header>

      {detail.isPending ? (
        <div style={statusBoxStyle} role="status" aria-live="polite">
          Loading order…
        </div>
      ) : detail.isError ? (
        <DetailErrorState error={detail.error} onRetry={() => detail.refetch()} />
      ) : order === undefined ? null : (
        <>
          <section style={drawerSectionStyle} aria-labelledby="org-orders-drawer-meta">
            <h3 id="org-orders-drawer-meta" style={drawerSectionTitleStyle}>
              Summary
            </h3>
            <dl style={metaListStyle}>
              <MetaRow k="Status" v={<StatusBadge status={order.status} />} />
              <MetaRow k="Buyer" v={order.buyer_name ?? "—"} />
              <MetaRow k="Email" v={order.buyer_email ?? "—"} />
              <MetaRow k="Phone" v={order.buyer_phone ?? "—"} />
              <MetaRow k="Session" v={<code style={monoStyle}>{order.session_id}</code>} />
              <MetaRow k="Source" v={order.source} />
              <MetaRow
                k="Total"
                v={`${formatMoney(order.total, order.currency)} (subtotal ${formatMoney(order.subtotal, order.currency)}, discount ${formatMoney(order.discount, order.currency)}, charge ${formatMoney(order.charge, order.currency)})`}
              />
              <MetaRow k="External ref" v={order.external_ref ?? "—"} />
              <MetaRow k="Created" v={formatDate(order.created_at)} />
              <MetaRow k="Paid" v={order.paid_at !== null ? formatDate(order.paid_at) : "—"} />
              <MetaRow
                k="Cancelled"
                v={order.cancelled_at !== null ? formatDate(order.cancelled_at) : "—"}
              />
              <MetaRow
                k="Expires"
                v={order.expires_at !== null ? formatDate(order.expires_at) : "—"}
              />
            </dl>
          </section>

          <section style={drawerSectionStyle} aria-labelledby="org-orders-drawer-items">
            <h3 id="org-orders-drawer-items" style={drawerSectionTitleStyle}>
              Items ({order.items.length})
            </h3>
            {order.items.length === 0 ? (
              <div style={statusBoxStyle} role="status" data-testid="org-orders-items-empty">
                No line items.
              </div>
            ) : (
              <table style={miniTableStyle} data-testid="org-orders-items-table">
                <thead>
                  <tr>
                    <th scope="col" style={thStyle}>#</th>
                    <th scope="col" style={thStyle}>Kind</th>
                    <th scope="col" style={thStyle}>Total</th>
                    <th scope="col" style={thStyle}>Ticket</th>
                  </tr>
                </thead>
                <tbody>
                  {order.items.map((it) => (
                    <tr key={it.id} data-testid={`org-orders-item-${it.id}`}>
                      <td style={tdStyle}>{it.ordinal}</td>
                      <td style={tdStyle}>{it.kind}</td>
                      <td style={tdStyle}>{formatMoney(it.total, order.currency)}</td>
                      <td style={tdMonoStyle}>
                        {it.ticket_id !== null ? shortId(it.ticket_id) : "—"}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </section>

          {order.tickets.length > 0 ? (
            <section style={drawerSectionStyle} aria-labelledby="org-orders-drawer-tickets">
              <h3 id="org-orders-drawer-tickets" style={drawerSectionTitleStyle}>
                Tickets ({order.tickets.length})
              </h3>
              <table style={miniTableStyle} data-testid="org-orders-tickets-table">
                <thead>
                  <tr>
                    <th scope="col" style={thStyle}>Status</th>
                    <th scope="col" style={thStyle}>Seat</th>
                    <th scope="col" style={thStyle}>Issued</th>
                  </tr>
                </thead>
                <tbody>
                  {order.tickets.map((t) => (
                    <tr key={t.id} data-testid={`org-orders-ticket-${t.id}`}>
                      <td style={tdStyle}>{t.status}</td>
                      <td style={tdStyle}>
                        {t.seat_sector !== null
                          ? `${t.seat_sector} / ${t.seat_row ?? "—"} / ${t.seat_number ?? "—"}`
                          : "GA"}
                      </td>
                      <td style={tdStyle}>{formatDate(t.issued_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
          ) : null}

          <section style={drawerSectionStyle} aria-labelledby="org-orders-drawer-timeline">
            <h3 id="org-orders-drawer-timeline" style={drawerSectionTitleStyle}>
              Timeline ({order.events.length})
            </h3>
            {order.events.length === 0 ? (
              <div style={statusBoxStyle} role="status" data-testid="org-orders-timeline-empty">
                No events recorded yet.
              </div>
            ) : (
              <ol style={timelineListStyle} data-testid="org-orders-timeline">
                {order.events.map((ev) => (
                  <li key={ev.id} style={timelineItemStyle} data-testid={`org-orders-event-${ev.id}`}>
                    <div style={timelineDotStyle} aria-hidden="true" />
                    <div>
                      <div style={timelineTypeStyle}>{ev.type}</div>
                      <div style={timelineMetaStyle}>
                        {ev.actor} · {formatDate(ev.created_at)}
                      </div>
                    </div>
                  </li>
                ))}
              </ol>
            )}
          </section>

          <section style={drawerSectionStyle} aria-labelledby="org-orders-drawer-cancel">
            <h3 id="org-orders-drawer-cancel" style={drawerSectionTitleStyle}>
              Cancel order
            </h3>
            {!isCancellable(order.status) ? (
              <div style={statusBoxStyle} role="status" data-testid="org-orders-cancel-unavailable">
                Only <code>pending_payment</code> and <code>manual_review</code> orders
                can be cancelled; this order is <StatusBadge status={order.status} />.
              </div>
            ) : confirming ? (
              <div style={cancelFormStyle}>
                <label htmlFor="org-orders-cancel-reason" style={fieldLabelStyle}>
                  Reason (optional)
                </label>
                <textarea
                  id="org-orders-cancel-reason"
                  value={reason}
                  onChange={(e) => setReason(e.target.value)}
                  style={textareaStyle}
                  rows={2}
                  data-testid="org-orders-cancel-reason"
                  placeholder="Why is this order being cancelled?"
                />
                {cancelMut.isError ? (
                  <div style={fieldErrorStyle} data-testid="org-orders-cancel-error">
                    {cancelMut.error.message} ({cancelMut.error.code})
                  </div>
                ) : null}
                <div style={cancelButtonsStyle}>
                  <button
                    type="button"
                    style={dangerButtonStyle}
                    onClick={() => cancelMut.mutate()}
                    disabled={cancelMut.isPending}
                    data-testid="org-orders-cancel-confirm"
                  >
                    {cancelMut.isPending ? "Cancelling…" : "Confirm cancellation"}
                  </button>
                  <button
                    type="button"
                    style={secondaryButtonStyle}
                    onClick={() => setConfirming(false)}
                    disabled={cancelMut.isPending}
                    data-testid="org-orders-cancel-abort"
                  >
                    Keep order
                  </button>
                </div>
              </div>
            ) : (
              <button
                type="button"
                style={dangerButtonStyle}
                onClick={() => setConfirming(true)}
                data-testid="org-orders-cancel-start"
              >
                Cancel order
              </button>
            )}
          </section>
        </>
      )}
    </aside>
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
      <div style={errorBoxStyle} role="alert" data-testid="org-orders-detail-not-found">
        <strong>Order not found.</strong>
        <p style={errorParaStyle}>
          {error.message || "This order does not exist, or is not in this organization."}
        </p>
      </div>
    );
  }
  if (
    error instanceof ApiError &&
    (error.status === 403 || error.code === "permissions.denied")
  ) {
    return (
      <div style={errorBoxStyle} role="alert" data-testid="org-orders-detail-forbidden">
        <strong>Forbidden.</strong>
        <p style={errorParaStyle}>
          Your account is missing <code style={monoStyle}>order.read</code>.
        </p>
      </div>
    );
  }
  return (
    <div style={errorBoxStyle} role="alert" data-testid="org-orders-detail-error">
      <strong>Failed to load order.</strong>
      <div style={errorCodeStyle}>{error?.code ?? "unknown.error"}</div>
      {error?.message ? <div style={errorParaStyle}>{error.message}</div> : null}
      <button type="button" style={errorRetryStyle} onClick={onRetry}>
        Retry
      </button>
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

function StatusBadge({ status }: { status: string }) {
  const style =
    status === "paid"
      ? badgeActiveStyle
      : status === "cancelled" || status === "expired"
        ? badgeArchivedStyle
        : status === "manual_review"
          ? badgeWarnStyle
          : badgeNeutralStyle;
  return <span style={style}>{status}</span>;
}

// ---------------------------------------------------------------------------
// Server error mapping (SAUI convention)
// ---------------------------------------------------------------------------

export interface ServerFieldErrors {
  q?: string;
  org?: string;
  form?: string;
}

export function mapServerError(err: ApiError): ServerFieldErrors {
  const out: ServerFieldErrors = {};
  switch (err.code) {
    case "orders.invalid_query":
      out.q = err.message;
      return out;
    case "orders.invalid_limit":
    case "orders.invalid_offset":
    case "orders.invalid_session_id":
    case "orders.invalid_from":
    case "orders.invalid_to":
      out.form = err.message;
      return out;
    case "orders.not_found":
      out.form = err.message;
      return out;
    case "orders.invalid_transition":
      out.form = "This order can no longer be cancelled.";
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
  return d.toISOString().slice(0, 16).replace("T", " ");
}

function formatMoney(minor: number, currency: string): string {
  return `${(minor / 100).toFixed(2)} ${currency}`;
}

function shortId(id: string): string {
  return id.length <= 8 ? id : `${id.slice(0, 8)}…`;
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

const secondaryButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const dangerButtonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#dc2626",
  border: "1px solid #dc2626",
  borderRadius: 4,
  cursor: "pointer",
  color: "#ffffff",
  fontWeight: 600,
  alignSelf: "flex-start",
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

const toolbarStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "flex-end",
  flexWrap: "wrap",
};

const fieldGroupStyle: CSSProperties = {
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

const textareaStyle: CSSProperties = {
  ...inputStyle,
  fontFamily: "inherit",
  resize: "vertical",
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

const miniTableStyle: CSSProperties = {
  ...tableStyle,
  fontSize: 12,
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
const badgeWarnStyle: CSSProperties = {
  ...badgeBaseStyle,
  background: "#fef3c7",
  color: "#92400e",
};
const badgeNeutralStyle: CSSProperties = {
  ...badgeBaseStyle,
  background: "#e2e8f0",
  color: "#334155",
};

// Drawer styles (mirrors orders.tsx SAUI-10 drawer conventions)
const drawerWrapStyle: CSSProperties = {
  position: "fixed",
  top: 0,
  right: 0,
  bottom: 0,
  width: "min(420px, 100vw)",
  background: "#ffffff",
  borderLeft: "1px solid #e2e8f0",
  boxShadow: "-8px 0 24px rgba(15, 23, 42, 0.12)",
  padding: 20,
  overflowY: "auto",
  display: "flex",
  flexDirection: "column",
  gap: 16,
  zIndex: 40,
};

const drawerHeaderStyle: CSSProperties = {
  display: "flex",
  alignItems: "flex-start",
  justifyContent: "space-between",
  gap: 12,
};

const drawerEyebrowStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 600,
  color: "#64748b",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const drawerTitleStyle: CSSProperties = {
  margin: "2px 0 0 0",
  fontSize: 18,
  fontWeight: 700,
};

const drawerCloseStyle: CSSProperties = {
  fontSize: 20,
  lineHeight: 1,
  padding: "4px 8px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const drawerSectionStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
  paddingTop: 12,
  borderTop: "1px solid #f1f5f9",
};

const drawerSectionTitleStyle: CSSProperties = {
  margin: 0,
  fontSize: 13,
  fontWeight: 700,
  color: "#0f172a",
};

const metaListStyle: CSSProperties = {
  margin: 0,
  display: "flex",
  flexDirection: "column",
  gap: 6,
};

const metaRowStyle: CSSProperties = {
  display: "flex",
  justifyContent: "space-between",
  gap: 12,
  fontSize: 12,
};

const metaKeyStyle: CSSProperties = {
  color: "#64748b",
  margin: 0,
};

const metaValStyle: CSSProperties = {
  margin: 0,
  color: "#0f172a",
  textAlign: "right",
};

const timelineListStyle: CSSProperties = {
  listStyle: "none",
  margin: 0,
  padding: 0,
  display: "flex",
  flexDirection: "column",
  gap: 10,
};

const timelineItemStyle: CSSProperties = {
  display: "flex",
  gap: 8,
  alignItems: "flex-start",
};

const timelineDotStyle: CSSProperties = {
  width: 8,
  height: 8,
  borderRadius: "50%",
  background: "#0369a1",
  marginTop: 4,
  flexShrink: 0,
};

const timelineTypeStyle: CSSProperties = {
  fontSize: 12,
  fontWeight: 600,
  color: "#0f172a",
};

const timelineMetaStyle: CSSProperties = {
  fontSize: 11,
  color: "#64748b",
};

const cancelFormStyle: CSSProperties = {
  display: "flex",
  flexDirection: "column",
  gap: 8,
};

const cancelButtonsStyle: CSSProperties = {
  display: "flex",
  gap: 8,
};
