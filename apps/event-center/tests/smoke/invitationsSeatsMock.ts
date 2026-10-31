/**
 * The mocked API of the seat-map invitations screen (invitations-seats.mock.spec.ts).
 *
 * It sits on top of mockApi.ts: call mockApi(page) first, then
 * mockInvitationsSeats(page). The hall is the small hall of the unit tests
 * (Parter 2 rows x 4, Balcony 1 row x 3). The seats keep their status in
 * memory, so an issue marks them sold and a cancel gives them back where they
 * came from, as the real server does (spec 46, section 3.3). Every body the
 * screen sent is kept in `issued` / `cancelled` to assert what went on the wire.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, EVENT, ORG_A, SESSION } from "./mockApi";

export const TIER_PARTER = "0193f01a-0000-7000-8000-0000000000a1";
export const TIER_BALCONY = "0193f01a-0000-7000-8000-0000000000a2";

/** The small hall of src/lib/seatManager/hall.fixture.ts (the node tsconfig of the smoke has no `@seatmap` alias to import it). */
function smallHall() {
  const seat = (key: string, number: string, x: number, y: number) => ({ key, number, x, y, radius: 8, category_index: 0 });
  return {
    schema_version: 1,
    canvas: { width: 400, height: 300 },
    categories: [{ index: 0, name: "Plan", color: "#111111" }],
    sections: [
      {
        key: "A",
        name: "Parter",
        rows: [
          { key: "1", name: "1", seats: [1, 2, 3, 4].map((n) => seat(`A|1|${n}`, String(n), 100 + n * 30, 100)) },
          { key: "2", name: "2", seats: [1, 2, 3, 4].map((n) => seat(`A|2|${n}`, String(n), 100 + n * 30, 140)) },
        ],
      },
      {
        key: "B",
        name: "Balcony",
        rows: [{ key: "1", name: "1", seats: [1, 2, 3].map((n) => seat(`B|1|${n}`, String(n), 100 + n * 30, 220)) }],
      },
    ],
    tables: [],
    decor_svg: "",
  };
}

type Status = "available" | "held" | "sold" | "unavailable";

export interface InvitationsSeatsState {
  seats: Map<string, Status>;
  /** Where each seat of an invitation came from, by seat key. */
  origin: Map<string, "available" | "unavailable">;
  /** The JSON body of every issue request that reached the server. */
  issued: Record<string, unknown>[];
  /** The JSON body of every cancel request. */
  cancelled: Record<string, unknown>[];
  /** The next issue request is taken by this seat: answers 409 seats_taken for it, once. */
  takeOnNextIssue: string | null;
  /** The next issue request is processed and its answer is lost (the connection drops). */
  dropNextAnswer: boolean;
  /** How many reads of the list an invitation stays "issuing" for. */
  issuingReads: number;
}

interface Row {
  order_id: string;
  system_id: number;
  guest_name: string;
  delivery: string;
  request_id: string;
  places: { seat_key: string; tier_id: string; tier_name: string; origin: string; ticketId: string; cancelled: boolean }[];
  readsLeft: number;
}

const ALL = [
  "event.read",
  "order.read",
  "session.read",
  "tier.read",
  "session.seats.manage",
  "complimentary.read",
  "complimentary.issue",
  "complimentary.revoke",
];

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}

