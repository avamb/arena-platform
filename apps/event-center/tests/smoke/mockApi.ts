/**
 * A mocked arena-api for the smoke test. Every request of the app goes
 * through page.route(); the answers follow the shapes of openapi.yaml.
 *
 * Two organizations: in ORG_A the person is a manager (organizer), in
 * ORG_B an agent. ORG_C answers 403, like a foreign organization.
 */
import type { Page, Route } from "@playwright/test";

export const API = "http://api.mock.test";
export const ORG_A = "0193f01a-0000-7000-8000-00000000000a";
export const ORG_B = "0193f01a-0000-7000-8000-00000000000b";
export const ORG_C = "0193f01a-0000-7000-8000-00000000000c";
export const EVENT = "0193f01a-0000-7000-8000-0000000000e1";
export const EVENT_OLD = "0193f01a-0000-7000-8000-0000000000e2";
export const SESSION = "0193f01a-0000-7000-8000-000000000051";
export const ORDER = "0193f01a-0000-7000-8000-0000000000d1";
const TIER = "0193f01a-0000-7000-8000-0000000000f1";
const TICKET = "0193f01a-0000-7000-8000-0000000000a1";

const places = (sold: number, total: number) => ({
  total,
  available: total - sold - 2,
  held: 2,
  sold,
  sold_upstream: 0,
  unavailable: 0,
});
const zero = { total: 0, available: 0, held: 0, sold: 0, sold_upstream: 0, unavailable: 0 };
const money = [
  { currency: "EUR", paid_orders: 12, paid: 54000, service_charge: 0, discount: 500, refunded: 4500, net: 49500, pending_orders: 1, pending: 4500 },
];
const summaryCommon = {
  places: { seats: zero, ga: places(12, 80) },
  money,
  orders: [],
  tickets: { active: 12, cancelled: 1, transferred: 0, used: 3, complimentary: 2 },
  entered: { used: 3, total: 12 },
  refunds: [],
  promos: [{ id: "p1", code: "SPRING", currency: "EUR", orders: 2, redemptions: 2, discount: 500 }],
  complimentary: { orders: 1, tickets: 2 },
};

const eventItem = (id: string, name: string, extra: Record<string, unknown>) => ({
  id,
  display_number: 7,
  org_id: ORG_A,
  name,
  description: null,
  status: "published",
  first_session_at: "2026-11-20T19:00:00Z",
  last_session_at: "2026-11-20T21:00:00Z",
  venue_names: ["Palác Akropolis"],
  promoter_id: null,
  promoter_name: "Lampyris",
  sales_state: "on_sale",
  next_session_at: "2026-11-20T19:00:00Z",
  session_count: 1,
  visibility: "public",
  created_at: "2026-09-01T10:00:00Z",
  updated_at: "2026-09-01T10:00:00Z",
  ...extra,
});

const orderSummary = {
  id: ORDER,
  system_id: 1000000512,
  org_id: ORG_A,
  channel_id: "c1",
  event_id: EVENT,
  session_id: SESSION,
  checkout_session_id: "cs1",
  reservation_id: "r1",
  customer_id: null,
  source: "widget",
  status: "paid",
  currency: "EUR",
  subtotal: 9000,
  discount: 0,
  charge: 0,
  total: 9000,
  charge_percent_bp: 0,
  promo_code_id: null,
  buyer_name: "Anna Novak",
  buyer_email: "anna@example.test",
  buyer_phone: "+420600111222",
  payment_method: "card",
  external_ref: null,
  paid_at: "2026-10-01T09:05:00Z",
  cancelled_at: null,
  expires_at: null,
  created_at: "2026-10-01T09:00:00Z",
  updated_at: "2026-10-01T09:05:00Z",
};

export interface MockState {
  requests: string[];
}

/** The CORS headers arena-api sends to an allowed origin (router.go corsMiddleware). */
export const CORS = {
  "Access-Control-Allow-Origin": "*",
  "Access-Control-Expose-Headers": "X-Request-Id, X-Trace-Id, Content-Disposition",
};

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

function forbidden(route: Route) {
  return json(route, 403, { error: { code: "org.access_denied", message: "denied", request_id: "rq" } });
}

