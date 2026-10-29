import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { AdminSeatRow, StaffInvitationListItem } from "@/lib/api/types";
import {
  MAX_PLACES,
  buildIssueBody,
  canAct,
  chargeLine,
  dropUnpickable,
  emptyForm,
  formProblems,
  gaCounts,
  gaLines,
  gaTotal,
  groupSelection,
  invitationState,
  isDefinitiveRefusal,
  isGaTier,
  isMissingRoute,
  isPickable,
  isSeatConflict,
  issuerName,
  originCounts,
  pickSeats,
  pollDelay,
  requestIdFor,
  seatIndex,
  takenSeatKeys,
  withRequestId,
  type GuestForm,
} from "@/lib/staffInvitations";

const row = (key: string, status: AdminSeatRow["status"], tier: string | null = "t1", kind: AdminSeatRow["kind"] = "seat"): AdminSeatRow => ({
  id: `id-${key}`,
  seat_key: key,
  sector_name: "Sala",
  row_name: key.split("|")[1] ?? "1",
  seat_number: key.split("|")[2] ?? "1",
  status,
  tier_id: tier,
  kind,
});

const seats = seatIndex([
  row("S|1|1", "available"),
  row("S|1|2", "unavailable"),
  row("S|1|3", "held"),
  row("S|1|4", "sold"),
  row("S|2|1", "available", "t2"),
  row("ga|t3|1", "available", "t3", "ga_unit"),
  row("ga|t3|2", "unavailable", "t3", "ga_unit"),
  row("ga|t3|3", "sold", "t3", "ga_unit"),
]);

const form = (over: Partial<GuestForm> = {}): GuestForm => ({ ...emptyForm("es"), name: "Ana López", email: "ana@example.com", ...over });

describe("which seats can be given away", () => {
  it("takes free seats and seats the organizer holds back, never held or sold ones", () => {
    expect(isPickable("available")).toBe(true);
    expect(isPickable("unavailable")).toBe(true);
    expect(isPickable("held")).toBe(false);
    expect(isPickable("sold")).toBe(false);
    expect(isPickable(undefined)).toBe(false);
  });
  it("indexes physical seats only", () => {
    expect(seats.size).toBe(5);
    expect(seats.has("ga|t3|1")).toBe(false);
    expect(seats.get("S|1|2")).toMatchObject({ sector: "Sala", row: "1", seat: "2", status: "unavailable", tierId: "t1" });
  });
});

describe("pickSeats", () => {
  it("toggles one seat", () => {
    const a = pickSeats(new Set(), ["S|1|1"], seats);
    expect([...a.next]).toEqual(["S|1|1"]);
    const b = pickSeats(a.next, ["S|1|1"], seats);
    expect(b.next.size).toBe(0);
  });
  it("picks a closed seat of the reserve", () => {
    expect([...pickSeats(new Set(), ["S|1|2"], seats).next]).toEqual(["S|1|2"]);
  });
  it("refuses held, sold and unknown seats and counts them", () => {
    const r = pickSeats(new Set(), ["S|1|3", "S|1|4", "nope"], seats);
    expect(r.next.size).toBe(0);
    expect(r.refused).toBe(3);
  });
  it("a row adds the pickable seats and skips the rest", () => {
    const r = pickSeats(new Set(), ["S|1|1", "S|1|2", "S|1|3", "S|1|4"], seats);
    expect([...r.next].sort()).toEqual(["S|1|1", "S|1|2"]);
    expect(r.refused).toBe(2);
  });
  it("a row whose pickable seats are all in already takes them out", () => {
    const r = pickSeats(new Set(["S|1|1", "S|1|2"]), ["S|1|1", "S|1|2", "S|1|3"], seats);
    expect(r.next.size).toBe(0);
  });
  it("a row with some seats in adds the missing ones", () => {
    const r = pickSeats(new Set(["S|1|1"]), ["S|1|1", "S|1|2"], seats);
    expect([...r.next].sort()).toEqual(["S|1|1", "S|1|2"]);
  });
  it("adds nothing past the room left", () => {
    const r = pickSeats(new Set(["S|2|1"]), ["S|1|1", "S|1|2"], seats, 2);
    expect(r.overLimit).toBe(true);
    expect([...r.next]).toEqual(["S|2|1"]);
    expect(MAX_PLACES).toBe(50);
  });
  it("drops the seats that stopped being free", () => {
    const now = seatIndex([row("S|1|1", "sold"), row("S|1|2", "unavailable")]);
    expect([...dropUnpickable(new Set(["S|1|1", "S|1|2", "gone"]), now)]).toEqual(["S|1|2"]);
  });
});

