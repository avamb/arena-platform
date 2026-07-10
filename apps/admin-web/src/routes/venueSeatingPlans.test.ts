/**
 * Unit tests for pure helpers in the venue Seating-plans drawer (feature #315).
 *
 * We pin the client-side geometry->SVG renderer and the 422 error parser
 * so a regression surfaces without needing to boot the DOM.
 */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/client";
import {
  parseVersionValidationErrors,
  renderGeometryToSVG,
  type SeatingGeometry,
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

  it("omits decor_svg by default (opts.includeDecor=false)", () => {
    const svg = renderGeometryToSVG({
      canvas: { width: 10, height: 10 },
      decor_svg: '<rect data-danger="x"/>',
      sections: [],
    });
    expect(svg).not.toContain("data-danger");
  });

  it("emits decor_svg only when explicitly opted in", () => {
    const svg = renderGeometryToSVG(
      {
        canvas: { width: 10, height: 10 },
        decor_svg: '<rect x="0"/>',
        sections: [],
      },
      { includeDecor: true },
    );
    expect(svg).toContain('data-role="decor"');
    expect(svg).toContain('<rect x="0"/>');
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
