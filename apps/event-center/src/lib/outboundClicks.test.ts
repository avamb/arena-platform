import { describe, expect, it } from "vitest";
import { dayClickRows, formatClickShare, sellerClickRows, sourceClickRows } from "@/lib/outboundClicks";

describe("outbound clicks helpers", () => {
  it("sorts sources by clicks and marks the empty one as direct", () => {
    const rows = sourceClickRows([
      { utm_source: "", clicks: 3 },
      { utm_source: "ig", clicks: 5 },
      { utm_source: "  ", clicks: 1 },
    ]);
    expect(rows.map((r) => [r.label, r.clicks, r.direct])).toEqual([
      ["ig", 5, false],
      ["", 3, true],
      ["", 1, true],
    ]);
  });

  it("sorts sellers by clicks, ties by name", () => {
    const rows = sellerClickRows([
      { seller: "b", clicks: 2 },
      { seller: "a", clicks: 2 },
      { seller: "c", clicks: 9 },
    ]);
    expect(rows.map((r) => r.label)).toEqual(["c", "a", "b"]);
  });

  it("sorts days oldest first", () => {
    const rows = dayClickRows([
      { day: "2026-11-02", clicks: 1 },
      { day: "2026-11-01", clicks: 4 },
    ]);
    expect(rows.map((r) => r.label)).toEqual(["2026-11-01", "2026-11-02"]);
  });

  it("formats the share, null stays null", () => {
    expect(formatClickShare(null, "en")).toBeNull();
    expect(formatClickShare(0.125, "en")).toBe("12.5%");
  });
});
