import { describe, expect, it } from "vitest";
import type { ReferralCapability, ReferralProgram, ReferralRewardItem } from "@/lib/api/types";
import {
  buildReferralRequest,
  canManageReferral,
  canViewReferral,
  capabilityReasonKey,
  capabilityVerdict,
  cashbackLines,
  cashbackNotice,
  draftFromProgram,
  emptyReferralDraft,
  endsDayOf,
  endsInstantOf,
  groupByCurrency,
  isDirty,
  isStarting,
  parseMaxFriends,
  parseReferralPercent,
  phaseKey,
  programActions,
  programPhase,
  rewardReasonKey,
  rewardStateKey,
  rewardStateTone,
  referralText,
  reversalKey,
  SHOP_TEXT_KEYS,
  warningKey,
} from "@/lib/referral";
import { en } from "@/lib/i18n/en";
import { es } from "@/lib/i18n/es";
import { ru } from "@/lib/i18n/ru";

const NOW = new Date("2026-11-02T10:00:00Z");

const program = (over: Partial<ReferralProgram> = {}): ReferralProgram => ({
  id: "p1",
  event_id: "e1",
  status: "active",
  friend_discount_pct: 10,
  cashback_pct: 20,
  cashback_cap_pct: 100,
  max_friends: null,
  starts_at: null,
  ends_at: null,
  terms_accepted_at: "2026-11-01T10:00:00Z",
  created_at: "2026-11-01T10:00:00Z",
  updated_at: "2026-11-01T10:00:00Z",
  ...over,
});

const filled = { friend: "10", cashback: "20", cap: "100", max: "", endsAt: null };
const opts = { starting: true, accepted: true, original: null, now: NOW };

describe("permissions", () => {
  const holds = (...p: string[]) => (x: string) => p.includes(x);
  it("looking needs promo.read only", () => {
    expect(canViewReferral(holds("promo.read"))).toBe(true);
    expect(canViewReferral(holds("event.read"))).toBe(false);
  });
  it("managing needs promo.update and both refund rights", () => {
    expect(canManageReferral(holds("promo.update", "refund.create", "refund.approve"))).toBe(true);
    expect(canManageReferral(holds("promo.update", "refund.create"))).toBe(false);
    expect(canManageReferral(holds("promo.update", "refund.approve"))).toBe(false);
    expect(canManageReferral(holds("refund.create", "refund.approve"))).toBe(false);
  });
});

describe("percent and friends", () => {
  it("takes whole numbers from 1 to 100, with an optional percent sign", () => {
    expect(parseReferralPercent("15")).toEqual({ ok: true, value: 15 });
    expect(parseReferralPercent(" 15 % ")).toEqual({ ok: true, value: 15 });
    expect(parseReferralPercent("100")).toEqual({ ok: true, value: 100 });
    for (const bad of ["", "0", "101", "1.5", "-3", "abc", "12abc"]) expect(parseReferralPercent(bad).ok, bad).toBe(false);
  });
  it("friends per code: empty is no limit, otherwise 1 to 32767", () => {
    expect(parseMaxFriends("")).toEqual({ ok: true, value: null });
    expect(parseMaxFriends(" 5 ")).toEqual({ ok: true, value: 5 });
    expect(parseMaxFriends("32767")).toEqual({ ok: true, value: 32767 });
    for (const bad of ["0", "32768", "2.5", "x", "-1"]) expect(parseMaxFriends(bad).ok, bad).toBe(false);
  });
});

