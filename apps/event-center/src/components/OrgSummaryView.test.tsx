import { describe, expect, it } from "vitest";
import type { OrgSummary } from "@/lib/api/types";
import { renderInRouter } from "@/test-utils/renderInRouter";
import { OrgSummaryView } from "@/components/OrgSummaryView";

const ORG = "11111111-1111-4111-8111-111111111111";

const summary: OrgSummary = {
  sessions: [
    {
      id: "22222222-2222-4222-8222-222222222222",
      event_id: "33333333-3333-4333-8333-333333333333",
      event_name: "Wine night",
      start_at: "2026-11-20T18:00:00Z",
      status: "scheduled",
      venue_name: "Hall",
      venue_timezone: "Europe/Madrid",
      test_orders: 1,
      cards: [
        {
          currency: "EUR",
          orders: 3,
          sold: 4,
          complimentary: 1,
          refunded_tickets: 2,
          active_tickets: 3,
          revenue: 12000,
          refunded: 3000,
          cashback: 0,
          net: 9000,
          breakdown: [
            { tier_id: "t1", tier_name: "Standard", unit_price: 3000, promo_code: null, tickets: 3, discount: 0, revenue: 9000 },
            { tier_id: "t1", tier_name: "Standard", unit_price: 3000, promo_code: "CLUB", tickets: 1, discount: 500, revenue: 2500 },
          ],
          promos: [],
        },
      ],
    },
  ],
  totals: [{ currency: "EUR", orders: 3, sold: 4, complimentary: 1, refunded_tickets: 2, active_tickets: 3, revenue: 12000, refunded: 3000, cashback: 0, net: 9000 }],
  test_orders: 1,
  truncated: false,
};

describe("OrgSummaryView", () => {
  it("prints a card per session and currency with the money and the counts", async () => {
    const html = await renderInRouter(<OrgSummaryView orgId={ORG} summary={summary} />);
    expect(html).toContain("Wine night");
    expect(html).toContain("Revenue: €120.00");
    expect(html).toContain("Refunded: €30.00");
    expect(html).toContain("Net: €90.00");
    expect(html).toContain("Sold: 4");
    expect(html).toContain("Invitations: 1");
    expect(html).toContain("Refunded tickets: 2");
    expect(html).toContain("Active tickets: 3");
    expect(html).toContain("Total");
    expect(html).toContain("Test orders that we left out of all figures: 1");
    expect(html).not.toContain("summary-truncated");
  });

  it("folds the price x category rows into one line with the promo code under it", async () => {
    const html = await renderInRouter(<OrgSummaryView orgId={ORG} summary={summary} />);
    expect(html).toContain("Prices and categories");
    // one price line adds the plain and the promo rows: 4 tickets, 115.00
    expect(html).toContain("<td>4</td><td>€115.00</td>");
    expect(html).toContain("CLUB");
    expect(html.match(/summary-promo-row/g)?.length).toBe(1);
  });

  it("speaks the organization's language", async () => {
    const html = await renderInRouter(<OrgSummaryView orgId={ORG} summary={summary} />, "ru");
    expect(html).toContain("Продано: 4");
    expect(html).toContain("Цены и категории");
  });

  it("says so when the period is empty, and warns when the list was cut", async () => {
    const empty = await renderInRouter(<OrgSummaryView orgId={ORG} summary={{ sessions: [], totals: [], test_orders: 0, truncated: false }} />);
    expect(empty).toContain("No sales, invitations or refunds in this period.");
    const cut = await renderInRouter(<OrgSummaryView orgId={ORG} summary={{ ...summary, truncated: true }} />);
    expect(cut).toContain("Select fewer dates");
  });
});
