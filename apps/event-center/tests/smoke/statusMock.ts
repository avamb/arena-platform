/**
 * The mocked event-status API of the 5-1 mock smoke (event-status.mock.spec.ts).
 *
 * It sits on top of mockApi.ts: call mockApi(page) first, then mockStatus(page).
 * Playwright runs the LAST registered route first and `route.fallback()` hands a
 * request down to the earlier one, so what this file does not answer (the
 * summary, sessions, login) still comes from mockApi. Kept in its own file so
 * the parallel tasks do not fight over mockApi.ts.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, EVENT, ORG_A } from "./mockApi";

export interface StatusMockState {
  status: string;
  deleted: boolean;
  /** What the delete dry run answers. */
  impact: { sessions: number; paid_orders: number; tickets: number };
  /** The permissions the mocked person holds in ORG_A. */
  permissions: string[];
  /** "METHOD path body" of every call that changes something. */
  writes: string[];
  /** Make the next status call fail with this API error. */
  failNext: { status: number; code: string } | null;
}

const ALL = ["event.read", "order.read", "session.read", "event.publish", "event.delete"];

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

const FORWARD: Record<string, string[]> = {
  draft: ["published", "cancelled"],
  published: ["draft", "cancelled", "archived"],
  cancelled: ["archived"],
  archived: [],
};

export async function mockStatus(
  page: Page,
  opts: { status?: string; permissions?: string[]; impact?: StatusMockState["impact"] } = {},
): Promise<StatusMockState> {
  const state: StatusMockState = {
    status: opts.status ?? "published",
    deleted: false,
    impact: opts.impact ?? { sessions: 1, paid_orders: 0, tickets: 0 },
    permissions: opts.permissions ?? ALL,
    writes: [],
    failNext: null,
  };
  const eventItem = () => ({
    id: EVENT,
    display_number: 7,
    org_id: ORG_A,
    name: "Serebryanoe kopyttse",
    description: null,
    status: state.status,
    first_session_at: "2026-11-20T19:00:00Z",
    last_session_at: "2026-11-20T21:00:00Z",
    venue_names: ["Palác Akropolis"],
    promoter_id: null,
    promoter_name: null,
    sales_state: state.status === "published" ? "on_sale" : "archived",
    next_session_at: "2026-11-20T19:00:00Z",
    session_count: 1,
    visibility: "public",
    created_at: "2026-09-01T10:00:00Z",
    updated_at: "2026-09-01T10:00:00Z",
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
        permissions: state.permissions,
        platform_superadmin: false,
      });
    }
    if (path === "/events" && m === "GET") return json(route, 200, { events: state.deleted ? [] : [eventItem()] });
    if (path === `/events/${EVENT}/status` && m === "POST") {
      const body = req.postDataJSON() as { status: string };
      if (state.failNext !== null) {
        const f = state.failNext;
        state.failNext = null;
        return json(route, f.status, { error: { code: f.code, message: "refused", request_id: "rq" } });
      }
      if (body.status !== state.status && !(FORWARD[state.status] ?? []).includes(body.status)) {
        return json(route, 422, { error: { code: "event.invalid_transition", message: "no", request_id: "rq" } });
      }
      state.writes.push(`POST ${path} ${JSON.stringify(body)}`);
      state.status = body.status;
      return json(route, 200, { event: eventItem() });
    }
    if (path === `/events/${EVENT}/delete-impact` && m === "GET") {
      const sold = state.impact.paid_orders > 0 || state.impact.tickets > 0;
      return json(route, 200, {
        event_id: EVENT,
        status: state.status,
        ...state.impact,
        can_delete: !sold,
        can_archive: (FORWARD[state.status] ?? []).includes("archived"),
        blocked: sold ? "has_paid_orders" : "",
      });
    }
    if (path === `/events/${EVENT}` && m === "DELETE") {
      state.writes.push(`DELETE ${path}`);
      state.deleted = true;
      return json(route, 200, { event: eventItem(), deleted: true });
    }
    return route.fallback();
  });
  return state;
}