export async function mockInvitationsSeats(page: Page, permissions: string[] = ALL): Promise<InvitationsSeatsState> {
  const hall = smallHall();
  const state: InvitationsSeatsState = {
    seats: new Map(),
    origin: new Map(),
    issued: [],
    cancelled: [],
    takeOnNextIssue: null,
    dropNextAnswer: false,
    issuingReads: 1,
  };
  for (const s of hall.sections) for (const r of s.rows) for (const seat of r.seats) state.seats.set(seat.key, "available");
  // Seat number 1 of a row sits under the row handle on this small hall: the specs use the others.
  state.seats.set("A|1|3", "sold");
  state.seats.set("A|1|4", "held");
  state.seats.set("A|2|3", "unavailable");

  const tierOf = (key: string) => (key.startsWith("A|") ? TIER_PARTER : TIER_BALCONY);
  const tierName = (key: string) => (key.startsWith("A|") ? "Parter" : "Balcony");
  const rows: Row[] = [];
  let next = 1000000500;

  const placeOf = (r: Row["places"][number], issuing: boolean) => {
    const [sector, row, seat] = r.seat_key.split("|");
    return {
      seat_key: r.seat_key,
      sector: sector === "A" ? "Parter" : "Balcony",
      row,
      seat,
      kind: "seat",
      tier_id: r.tier_id,
      tier_name: r.tier_name,
      face_value: r.tier_id === TIER_PARTER ? 2500 : 1500,
      origin: r.origin,
      ticket_id: issuing ? null : r.ticketId,
      ticket_status: issuing ? null : r.cancelled ? "cancelled" : "active",
    };
  };
  const listItem = (r: Row) => {
    const issuing = r.readsLeft > 0;
    return {
      order_id: r.order_id,
      system_id: r.system_id,
      status: "paid",
      guest_name: r.guest_name,
      delivery: r.delivery,
      issued_via: "staff",
      issued_by: { user_id: "u1", email: "manager@example.test", first_name: "Marta", last_name: "Gil" },
      created_at: new Date().toISOString(),
      tickets_total: issuing ? 0 : r.places.length,
      tickets_active: issuing ? 0 : r.places.filter((p) => !p.cancelled).length,
      places: r.places.map((p) => placeOf(p, issuing)),
    };
  };

  // The map and the seats are read by the seat id, outside the organization prefix.
  await page.route(`${API}/v1/event-sessions/${SESSION}/**`, async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (req.method() === "OPTIONS") return route.fallback();
    if (path.endsWith("/schema")) {
      return json(route, 200, {
        session_id: SESSION,
        event_id: EVENT,
        admission_mode: "assigned_seats",
        seating_plan_version_id: "v1",
        seat_status_version: 1,
        geometry_checksum: "x",
        capacity_seated: 7,
        capacity_standing: 0,
        geometry: hall,
        category_prices: [],
      });
    }
    if (path.endsWith("/seats/admin")) {
      return json(route, 200, {
        session_id: SESSION,
        seat_status_version: 1,
        seats: [...state.seats.entries()].map(([seat_key, status]) => {
          const [sector, row, seat] = seat_key.split("|");
          return {
            id: `id-${seat_key}`,
            seat_key,
            sector_name: sector === "A" ? "Parter" : "Balcony",
            row_name: row,
            seat_number: seat,
            status,
            tier_id: tierOf(seat_key),
            kind: "seat",
          };
        }),
      });
    }
    return route.fallback();
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
    if (path === `/events/${EVENT}/sessions/${SESSION}/tiers` && m === "GET") {
      const tier = (id: string, name: string, price: number, sort: number, seats: number) => ({
        id,
        session_id: SESSION,
        name,
        pricing_mode: "fixed",
        price_amount: price,
        current_price: price,
        currency: "EUR",
        sort_order: sort,
        created_at: "2026-09-01T10:00:00Z",
        seat_count: seats,
        ga_unit_count: 0,
      });
      return json(route, 200, { tiers: [tier(TIER_PARTER, "Parter", 2500, 1, 8), tier(TIER_BALCONY, "Balcony", 1500, 2, 3)] });
    }
    if (path === `/sessions/${SESSION}/invitations` && m === "GET") {
      const items = rows.map(listItem).reverse();
      for (const r of rows) if (r.readsLeft > 0) r.readsLeft--;
      return json(route, 200, { items });
    }
    if (path === `/sessions/${SESSION}/invitations` && m === "POST") {
      const body = req.postDataJSON() as {
        request_id: string;
        seats: { seat_key: string }[];
        guest: { name: string };
        delivery: string;
      };
      state.issued.push(body);
      const again = rows.find((r) => r.request_id === body.request_id);
      if (again !== undefined) {
        return json(route, 200, { ...issueAnswer(again), replayed: true });
      }
      if (state.takeOnNextIssue !== null) {
        const key = state.takeOnNextIssue;
        state.takeOnNextIssue = null;
        state.seats.set(key, "sold");
        return json(route, 409, { error: { code: "invitation.seats_taken", message: "taken", details: { seat_keys: [key] } } });
      }
      const places = body.seats.map((s) => {
        const was = state.seats.get(s.seat_key) === "unavailable" ? "unavailable" : "available";
        state.origin.set(s.seat_key, was);
        state.seats.set(s.seat_key, "sold");
        return { seat_key: s.seat_key, tier_id: tierOf(s.seat_key), tier_name: tierName(s.seat_key), origin: was, ticketId: "tk-" + s.seat_key, cancelled: false };
      });
      const row: Row = {
        order_id: `0193f01a-0000-7000-8000-00000000c${String(rows.length).padStart(3, "0")}`,
        system_id: next++,
        guest_name: body.guest.name,
        delivery: body.delivery,
        request_id: body.request_id,
        places,
        readsLeft: state.issuingReads,
      };
      rows.push(row);
      if (state.dropNextAnswer) {
        state.dropNextAnswer = false;
        return route.abort("connectionreset");
      }
      return json(route, 201, issueAnswer(row));
    }
    const cancel = /^\/invitations\/([^/]+)\/cancel$/.exec(path);
    if (cancel !== null && m === "POST") {
      const body = (req.postDataJSON() ?? {}) as Record<string, unknown>;
      state.cancelled.push(body);
      const row = rows.find((r) => r.order_id === cancel[1]);
      if (row === undefined) return json(route, 404, { error: { code: "invitation.not_found", message: "no" } });
      const named = Array.isArray(body.ticket_ids) ? (body.ticket_ids as string[]) : null;
      const hit = row.places.filter((p) => !p.cancelled && (named === null || named.includes(p.ticketId)));
      for (const p of hit) {
        p.cancelled = true;
        state.seats.set(p.seat_key, p.origin === "unavailable" ? "unavailable" : "available");
      }
      return json(route, 200, {
        order_id: row.order_id,
        order_status: "paid",
        cancelled_ticket_ids: hit.map((p) => p.ticketId),
        skipped_ticket_ids: [],
        notify_guest: body.notify_guest !== false,
      });
    }
    if (/^\/invitations\/[^/]+\/tickets\.pdf$/.test(path) || /^\/invitations\/[^/]+\/resend$/.test(path)) {
      // INV-03 is not built into this mock: the bare 404 of a router that does not know the path.
      return json(route, 404, { error: { code: "http.404", message: "not found" } });
    }
    return route.fallback();
  });

  function issueAnswer(r: Row) {
    return {
      order_id: r.order_id,
      system_id: r.system_id,
      status: "paid",
      guest_name: r.guest_name,
      delivery: r.delivery,
      places: r.places.map((p) => placeOf(p, true)),
      from_reserve: r.places.filter((p) => p.origin === "unavailable").length,
      tickets_status: "issuing",
      charge: null,
      replayed: false,
    };
  }
  return state;
}
