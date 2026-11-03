/**
 * The pure rules of the "Refer a friend" screens (spec 52, section 5.2). Nothing
 * here calls the API or draws anything, and the clock is always a parameter.
 *
 * What the organizer sets: the discount the friend gets, the cashback the
 * referrer gets back on their card (a percent of what the FRIEND paid), the cap
 * of that cashback (a percent of what the REFERRER paid) and, optionally, how many
 * friends one code serves and when the programme ends. Percentages are whole
 * numbers of percent, 1 to 100. The terms are accepted once, when a programme
 * starts: the cashback is paid at once and never taken back.
 */
import type {
  OrderReferralCashbackPaid,
  ReferralCapability,
  ReferralCapabilityChannel,
  ReferralCashbackTotals,
  ReferralMoney,
  ReferralProgram,
  ReferralProgramRequest,
  ReferralRewardItem,
  ReferralWarning,
} from "@/lib/api/types";
import type { ReferralTarget } from "@/lib/api/endpoints";
import { windowEdgeInstant, edgeView } from "@/lib/categories";
import type { MessageKey } from "@/lib/i18n/en";
import type { Parsed } from "@/lib/promo";

// ─── the target: an event or the shop ───────────────────────────────────────

export type { ReferralTarget };

/**
 * The words that differ when the programme belongs to the shop: the shop sells goods, not tickets, so the
 * intro, the "none yet" line and the reasons that name an event or a ticket are told for the shop. Every
 * other key is shared. A shop key is `referral.shop.<rest>` of the shared key `referral.<rest>`.
 */
export const SHOP_TEXT_KEYS = [
  "referral.intro",
  "referral.none",
  "referral.blocked.title",
  "referral.blocked.next",
  "referral.warning.no_channel",
  "referral.warning.some_channels_unsupported",
  "referral.reason.other",
  "referral.reason.no_refundable_ticket",
  "referral.reason.shop_not_supported",
] as const;

/** The default target of the helpers: an event (its words are the shared ones). */
const EVENT_TARGET: ReferralTarget = { kind: "event", eventId: "" };

/** The key to print for a shared key: the shop's own variant for the shop, the shared one for an event. */
export function referralText(target: ReferralTarget, key: MessageKey): MessageKey {
  if (target.kind !== "shop" || !(SHOP_TEXT_KEYS as readonly string[]).includes(key)) return key;
  return `referral.shop.${key.slice("referral.".length)}` as MessageKey;
}

/** The most friends one code may serve (the API's `max_friends` ceiling). */
export const MAX_REFERRAL_FRIENDS = 32767;

// ─── permissions ─────────────────────────────────────────────────────────────

type Can = (permission: string) => boolean;

/** The route's own gate for looking: `promo.read`. */
export function canViewReferral(can: Can): boolean {
  return can("promo.read");
}

/**
 * Starting, changing, pausing and ending a programme need `promo.update` AND the two
 * refund rights: the organizer allows the cashback refunds in their own name. The owner
 * and the manager hold all three, an agent does not.
 */
export function canManageReferral(can: Can): boolean {
  return can("promo.update") && can("refund.create") && can("refund.approve");
}

// ─── the form ────────────────────────────────────────────────────────────────

export type ReferralField = "friend" | "cashback" | "cap" | "max" | "ends";

/** What is typed in the form. Numbers stay text until Save, so a typo is judged and not cut. */
export interface ReferralDraft {
  friend: string;
  cashback: string;
  cap: string;
  max: string;
  /** The end as an instant (RFC 3339), or null for "until the end of sales". */
  endsAt: string | null;
}

export function emptyReferralDraft(): ReferralDraft {
  return { friend: "", cashback: "", cap: "100", max: "", endsAt: null };
}

/** The form for an existing programme (an ended one is the base of the next one). */
export function draftFromProgram(p: ReferralProgram | null): ReferralDraft {
  if (p === null) return emptyReferralDraft();
  return {
    friend: String(p.friend_discount_pct),
    cashback: String(p.cashback_pct),
    cap: String(p.cashback_cap_pct),
    max: p.max_friends === null || p.max_friends === undefined ? "" : String(p.max_friends),
    endsAt: p.ends_at ?? null,
  };
}

