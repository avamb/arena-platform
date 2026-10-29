/**
 * The mocked API of the CSV upload of invitations (invitations-batch.mock.spec.ts).
 *
 * It sits on top of invitationsSeatsMock.ts: call mockApi(page), then
 * mockInvitationsSeats(page), then mockInvitationsBatch(page, seats). Routes
 * registered later win, and a path this mock does not know falls back to the
 * earlier ones. The "server" parses the multipart form for real, validates the
 * rows against the seats the seat mock keeps (sold and held seats refuse), keeps
 * every request it saw in `requests` and issues a batch that the list then shows.
 */
import type { Page, Route } from "@playwright/test";
import { API, CORS, ORG_A, SESSION } from "./mockApi";
import type { InvitationsSeatsState } from "./invitationsSeatsMock";

export interface BatchRequest {
  dryRun: boolean;
  requestId: string | null;
  delivery: string | null;
  fileName: string;
  fileText: string;
}

export interface InvitationsBatchState {
  /** Every upload that reached the server. */
  requests: BatchRequest[];
  /** The next real upload is processed and its answer is lost (the connection drops). */
  dropNextAnswer: boolean;
  /** Batches issued so far. */
  batches: number;
}

interface IssuedBatch {
  id: string;
  requestId: string;
  delivery: string;
  fileText: string;
  orders: { guest: number; order_id: string; system_id: number; guest_name: string; places: number; seat_keys: string[]; delivery: string }[];
  fromReserve: number;
}

const SEAT_KEY: Record<string, string> = { parter: "A", balcony: "B" };

function json(route: Route, status: number, body: unknown) {
  return route.fulfill({ status, contentType: "application/json", headers: CORS, body: JSON.stringify(body) });
}
const err = (route: Route, status: number, code: string, details?: Record<string, unknown>) =>
  json(route, status, { error: { code, message: code, details } });

/** Reads a multipart form: the text fields and the text of the file part. */
function parseForm(contentType: string, body: Buffer): { fields: Record<string, string>; fileName: string; fileText: string } {
  const boundary = /boundary=(?:"([^"]+)"|([^;]+))/.exec(contentType);
  const b = boundary?.[1] ?? boundary?.[2] ?? "";
  const fields: Record<string, string> = {};
  let fileName = "";
  let fileText = "";
  for (const part of body.toString("utf8").split(`--${b}`)) {
    const split = part.indexOf("\r\n\r\n");
    if (split < 0) continue;
    const head = part.slice(0, split);
    const value = part.slice(split + 4).replace(/\r\n$/, "");
    const name = /name="([^"]+)"/.exec(head)?.[1];
    if (name === undefined) continue;
    const file = /filename="([^"]*)"/.exec(head);
    if (file !== null) {
      fileName = file[1];
      fileText = value;
    } else fields[name] = value;
  }
  return { fields, fileName, fileText };
}

function parseCsv(text: string): { header: string[]; rows: { line: number; cells: Record<string, string> }[] } {
  const lines = text.replace(/^﻿/, "").split(/\r?\n/);
  const header = lines[0].split(",").map((h) => h.trim().toLowerCase());
  const rows: { line: number; cells: Record<string, string> }[] = [];
  lines.slice(1).forEach((l, i) => {
    if (l.trim() === "") return;
    const cells: Record<string, string> = {};
    l.split(",").forEach((c, k) => (cells[header[k]] = c.trim()));
    rows.push({ line: i + 2, cells });
  });
  return { header, rows };
}

