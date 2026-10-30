/**
 * Pure rules of the seat-map invitations screen (spec 46, section 4.1): which
 * seats a finger may pick, what the guest form needs, the body of the issue
 * request, the request id that survives a retry, and the one state the list
 * shows for an invitation.
 *
 * Not to be confused with `invitations.ts`, the older general-admission screen.
 *
 * vitest runs without a DOM, so every rule that a click would exercise lives
 * here and is tested in staffInvitations.test.ts. Nothing here logs: the form
 * holds a guest's contact details.
 */
import { ApiError } from "@/lib/api/http";
import type { Tone } from "@/lib/orders";
import type {
  AdminSeatRow,
  StaffInvitationIssueRequest,
  StaffInvitationListItem,
  StaffInvitationPlace,
  TicketTierItem,
} from "@/lib/api/types";

/** React Query key prefix of the invitations list of a session: [key, orgId, sessionId]. */
export const STAFF_INVITATIONS_KEY = "staff-invitations";
/** The most places of one invitation (the API's own limit, 400 `invitation.too_many`). */
export const MAX_PLACES = 50;
/** The longest guest name the API takes. */
export const MAX_GUEST_NAME = 200;
/** The longest internal note the API takes. */
export const MAX_NOTE = 1000;
/** The languages of the guest's letter (the API falls back to its default for any other). */
export const GUEST_LOCALES = ["cs", "de", "en", "es", "fr", "he", "ru"] as const;
export type GuestLocale = (typeof GUEST_LOCALES)[number];

export type Delivery = "guest_email" | "my_email" | "pdf_only";
export const DELIVERIES: readonly Delivery[] = ["guest_email", "my_email", "pdf_only"];

export type SeatStatus = AdminSeatRow["status"];

/** A seat as the screen knows it: its place on the plan and what the server says about it. */
export interface SeatInfo {
  readonly key: string;
  readonly sector: string;
  readonly row: string;
  readonly seat: string;
  readonly tierId: string | null;
  readonly status: SeatStatus;
}

/** Free seats and seats held back by the organizer can be given away. Held and sold ones never. */
export function isPickable(status: SeatStatus | undefined): boolean {
  return status === "available" || status === "unavailable";
}

/** The seats of the admin inventory by key (physical seats only, general-admission places are counted apart). */
export function seatIndex(rows: readonly AdminSeatRow[]): Map<string, SeatInfo> {
  const out = new Map<string, SeatInfo>();
  for (const r of rows) {
    if (r.kind !== "seat") continue;
    out.set(r.seat_key, {
      key: r.seat_key,
      sector: r.sector_name,
      row: r.row_name,
      seat: r.seat_number,
      tierId: r.tier_id,
      status: r.status,
    });
  }
  return out;
}

export interface PickResult {
  readonly next: Set<string>;
  /** Keys the tap named that cannot be given away (held, sold, unknown). */
  readonly refused: number;
  /** True when the list would pass the limit: nothing was added. */
  readonly overLimit: boolean;
}

/**
 * A tap, a row or a drawn area on the map. One pickable seat toggles. Several:
 * if every one of them is picked already they all go, otherwise they are all
 * added. `room` is how many more places the invitation may take (the limit
 * minus the general-admission places).
 */
export function pickSeats(
  selected: ReadonlySet<string>,
  keys: readonly string[],
  seats: ReadonlyMap<string, SeatInfo>,
  room: number = MAX_PLACES,
): PickResult {
  const pickable = [...new Set(keys)].filter((k) => isPickable(seats.get(k)?.status));
  const refused = new Set(keys).size - pickable.length;
  const next = new Set(selected);
  if (pickable.length === 0) return { next, refused, overLimit: false };
  const allIn = pickable.every((k) => selected.has(k));
  if (allIn) {
    for (const k of pickable) next.delete(k);
    return { next, refused, overLimit: false };
  }
  const fresh = pickable.filter((k) => !selected.has(k));
  if (selected.size + fresh.length > room) return { next: new Set(selected), refused, overLimit: true };
  for (const k of fresh) next.add(k);
  return { next, refused, overLimit: false };
}

