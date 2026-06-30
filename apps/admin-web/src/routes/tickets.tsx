/**
 * SuperAdmin Tickets support console (SAUI-10).
 *
 * Backed by GET /v1/admin/tickets (see
 * apps/backend/internal/platform/httpserver/superadmin.go). Tickets use
 * the `status` query parameter (not `state`); every other contract
 * (X-Admin-Reason, limit/offset, total = len(rows)) mirrors orders.
 *
 * Read-only. No issue/cancel/transfer actions are exposed here because
 * the support endpoints do not currently accept those mutations under
 * /v1/admin -- adding them is a backend change, not a UI workaround.
 *
 * Mock data: NONE. The page renders only what the backend returns.
 */
import { createRoute, Link } from "@tanstack/react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
  isValidUuid,
  readSupportFiltersFromLocation,
  shortUuid,
  type SupportFilters,
} from "@/lib/admin/supportConsole";
import * as S from "@/lib/admin/supportStyles";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/tickets",
  component: TicketsRoute,
});

/**
 * Known ticket statuses. Aligned with `tickets.status` in the backend
 * persistence layer. Update both ends when adding a new value.
 */
export const TICKET_STATUSES: readonly string[] = [
  "active",
  "issued",
  "redeemed",
  "cancelled",
  "expired",
  "transferred",
];

export interface AdminTicket {
  readonly id: string;
  readonly checkout_session_id: string;
  readonly session_id: string;
  readonly status: string;
  readonly issued_at: string;
  readonly created_at: string;
  readonly updated_at: string;
  readonly tier_id: string | null;
  readonly holder_email: string | null;
}

interface TicketsEnvelope {
  readonly tickets: readonly AdminTicket[];
  readonly total: number;
  readonly limit: number;
  readonly offset: number;
}

const NAV_ENTRY = NAV_BY_PATH["/tickets"];
if (NAV_ENTRY === undefined) {
  throw new Error("tickets route: NAV_BY_PATH['/tickets'] missing");
}

function TicketsRoute() {
  return (
    <RequirePermission entry={NAV_ENTRY}>
      <TicketsConsole />
    </RequirePermission>
  );
}

