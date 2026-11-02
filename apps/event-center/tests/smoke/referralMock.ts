/**
 * The mocked "Refer a friend" API of referral.mock.spec.ts (spec 52).
 *
 * It sits on top of mockApi.ts: call mockApi(page) first, then mockReferral(page). Playwright runs the
 * LAST registered route first and `route.fallback()` hands what this file does not answer down to the
 * earlier one, so the events, the order card and the login still come from mockApi.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, EVENT, ORDER, ORG_A } from "./mockApi";

interface Program {
  id: string;
  event_id: string;
  status: "active" | "paused" | "ended";
  friend_discount_pct: number;
  cashback_pct: number;
  cashback_cap_pct: number;
  max_friends: number | null;
  starts_at: string | null;
  ends_at: string | null;
  terms_accepted_at: string;
  created_at: string;
  updated_at: string;
}

export interface ReferralMockState {
  program: Program | null;
  /** True: some channel can return a part of a payment. */
  supported: boolean;
  /** "METHOD path body" of every call that changes something. */
  writes: string[];
  permissions: string[];
  /** Calls to the referral routes, so a test can prove none was sent. */
  referralCalls: string[];
}

const BASE = ["event.read", "order.read", "session.read", "tier.read", "ticket.update", "refund.read", "promo.read"];
export const MANAGER = [...BASE, "promo.update", "refund.create", "refund.approve"];
export const READER = BASE;

const json = (route: Route, status: number, body: unknown) =>
  route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });

const capability = (supported: boolean) => ({
  supported,
  source: "event",
  channels: supported
    ? [{ channel_id: "c1", provider: "stripe", supported: true }]
    : [{ channel_id: "c1", provider: "allpay", supported: false, reason: "no_partial_refund" }],
});

const zero = { paid_minor: 0, sent_minor: 0, due_minor: 0, not_possible_minor: 0, capped_minor: 0, failed_minor: 0, cancelled_minor: 0 };

function reward(i: number, over: Record<string, unknown> = {}) {
  return {
    id: `0193f01a-0000-7000-8000-00000000f${String(i).padStart(3, "0")}`,
    code: `ANNA-${String(1000 + i)}`,
    friend_order_number: 1000000600 + i,
    friend_amount_minor: 4500,
    friend_currency: "EUR",
    referrer_order_number: 1000000500 + i,
    cashback_minor: 900,
    currency: "EUR",
    cashback_pct: 20,
    state: "paid",
    reason: null,
    friend_reversal: null,
    created_at: "2026-11-02T10:00:00Z",
    updated_at: "2026-11-02T10:05:00Z",
    ...over,
  };
}

/** 23 rewards: 21 paid, one capped, one paid whose friend opened a dispute. */
export function allRewards() {
  const rows = [
    reward(1, { state: "paid", friend_reversal: "disputed" }),
    reward(2, { state: "capped", reason: "cap_reached", cashback_minor: 0 }),
  ];
  for (let i = 3; i <= 23; i++) rows.push(reward(i));
  return rows;
}

