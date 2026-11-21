import { describe, expect, it } from "vitest";
import type { SeatLayoutProposal } from "@/lib/api/types";
import {
  NEW_CATEGORY,
  checkDraft,
  checkDrafts,
  draftOfSlot,
  mappingWithNew,
  overTierLimit,
  previewTable,
  proposalToStrokes,
  resolveNewSlots,
  type NewTierValue,
} from "@/lib/seatManager/layout";
import {
  EMPTY_TRAY,
  addPendingTiers,
  buildBatchRequest,
  clearTray,
  mergeStrokes,
  nextPendingKey,
  pendingTierId,
  pushStroke,
  removePendingTier,
  settleApplied,
  trayChanges,
  type PendingTier,
  type SeatState,
} from "@/lib/seatManager/pending";
import { parseTray, serializeTray } from "@/lib/seatManager/trayStore";

const proposal: SeatLayoutProposal = {
  slots: [
    { key: "c1", price_amount: 5000, share: 0.34, name: "Front", color: "#112233" },
    { key: "c2", price_amount: 3000, share: 0.33 },
    { key: "c3", price_amount: 1500, share: 0.33 },
  ],
  assignments: { a: "c1", b: "c1", c: "c2", d: "c3", e: "c3" },
  frozen_seat_keys: [],
};
const base = new Map<string, SeatState>(["a", "b", "c", "d", "e"].map((k) => [k, { tierId: "only", status: "available" }]));
const one = [{ id: "only", price: 3000, name: "Only" }];

describe("mapping with new categories", () => {
  it("keeps what suggestMapping finds and makes the rest a new category", () => {
    const m = mappingWithNew(proposal.slots, one);
    expect(m.get("c2")).toBe("only");
    expect(m.get("c1")).toBe(NEW_CATEGORY);
    expect(m.get("c3")).toBe(NEW_CATEGORY);
  });
});

describe("drafts", () => {
  it("starts from the slot: its name or the fallback, its price, its colour", () => {
    expect(draftOfSlot(proposal.slots[0]!, "EUR", "Category 1", "#000000")).toEqual({ name: "Front", price: "50", color: "#112233" });
    expect(draftOfSlot(proposal.slots[1]!, "EUR", "Category 2", "#abcdef")).toEqual({ name: "Category 2", price: "30", color: "#abcdef" });
  });

  it("is judged like the server: name 1-100, not taken (case ignored), price 0..1e9, colour #rrggbb", () => {
    const taken = new Set(["vip"]);
    expect(checkDraft({ name: " Gold ", price: "12,5", color: "#AABBCC" }, "EUR", taken)).toEqual({
      ok: true,
      value: { name: "Gold", price: 1250, color: "#aabbcc" },
    });
    expect(checkDraft({ name: "  ", price: "1", color: "#aabbcc" }, "EUR", taken)).toEqual({ ok: false, errors: { name: "empty" } });
    expect(checkDraft({ name: "x".repeat(101), price: "1", color: "#aabbcc" }, "EUR", taken)).toEqual({ ok: false, errors: { name: "long" } });
    expect(checkDraft({ name: "VIP", price: "1", color: "#aabbcc" }, "EUR", taken)).toEqual({ ok: false, errors: { name: "taken" } });
    expect(checkDraft({ name: "A", price: "abc", color: "red" }, "EUR", taken)).toEqual({ ok: false, errors: { price: "bad", color: "bad" } });
    expect(checkDraft({ name: "A", price: "10000001", color: "#aabbcc" }, "EUR", taken)).toEqual({ ok: false, errors: { price: "max" } });
    expect(checkDraft({ name: "A", price: "0", color: "#aabbcc" }, "EUR", taken).ok).toBe(true);
  });

  it("refuses the second of two equal names among the new categories", () => {
    const drafts = new Map([
      ["c1", { name: "Gold", price: "5", color: "#aabbcc" }],
      ["c3", { name: "gold", price: "5", color: "#aabbcc" }],
    ]);
    const res = checkDrafts(drafts, "EUR", ["Silver"]);
    expect(res.get("c1")!.ok).toBe(true);
    expect(res.get("c3")).toEqual({ ok: false, errors: { name: "taken" } });
  });

  it("counts the limit of 15 over live, pending and added categories", () => {
    expect(overTierLimit(10, 2, 3)).toBe(false);
    expect(overTierLimit(10, 2, 4)).toBe(true);
  });
});

