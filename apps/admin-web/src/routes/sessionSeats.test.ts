/**
 * Unit tests for the interactive seat management screen (feature #317).
 *
 * These pin the pure helpers that back the UI: the SVG renderer,
 * selection collectors, sector / category counters, sector-row indexer,
 * outcome summariser, and the delta-merge routine. The rendering React
 * layer is exercised elsewhere; here we only verify the byte-stable
 * transformations that a regression in the DOM code would not catch.
 */
import { describe, expect, it } from "vitest";
import type { SeatingGeometry } from "@/routes/venueSeatingPlans";
import {
  SEAT_STATUS_COLOURS,
  buildSectorRowIndex,
  collectSeatKeysForRow,
  collectSeatKeysForSector,
  computeCategoryCounters,
  computeSectorCounters,
  mergeSeatStatus,
  renderSeatMapSVG,
  summariseOutcomes,
  type PatchSeatsOutcome,
  type SeatStatus,
  type SeatStatusEnvelope,
} from "@/routes/sessionSeats";

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const geometry: SeatingGeometry = {
  schema_version: 1,
  canvas: { width: 400, height: 300 },
  categories: [
    { index: 1, name: "VIP", color: "#ff0000" },
    { index: 2, name: "Std", color: "#0000ff" },
  ],
  sections: [
    {
      key: "A",
      name: "Stalls",
      rows: [
        {
          key: "R1",
          name: "1",
          seats: [
            { key: "Stalls|1|1", number: "1", x: 10, y: 20, radius: 3, category_index: 1 },
            { key: "Stalls|1|2", number: "2", x: 20, y: 20, radius: 3, category_index: 1 },
          ],
        },
        {
          key: "R2",
          name: "2",
          seats: [
            { key: "Stalls|2|1", number: "1", x: 10, y: 30, radius: 3, category_index: 2 },
          ],
        },
      ],
    },
    {
      key: "B",
      name: "Balcony",
      rows: [
        {
          key: "R1",
          name: "1",
          seats: [
            { key: "Balcony|1|1", number: "1", x: 100, y: 200, radius: 3, category_index: 2 },
          ],
        },
      ],
    },
  ],
};

const statuses: Record<string, SeatStatus> = {
  "Stalls|1|1": "available",
  "Stalls|1|2": "sold",
  "Stalls|2|1": "held",
  "Balcony|1|1": "blocked",
};

// ---------------------------------------------------------------------------
// renderSeatMapSVG
// ---------------------------------------------------------------------------

describe("renderSeatMapSVG", () => {
  it("emits one circle per seat with the status colour", () => {
    const svg = renderSeatMapSVG(geometry, statuses, new Set<string>());
    expect(svg).toContain('viewBox="0 0 400 300"');
    const circles = svg.match(/<circle/g) ?? [];
    expect(circles.length).toBe(4);
    expect(svg).toContain(`fill="${SEAT_STATUS_COLOURS.available}"`);
    expect(svg).toContain(`fill="${SEAT_STATUS_COLOURS.sold}"`);
    expect(svg).toContain(`fill="${SEAT_STATUS_COLOURS.held}"`);
    expect(svg).toContain(`fill="${SEAT_STATUS_COLOURS.blocked}"`);
    expect(svg).toContain('data-seat-key="Stalls|1|1"');
    expect(svg).toContain('data-status="available"');
  });

  it("marks selected seats with a red stroke", () => {
    const svg = renderSeatMapSVG(
      geometry,
      statuses,
      new Set<string>(["Stalls|1|1"]),
    );
    // The only stroke in the output is the selection stroke.
    expect(svg).toContain('stroke="#e11d48"');
    // Only one seat is selected — one stroke attribute total.
    const strokes = svg.match(/stroke="#e11d48"/g) ?? [];
    expect(strokes.length).toBe(1);
  });

  it("falls back to a neutral fill when status is unknown", () => {
    const svg = renderSeatMapSVG(geometry, {}, new Set<string>());
    expect(svg).toContain('fill="#e2e8f0"');
  });

  it("survives non-finite canvas dimensions", () => {
    const svg = renderSeatMapSVG(
      { canvas: { width: Number.NaN, height: -1 }, sections: [] },
      {},
      new Set<string>(),
    );
    expect(svg).toContain("viewBox=\"0 0 100 100\"");
  });
});

// ---------------------------------------------------------------------------
// Sector / row collectors
// ---------------------------------------------------------------------------

describe("collectSeatKeysForSector", () => {
  it("returns every seat key in the named sector", () => {
    const keys = collectSeatKeysForSector(geometry, "Stalls");
    expect([...keys].sort()).toEqual(["Stalls|1|1", "Stalls|1|2", "Stalls|2|1"]);
  });

  it("returns [] for unknown sectors", () => {
    expect(collectSeatKeysForSector(geometry, "Nowhere")).toEqual([]);
  });
});

describe("collectSeatKeysForRow", () => {
  it("returns every seat key in the named (sector,row)", () => {
    const keys = collectSeatKeysForRow(geometry, "Stalls", "1");
    expect([...keys].sort()).toEqual(["Stalls|1|1", "Stalls|1|2"]);
  });

  it("returns [] when sector or row is unknown", () => {
    expect(collectSeatKeysForRow(geometry, "Stalls", "9")).toEqual([]);
    expect(collectSeatKeysForRow(geometry, "Foyer", "1")).toEqual([]);
  });
});

// ---------------------------------------------------------------------------
// Counters
// ---------------------------------------------------------------------------