/** True when the form differs from the stored programme. */
export function isDirty(draft: ReferralDraft, p: ReferralProgram | null): boolean {
  const base = draftFromProgram(p);
  return (
    draft.friend.trim() !== base.friend ||
    draft.cashback.trim() !== base.cashback ||
    draft.cap.trim() !== base.cap ||
    draft.max.trim() !== base.max ||
    (draft.endsAt ?? null) !== (base.endsAt ?? null)
  );
}

/** "20", "20%" or "20 %": a whole number from 1 to 100. */
export function parseReferralPercent(raw: string): Parsed<number> {
  const s = raw.trim().replace(/\s*%$/, "");
  if (!/^\d{1,12}$/.test(s)) return { ok: false, error: "referral.err.percent" };
  const n = Number(s);
  if (n < 1 || n > 100) return { ok: false, error: "referral.err.percent" };
  return { ok: true, value: n };
}

/** Empty = no limit (null). Otherwise a whole number from 1 to 32767. */
export function parseMaxFriends(raw: string): Parsed<number | null> {
  const s = raw.trim();
  if (s === "") return { ok: true, value: null };
  if (!/^\d{1,12}$/.test(s)) return { ok: false, error: "referral.err.max" };
  const n = Number(s);
  if (n < 1 || n > MAX_REFERRAL_FRIENDS) return { ok: false, error: "referral.err.max" };
  return { ok: true, value: n };
}

/** The last day as the instant the programme ends: the midnight AFTER it in the zone (the last day counts). */
export function endsInstantOf(day: string, timezone: string | null | undefined): string {
  return windowEdgeInstant(day, timezone, true);
}

/** The calendar day to show for a stored end, or null when it is not a midnight of the zone (the screen then prints the instant). */
export function endsDayOf(endsAt: string | null, timezone: string | null | undefined): string | null {
  if (endsAt === null) return null;
  return edgeView(endsAt, timezone, true).day;
}

export interface BuildOptions {
  /** True when this call starts a programme: the terms must be accepted. */
  starting: boolean;
  accepted: boolean;
  /** The stored programme, so an unchanged end is not judged "in the past". */
  original: ReferralProgram | null;
  now: Date;
}

export type BuildResult =
  | { ok: true; request: ReferralProgramRequest }
  | { ok: false; errors: Partial<Record<ReferralField, MessageKey>>; terms?: MessageKey };

/** Judges the whole form at once and, when it holds, builds the PUT body. */
export function buildReferralRequest(draft: ReferralDraft, o: BuildOptions): BuildResult {
  const errors: Partial<Record<ReferralField, MessageKey>> = {};
  const friend = parseReferralPercent(draft.friend);
  const cashback = parseReferralPercent(draft.cashback);
  const cap = parseReferralPercent(draft.cap);
  const max = parseMaxFriends(draft.max);
  if (!friend.ok) errors.friend = friend.error;
  if (!cashback.ok) errors.cashback = cashback.error;
  if (!cap.ok) errors.cap = cap.error;
  if (!max.ok) errors.max = max.error;
  if (draft.endsAt !== null) {
    const t = Date.parse(draft.endsAt);
    const unchanged = o.original !== null && o.original.ends_at === draft.endsAt;
    if (Number.isNaN(t) || (!unchanged && t <= o.now.getTime())) errors.ends = "referral.err.ends";
  }
  const terms = o.starting && !o.accepted ? ("referral.err.terms" as MessageKey) : undefined;
  if (!friend.ok || !cashback.ok || !cap.ok || !max.ok || errors.ends !== undefined || terms !== undefined) {
    return { ok: false, errors, terms };
  }
  const request: ReferralProgramRequest = {
    friend_discount_pct: friend.value,
    cashback_pct: cashback.value,
    cashback_cap_pct: cap.value,
    max_friends: max.value,
    ends_at: draft.endsAt,
  };
  if (o.starting) request.accept_terms = true;
  return { ok: true, request };
}

// ─── the programme's state ───────────────────────────────────────────────────