describe("buildReferralRequest", () => {
  it("builds the body and carries accept_terms only when a programme starts", () => {
    const r = buildReferralRequest(filled, opts);
    expect(r).toEqual({
      ok: true,
      request: { friend_discount_pct: 10, cashback_pct: 20, cashback_cap_pct: 100, max_friends: null, ends_at: null, accept_terms: true },
    });
    const change = buildReferralRequest(filled, { ...opts, starting: false, original: program() });
    expect(change.ok && "accept_terms" in change.request).toBe(false);
  });
  it("says what is wrong in each field at once", () => {
    const r = buildReferralRequest({ friend: "0", cashback: "", cap: "150", max: "0", endsAt: null }, opts);
    expect(r.ok).toBe(false);
    if (!r.ok) {
      expect(Object.keys(r.errors).sort()).toEqual(["cap", "cashback", "friend", "max"]);
      expect(r.errors.friend).toBe("referral.err.percent");
      expect(r.errors.max).toBe("referral.err.max");
    }
  });
  it("refuses a start without the tick", () => {
    const r = buildReferralRequest(filled, { ...opts, accepted: false });
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.terms).toBe("referral.err.terms");
    // A change of a live programme needs no tick.
    expect(buildReferralRequest(filled, { ...opts, starting: false, accepted: false, original: program() }).ok).toBe(true);
  });
  it("refuses an end that has passed, but keeps an unchanged one", () => {
    const past = "2026-10-01T00:00:00Z";
    expect(buildReferralRequest({ ...filled, endsAt: past }, opts).ok).toBe(false);
    expect(buildReferralRequest({ ...filled, endsAt: "2026-12-01T00:00:00Z" }, opts).ok).toBe(true);
    const same = buildReferralRequest({ ...filled, endsAt: past }, { ...opts, starting: false, original: program({ ends_at: past }) });
    expect(same.ok).toBe(true);
  });
});

describe("the form follows the programme", () => {
  it("starts with the cap at 100 and nothing else", () => {
    expect(emptyReferralDraft()).toEqual({ friend: "", cashback: "", cap: "100", max: "", endsAt: null });
    expect(draftFromProgram(null)).toEqual(emptyReferralDraft());
  });
  it("reads a stored programme back and notices a change", () => {
    const p = program({ max_friends: 3, ends_at: "2026-12-01T00:00:00Z" });
    const d = draftFromProgram(p);
    expect(d).toEqual({ friend: "10", cashback: "20", cap: "100", max: "3", endsAt: "2026-12-01T00:00:00Z" });
    expect(isDirty(d, p)).toBe(false);
    expect(isDirty({ ...d, friend: "11" }, p)).toBe(true);
    expect(isDirty({ ...d, endsAt: null }, p)).toBe(true);
    expect(isDirty(emptyReferralDraft(), null)).toBe(false);
  });
  it("the last day ends at the midnight after it in the venue's zone", () => {
    const iso = endsInstantOf("2026-12-31", "Europe/Madrid");
    expect(iso).toBe("2026-12-31T23:00:00.000Z");
    expect(endsDayOf(iso, "Europe/Madrid")).toBe("2026-12-31");
    expect(endsDayOf(null, "Europe/Madrid")).toBeNull();
    // Not a midnight of the zone: no calendar day to show.
    expect(endsDayOf("2026-12-31T10:30:00Z", "Europe/Madrid")).toBeNull();
  });
});

describe("state of the programme", () => {
  it("tells scheduled, expired, paused and ended apart from active", () => {
    expect(programPhase(program(), NOW)).toBe("active");
    expect(programPhase(program({ status: "paused" }), NOW)).toBe("paused");
    expect(programPhase(program({ status: "ended" }), NOW)).toBe("ended");
    expect(programPhase(program({ starts_at: "2026-12-01T00:00:00Z" }), NOW)).toBe("scheduled");
    expect(programPhase(program({ ends_at: "2026-11-01T00:00:00Z" }), NOW)).toBe("expired");
    expect(phaseKey("paused")).toBe("referral.status.paused");
  });
  it("offers the buttons of the state, and none for an ended programme", () => {
    expect(programActions(program())).toEqual(["pause", "end"]);
    expect(programActions(program({ status: "paused" }))).toEqual(["resume", "end"]);
    expect(programActions(program({ status: "ended" }))).toEqual([]);
    expect(programActions(null)).toEqual([]);
  });
  it("starting is when there is no programme or the last one ended", () => {
    expect(isStarting(null)).toBe(true);
    expect(isStarting(program({ status: "ended" }))).toBe(true);
    expect(isStarting(program())).toBe(false);
    expect(isStarting(program({ status: "paused" }))).toBe(false);
  });
});

