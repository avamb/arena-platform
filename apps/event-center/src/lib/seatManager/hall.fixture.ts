import type { Geometry } from "@seatmap/types";

/** A small hall for tests: section A (2 rows of 4), section B (1 row of 3). Seats run left to right. */
export function smallHall(): Geometry {
  const seat = (key: string, number: string, x: number, y: number) => ({ key, number, x, y, radius: 8, category_index: 0 });
  return {
    schema_version: 1,
    canvas: { width: 400, height: 300 },
    categories: [{ index: 0, name: "Plan", color: "#111111" }],
    sections: [
      {
        key: "A",
        name: "Parter",
        rows: [
          { key: "1", name: "1", seats: [1, 2, 3, 4].map((n) => seat(`A|1|${n}`, String(n), 100 + n * 30, 100)) },
          { key: "2", name: "2", seats: [1, 2, 3, 4].map((n) => seat(`A|2|${n}`, String(n), 100 + n * 30, 140)) },
        ],
      },
      {
        key: "B",
        name: "Balcony",
        rows: [{ key: "1", name: "1", seats: [1, 2, 3].map((n) => seat(`B|1|${n}`, String(n), 100 + n * 30, 220)) }],
      },
    ],
    tables: [],
    decor_svg: "",
  };
}