describe("computeSectorCounters", () => {
  it("rolls up totals and by-status counts per sector", () => {
    const counters = computeSectorCounters(geometry, statuses);
    expect(counters).toEqual([
      {
        name: "Stalls",
        total: 3,
        by_status: { available: 1, held: 1, sold: 1, blocked: 0 },
      },
      {
        name: "Balcony",
        total: 1,
        by_status: { available: 0, held: 0, sold: 0, blocked: 1 },
      },
    ]);
  });

  it("treats missing / unknown statuses as available", () => {
    const counters = computeSectorCounters(geometry, {});
    expect(counters[0]?.by_status).toEqual({
      available: 3,
      held: 0,
      sold: 0,
      blocked: 0,
    });
  });
});

describe("computeCategoryCounters", () => {
  it("preserves category order + colour and rolls up counts", () => {
    const counters = computeCategoryCounters(geometry, statuses);
    expect(counters.length).toBe(2);
    expect(counters[0]).toMatchObject({
      index: 1,
      name: "VIP",
      color: "#ff0000",
      total: 2,
      by_status: { available: 1, held: 0, sold: 1, blocked: 0 },
    });
    expect(counters[1]).toMatchObject({
      index: 2,
      name: "Std",
      color: "#0000ff",
      total: 2,
      by_status: { available: 0, held: 1, sold: 0, blocked: 1 },
    });
  });

  it("surfaces an Uncategorised bucket for orphaned seats", () => {
    const orphan: SeatingGeometry = {
      canvas: { width: 100, height: 100 },
      categories: [{ index: 1, name: "VIP", color: "#ff0000" }],
      sections: [
        {
          key: "A",
          name: "A",
          rows: [
            {
              key: "R1",
              name: "1",
              seats: [
                {
                  key: "A|1|1",
                  number: "1",
                  x: 1,
                  y: 1,
                  radius: 2,
                  category_index: 99,
                },
              ],
            },
          ],
        },
      ],
    };
    const counters = computeCategoryCounters(orphan, { "A|1|1": "available" });
    expect(counters[counters.length - 1]).toMatchObject({
      index: 0,
      name: "Uncategorised",
      total: 1,
    });
  });
});

// ---------------------------------------------------------------------------
// Sector / row index
// ---------------------------------------------------------------------------

describe("buildSectorRowIndex", () => {
  it("preserves first-appearance order for sectors and rows", () => {
    const idx = buildSectorRowIndex(geometry);
    expect(idx.sectorNames).toEqual(["Stalls", "Balcony"]);
    expect(idx.rowsBySector.Stalls).toEqual(["1", "2"]);
    expect(idx.rowsBySector.Balcony).toEqual(["1"]);
  });

  it("dedupes repeated names across duplicate sections", () => {
    const dup: SeatingGeometry = {
      sections: [
        {
          key: "A",
          name: "A",
          rows: [
            { key: "R1", name: "1", seats: [] },
            { key: "R1b", name: "1", seats: [] },
          ],
        },
      ],
    };
    const idx = buildSectorRowIndex(dup);
    expect(idx.rowsBySector.A).toEqual(["1"]);
  });
});

// ---------------------------------------------------------------------------
// Outcome summariser
// ---------------------------------------------------------------------------

describe("summariseOutcomes", () => {
  it("splits changed / noop / skipped and groups reasons", () => {
    const outcomes: PatchSeatsOutcome[] = [
      { seat_key: "a", outcome: "blocked", status: "blocked" },
      { seat_key: "b", outcome: "noop", status: "blocked" },
      { seat_key: "c", outcome: "skipped", reason: "sold", status: "sold" },
      { seat_key: "d", outcome: "skipped", reason: "sold", status: "sold" },
      { seat_key: "e", outcome: "skipped", reason: "held", status: "held" },
      {
        seat_key: "f",
        outcome: "skipped",
        reason: "seat_not_found",
        status: "",
      },
    ];
    const s = summariseOutcomes(outcomes);
    expect(s.changed.length).toBe(1);
    expect(s.noop.length).toBe(1);
    expect(s.skipped.length).toBe(4);
    expect(Object.keys(s.skippedByReason).sort()).toEqual([
      "held",
      "seat_not_found",
      "sold",
    ]);
    expect(s.skippedByReason.sold?.length).toBe(2);
  });

  it("falls back to 'unknown' when reason is missing", () => {
    const s = summariseOutcomes([
      { seat_key: "x", outcome: "skipped", status: "" },
    ]);
    expect(s.skippedByReason.unknown?.length).toBe(1);
  });
});

// ---------------------------------------------------------------------------
// Delta merge
// ---------------------------------------------------------------------------

describe("mergeSeatStatus", () => {
  it("returns a snapshot when delta=false, discarding prior state", () => {
    const prev = { "old|1|1": "sold" as SeatStatus };
    const snap: SeatStatusEnvelope = {
      session_id: "s",
      status_version: 5,
      delta: false,
      seats: { "new|1|1": "available" },
    };
    const merged = mergeSeatStatus(prev, snap);
    expect(merged).toEqual({ "new|1|1": "available" });
  });

  it("layers a delta onto the previous state", () => {
    const prev = {
      "a|1|1": "available" as SeatStatus,
      "a|1|2": "available" as SeatStatus,
    };
    const delta: SeatStatusEnvelope = {
      session_id: "s",
      status_version: 6,
      delta: true,
      seats: { "a|1|2": "sold" },
    };
    const merged = mergeSeatStatus(prev, delta);
    expect(merged).toEqual({ "a|1|1": "available", "a|1|2": "sold" });
  });

  it("drops entries with unknown status values (defence in depth)", () => {
    const merged = mergeSeatStatus(
      {},
      {
        session_id: "s",
        status_version: 1,
        delta: false,
        // Intentionally cast — protects against a backend regression.
        seats: { bad: "weird" as unknown as SeatStatus, ok: "held" },
      },
    );
    expect(merged).toEqual({ ok: "held" });
  });
});