describe("capability", () => {
  const cap = (supported: boolean, channels: ReferralCapability["channels"]): ReferralCapability => ({ supported, source: "event", channels });
  it("closes the form when no channel can pay and notes when only some cannot", () => {
    const bad = { channel_id: "c1", provider: "allpay", supported: false, reason: "no_partial_refund" };
    const good = { channel_id: "c2", provider: "stripe", supported: true };
    expect(capabilityVerdict(cap(false, [bad]))).toMatchObject({ canStart: false, partial: false, blocked: [bad] });
    expect(capabilityVerdict(cap(true, [bad, good]))).toMatchObject({ canStart: true, partial: true });
    expect(capabilityVerdict(cap(true, [good]))).toMatchObject({ canStart: true, partial: false, blocked: [] });
    // An event that sells nowhere yet is allowed.
    expect(capabilityVerdict(cap(true, []))).toMatchObject({ canStart: true, partial: false });
  });
  it("has a sentence for each reason and a general one for an unknown reason", () => {
    expect(capabilityReasonKey("seller_site")).toBe("referral.capability.reason.seller_site");
    expect(capabilityReasonKey("no_partial_refund")).toBe("referral.capability.reason.no_partial_refund");
    expect(capabilityReasonKey("something_new")).toBe("referral.capability.reason.other");
    expect(capabilityReasonKey(undefined)).toBe("referral.capability.reason.other");
  });
  it("knows its two warnings and leaves the rest to the server's own sentence", () => {
    expect(warningKey({ code: "referral.warning.no_channel", message: "" })).toBe("referral.warning.no_channel");
    expect(warningKey({ code: "referral.warning.future", message: "x" })).toBeNull();
  });
});

describe("rewards", () => {
  it("names every state in words and falls back for a new one", () => {
    for (const s of ["due", "sent", "paid", "capped", "not_possible", "failed", "cancelled"]) {
      expect(rewardStateKey(s)).toBe(`referral.state.${s}`);
    }
    expect(rewardStateKey("brand_new")).toBe("referral.state.other");
    expect(rewardStateTone("paid")).toBe("ok");
    expect(rewardStateTone("failed")).toBe("bad");
    expect(rewardStateTone("capped")).toBe("neutral");
  });
  it("has a sentence for a known reason, a general one for an unknown one, and none for no reason", () => {
    expect(rewardReasonKey("cap_reached")).toBe("referral.reason.cap_reached");
    expect(rewardReasonKey("card_unavailable")).toBe("referral.reason.card_unavailable");
    expect(rewardReasonKey("weird_provider_code")).toBe("referral.reason.other");
    expect(rewardReasonKey(null)).toBeNull();
    expect(rewardReasonKey("")).toBeNull();
  });
  it("marks a friend who took the money back", () => {
    expect(reversalKey("refunded")).toBe("referral.reversal.refunded");
    expect(reversalKey("partially_refunded")).toBe("referral.reversal.partial");
    expect(reversalKey("disputed")).toBe("referral.reversal.disputed");
    expect(reversalKey(null)).toBeNull();
    const r = { friend_reversal: "disputed" } as ReferralRewardItem;
    expect(reversalKey(r.friend_reversal)).not.toBeNull();
  });
});