/** Drops the keys the server no longer lets us give away (taken meanwhile). Returns the same set when nothing changed. */
export function dropUnpickable(selected: ReadonlySet<string>, seats: ReadonlyMap<string, SeatInfo>): Set<string> {
  const next = new Set<string>();
  for (const k of selected) if (isPickable(seats.get(k)?.status)) next.add(k);
  return next;
}

// ─── General admission ───────────────────────────────────────────────────────

/** A category that sells places without a seat (a hybrid session also has seated ones). */
export function isGaTier(t: Pick<TicketTierItem, "ga_unit_count" | "seat_count" | "kind">): boolean {
  if (t.kind === "ga") return true;
  return (t.ga_unit_count ?? 0) > 0 && (t.seat_count ?? 0) === 0;
}

export interface GaCounts {
  /** Places on sale. */
  readonly free: number;
  /** Places the organizer holds back. */
  readonly reserve: number;
}

/** Free and reserved places of one general-admission category, read from the admin inventory. */
export function gaCounts(rows: readonly AdminSeatRow[], tierId: string): GaCounts {
  let free = 0;
  let reserve = 0;
  for (const r of rows) {
    if (r.kind !== "ga_unit" || r.tier_id !== tierId) continue;
    if (r.status === "available") free++;
    else if (r.status === "unavailable") reserve++;
  }
  return { free, reserve };
}

/** Quantities by category id, zero entries dropped. */
export function gaLines(qty: Readonly<Record<string, number>>): { tier_id: string; qty: number }[] {
  return Object.entries(qty)
    .filter(([, n]) => Number.isInteger(n) && n > 0)
    .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
    .map(([tier_id, n]) => ({ tier_id, qty: n }));
}

export function gaTotal(qty: Readonly<Record<string, number>>): number {
  return gaLines(qty).reduce((n, l) => n + l.qty, 0);
}

// ─── The selection ───────────────────────────────────────────────────────────

export interface SelectedGroup {
  readonly tierId: string | null;
  readonly seats: readonly SeatInfo[];
  /** How many of them come from the organizer's reserve. */
  readonly fromReserve: number;
}

/** The picked seats by category, in plan order inside a group, groups in the order of the picked seats. */
export function groupSelection(selected: ReadonlySet<string>, seats: ReadonlyMap<string, SeatInfo>): SelectedGroup[] {
  const groups = new Map<string | null, SeatInfo[]>();
  for (const k of [...selected].sort()) {
    const s = seats.get(k);
    if (s === undefined) continue;
    const list = groups.get(s.tierId) ?? [];
    list.push(s);
    groups.set(s.tierId, list);
  }
  return [...groups.entries()].map(([tierId, list]) => ({
    tierId,
    seats: list,
    fromReserve: list.filter((s) => s.status === "unavailable").length,
  }));
}

// ─── The guest form ──────────────────────────────────────────────────────────

export interface GuestForm {
  name: string;
  email: string;
  phone: string;
  locale: string;
  note: string;
  delivery: Delivery;
}

export type FormProblem = "noPlaces" | "tooMany" | "name" | "nameTooLong" | "email" | "emailRequired" | "noteTooLong";

const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export function isEmail(value: string): boolean {
  return EMAIL.test(value.trim());
}

const runes = (s: string): number => [...s].length;

/** What stops the send. Empty means the form may go. */
export function formProblems(form: GuestForm, places: number): FormProblem[] {
  const out: FormProblem[] = [];
  if (places === 0) out.push("noPlaces");
  else if (places > MAX_PLACES) out.push("tooMany");
  const name = form.name.trim();
  if (name === "") out.push("name");
  else if (runes(name) > MAX_GUEST_NAME) out.push("nameTooLong");
  const email = form.email.trim();
  if (email !== "" && !isEmail(email)) out.push("email");
  else if (email === "" && form.delivery === "guest_email") out.push("emailRequired");
  if (runes(form.note) > MAX_NOTE) out.push("noteTooLong");
  return out;
}

export function emptyForm(locale: string): GuestForm {
  const l = (GUEST_LOCALES as readonly string[]).includes(locale) ? locale : "en";
  return { name: "", email: "", phone: "", locale: l, note: "", delivery: "guest_email" };
}

/** The body of the issue request, without the request id (that is `withRequestId`). */
export type IssueBody = Omit<StaffInvitationIssueRequest, "request_id">;

