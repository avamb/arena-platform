/**
 * The mocked organization summary and accounting workbook of
 * org-summary.mock.spec.ts. It sits on top of mockApi.ts: call mockApi(page)
 * first, then mockOrgSummary(page). `requests` keeps the query string of every
 * summary read and of every workbook download, so the spec can assert the range
 * the screen asked for.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, EVENT, ORG_A, SESSION } from "./mockApi";

export interface OrgSummaryMockState {
  summaryQueries: string[];
  workbookQueries: string[];
}

const ALL = ["event.read", "order.read", "session.read", "tier.read"];

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

const summary = {
  sessions: [
    {
      id: SESSION,
      event_id: EVENT,
      event_name: "Wine night",
      start_at: "2026-11-20T18:00:00Z",
      status: "scheduled",
      venue_name: "Hall",
      venue_timezone: "Europe/Madrid",
      test_orders: 2,
      cards: [
        {
          currency: "EUR",
          orders: 3,
          sold: 4,
          complimentary: 1,
          refunded_tickets: 1,
          active_tickets: 4,
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
  totals: [{ currency: "EUR", orders: 3, sold: 4, complimentary: 1, refunded_tickets: 1, active_tickets: 4, revenue: 12000, refunded: 3000, cashback: 0, net: 9000 }],
  test_orders: 2,
  truncated: false,
};

const XLSX = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet";

export async function mockOrgSummary(page: Page, permissions: string[] = ALL): Promise<OrgSummaryMockState> {
  const state: OrgSummaryMockState = { summaryQueries: [], workbookQueries: [] };
  await page.route(`${API}/v1/organizations/${ORG_A}/**`, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(`/v1/organizations/${ORG_A}`, "");
    if (req.method() === "OPTIONS") return route.fallback();
    if (path === "/me") {
      return json(route, 200, {
        org_id: ORG_A,
        org_name: "Teatr Kolybel",
        org_display_number: 11,
        caller_type: "user",
        role: "organizer",
        membership_roles: ["organizer"],
        roles: ["organizer"],
        permissions,
        platform_superadmin: false,
      });
    }
    if (path === "/summary" && req.method() === "GET") {
      state.summaryQueries.push(url.search);
      return json(route, 200, summary);
    }
    if (path === "/exports/accounting.xlsx" && req.method() === "GET") {
      state.workbookQueries.push(url.search);
      return route.fulfill({
        status: 200,
        headers: { ...CORS, "Content-Type": XLSX, "Content-Disposition": 'attachment; filename="accounting_2026-11-01_2026-11-30.xlsx"', "Access-Control-Expose-Headers": "Content-Disposition" },
        body: "PK",
      });
    }
    return route.fallback();
  });
  return state;
}