export type ReferralPhase = "active" | "paused" | "ended" | "scheduled" | "expired";

/** The state in words of the screen. Active before its start or after its end is told apart. */
export function programPhase(p: ReferralProgram, now: Date): ReferralPhase {
  if (p.status === "ended") return "ended";
  if (p.status === "paused") return "paused";
  if (p.starts_at !== null && p.starts_at !== undefined && Date.parse(p.starts_at) > now.getTime()) return "scheduled";
  if (p.ends_at !== null && p.ends_at !== undefined && Date.parse(p.ends_at) <= now.getTime()) return "expired";
  return "active";
}

export function phaseKey(phase: ReferralPhase): MessageKey {
  return `referral.status.${phase}` as MessageKey;
}

/** Which buttons a programme offers. Ended is final, so only "start a new one" (the form) remains. */
export function programActions(p: ReferralProgram | null): ("pause" | "resume" | "end")[] {
  if (p === null || p.status === "ended") return [];
  return p.status === "paused" ? ["resume", "end"] : ["pause", "end"];
}

/** A new programme (and the terms) is needed when there is none or the last one ended. */
export function isStarting(p: ReferralProgram | null): boolean {
  return p === null || p.status === "ended";
}

// ─── capability ──────────────────────────────────────────────────────────────

export interface CapabilityVerdict {
  /** False: no channel can pay the cashback, so the form is closed. */
  canStart: boolean;
  /** Some channels cannot pay it (the others can). */
  partial: boolean;
  /** The channels that cannot, with their reason. */
  blocked: ReferralCapabilityChannel[];
}

export function capabilityVerdict(c: ReferralCapability): CapabilityVerdict {
  const blocked = c.channels.filter((ch) => !ch.supported);
  return { canStart: c.supported, partial: c.supported && blocked.length > 0, blocked };
}

const CAPABILITY_REASONS = ["seller_site", "provider_unsupported", "no_partial_refund"] as const;

/** The sentence for a channel's reason. An unknown reason falls back to a general one. */
export function capabilityReasonKey(reason: string | undefined): MessageKey {
  return (CAPABILITY_REASONS as readonly string[]).includes(reason ?? "")
    ? (`referral.capability.reason.${reason}` as MessageKey)
    : "referral.capability.reason.other";
}

const WARNING_CODES = ["referral.warning.no_channel", "referral.warning.some_channels_unsupported"] as const;

/** The key of a known warning, else null (the screen then prints the server's own sentence). */
export function warningKey(w: ReferralWarning, target: ReferralTarget = EVENT_TARGET): MessageKey | null {
  return (WARNING_CODES as readonly string[]).includes(w.code) ? referralText(target, w.code as MessageKey) : null;
}

// ─── rewards ─────────────────────────────────────────────────────────────────

export const REWARD_STATES = ["due", "sent", "paid", "capped", "not_possible", "failed", "cancelled"] as const;
export type RewardState = (typeof REWARD_STATES)[number];

export function isRewardState(v: string): v is RewardState {
  return (REWARD_STATES as readonly string[]).includes(v);
}

export function rewardStateKey(state: string): MessageKey {
  return isRewardState(state) ? (`referral.state.${state}` as MessageKey) : "referral.state.other";
}

/** Tone of the state badge: money on the card is good, money held back is a warning, the rest is neutral. */
export function rewardStateTone(state: string): "ok" | "wait" | "bad" | "neutral" {
  switch (state) {
    case "paid":
      return "ok";
    case "due":
    case "sent":
      return "wait";
    case "failed":
    case "not_possible":
      return "bad";
    default:
      return "neutral";
  }
}

const REWARD_REASONS = [
  "self_referral",
  "currency_mismatch",
  "cap_reached",
  "cap_reduced",
  "zero_amount",
  "card_closed",
  "card_unavailable",
  "friend_refunded",
  "friend_disputed",
  "friend_not_paid",
  "shop_not_supported",
  "no_refundable_ticket",
  "too_many_attempts",
] as const;

