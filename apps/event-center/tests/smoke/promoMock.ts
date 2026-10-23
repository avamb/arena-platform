/**
 * The mocked promo-code API of the 4-3 mock smoke (promo.mock.spec.ts).
 *
 * It sits on top of mockApi.ts: call mockApi(page) first, then mockPromo(page).
 * Playwright runs the LAST registered route first and `route.fallback()` hands
 * a request down to the earlier one, so everything this file does not answer
 * (events, sessions, login) still comes from mockApi. Kept in its own file so
 * the parallel stage-4 tasks do not fight over mockApi.ts.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, EVENT, ORG_A, SESSION } from "./mockApi";

export const PROMO_USED = "0193f01a-0000-7000-8000-0000000000c1";
export const PROMO_EUR = "0193f01a-0000-7000-8000-0000000000c2";

interface Code {
  id: string;
  code: string;
  discount_type: "percent" | "fixed_amount";
  discount_value: number;
  currency: string | null;
  applies_to_session_ids: string[];
  sessions: unknown[];
  warnings: unknown[];
  status: "active" | "paused";
  uses: number;
  discount_total: number;
  discount_currency: string | null;
  last_used_at: string | null;
  max_uses: number | null;
  max_uses_per_customer: number | null;
  valid_from: string | null;
  valid_until: string | null;
}

export interface PromoMockState {
  codes: Code[];
  /** "METHOD path" of every promo call that changes something, with its body. */
  writes: string[];
  /** The permissions the mocked person holds in ORG_A. */
  permissions: string[];
}

const base = (id: string, code: string, extra: Partial<Code>): Code => ({
  id,
  code,
  discount_type: "percent",
  discount_value: 10,
  currency: null,
  applies_to_session_ids: [],
  sessions: [],
  warnings: [],
  status: "active",
  uses: 0,
  discount_total: 0,
  discount_currency: null,
  last_used_at: null,
  max_uses: null,
  max_uses_per_customer: null,
  valid_from: null,
  valid_until: null,
  ...extra,
});

const view = (c: Code) => ({ ...c, org_id: ORG_A, applies_to_tier_ids: [], min_order_amount: 0, created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-01T10:00:00Z" });

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

const ALL = ["event.read", "order.read", "session.read", "tier.read", "promo.read", "promo.create", "promo.update", "promo.delete"];

export async function mockPromo(page: Page, permissions: string[] = ALL): Promise<PromoMockState> {
  const state: PromoMockState = {
    permissions,
    writes: [],
    codes: [
      // Used in two currencies: the card must say so instead of printing one sum.
      base(PROMO_USED, "SPRING", { uses: 2, last_used_at: "2026-10-05T12:00:00Z", discount_total: 10500 }),
      base(PROMO_EUR, "EURFIVE", { discount_type: "fixed_amount", discount_value: 500, currency: "EUR", applies_to_session_ids: [SESSION] }),
    ],
  };
  await page.route(`${API}/v1/organizations/${ORG_A}/**`, async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace(`/v1/organizations/${ORG_A}`, "");
    const m = req.method();
    if (m === "OPTIONS") return route.fallback();

    if (path === "/me") {
      return json(route, 200, {
        org_id: ORG_A,
        org_name: "Teatr Kolybel",
        org_display_number: 11,
        caller_type: "user",
        role: "organizer",
        membership_roles: ["organizer"],
        roles: ["organizer"],
        permissions: state.permissions,
        platform_superadmin: false,
      });
    }
    if (path === "/promo-codes" && m === "GET") return json(route, 200, { promo_codes: state.codes.map(view) });
    if (path === "/promo-codes" && m === "POST") {
      const body = req.postDataJSON() as Partial<Code> & { code: string };
      state.writes.push(`POST ${path} ${JSON.stringify(body)}`);
      if (state.codes.some((c) => c.code.toUpperCase() === body.code.toUpperCase())) {
        return json(route, 409, { error: { code: "promo.duplicate", message: "taken" } });
      }
      const made = base(`0193f01a-0000-7000-8000-0000000000d${state.codes.length}`, body.code, {
        discount_type: body.discount_type,
        discount_value: body.discount_value,
        currency: body.currency ?? null,
        applies_to_session_ids: body.applies_to_session_ids ?? [],
        max_uses: body.max_uses ?? null,
        max_uses_per_customer: body.max_uses_per_customer ?? null,
        valid_until: body.valid_until ?? null,
        status: body.status ?? "active",
      });
      state.codes.push(made);
      return json(route, 201, { promo_code: view(made) });
    }
    const one = /^\/promo-codes\/([^/]+)$/.exec(path);
    if (one !== null) {
      const code = state.codes.find((c) => c.id === one[1]);
      if (code === undefined) return json(route, 404, { error: { code: "promo.not_found", message: "no code" } });
      if (m === "GET") return json(route, 200, { promo_code: view(code) });
      if (m === "PATCH") {
        const body = req.postDataJSON() as Partial<Code>;
        state.writes.push(`PATCH ${path} ${JSON.stringify(body)}`);
        if (body.status !== undefined) code.status = body.status;
        if (body.applies_to_session_ids !== undefined) code.applies_to_session_ids = body.applies_to_session_ids;
        return json(route, 200, { promo_code: view(code) });
      }
      if (m === "DELETE") {
        state.writes.push(`DELETE ${path}`);
        state.codes = state.codes.filter((c) => c.id !== code.id);
        return route.fulfill({ status: 204, headers: CORS });
      }
    }
    if (path === "/promo-code-redemptions" && m === "GET") {
      const id = new URL(req.url()).searchParams.get("promo_code_id");
      const rows =
        id !== PROMO_USED
          ? []
          : [
              { id: "r1", promo_code_id: id, code: "SPRING", redeemed_at: "2026-10-05T12:00:00Z", discount_amount: 500, order_amount: 4500, order_id: "0193f01a-0000-7000-8000-0000000000d1", order_number: 1000000512, order_status: "paid", currency: "EUR", buyer_email: "alice.mock@example.test", buyer_name: "Alice Mock", session_id: SESSION, channel_id: "c1", channel_name: "site" },
              { id: "r2", promo_code_id: id, code: "SPRING", redeemed_at: "2026-10-04T12:00:00Z", discount_amount: 10000, order_amount: 90000, order_id: "0193f01a-0000-7000-8000-0000000000d2", order_number: 1000000513, order_status: "paid", currency: "CZK", buyer_email: "boris.mock@example.test", buyer_name: "Boris Mock", session_id: null, channel_id: "c1", channel_name: "site" },
            ];
      return json(route, 200, { redemptions: rows });
    }
    if (path === `/promo-codes/${PROMO_USED}/redemptions.csv`) {
      return route.fulfill({
        status: 200,
        contentType: "text/csv; charset=utf-8",
        headers: { ...CORS, "Content-Disposition": 'attachment; filename="promo-spring.csv"' },
        body: "﻿Order;Buyer\r\n1000000512;Alice Mock\r\n",
      });
    }
    return route.fallback();
  });
  return state;
}

export { EVENT };
