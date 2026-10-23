import { describe, expect, it } from "vitest";
import { doorsOffset, formatPriceMinor, majorOf, parseCount, parsePriceMinor, parseTime, salesEndOffset } from "@/lib/wizard/parse";

// The cases of the bot's TestWizard_Parsers (eventbot/wizard_test.go): a parser changed on one side must change on the other.
describe("the bot's parsers, ported", () => {
  it("reads a time like ParseTime", () => {
    const cases: Record<string, string | null> = { "20:00": "20:00", "9.5": "09:05", "1930": "19:30", "24:00": null, x: null, "": null, "9:5:1": null };
    for (const [input, want] of Object.entries(cases)) expect(parseTime(input), input).toBe(want);
  });

  it("reads a price like ParsePriceMinor", () => {
    const cases: Record<string, number | null> = {
      "199": 19900,
      "99,90": 9990,
      "1 200": 120000,
      "1.200,50": 120050,
      "1,200.5": 120050,
      "0": 0,
      "-5": null,
      "12.345": 1234500,
      abc: null,
      "20 €": 2000,
      "18,9": 1890,
      "": null,
    };
    for (const [input, want] of Object.entries(cases)) expect(parsePriceMinor(input), input).toBe(want);
  });

  it("reads a count like ParseCount", () => {
    const cases: Record<string, number | null> = { "80": 80, "1 200": 1200, "0": null, "-1": null, x: null, "1000001": null, "1.000.000": 1_000_000 };
    for (const [input, want] of Object.entries(cases)) expect(parseCount(input), input).toBe(want);
  });

  it("puts money on the wire in major units without float noise", () => {
    expect(majorOf(1890)).toBe(18.9);
    expect(majorOf(120050)).toBe(1200.5);
    expect(majorOf(0)).toBe(0);
    expect(JSON.stringify(majorOf(1001))).toBe("10.01");
    expect(formatPriceMinor(1890)).toBe("18.90");
    expect(parsePriceMinor(formatPriceMinor(120050))).toBe(120050);
  });
});

describe("sales end and doors", () => {
  it("counts a sales end on the event's day, and after midnight when it is far before the start", () => {
    expect(salesEndOffset("19:00", "19:30")).toBe(30);
    expect(salesEndOffset("19:00", "18:00")).toBe(-60);
    expect(salesEndOffset("22:00", "01:00")).toBe(180);
    expect(salesEndOffset("19:00", "x")).toBeNull();
  });

  it("counts doors before the start, at most twelve hours", () => {
    expect(doorsOffset("19:00", "18:30")).toBe(30);
    expect(doorsOffset("19:00", "19:00")).toBeNull();
    expect(doorsOffset("19:00", "06:00")).toBeNull();
    expect(doorsOffset("00:30", "23:00")).toBe(90);
    expect(doorsOffset("19:00", "19:30")).toBeNull();
  });
});
