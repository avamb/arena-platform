import { describe, expect, it } from "vitest";
import type { SeatLayoutAlgorithm, SeatLayoutProposal } from "@/lib/api/types";
import { handleFit } from "@/lib/seatManager/render";
import {
  buildParams,
  humanizeKey,
  initialValues,
  majorText,
  mappingComplete,
  parseIntList,
  parseMajor,
  parseMoneyList,
  prefillFromTiers,
  proposalToStrokes,
  sliderBounds,
  suggestMapping,
} from "@/lib/seatManager/layout";
import { mergeStrokes, type SeatState } from "@/lib/seatManager/pending";

const alg: SeatLayoutAlgorithm = {
  id: "demo",
  label_key: "layoutgen.algorithm.demo",
  required_annotations: ["focal_point"],
  params: [
    { name: "categories", type: "integer", required: true, min: 1, max: 15, default: 4, unit: "count", label_key: "k" },
    { name: "price_max", type: "integer", required: false, min: 0, max: 100000000, default: null, unit: "minor_currency", label_key: "k" },
    { name: "prices", type: "integer_list", required: false, min: 0, max: 100000000, min_items: 1, max_items: 15, default: null, unit: "minor_currency", label_key: "k" },
    { name: "share_template", type: "enum", required: false, default: "pyramid", options: [{ value: "pyramid", label_key: "k" }], label_key: "k" },
    { name: "round_prices", type: "boolean", required: false, default: true, label_key: "k" },
  ],
};

describe("layout form", () => {
  it("starts from the defaults of the schema and leaves the rest empty", () => {
    expect(initialValues(alg)).toEqual({ categories: 4, share_template: "pyramid", round_prices: true });
  });

  it("draws a short integer as a slider and money as a typed field", () => {
    expect(sliderBounds(alg.params[0]!)).toEqual({ min: 1, max: 15 });
    expect(sliderBounds(alg.params[1]!)).toBeNull();
    expect(sliderBounds(alg.params[3]!)).toBeNull();
  });

  it("builds only what is set, and checks the bounds the schema names", () => {
    expect(buildParams(alg, { categories: 5, share_template: "equal" })).toEqual({
      ok: true,
      params: { categories: 5, share_template: "equal" },
    });
    expect(buildParams(alg, { categories: 16 })).toEqual({ ok: false, param: "categories", reason: "max" });
    expect(buildParams(alg, { categories: 3, prices: [] }).ok).toBe(true);
    const rejected = buildParams(alg, { categories: 3, prices: Array.from({ length: 16 }, () => 100) });
    expect(rejected).toEqual({ ok: false, param: "prices", reason: "items" });
  });

  it("asks for a required parameter that has no default", () => {
    const noDefault: SeatLayoutAlgorithm = { ...alg, params: [{ ...alg.params[0]!, default: null }] };
    expect(buildParams(noDefault, {})).toEqual({ ok: false, param: "categories", reason: "required" });
  });
});

describe("typed numbers", () => {
  it("reads money in major units, with a comma or a dot, zero allowed", () => {
    expect(parseMajor("12", "EUR")).toBe(1200);
    expect(parseMajor("12,5", "EUR")).toBe(1250);
    expect(parseMajor("0", "EUR")).toBe(0);
    expect(parseMajor("12.345", "EUR")).toBeNull();
    expect(parseMajor("1,000", "EUR")).toBeNull(); // a thousands separator is refused, never misread
    expect(parseMajor("abc", "EUR")).toBeNull();
    expect(parseMajor("500", "JPY")).toBe(500);
  });

  it("writes minor units back the way a person types them", () => {
    expect(majorText(1250, "EUR")).toBe("12.5");
    expect(majorText(1200, "EUR")).toBe("12");
    expect(majorText(500, "JPY")).toBe("500");
  });

  it("reads lists", () => {
    expect(parseIntList("40 30,20; 10")).toEqual([40, 30, 20, 10]);
    expect(parseIntList("4 x")).toBeNull();
    expect(parseMoneyList("50 40.5", "EUR")).toEqual([5000, 4050]);
    expect(parseMoneyList("50 ?", "EUR")).toBeNull();
  });

  it("makes a label from a key the catalog does not know", () => {
    expect(humanizeKey("layoutgen.param.price_max")).toBe("Price max");
    expect(humanizeKey("layoutgen.param.price_max.hint")).toBe("Price max");
  });
});

