/**
 * The pure rules of the promo-code screens (task 4-3). They are the rules of
 * the Telegram bot's promo dialog (eventbot/promo.go, `promoApply` and
 * friends), ported without its Telegram shape: what a typed code, percent,
 * amount or limit means, the derived state of a code, the per-event merge of
 * its sessions and the request a finished draft stands for. Nothing here
 * calls the API or draws anything.
 */
import type { CreatePromoCodeRequest, PromoCodeItem } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n/en";

/** The longest code accepted (spec 35 section 6.2). */
export const MAX_PROMO_CODE_LENGTH = 64;
/** A usage limit above this is a typo. */
export const MAX_PROMO_LIMIT = 1_000_000;
/** A fixed discount above this (10 million major units) is a typo. */
export const MAX_PROMO_AMOUNT_MAJOR = 10_000_000;

export type PromoType = "percent" | "fixed_amount";
export type PromoStatus = "active" | "paused";

// ─── parsing ─────────────────────────────────────────────────────────

export type Parsed<T> = { ok: true; value: T } | { ok: false; error: MessageKey };

/**
 * Upper-cases what the person typed and checks it. Letters of any alphabet,
 * digits and "-", "_", "." are allowed: a buyer types the code by hand into
 * a site, so no spaces and nothing exotic.
 */
export function normalizePromoCode(raw: string): Parsed<string> {
  const s = raw.trim().toUpperCase();
  if (s === "") return { ok: false, error: "promo.err.codeEmpty" };
  if (Array.from(s).length > MAX_PROMO_CODE_LENGTH) return { ok: false, error: "promo.err.codeLong" };
  if (!/^[\p{L}\p{N}\-_.]+$/u.test(s)) return { ok: false, error: "promo.err.codeChars" };
  return { ok: true, value: s };
}

/** True when the name is taken by a code of the organization. Names compare without case. */
export function isNameTaken(code: string, existing: readonly string[]): boolean {
  const up = code.toUpperCase();
  return existing.some((n) => n.toUpperCase() === up);
}

/** "15", "15%" or "15 %": a whole number from 1 to 100. */
export function parsePercent(raw: string): Parsed<number> {
  const s = raw.trim().replace(/\s*%$/, "");
  if (!/^\d{1,12}$/.test(s)) return { ok: false, error: "promo.err.percent" };
  const n = Number(s);
  if (n < 1 || n > 100) return { ok: false, error: "promo.err.percent" };
  return { ok: true, value: n };
}

/** How many decimals the currency has (2 for EUR, 0 for JPY). */
export function currencyDigits(currency: string): number {
  try {
    return new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
  } catch {
    return 2;
  }
}

/**
 * An amount in major units ("5", "5.5", "5,50") as MINOR units of the
 * currency. One separator only, so a thousands separator ("1,000") is
 * refused instead of being misread. At most the currency's own decimals.
 */
export function parseAmountMinor(raw: string, currency: string): Parsed<number> {
  const bad = { ok: false, error: "promo.err.amount" } as const;
  const digits = currencyDigits(currency);
  const s = raw.replace(/[\s  ]/g, "").replace(",", ".");
  if (s === "" || (s.match(/\./g)?.length ?? 0) > 1 || s.includes(",")) return bad;
  const [whole, frac = ""] = s.split(".");
  if (whole === "" && frac === "") return bad;
  if (!/^\d*$/.test(whole) || !/^\d*$/.test(frac) || whole.length > 12 || frac.length > digits) return bad;
  const w = whole === "" ? 0 : Number(whole);
  if (w > MAX_PROMO_AMOUNT_MAJOR) return bad;
  const minor = w * 10 ** digits + (frac === "" ? 0 : Number(frac.padEnd(digits, "0")));
  if (minor < 1 || minor > MAX_PROMO_AMOUNT_MAJOR * 10 ** digits) return bad;
  return { ok: true, value: minor };
}

/** A usage limit: a whole number from 1 to MAX_PROMO_LIMIT. */
export function parseLimit(raw: string): Parsed<number> {
  const s = raw.replace(/[\s ]/g, "");
  if (!/^\d{1,12}$/.test(s)) return { ok: false, error: "promo.err.limit" };
  const n = Number(s);
  if (n < 1 || n > MAX_PROMO_LIMIT) return { ok: false, error: "promo.err.limit" };
  return { ok: true, value: n };
}

