import { describe, expect, it, vi } from "vitest";
import { runBatch } from "@/lib/admin/batch";
import {
  cityOptions,
  countryOptions,
  filterVenuesByGeo,
  pruneSelection,
  selectableIds,
  toggleAll,
  toggleId,
  verifyVenuesBatch,
  type VenueLike,
} from "@/lib/admin/venueBulk";

const v = (id: string, patch: Partial<VenueLike> = {}): VenueLike => ({
  id,
  name: `Venue ${id}`,
  city_id: null,
  country: null,
  review_status: "unverified",
  ...patch,
});

const ROWS: VenueLike[] = [
  v("a", { country: "cz", city_id: "c-prague" }),
  v("b", { country: "CZ", city_id: "c-brno", review_status: "verified" }),
  v("c", { country: "IL", city_id: "c-haifa" }),
  v("d"),
];

describe("runBatch", () => {
  it("runs every item, never more than the limit at once, and keeps failures", async () => {
    let active = 0;
    let peak = 0;
    const progress: number[] = [];
    const res = await runBatch(
      [1, 2, 3, 4, 5, 6],
      async (n) => {
        active += 1;
        peak = Math.max(peak, active);
        await new Promise((r) => setTimeout(r, 2));
        active -= 1;
        if (n === 4) throw new Error("four");
        return n * 2;
      },
      { concurrency: 3, onProgress: (p) => progress.push(p.completed) },
    );
    expect(peak).toBeLessThanOrEqual(3);
    expect(res.done.map((d) => d.value).sort()).toEqual([10, 12, 2, 4, 6]);
    expect(res.failed.map((f) => f.item)).toEqual([4]);
    expect(progress[0]).toBe(0);
    expect(progress[progress.length - 1]).toBe(6);
  });

  it("stops starting items when asked", async () => {
    let stop = false;
    const res = await runBatch([1, 2, 3], async (n) => {
      stop = true;
      return n;
    }, { concurrency: 1, shouldStop: () => stop });
    expect(res.stopped).toBe(true);
    expect(res.done).toHaveLength(1);
  });

  it("handles an empty list", async () => {
    const res = await runBatch([], async () => 1, { concurrency: 4 });
    expect(res.done).toEqual([]);
    expect(res.stopped).toBe(false);
  });
});

describe("geo filter", () => {
  it("filters by country, case-insensitive", () => {
    expect(filterVenuesByGeo(ROWS, { country: "CZ", cityId: "" }).map((r) => r.id)).toEqual(["a", "b"]);
  });

  it("filters by city and by both", () => {
    expect(filterVenuesByGeo(ROWS, { country: "", cityId: "c-haifa" }).map((r) => r.id)).toEqual(["c"]);
    expect(filterVenuesByGeo(ROWS, { country: "CZ", cityId: "c-haifa" })).toEqual([]);
  });

  it("lists countries and cities for the selects", () => {
    expect(countryOptions(ROWS)).toEqual(["CZ", "IL"]);
    const names = new Map([["c-prague", "Prague"]]);
    expect(cityOptions(ROWS, names, "CZ")).toEqual([
      { id: "c-brno", label: "c-brno" },
      { id: "c-prague", label: "Prague" },
    ]);
    expect(cityOptions(ROWS, names, "").map((c) => c.id)).toContain("c-haifa");
  });
});

describe("selection", () => {
  it("offers only unverified venues", () => {
    expect(selectableIds(ROWS)).toEqual(["a", "c", "d"]);
  });

  it("toggles one id and all ids", () => {
    const one = toggleId(new Set<string>(), "a");
    expect([...one]).toEqual(["a"]);
    expect([...toggleId(one, "a")]).toEqual([]);
    const all = toggleAll(new Set<string>(), ROWS);
    expect([...all].sort()).toEqual(["a", "c", "d"]);
    expect(toggleAll(all, ROWS).size).toBe(0);
  });

  it("drops ids that left the screen or got verified", () => {
    const sel = new Set(["a", "b", "zzz"]);
    expect([...pruneSelection(sel, ROWS)]).toEqual(["a"]);
    const same = new Set(["a"]);
    expect(pruneSelection(same, ROWS)).toBe(same);
  });
});

describe("verifyVenuesBatch", () => {
  it("patches only unverified venues with the one reason and lists failures", async () => {
    const patch = vi.fn(async (id: string) => {
      if (id === "c") throw new Error("nope");
      return {};
    });
    const res = await verifyVenuesBatch({ venues: ROWS, reason: "Checked the addresses", patch, concurrency: 2 });
    expect(patch).toHaveBeenCalledTimes(3);
    expect(patch.mock.calls.every((c) => c[1] === "Checked the addresses")).toBe(true);
    expect(patch.mock.calls.map((c) => c[0])).not.toContain("b");
    expect(res.verified.sort()).toEqual(["a", "d"]);
    expect(res.skipped).toEqual(["b"]);
    expect(res.failed).toEqual([{ id: "c", name: "Venue c", message: "nope" }]);
  });
});
