import { describe, expect, it } from "vitest";
import {
  EMPTY_TRAY,
  batchKind,
  countByTier,
  effectiveState,
  makeStroke,
  mergeStrokes,
  pushStroke,
  tierDeltas,
  trayChanges,
  trayTotals,
  undoStroke,
  type SeatState,
} from "@/lib/seatManager/pending";

const VIP = "tier-vip";
const STD = "tier-std";

function base(): Map<string, SeatState> {
  return new Map<string, SeatState>([
    ["s1", { tierId: STD, status: "available" }],
    ["s2", { tierId: STD, status: "available" }],
    ["s3", { tierId: STD, status: "sold" }],
    ["s4", { tierId: STD, status: "held" }],
    ["s5", { tierId: STD, status: "unavailable" }],
  ]);
}

describe("strokes", () => {
  it("paints free seats and skips sold and held ones, counting them", () => {
    const res = makeStroke(1, { kind: "tier", tierId: VIP }, ["s1", "s2", "s3", "s4"], base(), new Map());
    expect(res.changed).toBe(2);
    expect(res.skippedFrozen).toBe(2);
    expect([...res.stroke!.targets.keys()]).toEqual(["s1", "s2"]);
  });

  it("leaves out seats that already are what the brush makes them", () => {
    const res = makeStroke(1, { kind: "tier", tierId: STD }, ["s1", "s2"], base(), new Map());
    expect(res.stroke).toBeNull();
    expect(res.unchanged).toBe(2);
  });

  it("counts a seat once even when it is named twice, and ignores a seat that is not of the session", () => {
    const res = makeStroke(1, { kind: "close" }, ["s1", "s1", "nope"], base(), new Map());
    expect(res.changed).toBe(1);
    expect(res.unchanged).toBe(1);
  });

  it("sees the tray: a second stroke over a painted seat changes nothing", () => {
    const b = base();
    const first = makeStroke(1, { kind: "tier", tierId: VIP }, ["s1"], b, new Map());
    const merged = mergeStrokes([first.stroke!]);
    const again = makeStroke(2, { kind: "tier", tierId: VIP }, ["s1"], b, merged);
    expect(again.stroke).toBeNull();
  });

  it("lets a later stroke win field by field", () => {
    const b = base();
    const a = makeStroke(1, { kind: "tier", tierId: VIP }, ["s1"], b, new Map()).stroke!;
    const c = makeStroke(2, { kind: "close" }, ["s1"], b, mergeStrokes([a])).stroke!;
    const d = makeStroke(3, { kind: "tier", tierId: STD }, ["s1"], b, mergeStrokes([a, c])).stroke!;
    expect(mergeStrokes([a, c, d]).get("s1")).toEqual({ tierId: STD, status: "unavailable" });
    expect(effectiveState(b, mergeStrokes([a, c, d]), "s1")).toEqual({ tierId: STD, status: "unavailable" });
  });

  it("undoes the last stroke and keeps the ids climbing", () => {
    const b = base();
    let tray = EMPTY_TRAY;
    tray = pushStroke(tray, makeStroke(tray.nextId, { kind: "close" }, ["s1"], b, new Map()).stroke!);
    tray = pushStroke(tray, makeStroke(tray.nextId, { kind: "close" }, ["s2"], b, mergeStrokes(tray.strokes)).stroke!);
    expect(tray.strokes.map((s) => s.id)).toEqual([1, 2]);
    tray = undoStroke(tray);
    expect(tray.strokes).toHaveLength(1);
    expect(tray.nextId).toBe(3);
  });
});

describe("the batch", () => {
  it("drops fields equal to the state of the server and sorts by seat key", () => {
    const b = base();
    const merged = new Map([
      ["s2", { tierId: VIP }],
      ["s1", { tierId: STD, status: "unavailable" as const }],
    ]);
    expect(trayChanges(b, merged)).toEqual([
      { seat_key: "s1", status: "unavailable" },
      { seat_key: "s2", tier_id: VIP },
    ]);
  });

  it("drops a seat that became sold while the tray was open", () => {
    const b = base();
    b.set("s1", { tierId: STD, status: "sold" });
    expect(trayChanges(b, new Map([["s1", { tierId: VIP }]]))).toEqual([]);
  });

  it("names the narrowest batch kind", () => {
    expect(batchKind([{ seat_key: "a", tier_id: VIP }])).toBe("paint");
    expect(batchKind([{ seat_key: "a", status: "unavailable" }])).toBe("close");
    expect(batchKind([{ seat_key: "a", status: "available" }])).toBe("open");
    expect(batchKind([{ seat_key: "a", tier_id: VIP }, { seat_key: "b", status: "unavailable" }])).toBe("apply_layout");
    expect(batchKind([{ seat_key: "a", tier_id: VIP, status: "available" }])).toBe("apply_layout");
  });

  it("totals and deltas read like the tray", () => {
    const changes = [
      { seat_key: "s1", tier_id: VIP },
      { seat_key: "s2", tier_id: VIP },
      { seat_key: "s5", status: "available" as const },
    ];
    expect(trayTotals(changes)).toEqual({ seats: 3, painted: 2, closed: 0, opened: 1 });
    expect(tierDeltas(base(), changes)).toEqual([
      { tierId: STD, gained: 0, lost: 2 },
      { tierId: VIP, gained: 2, lost: 0 },
    ]);
  });

  it("counts seats per category with the tray applied", () => {
    const b = base();
    const merged = mergeStrokes([makeStroke(1, { kind: "tier", tierId: VIP }, ["s1"], b, new Map()).stroke!]);
    const c = countByTier(b, merged, b.keys());
    expect(c.get(VIP)).toEqual({ total: 1, free: 1, sold: 0, held: 0, closed: 0 });
    expect(c.get(STD)).toEqual({ total: 4, free: 1, sold: 1, held: 1, closed: 1 });
  });
});
