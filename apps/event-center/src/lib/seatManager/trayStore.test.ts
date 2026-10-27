import { describe, expect, it } from "vitest";
import { EMPTY_TRAY, makeStroke, pushStroke, type SeatState } from "@/lib/seatManager/pending";
import { parseTray, serializeTray } from "@/lib/seatManager/trayStore";

describe("tray storage", () => {
  const base = new Map<string, SeatState>([["s1", { tierId: "a", status: "available" }]]);

  it("round-trips a tray", () => {
    const stroke = makeStroke(4, { kind: "tier", tierId: "b" }, ["s1"], base, new Map()).stroke!;
    const tray = pushStroke(EMPTY_TRAY, stroke);
    const back = parseTray(serializeTray(tray));
    expect(back.strokes).toHaveLength(1);
    expect(back.strokes[0]!.targets.get("s1")).toEqual({ tierId: "b" });
    expect(back.nextId).toBe(5);
  });

  it("treats anything unreadable as an empty tray", () => {
    expect(parseTray(null)).toEqual(EMPTY_TRAY);
    expect(parseTray("not json")).toEqual(EMPTY_TRAY);
    expect(parseTray(JSON.stringify({ strokes: [{ id: 1, brush: { kind: "evil" }, targets: [] }] }))).toEqual(EMPTY_TRAY);
    expect(parseTray(JSON.stringify({ strokes: [{ id: 1, brush: { kind: "close" }, targets: [["s1", { status: "sold" }]] }] }))).toEqual(EMPTY_TRAY);
  });
});
