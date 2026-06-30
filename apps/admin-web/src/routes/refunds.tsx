/**
 * SuperAdmin Refunds support console (SAUI-10).
 *
 * Backed by GET /v1/admin/refunds (see
 * apps/backend/internal/platform/httpserver/superadmin.go). Refunds use
 * the `state` query parameter (not `status`), matching the orders
 * endpoint contract. Read-only -- approval / cancellation flows live
 * elsewhere (org-scoped refunds API) and are intentionally NOT mirrored
 * here until a broader cross-tenant write contract is approved.
 *
 * Mock data: NONE. The page renders only what the backend returns.
 */
import { createRoute } from "@tanstack/react-router";
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

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/refunds",
  component: RefundsRoute,
});

/**
 * Known refund states. Aligned with `refunds.state` in the backend
 * persistence layer. Update both ends when adding a new value.
 */
export const REFUND_STATES: readonly string[] = [
  "requested",
  "approved",
  "processing",
  "succeeded",
  "failed",
  "cancelled",
];

export interface AdminRefund {
  readonly id: string;
  readonly payment_intent_id: string;
  readonly org_id: string;
  readonly amount: number;
  readonly currency: string;
  readonly state: string;
  readonly reason: string | null;
  readonly requested_by: string | null;
  readonly provider_refund_id: string | null;
  readonly requested_at: string;
  readonly approved_at: string | null;
  readonly succeeded_at: string | null;
  readonly created_at: string;
  readonly updated_at: string;
}

interface RefundsEnvelope {
  readonly refunds: readonly AdminRefund[];
  readonly total: number;
  readonly limit: number;
  readonly offset: number;
}

const NAV_ENTRY = NAV_BY_PATH["/refunds"];
if (NAV_ENTRY === undefined) {
  throw new Error("refunds route: NAV_BY_PATH['/refunds'] missing");
}

function RefundsRoute() {
  return (
    <RequirePermission entry={NAV_ENTRY}>
      <RefundsConsole />
    </RequirePermission>
  );
}

