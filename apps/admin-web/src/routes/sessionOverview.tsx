/**
 * Session overview — everything about one session on one screen.
 *
 * The superadmin used to piece a session together from four places (Ticket
 * tiers tab, seat map, Tickets, Orders, Refunds). This screen answers "how is
 * this session doing" at a glance: the money per currency, the hall by
 * status (telling a seat sold here from one sold in the system the session
 * was imported from), every category with what was paid for it, tickets,
 * orders, refunds and the latest orders.
 *
 *   GET /v1/organizations/{org_id}/sessions/{session_id}/summary
 *   GET /v1/organizations/{org_id}/orders?session_id=…&limit=20
 *
 * Mock data: NONE. Pure helpers are exported for sessionOverview.test.ts.
 */
import { createRoute, Link, useParams } from "@tanstack/react-router";
import { useQuery } from "@tanstack/react-query";
import { type CSSProperties, type ReactNode } from "react";
import { Route as RootRoute } from "./__root";
import { ApiError, authedFetch } from "@/lib/api/client";
import { formatDateTime, formatMoneyMinor } from "@/lib/admin/supportConsole";

export const Route = createRoute({
  getParentRoute: () => RootRoute,
  path: "/organizations/$orgId/events/$eventId/sessions/$sessionId/overview",
  component: SessionOverviewRoute,
});

// ---------------------------------------------------------------------------
// Wire types (mirror openapi/clients/ts/index.d.ts — SessionSummary)
// ---------------------------------------------------------------------------

export interface PlaceCounts {
  readonly total: number;
  readonly available: number;
  readonly held: number;
  readonly sold: number;
  readonly sold_upstream: number;
  readonly unavailable: number;
}

export interface SummaryTier {
  readonly id: string;
  readonly name: string;
  readonly kind: string;
  readonly price_amount: number;
  readonly currency: string;
  readonly is_open: boolean;
  readonly places: PlaceCounts;
  readonly paid_items: number;
  readonly paid_revenue: number;
}

export interface SummaryMoney {
  readonly currency: string;
  readonly paid_orders: number;
  readonly paid: number;
  readonly service_charge: number;
  readonly discount: number;
  readonly refunded: number;
  readonly net: number;
  readonly pending_orders: number;
  readonly pending: number;
}

export interface SessionSummary {
  readonly session: {
    readonly id: string;
    readonly event_id: string;
    readonly org_id: string;
    readonly event_name: string;
    readonly start_at: string;
    readonly status: string;
    readonly capacity_total: number;
    readonly has_seating_plan: boolean;
    readonly venue_name: string | null;
    readonly venue_timezone: string | null;
  };
  readonly places: { readonly seats: PlaceCounts; readonly ga: PlaceCounts };
  readonly tiers: readonly SummaryTier[];
  readonly money: readonly SummaryMoney[];
  readonly orders: ReadonlyArray<{
    readonly status: string;
    readonly source: string;
    readonly currency: string;
    readonly orders: number;
    readonly total: number;
  }>;
  readonly tickets: {
    readonly active: number;
    readonly cancelled: number;
    readonly transferred: number;
    readonly used: number;
    readonly complimentary: number;
  };
  readonly refunds: ReadonlyArray<{
    readonly settlement: string;
    readonly state: string;
    readonly currency: string;
    readonly refunds: number;
    readonly amount: number;
  }>;
}

export interface OrderRow {
  readonly id: string;
  readonly system_id: number;
  readonly status: string;
  readonly source: string;
  readonly currency: string;
  readonly total: number;
  readonly buyer_name: string;
  readonly buyer_email: string;
  readonly created_at: string;
}

interface OrdersEnvelope {
  readonly orders: readonly OrderRow[];
}

// ---------------------------------------------------------------------------
// Pure helpers
// ---------------------------------------------------------------------------

/** Places sold through arena itself: a ticket and an order stand behind each. */
export function soldHere(p: PlaceCounts): number {
  return Math.max(0, p.sold - p.sold_upstream);
}

/** Places that can be sold at all: everything except the withheld ones. */
export function sellable(p: PlaceCounts): number {
  return Math.max(0, p.total - p.unavailable);
}

/** Share of the sellable places that is sold, 0–100, rounded. */
export function soldPercent(p: PlaceCounts): number {
  const base = sellable(p);
  if (base === 0) return 0;
  return Math.round((p.sold / base) * 100);
}