describe("proposal with pending categories", () => {
  const values = new Map<string, NewTierValue>([
    ["c1", { name: "Front", price: 5000, color: "#112233" }],
    ["c3", { name: "Back", price: 1500, color: "#445566" }],
  ]);

  it("makes one pending category per new slot with keys that do not clash", () => {
    const existing: PendingTier[] = [{ key: "n4", name: "Old", price: 1, color: "#000000" }];
    const r = resolveNewSlots(proposal, mappingWithNew(proposal.slots, one), values, existing);
    expect(r.created.map((c) => c.key)).toEqual(["n5", "n6"]);
    expect(r.mapping.get("c1")).toBe("new:n5");
    expect(r.mapping.get("c2")).toBe("only");
    expect(r.mapping.get("c3")).toBe("new:n6");
  });

  it("makes no category for a new slot without seats", () => {
    const p: SeatLayoutProposal = { ...proposal, assignments: { a: "c1" } };
    const r = resolveNewSlots(p, new Map([["c1", NEW_CATEGORY], ["c3", NEW_CATEGORY]]), values, []);
    expect(r.created).toHaveLength(1);
    expect(r.mapping.get("c3")).toBeNull();
  });

  it("paints seats with the pending id", () => {
    const r = resolveNewSlots(proposal, mappingWithNew(proposal.slots, one), values, []);
    const res = proposalToStrokes(proposal, r.mapping, base, new Map(), 1);
    const merged = mergeStrokes(res.strokes);
    expect(merged.get("a")).toEqual({ tierId: "new:n1" });
    expect(merged.get("d")).toEqual({ tierId: "new:n2" });
    expect(merged.has("c")).toBe(false); // already the category "only"
  });
});

describe("the one request of Apply", () => {
  const pending: PendingTier[] = [
    { key: "n1", name: "Front", price: 5000, color: "#112233" },
    { key: "n2", name: "Back", price: 1500, color: "#445566" },
    { key: "n3", name: "Unused", price: 100, color: "#778899" },
  ];

  function trayWith() {
    const r = resolveNewSlots(
      proposal,
      mappingWithNew(proposal.slots, one),
      new Map([
        ["c1", { name: "Front", price: 5000, color: "#112233" }],
        ["c3", { name: "Back", price: 1500, color: "#445566" }],
      ]),
      [],
    );
    const res = proposalToStrokes(proposal, r.mapping, base, new Map(), 1);
    let tray = addPendingTiers(EMPTY_TRAY, pending);
    for (const s of res.strokes) tray = pushStroke(tray, s);
    return tray;
  }

  it("sends each used category once, as new_tiers, and the seats with new_tier_key", () => {
    const tray = trayWith();
    const changes = trayChanges(base, mergeStrokes(tray.strokes));
    const { body, unsent } = buildBatchRequest(changes, tray.pendingTiers);
    expect(body.kind).toBe("paint");
    expect(body.new_tiers).toEqual([
      { key: "n1", name: "Front", price_amount: 5000, color: "#112233" },
      { key: "n2", name: "Back", price_amount: 1500, color: "#445566" },
    ]);
    expect(body.changes.find((c) => c.seat_key === "a")).toEqual({ seat_key: "a", new_tier_key: "n1" });
    expect(body.changes.every((c) => c.tier_id === undefined)).toBe(true);
    expect(body.changes).toHaveLength(4);
    // A category no seat has is not sent.
    expect(unsent.map((u) => u.key)).toEqual(["n3"]);
  });

  it("has no new_tiers when nothing names a new category, and an existing tier_id stays", () => {
    const { body } = buildBatchRequest([{ seat_key: "a", tier_id: "t1" }, { seat_key: "b", status: "unavailable" }], pending);
    expect(body.new_tiers).toBeUndefined();
    expect(body.kind).toBe("apply_layout");
    expect(body.changes[0]).toEqual({ seat_key: "a", tier_id: "t1" });
  });

  it("settles after success: sent strokes go, created categories replace pending ones in later strokes", () => {
    let tray = trayWith();
    const sent = new Set(tray.strokes.map((s) => s.id));
    // A touch made while the request was on its way.
    tray = pushStroke(tray, { id: 99, brush: { kind: "tier", tierId: "new:n1" }, targets: new Map([["z", { tierId: "new:n1" }]]) });
    const after = settleApplied(tray, sent, [
      { key: "n1", tier_id: "real-1" },
      { key: "n2", tier_id: "real-2" },
    ]);
    expect(after.strokes).toHaveLength(1);
    expect(after.strokes[0]!.brush).toEqual({ kind: "tier", tierId: "real-1" });
    expect(after.strokes[0]!.targets.get("z")).toEqual({ tierId: "real-1" });
    expect(after.pendingTiers.map((p) => p.key)).toEqual(["n3"]);
  });
});