export async function mockInvitationsBatch(page: Page, seats: InvitationsSeatsState): Promise<InvitationsBatchState> {
  const state: InvitationsBatchState = { requests: [], dropNextAnswer: false, batches: 0 };
  const issued: IssuedBatch[] = [];
  let nextOrder = 1000000900;

  const charge = (places: number) => ({
    mode: "bulk",
    currency: "EUR",
    tickets: places,
    free_tickets: 0,
    charged_tickets: places,
    amount_minor: places * 50,
    lines: [{ tickets: places, unit_minor: 50, amount_minor: places * 50, currency: "EUR" }],
    free_limit: null,
    free_left: null,
    text: `${places} × 0.50 EUR = ${(places * 0.5).toFixed(2)} EUR`,
  });

  /** The validation of a file, as the server does it for the rows the smoke needs. */
  function validate(fileText: string, delivery: string) {
    const { rows } = parseCsv(fileText);
    const errors: { row: number; field: string; code: string; message: string }[] = [];
    const guests = new Map<string, { name: string; email: string; keys: string[]; ga: number }>();
    let seatCount = 0;
    let ga = 0;
    let reserve = 0;
    const seen = new Set<string>();
    let n = 0;
    for (const r of rows) {
      const c = r.cells;
      const id = c.guest !== undefined && c.guest !== "" ? c.guest : `row${r.line}`;
      let g = guests.get(id);
      if (g === undefined) {
        g = { name: c.name ?? "", email: c.email ?? "", keys: [], ga: 0 };
        guests.set(id, g);
      }
      if ((c.name ?? "") === "" && g.name === "") errors.push({ row: r.line, field: "name", code: "name_required", message: "x" });
      if ((c.email ?? "") !== "" && !c.email.includes("@")) errors.push({ row: r.line, field: "email", code: "invalid_email", message: "x" });
      if ((c.email ?? "") === "" && g.email === "" && delivery === "guest_email") errors.push({ row: r.line, field: "email", code: "email_required", message: "x" });
      if ((c.row ?? "") !== "") {
        const key = `${SEAT_KEY[(c.sector ?? "").toLowerCase()] ?? "?"}|${c.row}|${c.seat}`;
        const status = seats.seats.get(key);
        if (status === undefined) errors.push({ row: r.line, field: "seat", code: "unknown_seat", message: "x" });
        else if (status === "sold" || status === "held") errors.push({ row: r.line, field: "seat", code: "seat_taken", message: "x" });
        else if (seen.has(key)) errors.push({ row: r.line, field: "seat", code: "duplicate_seat", message: "x" });
        else {
          seen.add(key);
          g.keys.push(key);
          seatCount++;
          n++;
          if (status === "unavailable") reserve++;
        }
      } else if ((c.category ?? "") !== "") {
        const q = Number(c.qty);
        if (!Number.isInteger(q) || q < 1) errors.push({ row: r.line, field: "qty", code: "invalid_qty", message: "x" });
        else {
          g.ga += q;
          ga += q;
          n += q;
        }
      } else errors.push({ row: r.line, field: "", code: "place_missing", message: "x" });
    }
    return { rows, errors, guests, seatCount, ga, reserve, places: n };
  }

  const reportOf = (v: ReturnType<typeof validate>, delivery: string, dry: boolean) => ({
    dry_run: dry,
    valid: v.errors.length === 0,
    delivery,
    rows: v.rows.length,
    guests: v.guests.size,
    places: v.places,
    seats: v.seatCount,
    ga_places: v.ga,
    from_reserve: v.reserve,
    errors: v.errors,
    preview: [...v.guests.values()].map((g, i) => ({
      guest: i + 1,
      name: g.name,
      email: g.email,
      delivery: g.email !== "" ? "guest_email" : delivery,
      places: g.keys.length + g.ga,
    })),
    charge: v.places > 0 ? charge(v.places) : null,
  });

  const batchBody = (b: IssuedBatch, replayed: boolean) => ({
    id: b.id,
    session_id: SESSION,
    status: "issued",
    created_by: "u1",
    created_at: new Date().toISOString(),
    rows: b.orders.length,
    guests: b.orders.length,
    places: b.orders.reduce((a, o) => a + o.places, 0),
    from_reserve: b.fromReserve,
    orders: b.orders.map((o) => ({ ...o, status: "paid", guest_email: null, from_reserve: 0 })),
    charge: charge(b.orders.reduce((a, o) => a + o.places, 0)),
    replayed,
  });

  await page.route(`${API}/v1/organizations/${ORG_A}/**`, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(`/v1/organizations/${ORG_A}`, "");
    const m = req.method();
    if (m === "OPTIONS") return route.fallback();

    if (path === `/sessions/${SESSION}/invitation-batches` && m === "POST") {
      const dry = url.searchParams.get("dry_run") === "true";
      const form = parseForm(req.headers()["content-type"] ?? "", req.postDataBuffer() ?? Buffer.alloc(0));
      const seen: BatchRequest = {
        dryRun: dry,
        requestId: form.fields.request_id ?? null,
        delivery: form.fields.delivery ?? null,
        fileName: form.fileName,
        fileText: form.fileText,
      };
      state.requests.push(seen);
      const delivery = form.fields.delivery ?? "pdf_only";
      const v = validate(form.fileText, delivery);
      if (v.places > 250) return err(route, 422, "invitation_batch.too_many_places", { max_places: 250 });
      if (dry) return json(route, 200, reportOf(v, delivery, true));

      const requestId = form.fields.request_id;
      if (requestId === undefined) return err(route, 400, "invitation_batch.invalid_request_id");
      const again = issued.find((b) => b.requestId === requestId);
      if (again !== undefined) {
        if (again.fileText !== form.fileText || again.delivery !== delivery) return err(route, 409, "invitation_batch.request_id_reused");
        return json(route, 200, batchBody(again, true));
      }
      if (v.errors.length > 0) return err(route, 422, "invitation_batch.invalid", { report: reportOf(v, delivery, false) });

      const batch: IssuedBatch = { id: `0193f01a-0000-7000-8000-0000000b${String(issued.length).padStart(4, "0")}`, requestId, delivery, fileText: form.fileText, orders: [], fromReserve: v.reserve };
      let i = 0;
      for (const g of v.guests.values()) {
        i++;
        for (const k of g.keys) seats.seats.set(k, "sold");
        batch.orders.push({
          guest: i,
          order_id: `0193f01a-0000-7000-8000-0000000c${String(nextOrder).slice(-4)}`,
          system_id: nextOrder++,
          guest_name: g.name,
          places: g.keys.length + g.ga,
          seat_keys: g.keys,
          delivery: g.email !== "" ? "guest_email" : delivery,
        });
      }
      issued.push(batch);
      state.batches = issued.length;
      if (state.dropNextAnswer) {
        state.dropNextAnswer = false;
        return route.abort("connectionreset");
      }
      return json(route, 201, batchBody(batch, false));
    }

    const result = /^\/invitation-batches\/([^/]+)\/result\.csv$/.exec(path);
    if (result !== null && m === "GET") {
      const b = issued.find((x) => x.id === result[1]);
      if (b === undefined) return err(route, 404, "invitation_batch.not_found");
      const lines = ["guest;name;email;delivery;order_number;order_id;places;from_reserve;seat_keys", ...b.orders.map((o) => `${o.guest};"${o.guest_name}";"";${o.delivery};${o.system_id};${o.order_id};${o.places};0;"${o.seat_keys.join(" ")}"`)];
      return route.fulfill({
        status: 200,
        contentType: "text/csv",
        headers: { ...CORS, "Content-Disposition": `attachment; filename="invitation-batch-${b.id.slice(0, 8)}.csv"`, "Access-Control-Expose-Headers": "Content-Disposition" },
        body: "﻿" + lines.join("\r\n") + "\r\n",
      });
    }

    // The list shows the orders of the batches issued here.
    if (path === `/sessions/${SESSION}/invitations` && m === "GET") {
      const items = issued
        .flatMap((b) => b.orders.map((o) => ({ b, o })))
        .reverse()
        .map(({ b, o }) => ({
          order_id: o.order_id,
          system_id: o.system_id,
          status: "paid",
          guest_name: o.guest_name,
          delivery: o.delivery,
          issued_via: "staff",
          issued_by: { user_id: "u1", email: "manager@example.test", first_name: "Marta", last_name: "Gil" },
          created_at: new Date().toISOString(),
          invitation_batch_id: b.id,
          tickets_total: o.places,
          tickets_active: o.places,
          places: o.seat_keys.map((k) => ({
            seat_key: k,
            sector: k.startsWith("A|") ? "Parter" : "Balcony",
            row: k.split("|")[1],
            seat: k.split("|")[2],
            kind: "seat",
            tier_id: "t",
            tier_name: k.startsWith("A|") ? "Parter" : "Balcony",
            face_value: 2500,
            origin: "available",
            ticket_id: `tk-${k}`,
            ticket_status: "active",
          })),
        }));
      return json(route, 200, { items });
    }
    return route.fallback();
  });
  return state;
}
