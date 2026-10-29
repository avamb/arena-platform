/**
 * The mocked API of the organization-wide invitations screen
 * (invitations-org.mock.spec.ts). Call mockApi(page) first, then
 * mockInvitationsOrg(page).
 *
 * One event with two dates: a general admission date (SESSION) and a seated
 * one (SEATED). The organization list holds 25 invitations of the staff route
 * (so the 20-row page has a next page), one of them from a CSV file, one with
 * two places, one fully canceled. The old route holds one earlier invitation.
 * Every body the screen sent is kept to assert what went on the wire.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, EVENT, ORG_A, SESSION } from "./mockApi";

export const SEATED = "0193f01a-0000-7000-8000-000000000052";
export const TIER = "0193f01a-0000-7000-8000-0000000000f1";
const OLD_ID = "0193f01a-0000-7000-8000-0000000e0001";

const ALL = [
  "event.read",
  "order.read",
  "session.read",
  "tier.read",
  "complimentary.read",
  "complimentary.issue",
  "complimentary.revoke",
];

interface Place {
  tier_id: string;
  tier_name: string;
  ticket_id: string;
  cancelled: boolean;
  vip: boolean;
  seat_key?: string;
}

interface Row {
  order_id: string;
  system_id: number;
  guest_name: string;
  session: string;
  delivery: string;
  issued_via: string;
  request_id: string | null;
  places: Place[];
  created_at: string;
}

export interface InvitationsOrgState {
  /** Every query string of the org list, in order. */
  listQueries: string[];
  /** The JSON body of every issue request that reached the server. */
  issued: Record<string, unknown>[];
  cancelled: Record<string, unknown>[];
  revoked: string[];
  /** The next issue request is processed but its answer is lost (the connection drops), once. */
  dropNextAnswer: boolean;
  /** The next issue request is refused with 409 invitation.not_enough_places, once. */
  refuseNextIssue: boolean;
  oldRevoked: boolean;
  /** How many old rows the old list holds (set to 0 before the page loads to hide the section). */
  oldRows: number;
}

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

const tierPlace = (id: string, vip = false): Place => ({ tier_id: TIER, tier_name: "Parter", ticket_id: id, cancelled: false, vip });