const proposal: SeatLayoutProposal = {
  slots: [
    { key: "s1", price_amount: 5000, share: 0.5 },
    { key: "s2", price_amount: 3000, share: 0.5 },
  ],
  assignments: { a: "s1", b: "s1", c: "s2", d: "s2" },
  frozen_seat_keys: ["e"],
};

describe("proposal to the tray", () => {
  const tiers = [
    { id: "vip", price: 5000, name: "VIP" },
    { id: "std", price: 2000, name: "Standard" },
  ];

  it("maps by equal price first, then the dearest to the dearest, one category once", () => {
    const m = suggestMapping(proposal.slots, tiers);
    expect(m.get("s1")).toBe("vip");
    expect(m.get("s2")).toBe("std");
    const one = suggestMapping(proposal.slots, [tiers[0]!]);
    expect(one.get("s1")).toBe("vip");
    expect(one.get("s2")).toBeNull();
    expect(mappingComplete(proposal.slots, one)).toBe(false);
    expect(mappingComplete(proposal.slots, m)).toBe(true);
  });

  it("turns assigned seats into strokes and never touches frozen ones", () => {
    const base = new Map<string, SeatState>([
      ["a", { tierId: "std", status: "available" }],
      ["b", { tierId: "vip", status: "available" }],
      ["c", { tierId: "std", status: "available" }],
      ["d", { tierId: "std", status: "sold" }],
      ["e", { tierId: "std", status: "sold" }],
    ]);
    const mapping = suggestMapping(proposal.slots, tiers);
    const res = proposalToStrokes(proposal, mapping, base, new Map(), 7);
    // a goes to vip; b already is vip; c already is std; d is sold.
    expect(res.changed).toBe(1);
    expect(res.strokes).toHaveLength(1);
    expect(res.strokes[0]!.id).toBe(7);
    expect(mergeStrokes(res.strokes).get("a")).toEqual({ tierId: "vip" });
    expect(mergeStrokes(res.strokes).has("e")).toBe(false);
  });

  it("leaves the seats of an unmapped slot where they are and counts them", () => {
    const base = new Map<string, SeatState>([
      ["a", { tierId: "std", status: "available" }],
      ["c", { tierId: "vip", status: "available" }],
    ]);
    const res = proposalToStrokes(proposal, new Map([["s1", "vip"], ["s2", null]]), base, new Map(), 1);
    expect(res.changed).toBe(1);
    expect(res.leftOut).toBe(2);
  });
});

describe("row handles", () => {
  it("are as big as a finger when the rows leave room, smaller when they are close, hidden when they cannot fit", () => {
    expect(handleFit(60, 1).radius).toBeCloseTo(22);
    const tight = handleFit(30, 1);
    expect(tight.visible).toBe(true);
    expect(tight.radius).toBeLessThan(22);
    expect(handleFit(20, 1).visible).toBe(false);
    expect(handleFit(Number.POSITIVE_INFINITY, 1).visible).toBe(false);
  });

  it("keep their size on screen at any zoom", () => {
    expect(handleFit(60, 2).radius * 2).toBeCloseTo(22);
  });
});

describe("prefill from the categories", () => {
  it("fills an empty prices list with the category prices, the dearest first", () => {
    const out = prefillFromTiers(alg, initialValues(alg), [1500, 5000, 3000]);
    expect(out.prices).toEqual([5000, 3000, 1500]);
    // One price per category: the slider follows the list.
    expect(out.categories).toBe(3);
  });

  it("leaves a list that is already set, a list that does not fit and other algorithms alone", () => {
    expect(prefillFromTiers(alg, { prices: [100] }, [5000, 3000]).prices).toEqual([100]);
    expect(prefillFromTiers(alg, {}, Array.from({ length: 16 }, () => 100)).prices).toBeUndefined();
    expect(prefillFromTiers(alg, {}, []).prices).toBeUndefined();
    const other: SeatLayoutAlgorithm = { ...alg, params: [alg.params[0]!] };
    expect(prefillFromTiers(other, {}, [5000])).toEqual({});
  });
});
