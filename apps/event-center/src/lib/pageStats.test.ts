import { describe, expect, it } from "vitest";
import type { PageStatsResponse } from "@/lib/api/types";
import { hasPageStats, kindFigure, sourceRows } from "./pageStats";

function stats(over: Partial<PageStatsResponse["totals"]> = {}, sources: PageStatsResponse["sources"] = []): PageStatsResponse {
  return {
    event_id: "e",
    from: "2026-05-01",
    to: "2026-05-30",
    timezone: "Europe/Madrid",
    totals: {
      page_view: { hits: 0, suspect_hits: 0 },
      video_play: { hits: 0, suspect_hits: 0 },
      partner_click: { hits: 0, suspect_hits: 0 },
      ...over,
    },
    days: [],
    sources,
  };
}

describe("kindFigure", () => {
  it("counts people and probable bots together and names the bots", () => {
    expect(kindFigure({ hits: 14, suspect_hits: 2 })).toEqual({ total: 16, suspect: 2 });
  });
});

describe("hasPageStats", () => {
  it("is false when nothing was counted", () => {
    expect(hasPageStats(stats())).toBe(false);
  });
  it("is true for any kind, bots included", () => {
    expect(hasPageStats(stats({ video_play: { hits: 1, suspect_hits: 0 } }))).toBe(true);
    expect(hasPageStats(stats({ partner_click: { hits: 0, suspect_hits: 3 } }))).toBe(true);
  });
});

describe("sourceRows", () => {
  it("puts visits without utm last", () => {
    const rows = sourceRows(
      stats({}, [
        { utm_source: "", utm_campaign: "", hits: 20, suspect_hits: 0 },
        { utm_source: "instagram", utm_campaign: "spring", hits: 8, suspect_hits: 1 },
      ]),
    );
    expect(rows.map((r) => r.source)).toEqual(["instagram", ""]);
    expect(rows[0]?.figure).toEqual({ total: 9, suspect: 1 });
  });
});
