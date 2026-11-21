import { describe, expect, it } from "vitest";
import { TIER_PALETTE, planColorsByTier, tierColors } from "./colors";

const tier = (id: string, order: number, color?: string | null) => ({ id, name: id, sort_order: order, color });

describe("tierColors", () => {
  it("the tier's own colour wins over the plan colour and the palette", () => {
    const m = tierColors([tier("a", 1, "#AA0000")], new Map([["a", "#00aa00"]]));
    expect(m.get("a")).toBe("#aa0000");
  });

  it("without its own colour the tier takes the colour of the plan category it is home to", () => {
    const m = tierColors([tier("a", 1), tier("b", 2, null)], new Map([["a", "#00AA00"]]));
    expect(m.get("a")).toBe("#00aa00");
    expect(m.get("b")).toBe(TIER_PALETTE[1]);
  });

  it("without either it takes the palette by order", () => {
    const m = tierColors([tier("a", 1), tier("b", 2)]);
    expect(m.get("a")).toBe(TIER_PALETTE[0]);
    expect(m.get("b")).toBe(TIER_PALETTE[1]);
  });
});

describe("planColorsByTier", () => {
  it("maps tier ids to plan colours and skips categories without a tier or a valid colour", () => {
    const m = planColorsByTier([
      { tier_id: "a", color: "#112233" },
      { tier_id: "a", color: "#445566" },
      { tier_id: "b", color: "red" },
      { color: "#778899" },
    ]);
    expect([...m.entries()]).toEqual([["a", "#112233"]]);
    expect(planColorsByTier(undefined).size).toBe(0);
  });
});