function TicketsConsole() {
  const initial = useMemo<SupportFilters>(() => {
    if (typeof window === "undefined") {
      return { orgId: "", statusValue: "", limit: 50, offset: 0 };
    }
    return readSupportFiltersFromLocation(window.location.search, "status");
  }, []);
  const [orgIdInput, setOrgIdInput] = useState(initial.orgId);
  const [status, setStatus] = useState(initial.statusValue);
  const [limit, setLimit] = useState<number>(initial.limit);
  const [offset, setOffset] = useState<number>(initial.offset);
  const [activeId, setActiveId] = useState<string | null>(null);
  const isDesktop = useIsDesktop(true);
  const [filtersOpen, setFiltersOpen] = useState<boolean>(false);

  const orgIdInvalid =
    orgIdInput.trim() !== "" && !isValidUuid(orgIdInput.trim());

  const filters: SupportFilters = {
    orgId: orgIdInvalid ? "" : orgIdInput,
    statusValue: status,
    limit,
    offset,
  };

  const query = useQuery<TicketsEnvelope, ApiError>({
    queryKey: ["admin", "tickets", filters],
    queryFn: () =>
      authedFetch<TicketsEnvelope>({
        method: "GET",
        path: `/v1/admin/tickets?${buildSupportQuery(filters, "status")}`,
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

  const rows = query.data?.tickets ?? [];
  const active = useMemo(
    () => (activeId === null ? null : rows.find((t) => t.id === activeId) ?? null),
    [activeId, rows],
  );

  useEffect(() => {
    setOffset(0);
    setActiveId(null);
  }, [orgIdInput, status, limit]);

  return (
    <section aria-labelledby="tickets-heading" style={S.pageStyle}>
      <header style={S.headerStyle}>
        <div>
          <h1 id="tickets-heading" style={S.headingStyle}>
            Tickets
          </h1>
          <p style={S.subheadingStyle}>
            Cross-tenant ticket inventory. Filters map directly to the
            backend's <code>org_id</code>, <code>status</code>,{" "}
            <code>limit</code>, <code>offset</code> query parameters.
            Read-only; ticket mutations (issue, revoke, transfer) are
            not exposed under <code>/v1/admin</code>.
          </p>
        </div>
        <div style={S.refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={S.refreshButtonStyle}
            disabled={query.isFetching}
            data-testid="tickets-refresh"
          >
            {query.isFetching ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </header>

      {(() => {
        const toolbar = (
          <div style={S.toolbarStyle}>
            <label style={S.fieldGroupStyle}>
              <span style={S.fieldLabelStyle}>Organization ID</span>
              <input
                type="text"
                placeholder="UUID (optional)"
                value={orgIdInput}
                onChange={(e) => setOrgIdInput(e.target.value)}
                style={orgIdInvalid ? S.inputInvalidStyle : S.inputStyle}
                data-testid="tickets-org-id"
                aria-invalid={orgIdInvalid}
                aria-describedby={orgIdInvalid ? "tickets-org-id-err" : undefined}
              />
              {orgIdInvalid ? (
                <span
                  id="tickets-org-id-err"
                  style={{ color: "#7f1d1d", fontSize: 11 }}
                  data-testid="tickets-org-id-error"
                >
                  Must be a valid UUID — filter not applied.
                </span>
              ) : null}
            </label>
            <label style={S.fieldGroupStyle}>
              <span style={S.fieldLabelStyle}>Status</span>
              <select
                value={status}
                onChange={(e) => setStatus(e.target.value)}
                style={S.selectStyle}
                data-testid="tickets-status"
              >
                <option value="">Any status</option>
                {TICKET_STATUSES.map((s) => (
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
                data-testid="tickets-limit"
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
                data-testid="tickets-prev"
              >
                Prev
              </button>
              <span data-testid="tickets-page-caption">
                Page {currentPage(offset, limit)} · rows {rows.length}
              </span>
              <button
                type="button"
                style={S.buttonStyle}
                disabled={!canGoNext(rows.length, limit) || query.isFetching}
                onClick={() => setOffset(offset + limit)}
                data-testid="tickets-next"
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
              data-testid="tickets-filters-open"
            >
              Filters
            </button>
            <ResponsiveDrawer
              id="tickets-filters-drawer"
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
        activeId={activeId}
        onOpen={setActiveId}
      />

      {active !== null ? (
        <TicketDrawer ticket={active} onClose={() => setActiveId(null)} />
      ) : null}
    </section>
  );
}

interface BodyProps {
  query: ReturnType<typeof useQuery<TicketsEnvelope, ApiError>>;
  rows: readonly AdminTicket[];
  activeId: string | null;
  onOpen: (id: string) => void;
}

function Body({ query, rows, activeId, onOpen }: BodyProps) {
  if (query.isPending) {
    return (
      <div style={S.statusBoxStyle} role="status" aria-live="polite">
        Loading tickets from /v1/admin/tickets…
      </div>
    );
  }
  if (query.isError) {
    return (
      <SupportErrorState
        testIdPrefix="tickets"
        error={query.error}
        onRetry={() => query.refetch()}
      />
    );
  }
  if (rows.length === 0) {
    return (
      <div style={S.statusBoxStyle} role="status" data-testid="tickets-empty">
        No tickets match the current filters.
      </div>
    );
  }
  const columns: ResponsiveTableColumn<AdminTicket>[] = [
    {
      id: "id",
      header: "ID",
      primary: true,
      renderCell: (t) => (
        <span data-testid={`tickets-row-${t.id}`}>
          <button
            type="button"
            style={S.rowNameButtonStyle}
            onClick={() => onOpen(t.id)}
            aria-label={`Open details for ticket ${t.id}`}
            title={t.id}
          >
            {shortUuid(t.id)}
          </button>
        </span>
      ),
    },
    {
      id: "order",
      header: "Order (checkout session)",
      renderCell: (t) => (
        <span title={t.checkout_session_id}>
          {shortUuid(t.checkout_session_id)}
        </span>
      ),
    },
    {
      id: "status",
      header: "Status",
      renderCell: (t) => (
        <span style={badgeForTicketStatus(t.status)}>{t.status}</span>
      ),
    },
    {
      id: "tier",
      header: "Tier",
      renderCell: (t) => (
        <span title={t.tier_id ?? ""}>
          {t.tier_id === null ? "—" : shortUuid(t.tier_id)}
        </span>
      ),
    },
    {
      id: "holder",
      header: "Holder",
      renderCell: (t) =>
        t.holder_email === null ? (
          <span style={S.mutedStyle}>—</span>
        ) : (
          t.holder_email
        ),
    },
    {
      id: "issued",
      header: "Issued",
      renderCell: (t) => formatDateTime(t.issued_at),
    },
    {
      id: "actions",
      header: "Actions",
      hideOnMobile: true,
      renderCell: (t) => (
        <button
          type="button"
          style={S.rowActionButtonStyle}
          onClick={() => onOpen(t.id)}
          data-testid={`tickets-open-${t.id}`}
        >
          Details
        </button>
      ),
    },
  ];
  void activeId;
  return (
    <div style={S.tableWrapStyle} role="region" aria-label="Tickets table">
      <ResponsiveTable<AdminTicket>
        id="tickets-table"
        columns={columns}
        rows={rows}
        rowKey={(t) => t.id}
      />
    </div>
  );
}

/**
 * Pick a colour badge for the ticket lifecycle. Exported for tests.
 */
export function badgeForTicketStatus(status: string): CSSProperties {
  if (status === "active" || status === "issued") {
    return S.successBadgeStyle;
  }
  if (status === "redeemed") {
    return S.statusBadgeStyle;
  }
  if (status === "cancelled" || status === "expired") {
    return S.errorBadgeStyle;
  }
  if (status === "transferred") {
    return S.warnBadgeStyle;
  }
  return S.statusBadgeStyle;
}

function TicketDrawer({
  ticket,
  onClose,
}: {
  ticket: AdminTicket;
  onClose: () => void;
}) {
  // SAUI-13: Escape closes, focus lands on close, focus restores on unmount.
  const closeRef = useRef<HTMLButtonElement | null>(null);
  useEscapeClose(true, onClose);
  useFocusOnMount<HTMLButtonElement>(true, closeRef);
  useFocusRestore(true);
  return (
    <aside
      style={S.drawerWrapStyle}
      role="dialog"
      aria-modal="false"
      aria-labelledby="tickets-drawer-title"
      data-testid="tickets-drawer"
    >
      <header style={S.drawerHeaderStyle}>
        <div>
          <div style={S.drawerEyebrowStyle}>Ticket</div>
          <h2 id="tickets-drawer-title" style={S.drawerTitleStyle}>
            <code style={S.monoStyle}>{ticket.id}</code>
          </h2>
        </div>
        <button
          type="button"
          ref={closeRef}
          onClick={onClose}
          style={S.drawerCloseStyle}
          aria-label="Close ticket details"
          data-testid="tickets-drawer-close"
          title="Close (Esc)"
        >
          ×
        </button>
      </header>

      <section style={S.drawerSectionStyle} aria-labelledby="tickets-drawer-meta">
        <h3 id="tickets-drawer-meta" style={S.drawerSectionTitleStyle}>
          Fields
        </h3>
        <dl style={S.metaListStyle}>
          <MetaRow
            k="Status"
            v={<span style={badgeForTicketStatus(ticket.status)}>{ticket.status}</span>}
          />
          <MetaRow
            k="Checkout session"
            v={<code style={S.monoStyle}>{ticket.checkout_session_id}</code>}
          />
          <MetaRow
            k="Session"
            v={<code style={S.monoStyle}>{ticket.session_id}</code>}
          />
          <MetaRow
            k="Tier"
            v={
              ticket.tier_id === null ? (
                <span style={S.mutedStyle}>—</span>
              ) : (
                <code style={S.monoStyle}>{ticket.tier_id}</code>
              )
            }
          />
          <MetaRow
            k="Holder email"
            v={
              ticket.holder_email === null ? (
                <span style={S.mutedStyle}>—</span>
              ) : (
                ticket.holder_email
              )
            }
          />
          <MetaRow k="Issued" v={formatDateTime(ticket.issued_at)} />
          <MetaRow k="Created" v={formatDateTime(ticket.created_at)} />
          <MetaRow k="Updated" v={formatDateTime(ticket.updated_at)} />
        </dl>
      </section>

      <TicketDeliverySection ticketId={ticket.id} />

      <TicketScansSection ticketId={ticket.id} />

      <section style={S.drawerSectionStyle} aria-labelledby="tickets-drawer-related">
        <h3 id="tickets-drawer-related" style={S.drawerSectionTitleStyle}>
          Related data
        </h3>
        <div style={S.relatedGridStyle}>
          <BackendGapTile
            id="order-by-session"
            label="Parent order"
            reason="No /v1/admin/orders/{id} detail endpoint yet; checkout_session_id is shown for cross-reference but cannot be linked into a typed detail view."
          />
          <BackendGapTile
            id="event"
            label="Event / performance"
            reason="No /v1/admin/events endpoint exposed; event metadata is not joined into the ticket list."
          />
          <BackendGapTile
            id="seat"
            label="Seat / section"
            reason="List endpoint omits seat assignment; richer detail endpoint not exposed."
          />
        </div>
        <p style={S.gapNoteStyle}>
          Cross-tenant tickets/refunds filtered by the same org as this
          ticket can be reached from the parent organization's drawer
          (see <Link to="/organizations">Organizations</Link>). The
          ticket list endpoint itself does not expose <code>org_id</code>
          in its row payload today, so we cannot deep-link from this row.
        </p>
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

// ─────────────────────────────────────────────────────────────────────────────
// Delivery section (feature #291, T-4)
// ─────────────────────────────────────────────────────────────────────────────

/**
 * Shape of GET /v1/admin/tickets/{id}/delivery.
 * Mirrors apps/backend/internal/platform/httpserver/admin_ticket_delivery.go.
 */
export interface AdminTicketDelivery {
  readonly id: string;
  readonly ticket_id: string;
  readonly status: "pending" | "sent" | "failed" | string;
  readonly attempts: number;
  readonly recipient_email: string | null;
  readonly last_error: string | null;
  readonly queued_at: string;
  readonly sent_at: string | null;
  readonly created_at: string;
  readonly updated_at: string;
}

interface TicketDeliveryEnvelope {
  readonly delivery: AdminTicketDelivery;
}

interface TicketDeliveryResendEnvelope {
  readonly delivery: AdminTicketDelivery;
  readonly worker_job_id: string;
}

export function badgeForDeliveryStatus(status: string): CSSProperties {
  if (status === "sent") {
    return S.successBadgeStyle;
  }
  if (status === "failed") {
    return S.errorBadgeStyle;
  }
  // pending and unknown
  return S.warnBadgeStyle;
}

function TicketDeliverySection({ ticketId }: { ticketId: string }) {
  const queryClient = useQueryClient();
  const queryKey = ["admin", "ticket", ticketId, "delivery"] as const;

  const query = useQuery<TicketDeliveryEnvelope, ApiError>({
    queryKey,
    queryFn: () =>
      authedFetch<TicketDeliveryEnvelope>({
        method: "GET",
        path: `/v1/admin/tickets/${ticketId}/delivery`,
      }),
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (
          err.status === 401 ||
          err.status === 403 ||
          err.status === 404 ||
          err.status === 0
        ) {
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

  const resend = useMutation<TicketDeliveryResendEnvelope, ApiError, void>({
    mutationFn: () =>
      authedFetch<TicketDeliveryResendEnvelope>({
        method: "POST",
        path: `/v1/admin/tickets/${ticketId}/delivery/resend`,
      }),
    onSuccess: (data) => {
      // Seed the GET cache with the freshly enqueued row so the section
      // updates immediately without a round-trip.
      queryClient.setQueryData<TicketDeliveryEnvelope>(queryKey, {
        delivery: data.delivery,
      });
    },
  });

  const isNotFound =
    query.isError && query.error instanceof ApiError && query.error.status === 404;
  const delivery = query.data?.delivery ?? null;

  return (
    <section
      style={S.drawerSectionStyle}
      aria-labelledby="tickets-drawer-delivery"
      data-testid="tickets-drawer-delivery"
    >
      <h3 id="tickets-drawer-delivery" style={S.drawerSectionTitleStyle}>
        Delivery
      </h3>

      {query.isPending ? (
        <div style={S.statusBoxStyle} role="status" aria-live="polite">
          Loading delivery status…
        </div>
      ) : query.isError && !isNotFound ? (
        <SupportErrorState
          testIdPrefix="tickets-delivery"
          error={query.error}
          onRetry={() => query.refetch()}
        />
      ) : delivery === null ? (
        <p
          style={S.gapNoteStyle}
          data-testid="tickets-delivery-empty"
        >
          No delivery has been attempted for this ticket yet.
        </p>
      ) : (
        <dl style={S.metaListStyle} data-testid="tickets-delivery-detail">
          <MetaRow
            k="Status"
            v={
              <span style={badgeForDeliveryStatus(delivery.status)}>
                {delivery.status}
              </span>
            }
          />
          <MetaRow k="Attempts" v={String(delivery.attempts)} />
          <MetaRow
            k="Recipient"
            v={
              delivery.recipient_email === null ? (
                <span style={S.mutedStyle}>—</span>
              ) : (
                delivery.recipient_email
              )
            }
          />
          <MetaRow k="Queued" v={formatDateTime(delivery.queued_at)} />
          <MetaRow
            k="Last sent"
            v={
              delivery.sent_at === null ? (
                <span style={S.mutedStyle}>—</span>
              ) : (
                formatDateTime(delivery.sent_at)
              )
            }
          />
          {delivery.last_error === null ? null : (
            <MetaRow
              k="Last error"
              v={
                <span
                  style={{ color: "#7f1d1d", whiteSpace: "pre-wrap" }}
                  data-testid="tickets-delivery-last-error"
                >
                  {delivery.last_error}
                </span>
              }
            />
          )}
        </dl>
      )}

      <div style={{ marginTop: 12, display: "flex", gap: 8, alignItems: "center" }}>
        <button
          type="button"
          style={S.buttonStyle}
          onClick={() => resend.mutate()}
          disabled={resend.isPending}
          data-testid="tickets-delivery-resend"
          aria-describedby="tickets-delivery-resend-hint"
        >
          {resend.isPending ? "Resending…" : "Resend"}
        </button>
        <span
          id="tickets-delivery-resend-hint"
          style={{ fontSize: 11, color: "#475569" }}
        >
          Enqueues a fresh delivery_jobs row. Requires{" "}
          <code>ticket.update</code> or <code>support.act</code>.
        </span>
      </div>

      {resend.isError ? (
        <p
          style={{ color: "#7f1d1d", marginTop: 8, fontSize: 12 }}
          role="alert"
          data-testid="tickets-delivery-resend-error"
        >
          Resend failed:{" "}
          {resend.error instanceof ApiError
            ? `${resend.error.code} — ${resend.error.message}`
            : "Unknown error"}
        </p>
      ) : null}
      {resend.isSuccess ? (
        <p
          style={{ color: "#065f46", marginTop: 8, fontSize: 12 }}
          role="status"
          data-testid="tickets-delivery-resend-success"
        >
          Resend enqueued (worker_job_id={" "}
          <code style={S.monoStyle}>{resend.data.worker_job_id}</code>).
        </p>
      ) : null}
    </section>
  );
}

// ─────────────────────────────────────────────────────────────────────────────
// Scans section (feature #295, S-4)
// ─────────────────────────────────────────────────────────────────────────────

/**
 * Shape of one entry in GET /v1/admin/tickets/{id}/scans.
 * Mirrors apps/backend/internal/platform/httpserver/admin_ticket_scans.go.
 *
 * Nullable FK columns (event_id / session_id / ticket_id) come back as
 * explicit nulls so the drawer can render an em-dash without TypeScript
 * narrowing on optional fields. The backend serialiser pins this.
 */
export interface AdminScanEvent {
  readonly id: string;
  readonly org_id: string;
  readonly event_id: string | null;
  readonly session_id: string | null;
  readonly ticket_id: string | null;
  readonly credential_code: string;
  readonly scanned_at: string;
  readonly received_at: string;
  readonly gate: string;
  readonly device_id: string;
  readonly result: string;
}

interface ScanEventsEnvelope {
  readonly scans: readonly AdminScanEvent[];
  readonly total: number;
  readonly limit: number;
}

/**
 * Colour the admitted / denied result badge consistently with the rest of
 * the support drawer (success / error). Exported for unit tests.
 */
export function badgeForScanResult(result: string): CSSProperties {
  if (result === "admitted") {
    return S.successBadgeStyle;
  }
  if (result === "denied") {
    return S.errorBadgeStyle;
  }
  // Should not happen — the backend CHECK constraint pins admitted|denied —
  // but we render a neutral badge defensively so an unexpected enum value
  // does not blow up the row.
  return S.statusBadgeStyle;
}

function TicketScansSection({ ticketId }: { ticketId: string }) {
  const queryKey = ["admin", "ticket", ticketId, "scans"] as const;
  const query = useQuery<ScanEventsEnvelope, ApiError>({
    queryKey,
    queryFn: () =>
      authedFetch<ScanEventsEnvelope>({
        method: "GET",
        path: `/v1/admin/tickets/${ticketId}/scans`,
      }),
    retry: (failureCount, err) => {
      if (err instanceof ApiError) {
        if (
          err.status === 401 ||
          err.status === 403 ||
          err.status === 404 ||
          err.status === 0
        ) {
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

  const scans = query.data?.scans ?? [];

  return (
    <section
      style={S.drawerSectionStyle}
      aria-labelledby="tickets-drawer-scans"
      data-testid="tickets-drawer-scans"
    >
      <h3 id="tickets-drawer-scans" style={S.drawerSectionTitleStyle}>
        Scans
      </h3>
      <p style={S.gapNoteStyle}>
        Read-only history from <code>scan_events</code> for this ticket,
        newest scan first. Backed by{" "}
        <code>GET /v1/admin/tickets/{"{id}"}/scans</code>.
      </p>

      {query.isPending ? (
        <div style={S.statusBoxStyle} role="status" aria-live="polite">
          Loading scan history…
        </div>
      ) : query.isError ? (
        <SupportErrorState
          testIdPrefix="tickets-scans"
          error={query.error}
          onRetry={() => query.refetch()}
        />
      ) : scans.length === 0 ? (
        <div
          style={S.statusBoxStyle}
          role="status"
          data-testid="tickets-scans-empty"
        >
          No scans recorded for this ticket yet.
        </div>
      ) : (
        <div
          style={S.tableWrapStyle}
          role="region"
          aria-label="Ticket scan history"
        >
          <ResponsiveTable<AdminScanEvent>
            id="tickets-scans-table"
            caption="Ticket scan history"
            rows={scans}
            rowKey={(sc) => sc.id}
            columns={[
              {
                id: "scanned_at",
                header: "Scanned at",
                primary: true,
                renderCell: (sc) => formatDateTime(sc.scanned_at),
              },
              {
                id: "gate",
                header: "Gate",
                renderCell: (sc) =>
                  sc.gate === "" ? (
                    <span style={S.mutedStyle}>—</span>
                  ) : (
                    sc.gate
                  ),
              },
              {
                id: "device",
                header: "Device",
                renderCell: (sc) =>
                  sc.device_id === "" ? (
                    <span style={S.mutedStyle}>—</span>
                  ) : (
                    <code
                      style={{
                        fontFamily:
                          "ui-monospace, SFMono-Regular, Menlo, monospace",
                        fontSize: 12,
                      }}
                    >
                      {sc.device_id}
                    </code>
                  ),
              },
              {
                id: "result",
                header: "Result",
                renderCell: (sc) => (
                  <span style={badgeForScanResult(sc.result)}>
                    {sc.result}
                  </span>
                ),
              },
            ]}
          />
        </div>
      )}
    </section>
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
      data-testid={`tickets-related-gap-${id}`}
      title={reason}
    >
      <span style={S.relatedTileLabelStyle}>{label}</span>
      <span style={S.relatedTileGapBadgeStyle}>backend gap</span>
      <span style={S.relatedTileHintStyle}>{reason}</span>
    </div>
  );
}
