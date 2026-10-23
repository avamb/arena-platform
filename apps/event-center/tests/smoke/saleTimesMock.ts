/**
 * The mocked session API of the 5-3 mock smoke (sale-times.mock.spec.ts).
 *
 * It sits on top of mockApi.ts: call mockApi(page) first, then
 * mockSaleTimes(page). Playwright runs the LAST registered route first and
 * `route.fallback()` hands everything this file does not answer down to the
 * earlier one. The session keeps its sales end and doors time in memory, so a
 * PATCH is seen by the next list read, and every PATCH body is kept in
 * `writes` to assert what the screen sent.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, EVENT, ORG_A, SESSION } from "./mockApi";

export interface SaleTimesMockState {
  salesEndAt: string;
  doorsOpenAt: string | null;
  /** The JSON body of every PATCH of the session. */
  writes: Record<string, unknown>[];
  /** When set, the next PATCH answers this API error code with status 400. */
  failWith: string | null;
}

const START = "2026-11-20T19:00:00Z";
const ALL = ["event.read", "order.read", "session.read", "tier.read", "session.update"];

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

export async function mockSaleTimes(page: Page, permissions: string[] = ALL): Promise<SaleTimesMockState> {
  const state: SaleTimesMockState = { salesEndAt: START, doorsOpenAt: "2026-11-20T18:30:00Z", writes: [], failWith: null };
  const item = () => ({
    id: SESSION,
    event_id: EVENT,
    venue_id: "v1",
    start_at: START,
    end_at: "2026-11-20T21:00:00Z",
    sales_end_at: state.salesEndAt,
    doors_open_at: state.doorsOpenAt,
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
  });
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
        permissions,
        platform_superadmin: false,
      });
    }
    if (path === `/events/${EVENT}/sessions` && m === "GET") {
      return json(route, 200, { has_overlapping_sessions: false, sessions: [item()] });
    }
    if (path === `/events/${EVENT}/sessions/${SESSION}` && m === "PATCH") {
      const body = req.postDataJSON() as Record<string, unknown>;
      state.writes.push(body);
      if (state.failWith !== null) {
        const code = state.failWith;
        state.failWith = null;
        return json(route, 400, { error: { code, message: "refused" } });
      }
      if (typeof body.sales_end_at === "string") state.salesEndAt = body.sales_end_at;
      if ("doors_open_at" in body) state.doorsOpenAt = (body.doors_open_at as string | null) ?? null;
      return json(route, 200, { session: item() });
    }
    return route.fallback();
  });
  return state;
}