export function buildIssueBody(form: GuestForm, seatKeys: Iterable<string>, ga: readonly { tier_id: string; qty: number }[]): IssueBody {
  const seats = [...seatKeys].sort().map((seat_key) => ({ seat_key }));
  const email = form.email.trim();
  const phone = form.phone.trim();
  const note = form.note.trim();
  return {
    seats,
    ga: ga.map((l) => ({ tier_id: l.tier_id, qty: l.qty })),
    guest: {
      name: form.name.trim(),
      email: email === "" ? null : email,
      phone: phone === "" ? null : phone,
      locale: form.locale === "" ? null : form.locale,
    },
    delivery: form.delivery,
    note: note === "" ? null : note,
  };
}

// ─── The request id of one attempt ───────────────────────────────────────────

export interface PendingRequest {
  readonly id: string;
  /** The body this id was first sent with. */
  readonly fingerprint: string;
}

export function newRequestId(): string {
  const c = globalThis.crypto;
  if (c !== undefined && typeof c.randomUUID === "function") return c.randomUUID();
  // An old browser: a version 4 UUID from random numbers.
  return "xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx".replace(/[xy]/g, (ch) => {
    const r = Math.floor(Math.random() * 16);
    return (ch === "x" ? r : (r & 0x3) | 0x8).toString(16);
  });
}

/**
 * The digest of a body the way the SERVER sees it (hinvitations.Fingerprint): the seats and the General Admission
 * lines in a canonical order, the guest's name and phone trimmed, the e-mail trimmed and lower-cased, and the
 * delivery. The note and the letter's language are NOT part of it: the server answers the first invitation for
 * a repeat that differs only there, so the client must call that the same request too.
 */
export function serverFingerprint(body: IssueBody): string {
  const seats = (body.seats ?? []).map((s) => s.seat_key).sort();
  const ga = (body.ga ?? [])
    .map((l) => ({ tier_id: l.tier_id, qty: l.qty }))
    .sort((a, c) => (a.tier_id !== c.tier_id ? (a.tier_id < c.tier_id ? -1 : 1) : a.qty - c.qty));
  return JSON.stringify({
    seats,
    ga,
    name: body.guest.name.trim(),
    email: (body.guest.email ?? "").trim().toLowerCase(),
    phone: (body.guest.phone ?? "").trim(),
    delivery: body.delivery,
  });
}

/**
 * One id per submit attempt, reused while the request is the same: a retry after a dropped connection must reach
 * the server as the SAME request (the API answers the first order again), while a changed selection is a new
 * request. The API refuses one id with two different bodies (409 `invitation.request_id_reused`).
 */
export function requestIdFor(prev: PendingRequest | null, body: IssueBody, make: () => string = newRequestId): PendingRequest {
  const fingerprint = serverFingerprint(body);
  if (prev !== null && prev.fingerprint === fingerprint) return prev;
  return { id: make(), fingerprint };
}

export function withRequestId(body: IssueBody, id: string): StaffInvitationIssueRequest {
  return { request_id: id, ...body };
}

// ─── Errors ──────────────────────────────────────────────────────────────────

function seatKeysOf(err: unknown, codes: readonly string[]): string[] {
  if (!(err instanceof ApiError) || !codes.includes(err.code)) return [];
  const keys = err.details?.["seat_keys"];
  return Array.isArray(keys) ? keys.filter((k): k is string => typeof k === "string") : [];
}

/** The seats someone else took (409 `invitation.seats_taken`), empty for any other error. */
export function takenSeatKeys(err: unknown): string[] {
  return seatKeysOf(err, ["invitation.seats_taken"]);
}

/** Seats the server named as unusable: taken ones, or keys it does not know (400 `invitation.unknown_seat`). */
export function refusedSeatKeys(err: unknown): string[] {
  return seatKeysOf(err, ["invitation.seats_taken", "invitation.unknown_seat"]);
}

/** True when the server answered "the seats changed under you": the map must be read again. */
export function isSeatConflict(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  if (err.status === 400 && err.code === "invitation.unknown_seat") return true;
  return (
    err.status === 409 &&
    (err.code === "invitation.seats_taken" || err.code === "invitation.not_enough_places" || err.code === "invitation.capacity_exceeded")
  );
}