/** Sum of two place counters — the whole hall is seats plus GA. */
export function addPlaces(a: PlaceCounts, b: PlaceCounts): PlaceCounts {
  return {
    total: a.total + b.total,
    available: a.available + b.available,
    held: a.held + b.held,
    sold: a.sold + b.sold,
    sold_upstream: a.sold_upstream + b.sold_upstream,
    unavailable: a.unavailable + b.unavailable,
  };
}

/**
 * The session start in the venue's own timezone — an operator thinks in
 * local hall time, not UTC. Falls back to the UTC form when the timezone
 * is missing or unknown to the browser.
 */
export function formatSessionStart(iso: string, timezone: string | null): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  if (timezone !== null && timezone.trim() !== "") {
    try {
      const text = new Intl.DateTimeFormat("en-GB", {
        timeZone: timezone,
        weekday: "short",
        day: "2-digit",
        month: "short",
        year: "numeric",
        hour: "2-digit",
        minute: "2-digit",
      }).format(d);
      return `${text} (${timezone})`;
    } catch {
      // unknown timezone — fall through
    }
  }
  return formatDateTime(iso);
}

/** Deep link into the Tickets console, pre-filtered to this session. */
export function ticketsLink(orgId: string, eventId: string, sessionId: string): string {
  return `/tickets?org_id=${encodeURIComponent(orgId)}&event_id=${encodeURIComponent(eventId)}&session_id=${encodeURIComponent(sessionId)}`;
}

// ---------------------------------------------------------------------------
// Route component
// ---------------------------------------------------------------------------

function SessionOverviewRoute() {
  const { orgId, eventId, sessionId } = useParams({
    from: "/organizations/$orgId/events/$eventId/sessions/$sessionId/overview",
  });
  return <SessionOverviewScreen orgId={orgId} eventId={eventId} sessionId={sessionId} />;
}

export interface SessionOverviewScreenProps {
  readonly orgId: string;
  readonly eventId: string;
  readonly sessionId: string;
}

export function SessionOverviewScreen({
  orgId,
  eventId,
  sessionId,
}: SessionOverviewScreenProps): JSX.Element {
  const summaryQuery = useQuery<SessionSummary, ApiError>({
    queryKey: ["session-overview", orgId, sessionId],
    queryFn: () =>
      authedFetch<SessionSummary>({
        method: "GET",
        path: `/v1/organizations/${orgId}/sessions/${sessionId}/summary`,
      }),
    refetchOnWindowFocus: false,
    retry: false,
  });

  const ordersQuery = useQuery<OrdersEnvelope, ApiError>({
    queryKey: ["session-overview-orders", orgId, sessionId],
    queryFn: () =>
      authedFetch<OrdersEnvelope>({
        method: "GET",
        path: `/v1/organizations/${orgId}/orders?session_id=${sessionId}&limit=20`,
      }),
    refetchOnWindowFocus: false,
    retry: false,
  });

  const refresh = () => {
    void summaryQuery.refetch();
    void ordersQuery.refetch();
  };

  const data = summaryQuery.data;

  return (
    <div style={pageStyle} data-testid="session-overview">
      <div style={{ marginBottom: 8, fontSize: 13 }}>
        <Link to="/events" data-testid="session-overview-back">
          ← Events and sessions
        </Link>
      </div>

      <div style={headerRowStyle}>
        <div>
          <h1 style={headingStyle} data-testid="session-overview-title">
            {data ? data.session.event_name : "Session overview"}
          </h1>
          {data ? (
            <p style={subheadingStyle}>
              {formatSessionStart(data.session.start_at, data.session.venue_timezone)}
              {data.session.venue_name ? ` · ${data.session.venue_name}` : ""}
              {" · "}
              <StatusPill text={data.session.status} />
            </p>
          ) : null}
        </div>
        <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
          {data?.session.has_seating_plan ? (
            <Link
              to="/organizations/$orgId/events/$eventId/sessions/$sessionId/seats"
              params={{ orgId, eventId, sessionId }}
              style={linkButtonStyle}
              data-testid="session-overview-seats"
            >
              Seat map
            </Link>
          ) : null}
          <a
            href={ticketsLink(orgId, eventId, sessionId)}
            style={linkButtonStyle}
            data-testid="session-overview-tickets-link"
          >
            All tickets
          </a>
          <button type="button" onClick={refresh} style={buttonStyle} data-testid="session-overview-refresh">
            Refresh
          </button>
        </div>
      </div>

      {summaryQuery.isLoading ? <p style={mutedStyle}>Loading…</p> : null}
      {summaryQuery.error ? (
        <div style={errorStyle} data-testid="session-overview-error">
          Could not load the session: {summaryQuery.error.message}
        </div>
      ) : null}

      {data ? (
        <SessionOverviewBody
          data={data}
          latestOrders={ordersQuery.data?.orders ?? []}
          ordersLoading={ordersQuery.isLoading}
          ordersError={ordersQuery.error?.message ?? null}
        />
      ) : null}
    </div>
  );
}