describe("totals per currency", () => {
  const totals = (currency: string, over: Record<string, number> = {}) => ({
    currency,
    paid_minor: 0,
    sent_minor: 0,
    due_minor: 0,
    not_possible_minor: 0,
    capped_minor: 0,
    failed_minor: 0,
    cancelled_minor: 0,
    ...over,
  });
  it("lists only the states that hold money, in a fixed order", () => {
    expect(cashbackLines(totals("EUR", { due_minor: 300, paid_minor: 1200 }))).toEqual([
      { state: "paid", amount: 1200 },
      { state: "due", amount: 300 },
    ]);
    expect(cashbackLines(totals("EUR"))).toEqual([]);
  });
  it("keeps every currency apart and never adds one to another", () => {
    const groups = groupByCurrency(
      [
        { currency: "EUR", amount_minor: 500 },
        { currency: "CZK", amount_minor: 20000 },
      ],
      [totals("EUR", { paid_minor: 100 }), totals("USD", { due_minor: 7 })],
    );
    expect(groups.map((g) => g.currency)).toEqual(["CZK", "EUR", "USD"]);
    const eur = groups.find((g) => g.currency === "EUR");
    expect(eur).toEqual({ currency: "EUR", discount: 500, cashback: [{ state: "paid", amount: 100 }] });
    expect(groups.find((g) => g.currency === "CZK")).toEqual({ currency: "CZK", discount: 20000, cashback: [] });
    expect(groups.find((g) => g.currency === "USD")).toEqual({ currency: "USD", discount: 0, cashback: [{ state: "due", amount: 7 }] });
  });
  it("merges two rows of the same currency instead of printing it twice", () => {
    const groups = groupByCurrency([], [totals("EUR", { paid_minor: 100 }), totals("EUR", { paid_minor: 50, due_minor: 5 })]);
    expect(groups).toHaveLength(1);
    expect(groups[0].cashback).toEqual([
      { state: "paid", amount: 150 },
      { state: "due", amount: 5 },
    ]);
  });
});

describe("the refund warning", () => {
  it("shows when the API names a code and a currency", () => {
    expect(cashbackNotice({ amount_minor: 500, currency: "EUR", code: "ANNA-7K2Q" })).toEqual({ amount_minor: 500, currency: "EUR", code: "ANNA-7K2Q" });
    expect(cashbackNotice(null)).toBeNull();
    expect(cashbackNotice(undefined)).toBeNull();
    expect(cashbackNotice({ amount_minor: 500, currency: "EUR", code: "" })).toBeNull();
  });
});

describe("the shop's own words", () => {
  const SHOP = { kind: "shop" } as const;
  const EVENT = { kind: "event", eventId: "e1" } as const;

  it("uses the shop variant for the shop and the shared key for an event", () => {
    expect(referralText(SHOP, "referral.intro")).toBe("referral.shop.intro");
    expect(referralText(EVENT, "referral.intro")).toBe("referral.intro");
    // A key without a shop variant is shared.
    expect(referralText(SHOP, "referral.save")).toBe("referral.save");
  });

  it("has a shop text for every listed key in all three languages", () => {
    for (const [name, catalog] of [["en", en], ["ru", ru], ["es", es]] as const) {
      for (const key of SHOP_TEXT_KEYS) {
        const shopKey = `referral.shop.${key.slice("referral.".length)}`;
        expect((catalog as Record<string, string>)[shopKey], `${name} ${shopKey}`).toBeTruthy();
        expect((catalog as Record<string, string>)[shopKey], `${name} ${shopKey}`).not.toBe((catalog as Record<string, string>)[key]);
      }
    }
  });

  it("tells warnings and reasons for the shop in the shop's words", () => {
    expect(warningKey({ code: "referral.warning.no_channel", message: "" }, SHOP)).toBe("referral.shop.warning.no_channel");
    expect(rewardReasonKey("something_new", SHOP)).toBe("referral.shop.reason.other");
    expect(rewardReasonKey("cap_reached", SHOP)).toBe("referral.reason.cap_reached");
    expect(rewardReasonKey("cap_reached", EVENT)).toBe("referral.reason.cap_reached");
  });
});
