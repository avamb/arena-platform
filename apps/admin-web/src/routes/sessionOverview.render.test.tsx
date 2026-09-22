/**
 * Render tests for the session overview body. The admin-web Vitest
 * environment is Node-only (no jsdom), so — following the
 * venueSeatingPlans precedent — the router- and query-free half of the
 * screen is rendered with renderToStaticMarkup.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import {
  SessionOverviewBody,
  type OrderRow,
  type SessionSummary,
} from "@/routes/sessionOverview";

const SEATED = "11111111-1111-7111-8111-111111111111";
const STANDING = "22222222-2222-7222-8222-222222222222";

const summary: SessionSummary = {
  session: {
    id: "s1",
    event_id: "e1",
    org_id: "o1",
    event_name: "Test Event",
    start_at: "2026-10-13T17:00:00Z",
    status: "scheduled",
    capacity_total: 566,
    has_seating_plan: true,
    venue_name: "Palác Akropolis",
    venue_timezone: "Europe/Prague",
  },
  places: {
    seats: { total: 90, available: 51, held: 0, sold: 22, sold_upstream: 21, unavailable: 17 },
    ga: { total: 476, available: 475, held: 0, sold: 1, sold_upstream: 0, unavailable: 0 },
  },
  tiers: [
    {
      id: SEATED,
      name: "Balcony",
      kind: "seated",
      price_amount: 50000,
      currency: "CZK",
      is_open: true,
      places: { total: 90, available: 51, held: 0, sold: 22, sold_upstream: 21, unavailable: 17 },
      paid_items: 1,
      paid_revenue: 50000,
    },
    {
      id: STANDING,
      name: "Standing",
      kind: "ga",
      price_amount: 59000,
      currency: "CZK",
      is_open: false,
      places: { total: 476, available: 475, held: 0, sold: 1, sold_upstream: 0, unavailable: 0 },
      paid_items: 1,
      paid_revenue: 59000,
    },
  ],
  money: [
    {
      currency: "CZK",
      paid_orders: 1,
      paid: 109000,
      service_charge: 0,
      discount: 0,
      refunded: 0,
      net: 109000,
      pending_orders: 0,
      pending: 0,
    },
  ],
  orders: [{ status: "paid", source: "bil24_gateway", currency: "CZK", orders: 1, total: 109000 }],
  tickets: { active: 2, cancelled: 0, transferred: 0, used: 1, complimentary: 0 },
  refunds: [],
  promos: [{ id: "p1", code: "ARENA10", currency: "CZK", orders: 1, discount: 4500 }],
};

const latest: readonly OrderRow[] = [
  {
    id: "ord-1",
    system_id: 1000038286,
    status: "paid",
    source: "bil24_gateway",
    currency: "CZK",
    total: 109000,
    buyer_name: "",
    buyer_email: "buyer@example.test",
    created_at: "2026-09-21T18:30:00Z",
  },
];

function markup(data: SessionSummary = summary, orders: readonly OrderRow[] = latest): string {
  return renderToStaticMarkup(
    <SessionOverviewBody data={data} latestOrders={orders} ordersLoading={false} ordersError={null} />,
  );
}

/** The row of one category, isolated so its cells can be asserted in order. */
function tierRow(html: string, id: string): string {
  const start = html.indexOf(`data-testid="session-overview-tier-${id}"`);
  return html.slice(start, html.indexOf("</tr>", start));
}

describe("SessionOverviewBody", () => {
  it("shows the money of the session with the net figure", () => {
    const html = markup();
    expect(html).toContain('data-testid="session-overview-net-CZK"');
    expect(html).toContain("1090.00 CZK");
    expect(html).toContain("Paid · 1 orders");
  });

  it("tells a seat sold here from one sold upstream", () => {
    const cells = tierRow(markup(), SEATED).match(/<td[^>]*>([^<]*)/g)?.map((c) => c.replace(/<td[^>]*>/, ""));
    // Category, Type, Price, Places, Free, Held, Sold here, Sold upstream, Withheld, Paid lines, Revenue
    expect(cells).toEqual(["Balcony", "Seated", "500.00 CZK", "90", "51", "0", "1", "21", "17", "1", "500.00 CZK"]);
  });

  it("marks a closed category", () => {
    expect(tierRow(markup(), STANDING)).toContain("closed");
    expect(tierRow(markup(), SEATED)).not.toContain("closed");
  });

  it("renders seats and general admission as separate hall rows", () => {
    const html = markup();
    expect(html).toContain('data-testid="session-overview-seats-row"');
    expect(html).toContain('data-testid="session-overview-ga-row"');
    expect(html).toContain("Seats · 30% of sellable sold");
  });

  it("falls back to the e-mail when the order carries no buyer name", () => {
    const html = markup();
    expect(html).toContain('data-testid="session-overview-order-1000038286"');
    expect(html).toContain("buyer@example.test");
  });

  it("lists the promo codes the paid orders used", () => {
    const html = markup();
    expect(html).toContain('data-testid="session-overview-promo-ARENA10"');
    expect(html).toContain("45.00 CZK");
  });

  it("says so plainly when a session has nothing yet", () => {
    const zero = { total: 0, available: 0, held: 0, sold: 0, sold_upstream: 0, unavailable: 0 };
    const html = markup(
      { ...summary, places: { seats: zero, ga: zero }, tiers: [], money: [], orders: [], refunds: [], promos: [] },
      [],
    );
    expect(html).toContain("This session has no places yet.");
    expect(html).toContain("No categories.");
    expect(html).toContain("No refunds.");
    expect(html).toContain("No promo codes used.");
    expect(html).not.toContain("session-overview-seats-row");
  });
});