export interface SessionOverviewBodyProps {
  readonly data: SessionSummary;
  readonly latestOrders: readonly OrderRow[];
  readonly ordersLoading: boolean;
  readonly ordersError: string | null;
}

/**
 * Everything below the header. State-, query- and router-free, so the
 * Node-only test environment can render it with renderToStaticMarkup.
 */
export function SessionOverviewBody({
  data,
  latestOrders,
  ordersLoading,
  ordersError,
}: SessionOverviewBodyProps): JSX.Element {
  return (
    <>
      <MoneySection money={data.money} />
      <HallSection places={data.places} capacity={data.session.capacity_total} />
      <TiersSection tiers={data.tiers} />
      <div style={twoColumnStyle}>
        <TicketsSection tickets={data.tickets} />
        <OrdersByStatusSection orders={data.orders} />
      </div>
      <RefundsSection refunds={data.refunds} />
      <LatestOrdersSection orders={latestOrders} loading={ordersLoading} error={ordersError} />
    </>
  );
}

// ---------------------------------------------------------------------------
// Sections
// ---------------------------------------------------------------------------

function Section({ title, hint, children, testId }: {
  title: string;
  hint?: string;
  children: ReactNode;
  testId: string;
}): JSX.Element {
  return (
    <section style={sectionStyle} data-testid={testId}>
      <h2 style={sectionTitleStyle}>{title}</h2>
      {hint ? <p style={hintStyle}>{hint}</p> : null}
      {children}
    </section>
  );
}

function Stat({ label, value, tone, testId }: {
  label: string;
  value: string | number;
  tone?: "good" | "warn" | "muted" | "accent";
  testId?: string;
}): JSX.Element {
  const color =
    tone === "good" ? "#15803d" : tone === "warn" ? "#b45309" : tone === "muted" ? "#64748b" : tone === "accent" ? "#0369a1" : "#0f172a";
  return (
    <div style={statStyle} data-testid={testId}>
      <div style={statLabelStyle}>{label}</div>
      <div style={{ ...statValueStyle, color }}>{value}</div>
    </div>
  );
}

function StatusPill({ text }: { text: string }): JSX.Element {
  return <span style={pillStyle}>{text}</span>;
}

function MoneySection({ money }: { money: readonly SummaryMoney[] }): JSX.Element {
  return (
    <Section
      title="Money"
      hint="Order totals as arena recorded them. For a site that takes payment itself, the money sits in that site's own acquiring — these are the amounts it reported."
      testId="session-overview-money"
    >
      {money.length === 0 ? <p style={mutedStyle}>No orders yet.</p> : null}
      {money.map((m) => (
        <div key={m.currency} style={statRowStyle} data-testid={`session-overview-money-${m.currency}`}>
          <Stat label={`Paid · ${m.paid_orders} orders`} value={formatMoneyMinor(m.paid, m.currency)} />
          <Stat label="Refunded" value={formatMoneyMinor(m.refunded, m.currency)} tone={m.refunded > 0 ? "warn" : "muted"} />
          <Stat label="Net" value={formatMoneyMinor(m.net, m.currency)} tone="good" testId={`session-overview-net-${m.currency}`} />
          <Stat label="Service charge inside" value={formatMoneyMinor(m.service_charge, m.currency)} tone="muted" />
          <Stat label="Discounts given" value={formatMoneyMinor(m.discount, m.currency)} tone="muted" />
          <Stat
            label={`Awaiting payment · ${m.pending_orders}`}
            value={formatMoneyMinor(m.pending, m.currency)}
            tone={m.pending_orders > 0 ? "warn" : "muted"}
          />
        </div>
      ))}
    </Section>
  );
}

