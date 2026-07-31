/**
 * Unit tests for pure helpers in the venue Seating-plans drawer (feature #315).
 *
 * We pin the client-side geometry->SVG renderer and the 422 error parser
 * so a regression surfaces without needing to boot the DOM.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  buildCreateVersionBody,
  createPlanFormIssues,
  formatVersionTimestamp,
  issueForField,
  parseStandingCapacity,
  parseVersionValidationErrors,
  renderGeometryToSVG,
  resolveCurrentVersionNumber,
  resolveSelectedVersion,
  validateCreatePlanForm,
  type SeatingGeometry,
  type SeatingPlan,
  type SeatingPlanVersion,
} from "@/routes/venueSeatingPlans";

const geometry: SeatingGeometry = {
  schema_version: 1,
  canvas: { width: 400, height: 200 },
  categories: [
    { index: 1, name: "VIP", color: "#ff0000" },
    { index: 2, name: "Std", color: "#0000ff" },
  ],
  sections: [
    {
      key: "A",
      name: "Section A",
      rows: [
        {
          key: "R1",
          name: "Row 1",
          seats: [
            { key: "A|R1|1", number: "1", x: 10, y: 20, radius: 4, category_index: 1 },
            { key: "A|R1|2", number: "2", x: 20, y: 20, radius: 4, category_index: 2 },
          ],
        },
      ],
    },
  ],
};

describe("renderGeometryToSVG", () => {
  it("emits an svg with viewBox from canvas", () => {
    const svg = renderGeometryToSVG(geometry);
    expect(svg.startsWith("<svg")).toBe(true);
    expect(svg).toContain('viewBox="0 0 400 200"');
    expect(svg).toContain('role="img"');
  });

  it("emits one <circle> per seat with correct fill", () => {
    const svg = renderGeometryToSVG(geometry);
    const circles = svg.match(/<circle/g) ?? [];
    expect(circles.length).toBe(2);
    expect(svg).toContain('fill="#ff0000"');
    expect(svg).toContain('fill="#0000ff"');
    expect(svg).toContain('data-seat-key="A|R1|1"');
    expect(svg).toContain('data-seat-key="A|R1|2"');
  });

  it("falls back to a neutral colour when category is unknown", () => {
    const svg = renderGeometryToSVG({
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
                {
                  key: "A|R1|1",
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
    });
    expect(svg).toContain('fill="#94a3b8"');
  });

  it("escapes HTML-dangerous characters in seat keys", () => {
    const svg = renderGeometryToSVG({
      canvas: { width: 10, height: 10 },
      categories: [{ index: 1, name: "x", color: "#abcdef" }],
      sections: [
        {
          key: "<S>",
          name: "S",
          rows: [
            {
              key: "R&1",
              name: "R",
              seats: [
                {
                  key: '<S>|R&1|"1"',
                  number: "1",
                  x: 0,
                  y: 0,
                  radius: 1,
                  category_index: 1,
                },
              ],
            },
          ],
        },
      ],
    });
    // No unescaped angle brackets or quotes in the attribute payload.
    expect(svg).toContain("&lt;S&gt;|R&amp;1|&quot;1&quot;");
    expect(svg).not.toContain('data-seat-key="<S>');
  });

  it("defaults canvas to 800x600 when absent", () => {
    const svg = renderGeometryToSVG({ sections: [] });
    expect(svg).toContain('viewBox="0 0 800 600"');
  });

  it("never emits decor_svg (raw markup is an XSS channel)", () => {
    const svg = renderGeometryToSVG({
      canvas: { width: 10, height: 10 },
      decor_svg: '<rect data-danger="x"/><script>alert(1)</script>',
      sections: [],
    });
    expect(svg).not.toContain("data-danger");
    expect(svg).not.toContain("script");
    expect(svg).not.toContain('data-role="decor"');
  });

  it("handles empty geometry without throwing", () => {
    const svg = renderGeometryToSVG({});
    expect(svg).toContain("<svg");
    expect(svg).toContain("</svg>");
    expect(svg.match(/<circle/g)).toBeNull();
  });

  it("rounds floating-point coordinates to two decimals", () => {
    const svg = renderGeometryToSVG({
      canvas: { width: 100, height: 100 },
      categories: [{ index: 1, name: "x", color: "#111111" }],
      sections: [
        {
          key: "S",
          name: "S",
          rows: [
            {
              key: "R",
              name: "R",
              seats: [
                {
                  key: "S|R|1",
                  number: "1",
                  x: 10.123456,
                  y: 20.987654,
                  radius: 3.14159,
                  category_index: 1,
                },
              ],
            },
          ],
        },
      ],
    });
    expect(svg).toContain('cx="10.12"');
    expect(svg).toContain('cy="20.99"');
    expect(svg).toContain('r="3.14"');
  });

  it("substitutes 0 for non-finite coordinates", () => {
    const svg = renderGeometryToSVG({
      canvas: { width: Number.NaN, height: 50 },
      categories: [{ index: 1, name: "x", color: "#222222" }],
      sections: [
        {
          key: "S",
          name: "S",
          rows: [
            {
              key: "R",
              name: "R",
              seats: [
                {
                  key: "S|R|1",
                  number: "1",
                  x: Number.POSITIVE_INFINITY,
                  y: 5,
                  radius: 2,
                  category_index: 1,
                },
              ],
            },
          ],
        },
      ],
    });
    expect(svg).toContain('viewBox="0 0 0 50"');
    expect(svg).toContain('cx="0"');
  });
});

describe("resolveCurrentVersionNumber", () => {
  const basePlan: SeatingPlan = {
    id: "01929d0e-0e47-7000-8000-000000000301",
    venue_id: "01929d0e-0e47-7000-8000-000000000201",
    owner_org_id: "01929d0e-0e47-7000-8000-000000000001",
    name: "Main Hall",
    plan_type: "assigned_seats",
    visibility: "private",
    status: "draft",
    source_seating_plan_id: null,
    current_version_id: null,
    current_version_number: null,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  it("returns null when the plan has no current version", () => {
    expect(resolveCurrentVersionNumber(basePlan)).toBeNull();
    // A version number without a version id is nonsensical — the id is
    // the source of truth for "does a version exist".
    expect(
      resolveCurrentVersionNumber({ ...basePlan, current_version_number: 3 }),
    ).toBeNull();
  });

  it("returns the payload's current_version_number", () => {
    const plan: SeatingPlan = {
      ...basePlan,
      current_version_id: "01929d0e-0e47-7000-8000-000000000401",
      current_version_number: 4,
    };
    expect(resolveCurrentVersionNumber(plan)).toBe(4);
  });

  it("falls back to 1 when the server omits current_version_number", () => {
    const { current_version_number: _omitted, ...withoutNumber } = {
      ...basePlan,
      current_version_id: "01929d0e-0e47-7000-8000-000000000401",
    };
    expect(resolveCurrentVersionNumber(withoutNumber)).toBe(1);
    expect(
      resolveCurrentVersionNumber({
        ...basePlan,
        current_version_id: "01929d0e-0e47-7000-8000-000000000401",
        current_version_number: null,
      }),
    ).toBe(1);
  });

  it("falls back to 1 on malformed version numbers", () => {
    for (const bad of [0, -2, 1.5, Number.NaN]) {
      expect(
        resolveCurrentVersionNumber({
          ...basePlan,
          current_version_id: "01929d0e-0e47-7000-8000-000000000401",
          current_version_number: bad,
        }),
      ).toBe(1);
    }
  });
});

describe("parseVersionValidationErrors", () => {
  it("returns issues from a 422 details.errors array", () => {
    const err = new ApiError(422, {
      code: "seating_plan.version_validation_failed",
      message: "invalid geometry",
      details: {
        errors: [
          { code: "seat_not_circle", element: "rect#seat-1", detail: "seats must be circles" },
          { code: "missing_row_label" },
        ],
      },
    });
    const issues = parseVersionValidationErrors(err);
    expect(issues).toHaveLength(2);
    expect(issues[0]).toEqual({
      code: "seat_not_circle",
      element: "rect#seat-1",
      detail: "seats must be circles",
    });
    expect(issues[1]).toEqual({
      code: "missing_row_label",
      element: undefined,
      detail: undefined,
    });
  });

  it("returns [] for non-422 errors", () => {
    const err = new ApiError(400, {
      code: "seating_plan.version_body_invalid",
      message: "bad body",
      details: { errors: [{ code: "x" }] },
    });
    expect(parseVersionValidationErrors(err)).toEqual([]);
  });

  it("returns [] when details.errors is absent or malformed", () => {
    const noErrors = new ApiError(422, {
      code: "seating_plan.version_validation_failed",
      message: "x",
    });
    expect(parseVersionValidationErrors(noErrors)).toEqual([]);

    const nonArray = new ApiError(422, {
      code: "seating_plan.version_validation_failed",
      message: "x",
      details: { errors: "oops" as unknown as never },
    });
    expect(parseVersionValidationErrors(nonArray)).toEqual([]);
  });

  it("drops non-object / missing-code entries", () => {
    const err = new ApiError(422, {
      code: "seating_plan.version_validation_failed",
      message: "x",
      details: {
        errors: [
          null,
          "not-an-object",
          { code: 5 }, // wrong type
          { code: "kept" },
        ],
      },
    });
    const issues = parseVersionValidationErrors(err);
    expect(issues).toHaveLength(1);
    expect(issues[0]?.code).toBe("kept");
  });
});

describe("validateCreatePlanForm", () => {
  it("returns null when name and ownerOrgID are both non-empty", () => {
    expect(validateCreatePlanForm("Main Floor", "org-uuid-1234")).toBeNull();
  });

  it("returns an error when name is empty string", () => {
    const msg = validateCreatePlanForm("", "org-uuid-1234");
    expect(msg).not.toBeNull();
    expect(msg).toContain("name");
  });

  it("returns an error when name is whitespace-only", () => {
    const msg = validateCreatePlanForm("   ", "org-uuid-1234");
    expect(msg).not.toBeNull();
    expect(msg).toContain("name");
  });

  it("returns an error when ownerOrgID is empty string", () => {
    const msg = validateCreatePlanForm("Main Floor", "");
    expect(msg).not.toBeNull();
    expect(msg).toContain("organization");
  });

  it("returns an error when ownerOrgID is whitespace-only", () => {
    const msg = validateCreatePlanForm("Main Floor", "   ");
    expect(msg).not.toBeNull();
    expect(msg).toContain("organization");
  });

  it("checks name before ownerOrgID (name error has priority)", () => {
    // Both empty — should report name error first
    const msg = validateCreatePlanForm("", "");
    expect(msg).not.toBeNull();
    expect(msg).toContain("name");
  });
});

describe("createPlanFormIssues", () => {
  it("returns no issues when both requirements are met", () => {
    expect(createPlanFormIssues("Main Floor", "org-uuid-1234")).toEqual([]);
  });

  it("reports EVERY unmet requirement, not just the first", () => {
    // AB-25a: the form must be able to explain all of its blockers at once.
    const issues = createPlanFormIssues("", "");
    expect(issues).toHaveLength(2);
    expect(issues.map((i) => i.field)).toEqual(["name", "owner_org_id"]);
  });

  it("treats whitespace-only values as missing", () => {
    expect(createPlanFormIssues("   ", "   ")).toHaveLength(2);
  });

  it("reports only the unmet requirement when the other is satisfied", () => {
    const nameOnly = createPlanFormIssues("", "org-uuid-1234");
    expect(nameOnly).toHaveLength(1);
    expect(nameOnly[0]?.field).toBe("name");

    const orgOnly = createPlanFormIssues("Main Floor", "");
    expect(orgOnly).toHaveLength(1);
    expect(orgOnly[0]?.field).toBe("owner_org_id");
  });

  it("agrees with validateCreatePlanForm's single-message projection", () => {
    const issues = createPlanFormIssues("", "");
    expect(validateCreatePlanForm("", "")).toBe(issues[0]?.message);
    expect(validateCreatePlanForm("Main Floor", "org-1")).toBeNull();
  });
});

describe("issueForField", () => {
  it("returns the message attached to the requested field", () => {
    const issues = createPlanFormIssues("", "");
    expect(issueForField(issues, "name")).toContain("name");
    expect(issueForField(issues, "owner_org_id")).toContain("organization");
  });

  it("returns null for a field with no issue", () => {
    const issues = createPlanFormIssues("Main Floor", "");
    expect(issueForField(issues, "name")).toBeNull();
    expect(issueForField(issues, "owner_org_id")).not.toBeNull();
  });
});

describe("parseStandingCapacity (AB-25b)", () => {
  it("treats a blank field as absent so the server default applies", () => {
    expect(parseStandingCapacity("")).toEqual({ value: undefined });
    expect(parseStandingCapacity("   ")).toEqual({ value: undefined });
  });

  it("accepts zero and positive whole numbers", () => {
    expect(parseStandingCapacity("0")).toEqual({ value: 0 });
    expect(parseStandingCapacity("120")).toEqual({ value: 120 });
    expect(parseStandingCapacity(" 45 ")).toEqual({ value: 45 });
  });

  it("rejects non-integers, negatives, and junk", () => {
    expect(parseStandingCapacity("12.5")).toBeNull();
    expect(parseStandingCapacity("-1")).toBeNull();
    expect(parseStandingCapacity("1e3")).toBeNull();
    expect(parseStandingCapacity("many")).toBeNull();
  });
});

describe("buildCreateVersionBody (AB-25b)", () => {
  it("sends svg plus the uploaded asset id", () => {
    const body = buildCreateVersionBody({
      svg: "<svg/>",
      svgAssetMediaID: "media-uuid",
      capacityStanding: undefined,
    });
    expect(body).toEqual({ svg: "<svg/>", svg_asset_media_id: "media-uuid" });
  });

  it("includes capacity_standing only when supplied", () => {
    const body = buildCreateVersionBody({
      svg: "<svg/>",
      svgAssetMediaID: "media-uuid",
      capacityStanding: 120,
    });
    expect(body).toEqual({
      svg: "<svg/>",
      svg_asset_media_id: "media-uuid",
      capacity_standing: 120,
    });
  });

  it("sends an explicit zero when the operator typed one", () => {
    const body = buildCreateVersionBody({
      svg: "<svg/>",
      svgAssetMediaID: "media-uuid",
      capacityStanding: 0,
    });
    expect(body.capacity_standing).toBe(0);
  });

  it("never sends geometry alongside svg", () => {
    // The handler rejects a body carrying both (400 version_body_invalid).
    const body = buildCreateVersionBody({
      svg: "<svg/>",
      svgAssetMediaID: "media-uuid",
      capacityStanding: 1,
    });
    expect(body).not.toHaveProperty("geometry");
  });

  it("never sends capacity_seated, which the server derives", () => {
    const body = buildCreateVersionBody({
      svg: "<svg/>",
      svgAssetMediaID: "media-uuid",
      capacityStanding: 1,
    });
    expect(body).not.toHaveProperty("capacity_seated");
  });
});

describe("formatVersionTimestamp (AB-25c)", () => {
  it("renders a locale-independent UTC stamp", () => {
    expect(formatVersionTimestamp("2026-07-30T10:00:00Z")).toBe(
      "2026-07-30 10:00 UTC",
    );
  });

  it("normalises an offset timestamp to UTC", () => {
    expect(formatVersionTimestamp("2026-07-30T12:00:00+02:00")).toBe(
      "2026-07-30 10:00 UTC",
    );
  });

  it("passes through an unparseable value rather than rendering NaN", () => {
    expect(formatVersionTimestamp("not-a-date")).toBe("not-a-date");
  });
});

describe("resolveSelectedVersion (AB-25c)", () => {
  const plan: SeatingPlan = {
    id: "01929d0e-0e47-7000-8000-000000000301",
    venue_id: "01929d0e-0e47-7000-8000-000000000201",
    owner_org_id: "01929d0e-0e47-7000-8000-000000000001",
    name: "Main Hall",
    plan_type: "assigned_seats",
    visibility: "private",
    status: "draft",
    source_seating_plan_id: null,
    current_version_id: "ver-2",
    current_version_number: 2,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };

  const mkVersion = (id: string, n: number): SeatingPlanVersion => ({
    id,
    seating_plan_id: plan.id,
    version_number: n,
    geometry: {},
    geometry_checksum: "sum",
    svg_asset_media_id: null,
    capacity_seated: n * 10,
    capacity_standing: 0,
    locked_at: null,
    created_at: "2026-01-01T00:00:00Z",
  });

  const versions = [mkVersion("ver-3", 3), mkVersion("ver-2", 2), mkVersion("ver-1", 1)];

  it("returns null when the plan has no versions", () => {
    expect(resolveSelectedVersion(plan, [], null)).toBeNull();
    expect(resolveSelectedVersion(plan, [], "ver-2")).toBeNull();
  });

  it("defaults to the plan's current version", () => {
    expect(resolveSelectedVersion(plan, versions, null)?.id).toBe("ver-2");
  });

  it("honours an explicit operator selection", () => {
    expect(resolveSelectedVersion(plan, versions, "ver-1")?.id).toBe("ver-1");
  });

  it("falls back to the current version when the selection no longer exists", () => {
    // e.g. a stale id left over from a plan whose history was refetched.
    expect(resolveSelectedVersion(plan, versions, "ver-gone")?.id).toBe("ver-2");
  });

  it("resolves by current_version_number when the id does not match a row", () => {
    const legacy: SeatingPlan = { ...plan, current_version_id: "unknown-id" };
    expect(resolveSelectedVersion(legacy, versions, null)?.version_number).toBe(2);
  });

  it("falls back to the newest version when the pointer is unresolvable", () => {
    const orphan: SeatingPlan = {
      ...plan,
      current_version_id: "unknown-id",
      current_version_number: 99,
    };
    expect(resolveSelectedVersion(orphan, versions, null)?.id).toBe("ver-3");
  });
});