export async function mockReferral(
  page: Page,
  opts: { permissions?: string[]; program?: Program | null; supported?: boolean } = {},
): Promise<ReferralMockState> {
  const state: ReferralMockState = {
    program: opts.program ?? null,
    supported: opts.supported ?? true,
    writes: [],
    permissions: opts.permissions ?? MANAGER,
    referralCalls: [],
  };
  const rows = allRewards();

  await page.route(`${API}/v1/organizations/${ORG_A}/**`, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(`/v1/organizations/${ORG_A}`, "");
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

    if (path === `/orders/${ORDER}/refund-options`) {
      return json(route, 200, {
        order_id: ORDER,
        route: "arena_provider",
        provider: "stripe",
        currency: "EUR",
        partial_refund: true,
        payment_left: 9000,
        tickets: [
          { ticket_id: "0193f01a-0000-7000-8000-0000000000a1", ticket_number: 70011, status: "active", paid: 4500, refunded: 0, refundable: 4500, in_progress: false },
        ],
        active_refunds: [],
        referral_cashback_paid: { amount_minor: 900, currency: "EUR", code: "ANNA-7K2Q" },
      });
    }

    if (!path.startsWith(`/events/${EVENT}/referral-`)) return route.fallback();
    const sub = path.slice(`/events/${EVENT}/`.length);
    state.referralCalls.push(`${m} ${sub}`);
    const answer = (status: number) =>
      json(route, status, { program: state.program, capability: capability(state.supported), warnings: [] });

    if (sub === "referral-program" && m === "GET") {
      if (state.program === null) {
        return json(route, 404, {
          error: { code: "referral.program_not_found", message: "none", details: { capability: capability(state.supported), warnings: [] } },
        });
      }
      return answer(200);
    }
    if (sub === "referral-program" && m === "PUT") {
      const body = req.postDataJSON() as Record<string, unknown>;
      state.writes.push(`PUT ${sub} ${JSON.stringify(body)}`);
      const starting = state.program === null || state.program.status === "ended";
      if (starting && body.accept_terms !== true) {
        return json(route, 422, { error: { code: "referral.terms_not_accepted", message: "terms" } });
      }
      if (!state.supported) return json(route, 422, { error: { code: "referral.provider_unsupported", message: "no" } });
      const now = "2026-11-02T10:00:00Z";
      state.program = {
        id: state.program?.id ?? "0193f01a-0000-7000-8000-0000000000b1",
        event_id: EVENT,
        status: starting ? "active" : (state.program as Program).status,
        friend_discount_pct: body.friend_discount_pct as number,
        cashback_pct: body.cashback_pct as number,
        cashback_cap_pct: (body.cashback_cap_pct as number | undefined) ?? 100,
        max_friends: (body.max_friends as number | null | undefined) ?? null,
        starts_at: null,
        ends_at: (body.ends_at as string | null | undefined) ?? null,
        terms_accepted_at: now,
        created_at: now,
        updated_at: `${now}-${state.writes.length}`.slice(0, 20) === now ? now : new Date(Date.now()).toISOString(),
      };
      return answer(starting ? 201 : 200);
    }
    if (m === "POST" && /^referral-program\/(pause|resume|end)$/.test(sub)) {
      state.writes.push(`POST ${sub}`);
      const which = sub.split("/")[1];
      if (state.program === null) return json(route, 404, { error: { code: "referral.program_not_found", message: "none" } });
      state.program = {
        ...state.program,
        status: which === "pause" ? "paused" : which === "resume" ? "active" : "ended",
        updated_at: new Date(Date.now()).toISOString(),
      };
      return answer(200);
    }
    if (sub === "referral-program/summary" && m === "GET") {
      return json(route, 200, {
        event_id: EVENT,
        codes_issued: 14,
        friends: 23,
        friend_reversals: 1,
        discount_given: [
          { currency: "EUR", amount_minor: 10350 },
          { currency: "CZK", amount_minor: 90000 },
        ],
        cashback: [
          { currency: "EUR", ...zero, paid_minor: 18900, due_minor: 900 },
          { currency: "CZK", ...zero, not_possible_minor: 20000 },
        ],
      });
    }
    if (sub === "referral-rewards" && m === "GET") {
      const q = url.searchParams;
      const state_ = q.get("state");
      const limit = Number(q.get("limit") ?? 50);
      const filtered = state_ === null || state_ === "" ? rows : rows.filter((r) => r.state === state_);
      const from = q.get("cursor") === null ? 0 : Number(q.get("cursor"));
      const items = filtered.slice(from, from + limit);
      return json(route, 200, { items, next_cursor: from + limit < filtered.length ? String(from + limit) : null });
    }
    return route.fallback();
  });
  return state;
}

export { EVENT, ORDER };