describe("pending categories in the tray", () => {
  it("are removed together with their strokes", () => {
    let tray = addPendingTiers(EMPTY_TRAY, [{ key: "n1", name: "A", price: 1, color: "#000000" }]);
    tray = pushStroke(tray, { id: 1, brush: { kind: "tier", tierId: "new:n1" }, targets: new Map([["a", { tierId: "new:n1" }]]) });
    tray = pushStroke(tray, { id: 2, brush: { kind: "close" }, targets: new Map([["b", { status: "unavailable" as const }]]) });
    const out = removePendingTier(tray, "n1");
    expect(out.pendingTiers).toEqual([]);
    expect(out.strokes.map((s) => s.id)).toEqual([2]);
    expect(clearTray(tray).pendingTiers).toEqual([]);
  });

  it("get the next key from the ones held", () => {
    expect(nextPendingKey([])).toBe("n1");
    expect(nextPendingKey([{ key: "n7", name: "", price: 0, color: "#000000" }])).toBe("n8");
  });

  it("survive a reload with the tray, and a broken list is an empty tray", () => {
    let tray = addPendingTiers(EMPTY_TRAY, [{ key: "n1", name: "Front", price: 5000, color: "#112233" }]);
    tray = pushStroke(tray, { id: 3, brush: { kind: "tier", tierId: pendingTierId("n1") }, targets: new Map([["a", { tierId: "new:n1" }]]) });
    const back = parseTray(serializeTray(tray));
    expect(back.pendingTiers).toEqual(tray.pendingTiers);
    expect(back.strokes[0]!.targets.get("a")).toEqual({ tierId: "new:n1" });
    expect(parseTray(JSON.stringify({ strokes: [], pendingTiers: [{ key: "bad key", name: "x", price: 1, color: "#000000" }] }))).toEqual(EMPTY_TRAY);
    // A stroke naming a category the tray does not hold is refused.
    expect(parseTray(JSON.stringify({ strokes: [{ id: 1, brush: { kind: "tier", tierId: "new:zz" }, targets: [] }], pendingTiers: [] }))).toEqual(EMPTY_TRAY);
    // A tray stored before this feature has no list.
    expect(parseTray(JSON.stringify({ strokes: [] })).pendingTiers).toEqual([]);
  });
});

describe("the preview table", () => {
  it("joins slots and by_slot, and totals seats and the full-house revenue", () => {
    const t = previewTable(
      proposal.slots,
      [
        { slot_key: "c1", seats: 2, price_amount: 5000, revenue_minor: 10000 },
        { slot_key: "c2", seats: 1, price_amount: 3000, revenue_minor: 3000 },
        { slot_key: "c3", seats: 2, price_amount: 1500, revenue_minor: 3000 },
      ],
      (s, i) => s.name ?? `Category ${i + 1}`,
    );
    expect(t.rows.map((r) => [r.name, r.seats, r.price, r.revenue])).toEqual([
      ["Front", 2, 5000, 10000],
      ["Category 2", 1, 3000, 3000],
      ["Category 3", 2, 1500, 3000],
    ]);
    expect(t.totalSeats).toBe(5);
    expect(t.totalRevenue).toBe(16000);
  });
});