/**
 * The server read the request and refused it (a 4xx): the attempt is over, the next send is a new request with a
 * new id. A dropped connection, a timeout or a 5xx may have been processed, so the retry must reuse the id.
 */
export function isDefinitiveRefusal(err: unknown): boolean {
  return err instanceof ApiError && err.status >= 400 && err.status < 500;
}

/** Codes a router answers for a path it does not know (the API's own 404s carry a `<thing>.not_found` code). */
const GENERIC_NOT_FOUND = new Set(["http.404", "not_found", "route.not_found", "common.not_found"]);

/** True for a function the server does not have yet (INV-03 routes): 405, or a 404 that no handler of ours wrote. */
export function isMissingRoute(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  if (err.status === 405) return true;
  return err.status === 404 && (GENERIC_NOT_FOUND.has(err.code) || !err.code.includes("."));
}

// ─── The list ────────────────────────────────────────────────────────────────

export type InvitationState = "issuing" | "active" | "partial" | "cancelled";

/**
 * One state for the list row. A cancelled invitation keeps the order status
 * `paid` (it stays billed), so the state comes from the ticket counts:
 * tickets are minted a moment after the issue (fewer tickets than places),
 * then `tickets_active` tells whether any is left.
 */
export function invitationState(item: Pick<StaffInvitationListItem, "tickets_total" | "tickets_active" | "places">): InvitationState {
  if (item.tickets_total < item.places.length) return "issuing";
  if (item.tickets_active <= 0) return "cancelled";
  if (item.tickets_active < item.tickets_total) return "partial";
  return "active";
}

/** A cancel or a resend makes sense only for an invitation with a ticket left. */
export function canAct(item: Pick<StaffInvitationListItem, "tickets_total" | "tickets_active" | "places">): boolean {
  const s = invitationState(item);
  return s === "active" || s === "partial";
}

// ─── The letter of an invitation ─────────────────────────────────────────────

export type LetterKind = "none" | "pending" | "sent" | "failed" | "skipped" | "disabled";

export interface LetterView {
  kind: LetterKind;
  tone: Tone;
  /** The time the letter went out ("14:05", with a date when it was not today), only for `sent`. */
  when: string;
  /** Key of the hint under a failed letter, only for `failed`. */
  hint: "seatInv.letter.failedHint" | "seatInv.letter.addressRejected" | null;
}

/**
 * The time a letter went out in the viewer's zone: just the clock when it was
 * today, else the short date and the clock. Empty for a time that does not parse.
 */
