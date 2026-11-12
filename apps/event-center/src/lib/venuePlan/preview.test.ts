import { describe, expect, it } from "vitest";
import { previewOf } from "@/lib/venuePlan/preview";

const geometry = {
  sections: [
    {
      rows: [
        { seats: [{ x: 0, y: 0 }, { x: 10, y: 0 }] },
        { seats: [{ x: 0, y: 20 }, { x: 10, y: 20 }] },
      ],
    },
  ],
};

describe("previewOf", () => {
  it("draws every seat of a small plan inside a box with a margin", () => {
    const p = previewOf(geometry);
    expect(p?.dots).toHaveLength(4);
    expect(p?.viewBox.x).toBeLessThan(0);
    expect(p?.viewBox.w).toBeGreaterThan(10);
  });

  it("takes a sample of a big plan", () => {
    const seats = Array.from({ length: 3000 }, (_, i) => ({ x: i, y: i % 40 }));
    const p = previewOf({ sections: [{ rows: [{ seats }] }] }, 600);
    expect(p?.dots.length).toBeLessThanOrEqual(600);
    expect(p?.dots.length).toBeGreaterThan(300);
  });

  it("gives nothing for a geometry without seats", () => {
    expect(previewOf(null)).toBeNull();
    expect(previewOf({})).toBeNull();
    expect(previewOf({ sections: [{ rows: [{ seats: [{ x: "1", y: 2 }] }] }] })).toBeNull();
  });
});
