/**
 * The mocked event editing API of event-edit.mock.spec.ts. Call mockApi(page) first, then mockEventEdit(page).
 * The last registered route runs first and route.fallback() hands the rest down to mockApi. The role and the
 * permissions of the caller are chosen per test (owner or manager), the event lives in memory so a PATCH is seen
 * by the next read, and every PATCH body is kept in writes.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, EVENT, ORG_A, SESSION } from "./mockApi";

export const OWNER = ["event.read", "event.update", "event.publish", "order.read", "session.read", "session.update", "tier.read", "report.read"];
export const MANAGER = ["event.read", "event.update", "order.read", "session.read", "session.update", "tier.read"];

export interface EventEditState {
  name: string;
  writes: Record<string, unknown>[];
  salesStateWrites: unknown[];
}

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

export async function mockEventEdit(page: Page, role: "org_admin" | "organizer", permissions: string[]): Promise<EventEditState> {
  const state: EventEditState = { name: "Serebryanoe kopyttse", writes: [], salesStateWrites: [] };
  let manual: string | null = null;
  const sales = () => ({
    session_id: SESSION,
    event_id: EVENT,
    manual,
    effective: manual ?? "open",
    total_places: 80,
    free_places: 66,
    held_places: 2,
    sales_end_at: "2026-11-20T19:00:00Z",
    changed_at: null,
    changed_by: null,
    site_sync: { status: "none", sites: 0, attempts: 0 },
    external_allocations: 0,
    org_sales_chat: true,
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
        role,
        membership_roles: [role],
        roles: [role],
        permissions,
        platform_superadmin: false,
      });
    }
    if (path === "/events" && m === "GET") {
      return json(route, 200, {
        events: [
          {
            id: EVENT,
            display_number: 7,
            org_id: ORG_A,
            name: state.name,
            description: "<p>Old text</p>",
            description_base: "<p>Old text</p>",
            description_format: "art",
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
          },
        ],
      });
    }
    if (path === `/events/${EVENT}` && m === "PATCH") {
      const body = req.postDataJSON() as Record<string, unknown>;
      state.writes.push(body);
      if (typeof body.name === "string") state.name = body.name;
      return json(route, 200, { event: { id: EVENT, org_id: ORG_A, name: state.name, status: "published" } });
    }
    if (path === `/events/${EVENT}/sessions` && m === "GET") {
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
            doors_open_at: null,
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
    if (path === `/sessions/${SESSION}/sales-state`) {
      if (m === "PUT" || m === "POST" || m === "PATCH") {
        const body = req.postDataJSON() as { state?: string };
        state.salesStateWrites.push(body);
        manual = body.state === "open" ? null : (body.state ?? null);
        return json(route, 200, { ...sales(), changed: true });
      }
      return json(route, 200, sales());
    }
    return route.fallback();
  });
  return state;
}