export function formatLetterTime(iso: string | null | undefined, tz: string | undefined, locale: string, now: number = Date.now()): string {
  if (iso == null) return "";
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return "";
  const loc = locale === "en" ? "en-GB" : locale;
  const opts: Intl.DateTimeFormatOptions = { timeZone: tz };
  try {
    const day = (d: Date) => new Intl.DateTimeFormat("en-CA", opts).format(d);
    const clock = new Intl.DateTimeFormat(loc, { ...opts, hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).format(at);
    if (day(at) === day(new Date(now))) return clock;
    return `${new Intl.DateTimeFormat(loc, { ...opts, day: "numeric", month: "short" }).format(at)} ${clock}`;
  } catch {
    return "";
  }
}

/**
 * What the list shows about the guest's letter. Nothing (`none`) for an
 * invitation without a letter (pdf_only, tickets not issued yet) and for a
 * fully canceled one, whose letter no longer matters.
 */
export function letterView(
  item: Pick<StaffInvitationListItem, "letter" | "tickets_total" | "tickets_active" | "places">,
  tz: string | undefined,
  locale: string,
  now: number = Date.now(),
): LetterView {
  const none: LetterView = { kind: "none", tone: "neutral", when: "", hint: null };
  const l = item.letter;
  if (l == null || invitationState(item) === "cancelled") return none;
  switch (l.status) {
    case "pending":
      return { kind: "pending", tone: "wait", when: "", hint: null };
    case "sent":
      return { kind: "sent", tone: "ok", when: formatLetterTime(l.sent_at, tz, locale, now), hint: null };
    case "failed":
      return { kind: "failed", tone: "bad", when: "", hint: l.last_error_code === "address_rejected" ? "seatInv.letter.addressRejected" : "seatInv.letter.failedHint" };
    case "skipped":
      return { kind: "skipped", tone: "neutral", when: "", hint: null };
    case "disabled":
      return { kind: "disabled", tone: "neutral", when: "", hint: null };
    default:
      return none;
  }
}

/** How often the list is read while tickets are being minted. */
export const POLL_MS = 2000;
/** The list stops asking this long after the newest unfinished invitation was issued (something is stuck, the person reloads). */
export const POLL_GIVE_UP_MS = 10 * 60_000;

/** The delay until the next read of the list, or false when every invitation is done. */
export function pollDelay(items: readonly StaffInvitationListItem[], now: number = Date.now()): number | false {
  const waiting = items.some((i) => {
    // A letter still on its way: read again so "sending" turns into "sent" without a reload.
    if (i.letter?.status === "pending" && invitationState(i) !== "cancelled") {
      const issued = Date.parse(i.created_at);
      return Number.isNaN(issued) || now - issued < POLL_GIVE_UP_MS;
    }
    if (invitationState(i) !== "issuing") return false;
    const at = Date.parse(i.created_at);
    return Number.isNaN(at) || now - at < POLL_GIVE_UP_MS;
  });
  return waiting ? POLL_MS : false;
}

/** True when the tickets of an invitation are still missing long after the issue: polling has stopped, something is stuck. */
export function issuingGaveUp(item: Pick<StaffInvitationListItem, "tickets_total" | "places" | "created_at">, now: number = Date.now()): boolean {
  if (invitationState({ ...item, tickets_active: 0 }) !== "issuing") return false;
  const at = Date.parse(item.created_at);
  return !Number.isNaN(at) && now - at >= POLL_GIVE_UP_MS;
}

export interface OriginCounts {
  /** Places that go back to the organizer's reserve on cancellation. */
  readonly reserve: number;
  /** Places that go back on sale. */
  readonly sale: number;
}

export function originCounts(places: readonly Pick<StaffInvitationPlace, "origin">[]): OriginCounts {
  let reserve = 0;
  for (const p of places) if (p.origin === "unavailable") reserve++;
  return { reserve, sale: places.length - reserve };
}

/**
 * The places whose ticket can still be used. A place without a ticket yet (tickets are minted a moment after the
 * issue) counts as active, a place whose ticket is cancelled does not.
 */
export function activePlaces<T extends Pick<StaffInvitationPlace, "ticket_status">>(places: readonly T[]): T[] {
  return places.filter((p) => p.ticket_status == null || p.ticket_status === "active");
}

/** True when a per-ticket cancel makes sense for this place: it has a ticket, the ticket is active and unused, and it is not the last one. */
export function canCancelPlace(place: Pick<StaffInvitationPlace, "ticket_id" | "ticket_status"> & { scanned?: boolean }, activeCount: number): boolean {
  // A ticket used at the door is never cancelled: the server refuses it with invitation.ticket_scanned.
  return place.ticket_id != null && place.ticket_status === "active" && place.scanned !== true && activeCount > 1;
}

/** The guest's name for the list, or nothing when the invitation has none. */
export function guestOf(item: Pick<StaffInvitationListItem, "guest_name">): string {
  return (item.guest_name ?? "").trim();
}

/** "Anna Lopez" or an e-mail local part of whoever issued it. */
export function issuerName(item: Pick<StaffInvitationListItem, "issued_by">): string {
  const by = item.issued_by;
  const full = [by.first_name, by.last_name].filter((x): x is string => typeof x === "string" && x.trim() !== "").join(" ");
  if (full !== "") return full;
  return by.email ?? "";
}

/**
 * The billing preview of an issue (`charge`), once the server fills it in
 * (INV-07): rendered only when it names an amount in minor units and a currency.
 */
export function chargeLine(charge: unknown): { amountMinor: number; currency: string } | null {
  if (charge === null || typeof charge !== "object") return null;
  const c = charge as { amount_minor?: unknown; currency?: unknown };
  if (typeof c.amount_minor !== "number" || !Number.isFinite(c.amount_minor)) return null;
  if (typeof c.currency !== "string" || c.currency === "") return null;
  return { amountMinor: c.amount_minor, currency: c.currency };
}
