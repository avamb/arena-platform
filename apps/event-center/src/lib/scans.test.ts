import { describe, expect, it } from "vitest";
import { firstAdmittedScan, isAdmitted, scanPlace, scanSourceKey } from "@/lib/scans";
import type { TicketScan } from "@/lib/api/types";

function scan(over: Partial<TicketScan>): TicketScan {
  return {
    scanned_at: "2026-11-01T18:32:00Z",
    result: "admitted",
    source: "macs",
    scanner_label: "",
    gate: "",
    ...over,
  };
}

describe("scans", () => {
  it("names the two sources and nothing else", () => {
    expect(scanSourceKey("macs")).toBe("scan.source.macs");
    expect(scanSourceKey("arena_scanner")).toBe("scan.source.arena_scanner");
    expect(scanSourceKey("something_new")).toBeNull();
  });

  it("prefers the gate, falls back to the scanner's name", () => {
    expect(scanPlace(scan({ gate: " Gate 2 ", scanner_label: "S7" }))).toEqual({ gate: "Gate 2" });
    expect(scanPlace(scan({ gate: "  ", scanner_label: "S7" }))).toEqual({ scanner: "S7" });
    expect(scanPlace(scan({}))).toBeNull();
  });

  it("finds the earliest admission in a newest-first list", () => {
    const list = [
      scan({ scanned_at: "2026-11-01T18:50:00Z", gate: "late" }),
      scan({ scanned_at: "2026-11-01T18:40:00Z", result: "denied", gate: "x" }),
      scan({ scanned_at: "2026-11-01T18:32:00Z", gate: "first" }),
    ];
    expect(firstAdmittedScan(list)?.gate).toBe("first");
    expect(firstAdmittedScan([list[1] as TicketScan])).toBeNull();
    expect(firstAdmittedScan([])).toBeNull();
  });

  it("reads the gate's verdict", () => {
    expect(isAdmitted(scan({}))).toBe(true);
    expect(isAdmitted(scan({ result: "denied" }))).toBe(false);
  });
});
