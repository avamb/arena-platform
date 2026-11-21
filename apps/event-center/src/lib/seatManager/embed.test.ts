import { describe, expect, it } from "vitest";
import { parseSeatsSearch, splitSeatsByPricePath } from "@/lib/seatManager/embed";

describe("the address of the seat manager", () => {
  it("reads ?embed and ?tab, and ignores anything else", () => {
    expect(parseSeatsSearch({})).toEqual({});
    expect(parseSeatsSearch({ embed: "1" })).toEqual({ embed: true });
    expect(parseSeatsSearch({ tab: "layout" })).toEqual({ tab: "layout" });
    expect(parseSeatsSearch({ embed: true, tab: "history" })).toEqual({ embed: true, tab: "history" });
    expect(parseSeatsSearch({ tab: "evil" })).toEqual({});
    expect(parseSeatsSearch({ tab: 5 })).toEqual({});
  });

  it("splitSeatsByPricePath is the seat manager route opened on the layout tab", () => {
    const path = splitSeatsByPricePath("org1", "ev1", "se1");
    expect(path).toBe("/o/org1/events/ev1/sessions/se1/seats?tab=layout");
    const query = Object.fromEntries(new URLSearchParams(path.split("?")[1]));
    expect(parseSeatsSearch(query)).toEqual({ tab: "layout" });
  });
});