function RefundsConsole() {
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
  const [activeId, setActiveId] = useState<string | null>(null);
  const isDesktop = useIsDesktop(true);
  const [filtersOpen, setFiltersOpen] = useState<boolean>(false);

  const orgIdInvalid =
    orgIdInput.trim() !== "" && !isValidUuid(orgIdInput.trim());

  const filters: SupportFilters = {
    orgId: orgIdInvalid ? "" : orgIdInput,
    statusValue: state,
    limit,
    offset,
  };

  const query = useQuery<RefundsEnvelope, ApiError>({
    queryKey: ["admin", "refunds", filters],
    queryFn: () =>
      authedFetch<RefundsEnvelope>({
        method: "GET",
        path: `/v1/admin/refunds?${buildSupportQuery(filters, "state")}`,
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

  const rows = query.data?.refunds ?? [];
  const active = useMemo(
    () => (activeId === null ? null : rows.find((r) => r.id === activeId) ?? null),
    [activeId, rows],
  );

  useEffect(() => {
    setOffset(0);
    setActiveId(null);
  }, [orgIdInput, state, limit]);

  return (
    <section aria-labelledby="refunds-heading" style={S.pageStyle}>
      <header style={S.headerStyle}>
        <div>
          <h1 id="refunds-heading" style={S.headingStyle}>
            Refunds
          </h1>
          <p style={S.subheadingStyle}>
            Cross-tenant refund register. Filters map directly to the
            backend's <code>org_id</code>, <code>state</code>,{" "}
            <code>limit</code>, <code>offset</code> query parameters.
            Read-only; refund approval / cancellation live in the
            org-scoped console and are not mirrored here.
          </p>
        </div>
        <div style={S.refreshWrapStyle}>
          <button
            type="button"
            onClick={() => query.refetch()}
            style={S.refreshButtonStyle}
            disabled={query.isFetching}
            data-testid="refunds-refresh"
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
                data-testid="refunds-org-id"
                aria-invalid={orgIdInvalid}
                aria-describedby={orgIdInvalid ? "refunds-org-id-err" : undefined}
              />
              {orgIdInvalid ? (
                <span
                  id="refunds-org-id-err"
                  style={{ color: "#7f1d1d", fontSize: 11 }}
                  data-testid="refunds-org-id-error"
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
                data-testid="refunds-state"
              >
                <option value="">Any state</option>
                {REFUND_STATES.map((s) => (
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
                data-testid="refunds-limit"
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
                data-testid="refunds-prev"
              >
                Prev
              </button>
              <span data-testid="refunds-page-caption">
                Page {currentPage(offset, limit)} · rows {rows.length}
              </span>
              <button
                type="button"
                style={S.buttonStyle}
                disabled={!canGoNext(rows.length, limit) || query.isFetching}
                onClick={() => setOffset(offset + limit)}
                data-testid="refunds-next"
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
              data-testid="refunds-filters-open"
            >
              Filters
            </button>
            <ResponsiveDrawer
              id="refunds-filters-drawer"
              open={filtersOpen}
              onClose={() => setFiltersOpen(false)}
              title="Filters"
            >
              {toolbar}
            </ResponsiveDrawer>
          </>
        );
      })()}

      <Body query={query} rows={rows} activeId={activeId} onOpen={setActiveId} />

      {active !== null ? (
        <RefundDrawer refund={active} onClose={() => setActiveId(null)} />
      ) : null}
    </section>
  );
}

interface BodyProps {
  query: ReturnType<typeof useQuery<RefundsEnvelope, ApiError>>;
  rows: readonly AdminRefund[];
  activeId: string | null;
  onOpen: (id: string) => void;
}

function Body({ query, rows, activeId, onOpen }: BodyProps) {
  if (query.isPending) {
    return (
      <div style={S.statusBoxStyle} role="status" aria-live="polite">
        Loading refunds from /v1/admin/refunds…
      </div>
    );
  }
  if (query.isError) {
    return (
      <SupportErrorState
        testIdPrefix="refunds"
        error={query.error}
        onRetry={() => query.refetch()}
      />
    );
  }
  if (rows.length === 0) {
    return (
      <div style={S.statusBoxStyle} role="status" data-testid="refunds-empty">
        No refunds match the current filters.
      </div>
    );
  }
  const columns: ResponsiveTableColumn<AdminRefund>[] = [
    {
      id: "id",
      header: "ID",
      primary: true,
      renderCell: (r) => (
        <span data-testid={`refunds-row-${r.id}`}>
          <button
            type="button"
            style={S.rowNameButtonStyle}
            onClick={() => onOpen(r.id)}
            aria-label={`Open details for refund ${r.id}`}
            title={r.id}
          >
            {shortUuid(r.id)}
          </button>
        </span>
      ),
    },
    {
      id: "org",
      header: "Org",
      renderCell: (r) => <span title={r.org_id}>{shortUuid(r.org_id)}</span>,
    },
    {
      id: "state",
      header: "State",
      renderCell: (r) => (
        <span style={badgeForRefundState(r.state)}>{r.state}</span>
      ),
    },
    {
      id: "amount",
      header: "Amount",
      renderCell: (r) => formatMoneyMinor(r.amount, r.currency),
    },
    {
      id: "requested",
      header: "Requested",
      renderCell: (r) => formatDateTime(r.requested_at),
    },
    {
      id: "succeeded",
      header: "Succeeded",
      renderCell: (r) => formatDateTime(r.succeeded_at),
    },
    {
      id: "actions",
      header: "Actions",
      hideOnMobile: true,
      renderCell: (r) => (
        <button
          type="button"
          style={S.rowActionButtonStyle}
          onClick={() => onOpen(r.id)}
          data-testid={`refunds-open-${r.id}`}
        >
          Details
        </button>
      ),
    },
  ];
  void activeId;
  return (
    <div style={S.tableWrapStyle} role="region" aria-label="Refunds table">
      <ResponsiveTable<AdminRefund>
        id="refunds-table"
        columns={columns}
        rows={rows}
        rowKey={(r) => r.id}
      />
    </div>
  );
}

/**
 * Pick a colour badge appropriate to the refund lifecycle.
 * Exported for tests.
 */
export function badgeForRefundState(state: string): CSSProperties {
  if (state === "succeeded") {
    return S.successBadgeStyle;
  }
  if (state === "failed" || state === "cancelled") {
    return S.errorBadgeStyle;
  }
  if (
    state === "requested" ||
    state === "approved" ||
    state === "processing"
  ) {
    return S.warnBadgeStyle;
  }
  return S.statusBadgeStyle;
}

function RefundDrawer({
  refund,
  onClose,
}: {
  refund: AdminRefund;
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
      aria-labelledby="refunds-drawer-title"
      data-testid="refunds-drawer"
    >
      <header style={S.drawerHeaderStyle}>
        <div>
          <div style={S.drawerEyebrowStyle}>Refund</div>
          <h2 id="refunds-drawer-title" style={S.drawerTitleStyle}>
            <code style={S.monoStyle}>{refund.id}</code>
          </h2>
        </div>
        <button
          type="button"
          ref={closeRef}
          onClick={onClose}
          style={S.drawerCloseStyle}
          aria-label="Close refund details"
          data-testid="refunds-drawer-close"
          title="Close (Esc)"
        >
          ×
        </button>
      </header>

      <section style={S.drawerSectionStyle} aria-labelledby="refunds-drawer-meta">
        <h3 id="refunds-drawer-meta" style={S.drawerSectionTitleStyle}>
          Fields
        </h3>
        <dl style={S.metaListStyle}>
          <MetaRow
            k="State"
            v={<span style={badgeForRefundState(refund.state)}>{refund.state}</span>}
          />
          <MetaRow k="Amount" v={formatMoneyMinor(refund.amount, refund.currency)} />
          <MetaRow k="Organization" v={<code style={S.monoStyle}>{refund.org_id}</code>} />
          <MetaRow
            k="Payment intent"
            v={<code style={S.monoStyle}>{refund.payment_intent_id}</code>}
          />
          <MetaRow
            k="Provider refund ID"
            v={
              refund.provider_refund_id === null ? (
                <span style={S.mutedStyle}>—</span>
              ) : (
                <code style={S.monoStyle}>{refund.provider_refund_id}</code>
              )
            }
          />
          <MetaRow
            k="Reason"
            v={
              refund.reason === null ? (
                <span style={S.mutedStyle}>—</span>
              ) : (
                refund.reason
              )
            }
          />
          <MetaRow
            k="Requested by"
            v={
              refund.requested_by === null ? (
                <span style={S.mutedStyle}>—</span>
              ) : (
                <code style={S.monoStyle}>{refund.requested_by}</code>
              )
            }
          />
          <MetaRow k="Requested" v={formatDateTime(refund.requested_at)} />
          <MetaRow k="Approved" v={formatDateTime(refund.approved_at)} />
          <MetaRow k="Succeeded" v={formatDateTime(refund.succeeded_at)} />
          <MetaRow k="Created" v={formatDateTime(refund.created_at)} />
          <MetaRow k="Updated" v={formatDateTime(refund.updated_at)} />
        </dl>
      </section>

      <section style={S.drawerSectionStyle} aria-labelledby="refunds-drawer-related">
        <h3 id="refunds-drawer-related" style={S.drawerSectionTitleStyle}>
          Related data
        </h3>
        <div style={S.relatedGridStyle}>
          <BackendGapTile
            id="payment-detail"
            label="Payment intent detail"
            reason="No /v1/admin/payments endpoint exposed; payment_intent_id is shown for reference only."
          />
          <BackendGapTile
            id="parent-order"
            label="Parent order"
            reason="List endpoint does not return checkout_session_id for refunds; cross-link to /orders is not safe."
          />
          <BackendGapTile
            id="refund-events"
            label="Refund lifecycle events"
            reason="No /v1/admin/refunds/{id}/events endpoint yet; lifecycle is inferred from requested/approved/succeeded timestamps."
          />
          <BackendGapTile
            id="approve-action"
            label="Approve / cancel"
            reason="Write actions are intentionally not exposed under /v1/admin/refunds in this milestone. Use the org-scoped refunds API."
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
      data-testid={`refunds-related-gap-${id}`}
      title={reason}
    >
      <span style={S.relatedTileLabelStyle}>{label}</span>
      <span style={S.relatedTileGapBadgeStyle}>backend gap</span>
      <span style={S.relatedTileHintStyle}>{reason}</span>
    </div>
  );
}
