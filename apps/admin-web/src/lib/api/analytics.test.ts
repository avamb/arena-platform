import { describe, expect, it } from "vitest";
import {
  barWidth,
  buildQuery,
  changeDirection,
  changeLabel,
  formatMadrid,
  formatPct,
  formatSeconds,
  gmvRows,
  isValidRange,
  milestoneRows,
  presetRange,
  propsText,
  timelineLink,
  touchText,
} from "@/lib/api/analytics";

describe("period helpers", () => {
  it("presetRange covers the last N UTC days including today", () => {
    const now = new Date("2026-11-02T23:30:00Z");
    expect(presetRange(7, now)).toEqual({ from: "2026-10-27", to: "2026-11-02" });
    expect(presetRange(1, now)).toEqual({ from: "2026-11-02", to: "2026-11-02" });
  });

  it("isValidRange demands dates, an ordered pair and at most 366 days", () => {
    expect(isValidRange("2026-10-01", "2026-10-31")).toBe(true);
    expect(isValidRange("2026-10-31", "2026-10-01")).toBe(false);
    expect(isValidRange("2026-1-1", "2026-10-01")).toBe(false);
    expect(isValidRange("2026-13-01", "2026-13-02")).toBe(false);
    expect(isValidRange("2024-01-01", "2026-01-01")).toBe(false);
  });

  it("buildQuery leaves the surface out when it is empty", () => {
    expect(buildQuery({ surface: "", from: "2026-10-27", to: "2026-11-02" })).toBe("from=2026-10-27&to=2026-11-02");
    expect(buildQuery({ surface: "bot", from: "2026-10-27", to: "2026-11-02" })).toBe(
      "surface=bot&from=2026-10-27&to=2026-11-02",
    );
  });
});

describe("number formatting", () => {
  it("formats percentages, seconds and changes", () => {
    expect(formatPct(null)).toBe("—");
    expect(formatPct(50)).toBe("50%");
    expect(formatPct(47.5)).toBe("47.5%");
    expect(formatSeconds(null)).toBe("—");
    expect(formatSeconds(45)).toBe("45 s");
    expect(formatSeconds(185)).toBe("3 min 05 s");
    expect(changeLabel(15, 10)).toBe("+50%");
    expect(changeLabel(5, 10)).toBe("-50%");
    expect(changeLabel(10, 10)).toBe("0%");
    expect(changeLabel(3, 0)).toBe("new");
    expect(changeLabel(0, 0)).toBe("—");
    expect(changeDirection(3, 2)).toBe("up");
    expect(changeDirection(1, 2)).toBe("down");
    expect(changeDirection(2, 2)).toBe("flat");
  });

  it("clamps a bar width to 0-100", () => {
    expect(barWidth(null)).toBe(0);
    expect(barWidth(-4)).toBe(0);
    expect(barWidth(250)).toBe(100);
    expect(barWidth(37.5)).toBe(37.5);
  });
});

describe("formatMadrid", () => {
  it("shows winter time as CET and summer time as CEST", () => {
    expect(formatMadrid("2026-11-02T08:30:00Z")).toBe("2026-11-02 09:30 CET");
    expect(formatMadrid("2026-07-02T08:30:00Z")).toBe("2026-07-02 10:30 CEST");
  });

  it("handles missing and unparsable values", () => {
    expect(formatMadrid(null)).toBe("—");
    expect(formatMadrid("")).toBe("—");
    expect(formatMadrid("not a date")).toBe("not a date");
  });
});

describe("timeline helpers", () => {
  it("lists time milestones oldest first and splits off gmv", () => {
    const ms = {
      first_sale: "2026-10-30T10:00:00Z",
      application_started: "2026-10-20T09:00:00Z",
      gmv_30d: { EUR: 12000 },
    };
    expect(milestoneRows(ms).map((m) => m.name)).toEqual(["application_started", "first_sale"]);
    expect(gmvRows(ms)).toEqual([{ currency: "EUR", minor: 12000 }]);
    expect(gmvRows({})).toEqual([]);
  });

  it("renders properties with sorted keys and touches in one line", () => {
    expect(propsText({ b: 2, a: "x" })).toBe("a=x, b=2");
    expect(propsText(null)).toBe("");
    expect(touchText({ source: "newsletter", medium: "email", campaign: "oct", ref_host: "google.com" })).toBe(
      "newsletter / email / oct (google.com)",
    );
    expect(touchText({})).toBe("—");
  });

  it("builds the link a CRM card uses", () => {
    expect(timelineLink("6f1c2c34-9b7d-4a76-8c52-5b5a3c3b2a10")).toBe(
      "/analytics/people/6f1c2c34-9b7d-4a76-8c52-5b5a3c3b2a10",
    );
  });
});