function PlacesRow({ label, p, testId }: { label: string; p: PlaceCounts; testId: string }): JSX.Element {
  return (
    <div data-testid={testId} style={{ marginBottom: 12 }}>
      <div style={rowLabelStyle}>
        {label} · {soldPercent(p)}% of sellable sold
      </div>
      <div style={barStyle} aria-hidden="true">
        <BarPart value={soldHere(p)} total={p.total} color="#0369a1" />
        <BarPart value={p.sold_upstream} total={p.total} color="#7dd3fc" />
        <BarPart value={p.held} total={p.total} color="#f59e0b" />
        <BarPart value={p.available} total={p.total} color="#22c55e" />
        <BarPart value={p.unavailable} total={p.total} color="#94a3b8" />
      </div>
      <div style={statRowStyle}>
        <Stat label="Total" value={p.total} />
        <Stat label="Free" value={p.available} tone="good" />
        <Stat label="Held" value={p.held} tone={p.held > 0 ? "warn" : "muted"} />
        <Stat label="Sold here" value={soldHere(p)} tone="accent" />
        <Stat label="Sold upstream" value={p.sold_upstream} tone={p.sold_upstream > 0 ? "accent" : "muted"} />
        <Stat label="Withheld" value={p.unavailable} tone="muted" />
      </div>
    </div>
  );
}

function BarPart({ value, total, color }: { value: number; total: number; color: string }): JSX.Element | null {
  if (total <= 0 || value <= 0) return null;
  return <div style={{ width: `${(value / total) * 100}%`, background: color, height: "100%" }} />;
}

function HallSection({ places, capacity }: {
  places: SessionSummary["places"];
  capacity: number;
}): JSX.Element {
  const all = addPlaces(places.seats, places.ga);
  return (
    <Section
      title="Hall"
      hint={`Stated capacity ${capacity}. "Sold upstream" are places sold in the system this session was imported from: no ticket or order stands behind them here. "Withheld" are places an operator closed for sale and can reopen.`}
      testId="session-overview-hall"
    >
      {all.total === 0 ? <p style={mutedStyle}>This session has no places yet.</p> : null}
      {places.seats.total > 0 ? <PlacesRow label="Seats" p={places.seats} testId="session-overview-seats-row" /> : null}
      {places.ga.total > 0 ? <PlacesRow label="General admission" p={places.ga} testId="session-overview-ga-row" /> : null}
    </Section>
  );
}