describe("general admission", () => {
  it("tells a general-admission category from a seated one", () => {
    expect(isGaTier({ ga_unit_count: 10, seat_count: 0 })).toBe(true);
    expect(isGaTier({ ga_unit_count: 10, seat_count: 5 })).toBe(false);
    expect(isGaTier({ ga_unit_count: null, seat_count: 5 })).toBe(false);
  });
  it("counts free and reserved places of one category", () => {
    const rows = [
      row("ga|t3|1", "available", "t3", "ga_unit"),
      row("ga|t3|2", "unavailable", "t3", "ga_unit"),
      row("ga|t3|3", "sold", "t3", "ga_unit"),
      row("S|1|1", "available", "t3"),
      row("ga|t4|1", "available", "t4", "ga_unit"),
    ];
    expect(gaCounts(rows, "t3")).toEqual({ free: 1, reserve: 1 });
  });
  it("sums quantities and drops zeros", () => {
    expect(gaLines({ b: 2, a: 0, c: 3 })).toEqual([
      { tier_id: "b", qty: 2 },
      { tier_id: "c", qty: 3 },
    ]);
    expect(gaTotal({ b: 2, a: 0, c: 3 })).toBe(5);
  });
});

describe("the picked seats", () => {
  it("groups by category and counts the reserve", () => {
    const g = groupSelection(new Set(["S|2|1", "S|1|2", "S|1|1"]), seats);
    expect(g).toHaveLength(2);
    const t1 = g.find((x) => x.tierId === "t1");
    expect(t1?.seats.map((s) => s.key)).toEqual(["S|1|1", "S|1|2"]);
    expect(t1?.fromReserve).toBe(1);
    expect(g.find((x) => x.tierId === "t2")?.fromReserve).toBe(0);
  });
});

describe("the guest form", () => {
  it("accepts a complete form", () => {
    expect(formProblems(form(), 2)).toEqual([]);
  });
  it("needs places, a name and, for a letter to the guest, an e-mail", () => {
    expect(formProblems(form(), 0)).toEqual(["noPlaces"]);
    expect(formProblems(form({ name: "  " }), 1)).toEqual(["name"]);
    expect(formProblems(form({ email: "" }), 1)).toEqual(["emailRequired"]);
    expect(formProblems(form({ email: "", delivery: "pdf_only" }), 1)).toEqual([]);
    expect(formProblems(form({ email: "", delivery: "my_email" }), 1)).toEqual([]);
  });
  it("refuses an e-mail that is not one, whatever the delivery", () => {
    expect(formProblems(form({ email: "ana@", delivery: "pdf_only" }), 1)).toEqual(["email"]);
  });
  it("limits places, the name and the note", () => {
    expect(formProblems(form(), MAX_PLACES + 1)).toEqual(["tooMany"]);
    expect(formProblems(form({ name: "x".repeat(201) }), 1)).toEqual(["nameTooLong"]);
    expect(formProblems(form({ note: "x".repeat(1001) }), 1)).toEqual(["noteTooLong"]);
  });
  it("starts in the interface language when the letter has it, else English", () => {
    expect(emptyForm("ru").locale).toBe("ru");
    expect(emptyForm("xx").locale).toBe("en");
    expect(emptyForm("es").delivery).toBe("guest_email");
  });
});

describe("the request", () => {
  it("builds the body with sorted seats and nulls for empty contact fields", () => {
    const body = buildIssueBody(form({ email: " ", phone: "", note: "", delivery: "pdf_only" }), ["S|2|1", "S|1|1"], [{ tier_id: "t3", qty: 2 }]);
    expect(body).toEqual({
      seats: [{ seat_key: "S|1|1" }, { seat_key: "S|2|1" }],
      ga: [{ tier_id: "t3", qty: 2 }],
      guest: { name: "Ana López", email: null, phone: null, locale: "es" },
      delivery: "pdf_only",
      note: null,
    });
    expect(withRequestId(body, "r1").request_id).toBe("r1");
  });
  it("reuses the id while the body is the same, takes a new one when it changes", () => {
    let n = 0;
    const make = () => `id${++n}`;
    const a = buildIssueBody(form(), ["S|1|1"], []);
    const first = requestIdFor(null, a, make);
    expect(first.id).toBe("id1");
    expect(requestIdFor(first, buildIssueBody(form(), ["S|1|1"], []), make)).toBe(first);
    const changed = requestIdFor(first, buildIssueBody(form(), ["S|1|1", "S|1|2"], []), make);
    expect(changed.id).toBe("id2");
    expect(requestIdFor(changed, buildIssueBody(form({ name: "Other" }), ["S|1|1", "S|1|2"], []), make).id).toBe("id3");
  });
  it("a definite 4xx closes the attempt, a dropped connection or a 5xx keeps it", () => {
    expect(isDefinitiveRefusal(new ApiError(409, "invitation.seats_taken", "x"))).toBe(true);
    expect(isDefinitiveRefusal(new ApiError(422, "x", "x"))).toBe(true);
    expect(isDefinitiveRefusal(new ApiError(0, "network.error", "x"))).toBe(false);
    expect(isDefinitiveRefusal(new ApiError(503, "x", "x"))).toBe(false);
    expect(isDefinitiveRefusal(new Error("x"))).toBe(false);
  });
});

