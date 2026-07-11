/**
 * Unit tests for pure helpers in the session-editor seating bind panel
 * (feature #316, Wave SEAT-E2). These pin the wire-shape mapping so a
 * regression surfaces without needing to boot the DOM.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import type {
  SeatingGeometry,
  SeatingPlanVersion,
} from "@/routes/venueSeatingPlans";
import {
  anyAutoCreate,
  buildCategoryTierMap,
  extractGeometryCategories,
  formatCapacityCounters,
  isAdmissionMode,
  mapBindError,
  validateBindForm,
  type CategoryTierMapRow,
} from "@/routes/sessionSeatingBind";

const geometry: SeatingGeometry = {
  canvas: { width: 100, height: 100 },
  categories: [
    { index: 1, name: "VIP", color: "#f00" },
    { index: 2, name: "Std", color: "#0f0" },
    // A category with an empty name should fall back to "Category N".
    { index: 3, name: "", color: "#00f" },
  ],
  sections: [
    {
      key: "A",
      name: "A",
      rows: [
        {
          key: "R1",
          name: "R1",
          seats: [
            { key: "A|R1|1", number: "1", x: 1, y: 1, radius: 2, category_index: 1 },
            { key: "A|R1|2", number: "2", x: 2, y: 1, radius: 2, category_index: 2 },
            { key: "A|R1|3", number: "3", x: 3, y: 1, radius: 2, category_index: 3 },
          ],
        },
      ],
    },
  ],
};

describe("isAdmissionMode", () => {
  it("accepts the three known enum values", () => {
    expect(isAdmissionMode("assigned_seats")).toBe(true);
    expect(isAdmissionMode("hybrid")).toBe(true);
    expect(isAdmissionMode("general_admission")).toBe(true);
  });
  it("rejects anything else", () => {
    expect(isAdmissionMode("")).toBe(false);
    expect(isAdmissionMode("foo")).toBe(false);
    expect(isAdmissionMode("ASSIGNED_SEATS")).toBe(false);
  });
});

describe("extractGeometryCategories", () => {
  it("passes through the categories array as-is", () => {
    const cats = extractGeometryCategories(geometry);
    expect(cats).toHaveLength(3);
    expect(cats[0]).toEqual({ index: 1, name: "VIP", color: "#f00" });
    expect(cats[1]).toEqual({ index: 2, name: "Std", color: "#0f0" });
  });

  it("falls back to `Category N` when the name is empty", () => {
    const cats = extractGeometryCategories(geometry);
    expect(cats[2]).toEqual({ index: 3, name: "Category 3", color: "#00f" });
  });

  it("synthesises categories from seats when categories[] is absent", () => {
    const cats = extractGeometryCategories({
      canvas: { width: 100, height: 100 },
      sections: [
        {
          key: "A",
          name: "A",
          rows: [
            {
              key: "R1",
              name: "R1",
              seats: [
                { key: "A|R1|1", number: "1", x: 0, y: 0, radius: 1, category_index: 2 },
                { key: "A|R1|2", number: "2", x: 0, y: 0, radius: 1, category_index: 1 },
                { key: "A|R1|3", number: "3", x: 0, y: 0, radius: 1, category_index: 2 },
              ],
            },
          ],
        },
      ],
    });
    // Deduped and sorted ascending by index.
    expect(cats.map((c) => c.index)).toEqual([1, 2]);
    expect(cats[0]?.name).toBe("Category 1");
    expect(cats[1]?.name).toBe("Category 2");
  });

  it("returns an empty array when geometry has neither categories nor seats", () => {
    expect(extractGeometryCategories({ canvas: { width: 1, height: 1 } })).toEqual([]);
  });
});

describe("buildCategoryTierMap", () => {
  it("emits null for auto-create rows and the tier id for explicit rows", () => {
    const rows: readonly CategoryTierMapRow[] = [
      { categoryIndex: 1, categoryName: "VIP", tierId: "tier-a", autoCreate: false },
      { categoryIndex: 2, categoryName: "Std", tierId: "", autoCreate: true },
      { categoryIndex: 3, categoryName: "N", tierId: "  tier-b  ", autoCreate: false },
    ];
    expect(buildCategoryTierMap(rows)).toEqual({
      "1": "tier-a",
      "2": null,
      "3": "tier-b",
    });
  });

  it("omits unmapped rows (no auto-create, no tier id)", () => {
    const rows: readonly CategoryTierMapRow[] = [
      { categoryIndex: 1, categoryName: "V", tierId: "", autoCreate: false },
    ];
    expect(buildCategoryTierMap(rows)).toEqual({});
  });
});

describe("validateBindForm", () => {
  const okRows: readonly CategoryTierMapRow[] = [
    { categoryIndex: 1, categoryName: "VIP", tierId: "tier-a", autoCreate: false },
    { categoryIndex: 2, categoryName: "Std", tierId: "", autoCreate: true },
  ];

  it("returns no errors when every input is valid", () => {
    const errs = validateBindForm({
      admissionMode: "assigned_seats",
      planVersionId: "ver-1",
      rows: okRows,
    });
    expect(errs).toEqual({});
  });

  it("flags GA admission mode as invalid for binding", () => {
    const errs = validateBindForm({
      admissionMode: "general_admission",
      planVersionId: "ver-1",
      rows: okRows,
    });
    expect(errs.admission_mode).toBeDefined();
  });

  it("flags a missing plan version", () => {
    const errs = validateBindForm({
      admissionMode: "assigned_seats",
      planVersionId: "",
      rows: okRows,
    });
    expect(errs.plan_version_id).toBeDefined();
  });

  it("lists unmapped category indices when neither tier nor auto-create is set", () => {
    const errs = validateBindForm({
      admissionMode: "hybrid",
      planVersionId: "ver-1",
      rows: [
        { categoryIndex: 1, categoryName: "VIP", tierId: "", autoCreate: false },
        { categoryIndex: 4, categoryName: "?", tierId: "", autoCreate: false },
        { categoryIndex: 2, categoryName: "OK", tierId: "tier-x", autoCreate: false },
      ],
    });
    expect(errs.unmapped_categories).toEqual([1, 4]);
  });
});

describe("anyAutoCreate", () => {
  it("returns true when at least one row is auto-create", () => {
    expect(
      anyAutoCreate([
        { categoryIndex: 1, categoryName: "V", tierId: "t", autoCreate: false },
        { categoryIndex: 2, categoryName: "S", tierId: "", autoCreate: true },
      ]),
    ).toBe(true);
  });
  it("returns false when every row is manually mapped", () => {
    expect(
      anyAutoCreate([
        { categoryIndex: 1, categoryName: "V", tierId: "t", autoCreate: false },
      ]),
    ).toBe(false);
  });
});

describe("mapBindError", () => {
  const err = (code: string, status: number, message = ""): ApiError =>
    new ApiError(status, { code, message });

  it("maps the SEAT-B2 error catalogue to human-friendly sentences", () => {
    expect(mapBindError(err("seating.invalid_admission_mode", 400))).toMatch(
      /assigned_seats or hybrid/,
    );
    expect(mapBindError(err("seating.version_not_found", 400))).toMatch(
      /no longer exists/,
    );
    expect(mapBindError(err("seating.category_tier_map_incomplete", 400))).toMatch(
      /Every geometry category/,
    );
    expect(mapBindError(err("seating.tier_not_found", 400))).toMatch(
      /ticket tiers/,
    );
    expect(mapBindError(err("seating.unknown_category", 400))).toMatch(
      /not present in the plan version/,
    );
    expect(mapBindError(err("seating.invalid_category_key", 400))).toMatch(
      /category index/,
    );
    expect(mapBindError(err("seating.invalid_category_tier_map", 400))).toMatch(
      /not valid/,
    );
    expect(mapBindError(err("seating.invalid_body", 400))).toMatch(/rejected/);
  });

  it("maps the 409 rebind guard to a copy-safe operator sentence", () => {
    const msg = mapBindError(err("seating.rebind_forbidden", 409));
    expect(msg).toMatch(/reservations/);
    expect(msg).toMatch(/Create a new session/);
  });

  it("falls back to status-based messages for auth failures", () => {
    expect(mapBindError(err("permissions.denied", 403))).toMatch(
      /assign_seating_plan/,
    );
    expect(mapBindError(err("http.401", 401))).toMatch(/sign in/);
    expect(mapBindError(err("http.403", 403))).toMatch(/Forbidden/);
    expect(mapBindError(err("http.503", 503))).toMatch(/temporarily unavailable/);
    expect(mapBindError(err("http.404", 404))).toMatch(/not found/);
  });

  it("mirrors a bare 409 to the same rebind guard sentence", () => {
    const msg = mapBindError(err("something.else", 409));
    expect(msg).toMatch(/reservations or tickets/);
  });

  it("falls back to `<message> (<code>)` for unknown 400s", () => {
    expect(mapBindError(err("seating.other", 400, "boom"))).toBe(
      "boom (seating.other)",
    );
  });
});

describe("formatCapacityCounters", () => {
  it("renders both counters with locale-aware separators", () => {
    const v: SeatingPlanVersion = {
      id: "ver-1",
      seating_plan_id: "plan-1",
      version_number: 1,
      geometry: { canvas: { width: 1, height: 1 } },
      geometry_checksum: "abc",
      svg_asset_media_id: null,
      capacity_seated: 1200,
      capacity_standing: 300,
      locked_at: null,
      created_at: "2026-07-11T00:00:00Z",
    };
    const s = formatCapacityCounters(v);
    expect(s).toMatch(/seated/);
    expect(s).toMatch(/standing/);
    // Value substrings must appear (with or without locale separators).
    expect(s.replace(/[^0-9]/g, "")).toContain("1200");
    expect(s.replace(/[^0-9]/g, "")).toContain("300");
  });
});