/** The sentence for a reward's reason, or null when it has none. An unknown reason falls back to a general sentence. */
export function rewardReasonKey(reason: string | null | undefined, target: ReferralTarget = EVENT_TARGET): MessageKey | null {
  if (reason === null || reason === undefined || reason === "") return null;
  return referralText(
    target,
    (REWARD_REASONS as readonly string[]).includes(reason) ? (`referral.reason.${reason}` as MessageKey) : "referral.reason.other",
  );
}

/** The mark for a friend who got their money back after the cashback left, or null. */
export function reversalKey(reversal: string | null | undefined): MessageKey | null {
  switch (reversal) {
    case "refunded":
      return "referral.reversal.refunded";
    case "partially_refunded":
      return "referral.reversal.partial";
    case "disputed":
      return "referral.reversal.disputed";
    default:
      return null;
  }
}

/** True when the reward carries any "the friend took the money back" mark. */
export function hasReversal(r: ReferralRewardItem): boolean {
  return reversalKey(r.friend_reversal) !== null;
}

// ─── totals ──────────────────────────────────────────────────────────────────

export interface CashbackLine {
  state: "paid" | "sent" | "due" | "not_possible" | "capped" | "failed" | "cancelled";
  amount: number;
}

const TOTAL_FIELDS: readonly [CashbackLine["state"], keyof ReferralCashbackTotals][] = [
  ["paid", "paid_minor"],
  ["sent", "sent_minor"],
  ["due", "due_minor"],
  ["not_possible", "not_possible_minor"],
  ["capped", "capped_minor"],
  ["failed", "failed_minor"],
  ["cancelled", "cancelled_minor"],
];

/** The non-zero states of one currency's cashback, in the order the screen prints them. */
export function cashbackLines(t: ReferralCashbackTotals): CashbackLine[] {
  const out: CashbackLine[] = [];
  for (const [state, field] of TOTAL_FIELDS) {
    const amount = t[field];
    if (typeof amount === "number" && amount !== 0) out.push({ state, amount });
  }
  return out;
}

export interface CurrencyGroup {
  currency: string;
  discount: number;
  cashback: CashbackLine[];
}

/**
 * The discount and the cashback of each currency side by side. A currency is one group and
 * amounts of different currencies are never added: the API already answers per currency, this
 * only puts the discount and the cashback of the same currency together, in a stable order.
 */
export function groupByCurrency(discount: readonly ReferralMoney[], cashback: readonly ReferralCashbackTotals[]): CurrencyGroup[] {
  const map = new Map<string, CurrencyGroup>();
  const slot = (currency: string): CurrencyGroup => {
    let g = map.get(currency);
    if (g === undefined) {
      g = { currency, discount: 0, cashback: [] };
      map.set(currency, g);
    }
    return g;
  };
  for (const d of discount) slot(d.currency).discount += d.amount_minor;
  for (const c of cashback) {
    const g = slot(c.currency);
    const merged = new Map<CashbackLine["state"], number>(g.cashback.map((l) => [l.state, l.amount]));
    for (const l of cashbackLines(c)) merged.set(l.state, (merged.get(l.state) ?? 0) + l.amount);
    g.cashback = TOTAL_FIELDS.filter(([s]) => merged.has(s)).map(([s]) => ({ state: s, amount: merged.get(s) as number }));
  }
  return [...map.values()].sort((a, b) => a.currency.localeCompare(b.currency));
}

// ─── the refund warning ──────────────────────────────────────────────────────

/**
 * How the order card words one friend's cashback: `paid` (the money went back,
 * with its date), `pending` (owed or with the payment provider) or `unpaid`
 * (capped, not possible, failed, cancelled, or a state this page does not know).
 */
export function cashbackRowKind(state: string): "paid" | "pending" | "unpaid" {
  if (state === "paid") return "paid";
  if (state === "due" || state === "sent") return "pending";
  return "unpaid";
}

/** The cashback already given to the referrer for this order, when the API says so and it is a real amount. */
export function cashbackNotice(c: OrderReferralCashbackPaid | null | undefined): OrderReferralCashbackPaid | null {
  if (c === null || c === undefined) return null;
  return c.code !== "" && c.currency !== "" ? c : null;
}