describe("errors", () => {
  it("reads the taken seats from a 409", () => {
    const e = new ApiError(409, "invitation.seats_taken", "x", undefined, { seat_keys: ["S|1|1", 7, "S|1|2"] });
    expect(takenSeatKeys(e)).toEqual(["S|1|1", "S|1|2"]);
    expect(takenSeatKeys(new ApiError(409, "invitation.not_enough_places", "x"))).toEqual([]);
    expect(takenSeatKeys(new Error("x"))).toEqual([]);
  });
  it("knows when the seats changed under the person", () => {
    expect(isSeatConflict(new ApiError(409, "invitation.seats_taken", "x"))).toBe(true);
    expect(isSeatConflict(new ApiError(409, "invitation.not_enough_places", "x"))).toBe(true);
    expect(isSeatConflict(new ApiError(409, "invitation.request_id_reused", "x"))).toBe(false);
    expect(isSeatConflict(new ApiError(422, "invitation.seats_taken", "x"))).toBe(false);
  });
  it("tells a route the server does not have from an answer of ours", () => {
    expect(isMissingRoute(new ApiError(405, "http.405", "x"))).toBe(true);
    expect(isMissingRoute(new ApiError(404, "http.404", "x"))).toBe(true);
    expect(isMissingRoute(new ApiError(404, "invitation.not_found", "x"))).toBe(false);
    expect(isMissingRoute(new ApiError(404, "session.not_found", "x"))).toBe(false);
    expect(isMissingRoute(new ApiError(500, "x", "x"))).toBe(false);
  });
});

const place = (origin: string | null) => ({ tier_id: "t1", tier_name: "T", face_value: 100, origin });
const item = (total: number, active: number, places = 2, created = "2026-10-29T10:00:00Z"): StaffInvitationListItem => ({
  order_id: "o1",
  system_id: 1,
  status: "paid",
  guest_name: "Ana",
  delivery: "pdf_only",
  issued_via: "staff",
  issued_by: { user_id: null, email: "boss@example.com", first_name: "Boss", last_name: null },
  created_at: created,
  tickets_total: total,
  tickets_active: active,
  places: Array.from({ length: places }, () => place("unavailable")),
});

describe("the list", () => {
  it("reads the state from the ticket counts, not the order status", () => {
    expect(invitationState(item(0, 0))).toBe("issuing");
    expect(invitationState(item(1, 1))).toBe("issuing");
    expect(invitationState(item(2, 2))).toBe("active");
    expect(invitationState(item(2, 1))).toBe("partial");
    expect(invitationState(item(2, 0))).toBe("cancelled");
  });
  it("offers actions only while a ticket is left", () => {
    expect(canAct(item(2, 2))).toBe(true);
    expect(canAct(item(2, 1))).toBe(true);
    expect(canAct(item(2, 0))).toBe(false);
    expect(canAct(item(0, 0))).toBe(false);
  });
  it("polls while tickets are minted and gives up on a stuck one", () => {
    const now = Date.parse("2026-10-29T10:00:30Z");
    expect(pollDelay([item(2, 2)], now)).toBe(false);
    expect(pollDelay([item(2, 2), item(0, 0)], now)).toBe(2000);
    expect(pollDelay([item(0, 0)], Date.parse("2026-10-29T10:11:00Z"))).toBe(false);
    expect(pollDelay([], now)).toBe(false);
  });
  it("counts where the places go back to", () => {
    expect(originCounts([place("unavailable"), place("available"), place(null)])).toEqual({ reserve: 1, sale: 2 });
  });
  it("names the issuer", () => {
    expect(issuerName(item(1, 1))).toBe("Boss");
    expect(issuerName({ issued_by: { user_id: null, email: null, first_name: null, last_name: null } })).toBe("");
    expect(issuerName({ issued_by: { user_id: "u", email: "a@b.c", first_name: null, last_name: null } })).toBe("a@b.c");
  });
});

describe("the charge", () => {
  it("is shown only when the server fills it in", () => {
    expect(chargeLine(null)).toBeNull();
    expect(chargeLine({})).toBeNull();
    expect(chargeLine({ amount_minor: 150 })).toBeNull();
    expect(chargeLine({ amount_minor: 150, currency: "EUR" })).toEqual({ amountMinor: 150, currency: "EUR" });
  });
});
