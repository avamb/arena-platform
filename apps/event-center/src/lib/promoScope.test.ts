import { describe, expect, it } from "vitest";
import type { PromoCodeItem } from "@/lib/api/types";
import { canExtend, copySeed, eventNames, extendTarget, parseCopyFrom, promoZone, warningKey } from "@/lib/promoScope";
import { promoValidText } from "@/lib/promoView";

const NOW = new Date("2026-10-01T12:00:00Z");
const session = (over: Partial<PromoCodeItem["sessions"][number]> = {}) => ({
  session_id: "s1",
  event_id: "e1",
  event_name: "White",
  start_at: "2026-11-05T19:00:00Z",
  sales_end_at: "2026-11-05T18:00:00Z",
  timezone: "Asia/Jerusalem",
  status: "scheduled",
  sales_start_at: null,
  ...over,
});
const code = (over: Partial<PromoCodeItem> = {}): PromoCodeItem =>
  ({
    id: "1",
    code: "RED30",
    discount_type: "percent",
    discount_value: 30,
    currency: null,
    applies_to_session_ids: [],
    sessions: [],
    warnings: [],
    max_uses: null,
    max_uses_per_customer: null,
    valid_from: null,
    valid_until: null,
    ...over,
  }) as unknown as PromoCodeItem;

describe("promo scope", () => {
  it("shows dates in the venue zone when the venues agree, else UTC", () => {
    expect(promoZone(code({ sessions: [session(), session({ session_id: "s2" })] }))).toBe("Asia/Jerusalem");
    expect(promoZone(code({ sessions: [session(), session({ timezone: "Europe/Madrid" })] }))).toBe("UTC");
    expect(promoZone(code())).toBe("UTC");
  });

  it("extends to the latest sales end of a live session only when the code ends earlier", () => {
    const later = session({ session_id: "s2", sales_end_at: "2026-12-01T18:00:00Z", start_at: "2026-12-01T19:00:00Z" });
    const gone = session({ session_id: "s3", status: "cancelled", sales_end_at: "2027-01-01T00:00:00Z", start_at: "2027-01-01T01:00:00Z" });
    const past = session({ session_id: "s4", start_at: "2026-09-01T19:00:00Z", sales_end_at: "2026-09-30T00:00:00Z" });
    const item = code({ sessions: [session(), later, gone, past], valid_until: "2026-11-04T00:00:00Z" });
    expect(extendTarget(item, NOW)).toBe("2026-12-01T18:00:00Z");
    expect(canExtend(item, NOW)).toBe(true);
    expect(canExtend({ ...item, valid_until: "2026-12-01T18:00:00Z" }, NOW)).toBe(false);
    expect(canExtend({ ...item, valid_until: null }, NOW)).toBe(false);
    expect(canExtend(code({ sessions: [gone], valid_until: "2026-11-04T00:00:00Z" }), NOW)).toBe(false);
  });

  it("names every event once", () => {
    expect(eventNames(code({ sessions: [session(), session({ session_id: "s2" }), session({ event_name: "Red" })] }))).toEqual(["White", "Red"]);
  });

  it("maps every API warning code to its own text", () => {
    const keys = [
      "promo.warning.valid_until_before_sales_end",
      "promo.warning.valid_from_after_sales_start",
      "promo.warning.session_cancelled",
      "promo.warning.session_past",
    ].map(warningKey);
    expect(new Set(keys).size).toBe(4);
    expect(warningKey("promo.warning.something_new")).toBe("promo.warn.other");
  });

  it("a copy keeps the discount and limits and never the sessions or the last day", () => {
    const original = code({ discount_type: "fixed_amount", discount_value: 550, currency: "EUR", max_uses: 10, max_uses_per_customer: 1, sessions: [session()], applies_to_session_ids: ["s1"], valid_until: "2026-11-04T00:00:00Z" });
    const seed = copySeed(original, 2);
    expect(seed).toEqual({ type: "fixed_amount", value: "5.50", currency: "EUR", total: "10", per: "1" });
    expect(JSON.stringify(seed)).not.toContain("s1");
    expect(copySeed(code({ discount_value: 15 }), 2)).toMatchObject({ type: "percent", value: "15", currency: "", total: "", per: "" });
  });

  it("reads a copy source only from a well-formed id", () => {
    expect(parseCopyFrom({ copyFrom: "0194b5e0-1111-7222-8333-444455556666" })).toEqual({ copyFrom: "0194b5e0-1111-7222-8333-444455556666" });
    expect(parseCopyFrom({ copyFrom: "nope" })).toEqual({});
    expect(parseCopyFrom({})).toEqual({});
  });

  it("writes an extended last day in the venue zone, a chosen day as its UTC date", () => {
    const t = (k: string, p?: Record<string, unknown>) => `${k}:${String(p?.date ?? "")}`;
    const extended = code({ sessions: [session()], valid_until: "2026-11-05T18:00:00Z" });
    const chosen = code({ sessions: [session()], valid_until: "2026-11-04T23:59:59Z" });
    expect(promoValidText(extended, t, "en")).toContain("20:00");
    expect(promoValidText(chosen, t, "en")).toBe("promo.valid.until:4 Nov 2026");
  });
});