export async function mockInvitationsOrg(page: Page, permissions: string[] = ALL): Promise<InvitationsOrgState> {
  const state: InvitationsOrgState = {
    listQueries: [],
    issued: [],
    cancelled: [],
    revoked: [],
    dropNextAnswer: false,
    refuseNextIssue: false,
    oldRevoked: false,
    oldRows: 1,
  };
  const rows: Row[] = [];
  const base = Date.parse("2026-10-20T10:00:00Z");
  for (let i = 0; i < 25; i++) {
    rows.push({
      order_id: `0193f01a-0000-7000-8000-00000000c${String(i).padStart(3, "0")}`,
      system_id: 1000000100 + i,
      guest_name: `Guest ${String(i + 1).padStart(2, "0")}`,
      session: SESSION,
      delivery: "guest_email",
      issued_via: "staff",
      request_id: null,
      places: [tierPlace(`tk-${i}`)],
      created_at: new Date(base - i * 60_000).toISOString(),
    });
  }
  // Row 1: two places, one is VIP. Row 2: from a CSV file. Row 3: fully canceled. Row 4: a seated date.
  rows[1].places = [tierPlace("tk-1a", true), tierPlace("tk-1b")];
  rows[2].issued_via = "staff_bulk";
  rows[3].places[0].cancelled = true;
  rows[4].session = SEATED;
  rows[4].places = [{ tier_id: TIER, tier_name: "Parter", ticket_id: "tk-4", cancelled: false, vip: false, seat_key: "A|2|3" }];

  const sessionInfo = (id: string) => ({
    id,
    start_at: id === SESSION ? "2099-11-20T19:00:00Z" : "2099-11-21T19:00:00Z",
    event_id: EVENT,
    event_name: "Serebryanoe kopyttse",
    venue_name: "Palác Akropolis",
    timezone: "Europe/Prague",
  });
  const place = (p: Place, issuing: boolean) => ({
    seat_key: p.seat_key ?? null,
    sector: p.seat_key ? "Parter" : null,
    row: p.seat_key ? "2" : null,
    seat: p.seat_key ? "3" : null,
    kind: p.seat_key ? "seat" : "ga",
    tier_id: p.tier_id,
    tier_name: p.tier_name,
    face_value: 4500,
    origin: "available",
    ticket_id: issuing ? null : p.ticket_id,
    ticket_status: issuing ? null : p.cancelled ? "cancelled" : "active",
    vip: p.vip,
  });
  const item = (r: Row) => ({
    order_id: r.order_id,
    system_id: r.system_id,
    status: "paid",
    guest_name: r.guest_name,
    delivery: r.delivery,
    issued_via: r.issued_via,
    issued_by: { user_id: "u1", email: "manager@example.test", first_name: "Marta", last_name: "Gil" },
    created_at: r.created_at,
    invitation_batch_id: null,
    tickets_total: r.places.length,
    tickets_active: r.places.filter((p) => !p.cancelled).length,
    places: r.places.map((p) => place(p, false)),
    session: sessionInfo(r.session),
  });
  const answerOf = (r: Row) => ({
    order_id: r.order_id,
    system_id: r.system_id,
    status: "paid",
    guest_name: r.guest_name,
    delivery: r.delivery,
    places: r.places.map((p) => place(p, true)),
    from_reserve: 0,
    tickets_status: "issuing",
    charge: null,
    replayed: false,
  });
  let next = 1000000500;

  await page.route(`${API}/v1/complimentary/*/revoke`, async (route) => {
    if (route.request().method() === "OPTIONS") return route.fallback();
    const id = new URL(route.request().url()).pathname.split("/")[3];
    state.revoked.push(id);
    state.oldRevoked = true;
    return json(route, 200, {});
  });

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
        permissions,
        platform_superadmin: false,
      });
    }
    if (path === `/events/${EVENT}/sessions` && m === "GET") {
      const s = (id: string, day: string, plan: string | null) => ({
        id,
        event_id: EVENT,
        venue_id: "v1",
        start_at: `2099-11-${day}T19:00:00Z`,
        end_at: `2099-11-${day}T21:00:00Z`,
        sales_end_at: `2099-11-${day}T19:00:00Z`,
        doors_open_at: null,
        capacity_total: 80,
        capacity_override: null,
        status: "scheduled",
        admission_mode: plan === null ? "general_admission" : "assigned_seats",
        seating_plan_version_id: plan,
        currency: "EUR",
        currency_source: "venue",
        created_at: "2026-09-01T10:00:00Z",
        updated_at: "2026-09-01T10:00:00Z",
        has_overlapping_sessions: false,
      });
      return json(route, 200, { has_overlapping_sessions: false, sessions: [s(SESSION, "20", null), s(SEATED, "21", "v1")] });
    }
    if (path === `/events/${EVENT}/summary` && m === "GET") return route.fallback();
    if (path === `/events/${EVENT}/sessions/${SESSION}/tiers` && m === "GET") {
      return json(route, 200, {
        tiers: [
          {
            id: TIER,
            session_id: SESSION,
            name: "Parter",
            kind: "ga",
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
    if (path === "/invitations" && m === "GET") {
      state.listQueries.push(url.search);
      const q = url.searchParams;
      const stateQ = q.get("state") ?? "active";
      const limit = Number(q.get("limit") ?? 50);
      const offset = q.get("cursor") === null ? 0 : Number(q.get("cursor")!.replace("c", ""));
      const wanted = rows.filter((r) => {
        if (q.get("session_id") !== null && r.session !== q.get("session_id")) return false;
        if (q.get("event_id") !== null && q.get("event_id") !== EVENT) return false;
        const active = r.places.some((p) => !p.cancelled);
        return stateQ === "all" || (stateQ === "active" ? active : !active);
      });
      const slice = wanted.slice(offset, offset + limit);
      const more = offset + limit < wanted.length;
      return json(route, 200, { items: slice.map(item), next_cursor: more ? `c${offset + limit}` : null });
    }
    if (path === `/sessions/${SESSION}/invitations` && m === "POST") {
      const body = req.postDataJSON() as {
        request_id: string;
        ga: { tier_id: string; qty: number }[];
        guest: { name: string; email: string | null };
        delivery: string;
      };
      state.issued.push(body);
      const again = rows.find((r) => r.request_id === body.request_id);
      if (again !== undefined) return json(route, 200, { ...answerOf(again), replayed: true });
      if (state.refuseNextIssue) {
        state.refuseNextIssue = false;
        return json(route, 409, { error: { code: "invitation.not_enough_places", message: "full" } });
      }
      const row: Row = {
        order_id: `0193f01a-0000-7000-8000-00000000d${String(rows.length).padStart(3, "0")}`,
        system_id: next++,
        guest_name: body.guest.name,
        session: SESSION,
        delivery: body.delivery,
        issued_via: "staff",
        request_id: body.request_id,
        places: [tierPlace(`tk-new-${rows.length}`, (body as { vip?: boolean }).vip === true)],
        created_at: new Date().toISOString(),
      };
      rows.unshift(row);
      if (state.dropNextAnswer) {
        state.dropNextAnswer = false;
        return route.abort("connectionreset");
      }
      return json(route, 201, answerOf(row));
    }
    const cancel = /^\/invitations\/([^/]+)\/cancel$/.exec(path);
    if (cancel !== null && m === "POST") {
      const body = (req.postDataJSON() ?? {}) as Record<string, unknown>;
      state.cancelled.push({ order_id: cancel[1], ...body });
      const row = rows.find((r) => r.order_id === cancel[1]);
      if (row === undefined) return json(route, 404, { error: { code: "invitation.not_found", message: "no" } });
      const named = Array.isArray(body.ticket_ids) ? (body.ticket_ids as string[]) : null;
      const hit = row.places.filter((p) => !p.cancelled && (named === null || named.includes(p.ticket_id)));
      for (const p of hit) p.cancelled = true;
      return json(route, 200, {
        order_id: row.order_id,
        order_status: "paid",
        cancelled_ticket_ids: hit.map((p) => p.ticket_id),
        skipped_ticket_ids: [],
        notify_guest: body.notify_guest !== false,
      });
    }
    if (path === "/complimentary" && m === "GET") {
      if (state.oldRows === 0) return json(route, 200, { issuances: [], total: 0, has_more: false });
      return json(route, 200, {
        total: 1,
        has_more: false,
        issuances: [
          {
            id: OLD_ID,
            org_id: ORG_A,
            session_id: SESSION,
            tier_id: TIER,
            qty: 1,
            recipients: ["old@example.com"],
            batch_id: "web-old-0",
            status: "issued",
            event_name: "Serebryanoe kopyttse",
            session_start_at: "2099-11-20T19:00:00Z",
            venue_timezone: "Europe/Prague",
            tier_name: "Parter",
            ticket_count: 1,
            state: state.oldRevoked ? "revoked" : "valid",
            tickets: [{ id: "tk-old", system_ticket_id: 777, holder_email: "old@example.com", holder_name: "Old Guest", status: "active", used: false, vip: false }],
            letters: [{ kind: "invitation", status: "delivered", stuck: false, ticket_ids: ["tk-old"] }],
            created_at: "2026-09-01T10:00:00Z",
          },
        ],
      });
    }
    return route.fallback();
  });
  return state;
}