function TiersSection({ tiers }: { tiers: readonly SummaryTier[] }): JSX.Element {
  return (
    <Section title="Categories" testId="session-overview-tiers">
      {tiers.length === 0 ? (
        <p style={mutedStyle}>No categories.</p>
      ) : (
        <div style={{ overflowX: "auto" }}>
          <table style={tableStyle}>
            <thead>
              <tr>
                <th style={thStyle}>Category</th>
                <th style={thStyle}>Type</th>
                <th style={thNumStyle}>Price</th>
                <th style={thNumStyle}>Places</th>
                <th style={thNumStyle}>Free</th>
                <th style={thNumStyle}>Held</th>
                <th style={thNumStyle}>Sold here</th>
                <th style={thNumStyle}>Sold upstream</th>
                <th style={thNumStyle}>Withheld</th>
                <th style={thNumStyle}>Paid lines</th>
                <th style={thNumStyle}>Revenue</th>
              </tr>
            </thead>
            <tbody>
              {tiers.map((t) => (
                <tr key={t.id} data-testid={`session-overview-tier-${t.id}`}>
                  <td style={tdStyle}>
                    {t.name}
                    {t.is_open ? null : <span style={closedStyle}> closed</span>}
                  </td>
                  <td style={tdStyle}>{t.kind === "seated" ? "Seated" : "GA"}</td>
                  <td style={tdNumStyle}>{formatMoneyMinor(t.price_amount, t.currency)}</td>
                  <td style={tdNumStyle}>{t.places.total}</td>
                  <td style={tdNumStyle}>{t.places.available}</td>
                  <td style={tdNumStyle}>{t.places.held}</td>
                  <td style={tdNumStyle}>{soldHere(t.places)}</td>
                  <td style={tdNumStyle}>{t.places.sold_upstream}</td>
                  <td style={tdNumStyle}>{t.places.unavailable}</td>
                  <td style={tdNumStyle}>{t.paid_items}</td>
                  <td style={tdNumStyle}>{formatMoneyMinor(t.paid_revenue, t.currency)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Section>
  );
}

function TicketsSection({ tickets }: { tickets: SessionSummary["tickets"] }): JSX.Element {
  return (
    <Section title="Tickets" testId="session-overview-tickets">
      <div style={statRowStyle}>
        <Stat label="Valid" value={tickets.active} tone="good" />
        <Stat label="Scanned at the door" value={tickets.used} tone={tickets.used > 0 ? "accent" : "muted"} />
        <Stat label="Invitations" value={tickets.complimentary} tone="muted" />
        <Stat label="Cancelled" value={tickets.cancelled} tone={tickets.cancelled > 0 ? "warn" : "muted"} />
      </div>
    </Section>
  );
}

function OrdersByStatusSection({ orders }: { orders: SessionSummary["orders"] }): JSX.Element {
  return (
    <Section title="Orders by status" testId="session-overview-orders">
      {orders.length === 0 ? (
        <p style={mutedStyle}>No orders yet.</p>
      ) : (
        <table style={tableStyle}>
          <thead>
            <tr>
              <th style={thStyle}>Status</th>
              <th style={thStyle}>Source</th>
              <th style={thNumStyle}>Orders</th>
              <th style={thNumStyle}>Total</th>
            </tr>
          </thead>
          <tbody>
            {orders.map((o) => (
              <tr key={`${o.status}|${o.source}|${o.currency}`}>
                <td style={tdStyle}>{o.status}</td>
                <td style={tdStyle}>{o.source}</td>
                <td style={tdNumStyle}>{o.orders}</td>
                <td style={tdNumStyle}>{formatMoneyMinor(o.total, o.currency)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Section>
  );
}

function RefundsSection({ refunds }: { refunds: SessionSummary["refunds"] }): JSX.Element {
  return (
    <Section
      title="Refunds"
      hint="provider — arena returns the money through its payment provider. external — the selling site returned the money itself and told arena."
      testId="session-overview-refunds"
    >
      {refunds.length === 0 ? (
        <p style={mutedStyle}>No refunds.</p>
      ) : (
        <table style={tableStyle}>
          <thead>
            <tr>
              <th style={thStyle}>Settlement</th>
              <th style={thStyle}>State</th>
              <th style={thNumStyle}>Refunds</th>
              <th style={thNumStyle}>Amount</th>
            </tr>
          </thead>
          <tbody>
            {refunds.map((r) => (
              <tr key={`${r.settlement}|${r.state}|${r.currency}`}>
                <td style={tdStyle}>{r.settlement}</td>
                <td style={tdStyle}>{r.state}</td>
                <td style={tdNumStyle}>{r.refunds}</td>
                <td style={tdNumStyle}>{formatMoneyMinor(r.amount, r.currency)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Section>
  );
}

function LatestOrdersSection({ orders, loading, error }: {
  orders: readonly OrderRow[];
  loading: boolean;
  error: string | null;
}): JSX.Element {
  return (
    <Section title="Latest orders" hint="The 20 most recent. The full list with search lives in Org orders." testId="session-overview-latest-orders">
      {loading ? <p style={mutedStyle}>Loading…</p> : null}
      {error !== null ? <div style={errorStyle}>Could not load orders: {error}</div> : null}
      {!loading && error === null && orders.length === 0 ? <p style={mutedStyle}>No orders yet.</p> : null}
      {orders.length > 0 ? (
        <div style={{ overflowX: "auto" }}>
          <table style={tableStyle}>
            <thead>
              <tr>
                <th style={thStyle}>Order</th>
                <th style={thStyle}>Created</th>
                <th style={thStyle}>Status</th>
                <th style={thStyle}>Source</th>
                <th style={thStyle}>Buyer</th>
                <th style={thNumStyle}>Total</th>
              </tr>
            </thead>
            <tbody>
              {orders.map((o) => (
                <tr key={o.id} data-testid={`session-overview-order-${o.system_id}`}>
                  <td style={tdMonoStyle}>{o.system_id}</td>
                  <td style={tdStyle}>{formatDateTime(o.created_at)}</td>
                  <td style={tdStyle}>{o.status}</td>
                  <td style={tdStyle}>{o.source}</td>
                  <td style={tdStyle}>{o.buyer_name !== "" ? o.buyer_name : o.buyer_email}</td>
                  <td style={tdNumStyle}>{formatMoneyMinor(o.total, o.currency)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </Section>
  );
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const pageStyle: CSSProperties = { padding: 24, maxWidth: 1280, color: "#0f172a" };

const headerRowStyle: CSSProperties = {
  display: "flex",
  justifyContent: "space-between",
  alignItems: "flex-start",
  gap: 16,
  flexWrap: "wrap",
  marginBottom: 16,
};

const headingStyle: CSSProperties = { margin: 0, fontSize: 22, fontWeight: 600, letterSpacing: -0.2 };

const subheadingStyle: CSSProperties = { margin: "4px 0 0 0", fontSize: 13, color: "#475569", lineHeight: 1.45 };

const sectionStyle: CSSProperties = {
  background: "#ffffff",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
  padding: 16,
  marginBottom: 16,
};

const sectionTitleStyle: CSSProperties = { margin: "0 0 4px 0", fontSize: 15, fontWeight: 600 };

const hintStyle: CSSProperties = { margin: "0 0 12px 0", fontSize: 12, color: "#64748b", lineHeight: 1.45, maxWidth: 900 };

const mutedStyle: CSSProperties = { margin: 0, fontSize: 13, color: "#64748b" };

const statRowStyle: CSSProperties = { display: "flex", gap: 12, flexWrap: "wrap", marginTop: 8 };

const statStyle: CSSProperties = {
  minWidth: 130,
  flex: "1 1 130px",
  padding: "10px 12px",
  background: "#f8fafc",
  border: "1px solid #e2e8f0",
  borderRadius: 6,
};

const statLabelStyle: CSSProperties = {
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
};

const statValueStyle: CSSProperties = { marginTop: 4, fontSize: 20, fontWeight: 600, fontVariantNumeric: "tabular-nums" };

const rowLabelStyle: CSSProperties = { fontSize: 13, fontWeight: 600, marginBottom: 6 };

const barStyle: CSSProperties = {
  display: "flex",
  height: 10,
  borderRadius: 5,
  overflow: "hidden",
  background: "#e2e8f0",
};

const twoColumnStyle: CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fit, minmax(360px, 1fr))",
  gap: 16,
};

const tableStyle: CSSProperties = { width: "100%", borderCollapse: "collapse", fontSize: 13 };

const thStyle: CSSProperties = {
  textAlign: "left",
  padding: "8px 10px",
  borderBottom: "1px solid #e2e8f0",
  background: "#f8fafc",
  fontSize: 11,
  fontWeight: 600,
  color: "#475569",
  textTransform: "uppercase",
  letterSpacing: 0.4,
  whiteSpace: "nowrap",
};

const thNumStyle: CSSProperties = { ...thStyle, textAlign: "right" };

const tdStyle: CSSProperties = { padding: "8px 10px", borderBottom: "1px solid #f1f5f9", verticalAlign: "middle" };

const tdNumStyle: CSSProperties = { ...tdStyle, textAlign: "right", fontVariantNumeric: "tabular-nums", whiteSpace: "nowrap" };

const tdMonoStyle: CSSProperties = {
  ...tdStyle,
  fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
  fontSize: 12,
};

const buttonStyle: CSSProperties = {
  fontSize: 12,
  padding: "6px 12px",
  background: "#ffffff",
  border: "1px solid #cbd5e1",
  borderRadius: 4,
  cursor: "pointer",
  color: "#0f172a",
};

const linkButtonStyle: CSSProperties = { ...buttonStyle, textDecoration: "none", display: "inline-block" };

const pillStyle: CSSProperties = {
  display: "inline-block",
  padding: "1px 8px",
  borderRadius: 10,
  background: "#e0f2fe",
  color: "#075985",
  fontSize: 11,
  fontWeight: 600,
};

const closedStyle: CSSProperties = { color: "#b45309", fontSize: 11, fontWeight: 600 };

const errorStyle: CSSProperties = {
  padding: "10px 12px",
  background: "#fef2f2",
  border: "1px solid #fecaca",
  borderRadius: 6,
  color: "#991b1b",
  fontSize: 13,
  marginBottom: 16,
};