export async function mockApi(page: Page): Promise<MockState> {
  const state: MockState = { requests: [] };
  await page.route(`${API}/**`, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname;
    const m = req.method();
    state.requests.push(`${m} ${path}${url.search}`);
    if (m === "OPTIONS") {
      return route.fulfill({
        status: 204,
        headers: { ...CORS, "Access-Control-Allow-Headers": "*", "Access-Control-Allow-Methods": "GET, POST, OPTIONS" },
      });
    }

    if (m === "POST" && path === "/v1/auth/login") {
      const body = req.postDataJSON() as { email: string; password: string };
      if (body.password !== "right-password") {
        return json(route, 401, { error: { code: "auth.invalid_credentials", message: "bad" } });
      }
      return json(route, 200, {
        access_token: "at-1",
        refresh_token: "rt-1",
        token_type: "Bearer",
        expires_at: "2099-01-01T00:00:00Z",
        user_id: "u1",
      });
    }
    if (m === "POST" && path === "/v1/auth/refresh") {
      return json(route, 200, {
        access_token: "at-2",
        refresh_token: "rt-2",
        token_type: "Bearer",
        expires_at: "2099-01-01T00:00:00Z",
        user_id: "u1",
      });
    }
    if (m === "POST" && path === "/v1/auth/logout") return route.fulfill({ status: 204, headers: CORS });
    if (m === "POST" && path === "/v1/auth/password-reset/request") return json(route, 202, { status: "accepted" });

    if (req.headers()["authorization"] === undefined) {
      return json(route, 401, { error: { code: "auth.unauthorized", message: "no token" } });
    }

    if (path === "/v1/me") {
      return json(route, 200, {
        user: { id: "u1", type: "user", is_impersonated: false },
        roles: ["agent", "organizer"],
        permissions: ["event.read", "order.read"],
        organization_memberships: [
          { id: "m1", org_id: ORG_A, org_name: "Teatr Kolybel", org_display_number: 11, role: "organizer", status: "active", joined_at: "2026-01-01T00:00:00Z" },
          { id: "m2", org_id: ORG_B, org_name: "Vino & Co", org_display_number: 12, role: "agent", status: "active", joined_at: "2026-01-01T00:00:00Z" },
        ],
        assigned_networks: [],
        available_scopes: [],
      });
    }

    const org = /^\/v1\/organizations\/([^/]+)(\/.*)$/.exec(path);
    if (org === null) return json(route, 404, { error: { code: "not_found", message: "no route" } });
    const [, orgId, rest] = org;
    if (orgId === ORG_C) return forbidden(route);

    if (rest === "/me") {
      const manager = orgId === ORG_A;
      return json(route, 200, {
        org_id: orgId,
        org_name: manager ? "Teatr Kolybel" : "Vino & Co",
        org_display_number: manager ? 11 : 12,
        caller_type: "user",
        role: manager ? "organizer" : "agent",
        membership_roles: [manager ? "organizer" : "agent"],
        roles: [manager ? "organizer" : "agent"],
        permissions: manager
          ? ["event.read", "order.read", "session.read", "tier.read"]
          : ["event.read", "ticket.read"],
        platform_superadmin: false,
      });
    }
    if (rest === "/events") {
      return json(route, 200, {
        events: [
          eventItem(EVENT, "Serebryanoe kopyttse", {}),
          eventItem(EVENT_OLD, "Old concert", {
            sales_state: "archived",
            next_session_at: null,
            last_session_at: "2026-01-10T20:00:00Z",
          }),
        ],
      });
    }
    if (orgId !== ORG_A) return forbidden(route);

    if (rest === `/events/${EVENT}/summary` || rest === `/events/${EVENT_OLD}/summary`) {
      return json(route, 200, {
        event: { id: EVENT, org_id: ORG_A, name: "Serebryanoe kopyttse", status: "published", first_session_at: null, last_session_at: null, session_count: 1 },
        ...summaryCommon,
        tiers: [{ name: "Parter", kind: "ga", price_amount: 4500, currency: "EUR", is_open: true, sessions: 1, places: places(12, 80), paid_items: 12, paid_revenue: 54000 }],
        sessions: [
          {
            id: SESSION,
            start_at: "2026-11-20T19:00:00Z",
            end_at: "2026-11-20T21:00:00Z",
            status: "scheduled",
            capacity_total: 80,
            has_seating_plan: false,
            venue_name: "Palác Akropolis",
            venue_timezone: "Europe/Prague",
            ...summaryCommon,
            tiers: [],
          },
        ],
      });
    }
    if (rest === `/sessions/${SESSION}/summary`) {
      return json(route, 200, {
        session: {
          id: SESSION,
          event_id: EVENT,
          org_id: ORG_A,
          event_name: "Serebryanoe kopyttse",
          start_at: "2026-11-20T19:00:00Z",
          status: "scheduled",
          capacity_total: 80,
          has_seating_plan: false,
          venue_name: "Palác Akropolis",
          venue_timezone: "Europe/Prague",
        },
        ...summaryCommon,
        tiers: [],
      });
    }
    if (rest === `/events/${EVENT}/sessions`) {
      return json(route, 200, {
        has_overlapping_sessions: false,
        sessions: [
          {
            id: SESSION,
            event_id: EVENT,
            venue_id: "v1",
            start_at: "2026-11-20T19:00:00Z",
            end_at: "2026-11-20T21:00:00Z",
            sales_end_at: "2026-11-20T19:00:00Z",
            doors_open_at: "2026-11-20T18:30:00Z",
            capacity_total: 80,
            capacity_override: null,
            status: "scheduled",
            admission_mode: "general_admission",
            seating_plan_version_id: null,
            currency: "EUR",
            currency_source: "venue",
            created_at: "2026-09-01T10:00:00Z",
            updated_at: "2026-09-01T10:00:00Z",
            has_overlapping_sessions: false,
          },
        ],
      });
    }
    if (rest === `/events/${EVENT}/sessions/${SESSION}/tiers`) {
      return json(route, 200, {
        tiers: [
          {
            id: TIER,
            session_id: SESSION,
            name: "Parter",
            pricing_mode: "fixed",
            price_amount: 4500,
            currency: "EUR",
            sort_order: 1,
            created_at: "2026-09-01T10:00:00Z",
            updated_at: "2026-09-01T10:00:00Z",
            current_price: 4500,
            is_open: true,
            sold: 12,
            held: 2,
            available: 66,
          },
        ],
      });
    }
    if (rest.endsWith(".csv")) {
      return route.fulfill({
        status: 200,
        contentType: "text/csv; charset=utf-8",
        headers: { ...CORS, "Content-Disposition": 'attachment; filename="sales-2026-11-20.csv"' },
        body: "﻿Order;Total\r\n1000000512;90.00\r\n",
      });
    }
    if (rest === `/sessions/${SESSION}/sample-ticket`) {
      return route.fulfill({
        status: 200,
        contentType: "application/pdf",
        headers: { ...CORS, "Content-Disposition": 'attachment; filename="sample-ticket.pdf"' },
        body: "%PDF-1.4\n%mock\n",
      });
    }
    if (rest === "/orders") {
      const q = url.searchParams.get("q") ?? "";
      const tab = url.searchParams.get("tab") ?? "recent";
      const rows = q === "nobody" || tab === "unpaid" ? [] : [
        { ...orderSummary, event_name: "Serebryanoe kopyttse", session_start_at: "2026-11-20T19:00:00Z", session_timezone: "Europe/Prague" },
      ];
      return json(route, 200, { orders: rows, limit: 20, offset: 0, total_count: rows.length, has_more: false });
    }
    if (rest === `/orders/${ORDER}`) {
      return json(route, 200, {
        ...orderSummary,
        items: [],
        events: [],
        tickets: [
          {
            id: TICKET,
            item_id: "i1",
            status: "active",
            holder_email: null,
            seat_sector: null,
            seat_row: null,
            seat_number: null,
            seat_label: null,
            issued_at: "2026-10-01T09:05:00Z",
            cancelled_at: null,
            system_ticket_id: 70011,
            tier_id: TIER,
            tier_name: "Parter",
            price: 4500,
            currency: "EUR",
            barcode: "4600051000017",
            used_at: null,
          },
        ],
        delivery: [{ ticket_id: TICKET, status: "sent", sent_at: "2026-10-01T09:06:00Z", last_error: null }],
        delivery_state: "sent",
        payment: null,
        unpaid_reason: "",
        channel: { id: "c1", name: "kolybel.example", kind: "widget" },
      });
    }
    return json(route, 404, { error: { code: "not_found", message: "no mock" } });
  });
  return state;
}
