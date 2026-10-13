import { describe, expect, it } from "vitest";
import { formatDate, formatDateTime, formatMoney, zoneLabel } from "@/lib/format";

describe("formatMoney", () => {
  it("reads minor units by the currency's own digits", () => {
    expect(formatMoney(1890, "EUR", "en")).toBe("€18.90");
    expect(formatMoney(1500, "JPY", "en")).toMatch(/¥1,500$/);
  });
  it("uses the locale's separators", () => {
    expect(formatMoney(123450, "EUR", "es")).toMatch(/1234,50|1\.234,50/);
    expect(formatMoney(123450, "EUR", "ru")).toMatch(/1\s?234,50/);
  });
  it("never throws on an unknown currency", () => {
    expect(formatMoney(100, "??", "en")).toBe("1.00 ??");
  });
});

describe("dates", () => {
  it("shows the venue's time and names its zone", () => {
    // 18:00 UTC is 20:00 in Madrid in summer.
    const text = formatDateTime("2026-07-01T18:00:00Z", "Europe/Madrid", "en");
    expect(text).toContain("20:00");
    expect(text).toContain("(Madrid)");
  });
  it("falls back to UTC and says so", () => {
    expect(formatDateTime("2026-07-01T18:00:00Z", null, "en")).toContain("(UTC)");
    expect(formatDateTime("2026-07-01T18:00:00Z", "Not/A_Zone", "en")).toContain("(UTC)");
  });
  it("returns an empty string for no date", () => {
    expect(formatDateTime(null, "Europe/Madrid", "en")).toBe("");
    expect(formatDate("not a date", null, "en")).toBe("");
  });
  it("labels zones by their city", () => {
    expect(zoneLabel("America/Argentina/Buenos_Aires")).toBe("Buenos Aires");
    expect(zoneLabel("")).toBe("");
  });
});