/** A three-letter currency code in any case. */
export function parseCurrency(raw: string): Parsed<string> {
  const s = raw.trim().toUpperCase();
  if (!/^[A-Z]{3}$/.test(s)) return { ok: false, error: "promo.err.currency" };
  return { ok: true, value: s };
}

/** The moment a chosen last day ends: 23:59:59 UTC of that day (the bot's rule). */
export function untilInstant(isoDay: string): string | null {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(isoDay)) return null;
  const d = new Date(`${isoDay}T23:59:59Z`);
  if (Number.isNaN(d.getTime()) || d.toISOString().slice(0, 10) !== isoDay) return null;
  return `${isoDay}T23:59:59Z`;
}

// ─── the state of a code ─────────────────────────────────────────────

export const PROMO_STATES = ["active", "paused", "expired", "exhausted", "scheduled"] as const;
export type PromoState = (typeof PROMO_STATES)[number];

/**
 * What a code is right now, as a buyer would meet it: paused by the
 * organizer, past its date, not yet valid, used up, or working. The API
 * keeps only active and paused; the rest is derived.
 */
export function promoState(item: PromoCodeItem, now: Date = new Date()): PromoState {
  if (item.status === "paused") return "paused";
  if (item.valid_until !== null && now.getTime() > Date.parse(item.valid_until)) return "expired";
  if (item.valid_from !== null && now.getTime() < Date.parse(item.valid_from)) return "scheduled";
  if (item.max_uses !== null && item.uses >= item.max_uses) return "exhausted";
  return "active";
}

/** A club code has no session list: it works on every session, future ones included. */
export function isClubCode(item: Pick<PromoCodeItem, "applies_to_session_ids">): boolean {
  return item.applies_to_session_ids.length === 0;
}

/**
 * The currency a code's money is in: the fixed discount's own, else the one
 * the usage report found. "" when it has none yet, or when its orders are in
 * several currencies (a sum of mixed currencies is no amount).
 */
export function promoCurrencyOf(item: PromoCodeItem): string {
  if (item.currency !== null && item.currency !== "") return item.currency;
  return item.discount_currency ?? "";
}

// ─── sessions ────────────────────────────────────────────────────────

/**
 * The new session list when the sessions of ONE event are re-chosen: what the
 * code already had on other events stays, the event's own are replaced by the
 * checked ones. The caller refuses an empty result: an empty list would
 * silently turn the code into a club code.
 */
export function mergeSessions(
  existing: readonly string[],
  eventSessionIds: ReadonlySet<string>,
  checked: readonly string[],
): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const id of existing) {
    if (!eventSessionIds.has(id) && !seen.has(id)) {
      seen.add(id);
      out.push(id);
    }
  }
  for (const id of checked) {
    if (!seen.has(id)) {
      seen.add(id);
      out.push(id);
    }
  }
  return out;
}

/** True when a session sold in `sessionCurrency` can take a fixed code in `fixedCurrency`. */
export function sessionTakesCode(fixedCurrency: string, sessionCurrency: string): boolean {
  return fixedCurrency === "" || sessionCurrency === "" || fixedCurrency.toUpperCase() === sessionCurrency.toUpperCase();
}

// ─── the draft of a new code ─────────────────────────────────────────

export interface PromoDraft {
  code: string;
  type: PromoType;
  /** Percent (1-100) or minor units of `currency`. */
  value: number;
  /** Required for a fixed discount. */
  currency: string;
  /** Every session, future ones included (a club code). */
  all: boolean;
  sessionIds: readonly string[];
  maxUses: number | null;
  perBuyer: number | null;
  /** ISO day, the last day the code works. */
  until: string | null;
  status: PromoStatus;
}

export function emptyDraft(): PromoDraft {
  return {
    code: "",
    type: "percent",
    value: 0,
    currency: "",
    all: true,
    sessionIds: [],
    maxUses: null,
    perBuyer: null,
    until: null,
    status: "active",
  };
}

/** The reason a draft cannot be created yet, or null. */
export function draftProblem(d: PromoDraft): MessageKey | null {
  if (d.code === "") return "promo.err.codeEmpty";
  if (d.value <= 0) return d.type === "percent" ? "promo.err.percent" : "promo.err.amount";
  if (d.type === "fixed_amount" && d.currency === "") return "promo.err.currency";
  if (!d.all && d.sessionIds.length === 0) return "promo.err.pickNone"; // an empty list would be a club code
  if (d.maxUses !== null && d.perBuyer !== null && d.perBuyer > d.maxUses) return "promo.err.perGtTotal";
  if (d.until !== null && untilInstant(d.until) === null) return "promo.err.limit";
  return null;
}

/** The request a finished draft stands for. */
export function buildCreateRequest(d: PromoDraft): CreatePromoCodeRequest | null {
  if (draftProblem(d) !== null) return null;
  const body: CreatePromoCodeRequest = {
    code: d.code,
    discount_type: d.type,
    discount_value: d.value,
    // An empty list is "every session, including future ones". The body names the array.
    applies_to_session_ids: d.all ? [] : [...d.sessionIds],
    max_uses: d.maxUses,
    max_uses_per_customer: d.perBuyer,
    status: d.status,
  };
  if (d.type === "fixed_amount") body.currency = d.currency;
  if (d.until !== null) body.valid_until = untilInstant(d.until);
  return body;
}

// ─── the form ────────────────────────────────────────────────────────

/** What the person typed, as text. */
export interface PromoFormRaw {
  code: string;
  type: PromoType;
  value: string;
  currency: string;
  all: boolean;
  total: string;
  per: string;
  until: string | null;
  status: PromoStatus;
}

export type PromoFieldErrors = Partial<Record<"code" | "value" | "currency" | "scope" | "total" | "per", MessageKey>>;

/** Reads every field. The first problem of each field is kept, so the person fixes them in one go. */
export function readPromoForm(raw: PromoFormRaw, sessionIds: readonly string[], existingNames: readonly string[]): { draft: PromoDraft | null; errors: PromoFieldErrors } {
  const errors: PromoFieldErrors = {};
  const d = emptyDraft();
  const code = normalizePromoCode(raw.code);
  if (!code.ok) errors.code = code.error;
  else if (isNameTaken(code.value, existingNames)) errors.code = "promo.err.codeDup";
  else d.code = code.value;

  d.type = raw.type;
  if (raw.type === "percent") {
    const p = parsePercent(raw.value);
    if (!p.ok) errors.value = p.error;
    else d.value = p.value;
  } else {
    const c = parseCurrency(raw.currency);
    if (!c.ok) errors.currency = c.error;
    else {
      d.currency = c.value;
      const a = parseAmountMinor(raw.value, c.value);
      if (!a.ok) errors.value = a.error;
      else d.value = a.value;
    }
    if (errors.currency !== undefined && raw.value.trim() === "") errors.value = "promo.err.amount";
  }

  d.all = raw.all;
  d.sessionIds = raw.all ? [] : sessionIds;
  if (!raw.all && sessionIds.length === 0) errors.scope = "promo.err.pickNone";

  if (raw.total.trim() !== "") {
    const n = parseLimit(raw.total);
    if (!n.ok) errors.total = n.error;
    else d.maxUses = n.value;
  }
  if (raw.per.trim() !== "") {
    const n = parseLimit(raw.per);
    if (!n.ok) errors.per = n.error;
    else d.perBuyer = n.value;
  }
  if (d.maxUses !== null && d.perBuyer !== null && d.perBuyer > d.maxUses) errors.per = "promo.err.perGtTotal";
  d.until = raw.until;
  d.status = raw.status;
  return { draft: Object.keys(errors).length === 0 && draftProblem(d) === null ? d : null, errors };
}


// ─── search parameters of the screens ────────────────────────────────

export interface PromoListSearch {
  /** The code that was just deleted, for the notice. */
  deleted?: string;
}

export function parsePromoListSearch(raw: Record<string, unknown>): PromoListSearch {
  const out: PromoListSearch = {};
  if (typeof raw.deleted === "string" && raw.deleted.length > 0 && raw.deleted.length <= MAX_PROMO_CODE_LENGTH) out.deleted = raw.deleted;
  return out;
}

export interface PromoCardSearch {
  /** The code was created a moment ago. */
  created?: boolean;
}

export function parsePromoCardSearch(raw: Record<string, unknown>): PromoCardSearch {
  return raw.created === true || raw.created === "true" || raw.created === 1 ? { created: true } : {};
}
