import { describe, expect, it } from "vitest";
import { smallHall } from "@/lib/seatManager/hall.fixture";
import {
  buildHallIndex,
  normalizeRect,
  pointInPolygon,
  rowRange,
  rowSeatsOf,
  sectionSeatsOf,
  seatAt,
  seatsInPolygon,
  seatsInRect,
} from "@/lib/seatManager/model";

const hall = buildHallIndex(smallHall());

describe("hall index", () => {
  it("indexes seats, rows and sections", () => {
    expect(hall.seats).toHaveLength(11);
    expect(hall.rows).toHaveLength(3);
    expect(hall.sections.map((s) => s.name)).toEqual(["Parter", "Balcony"]);
  });

  it("keeps two rows with the same key in two sections apart", () => {
    const ids = hall.rows.map((r) => r.id);
    expect(new Set(ids).size).toBe(3);
    expect(hall.rows.filter((r) => r.key === "1")).toHaveLength(2);
  });

  it("puts the row handle left of the first seat", () => {
    const row = hall.rowById.get(hall.rowOfSeat.get("A|1|1")!)!;
    expect(row.handle.x).toBeLessThan(130);
    expect(row.handle.y).toBe(100);
  });

  it("measures the gap between row handles", () => {
    expect(hall.minHandleGap).toBe(40);
  });
});

describe("picking", () => {
  it("finds the seat under a point, with a slack for a finger", () => {
    expect(seatAt(hall, { x: 130, y: 100 })?.key).toBe("A|1|1");
    expect(seatAt(hall, { x: 130, y: 112 })).toBeNull();
    expect(seatAt(hall, { x: 130, y: 112 }, 6)?.key).toBe("A|1|1");
  });

  it("picks a whole row and a whole section", () => {
    expect(rowSeatsOf(hall, "A|2|3")).toEqual(["A|2|1", "A|2|2", "A|2|3", "A|2|4"]);
    expect(sectionSeatsOf(hall, "B|1|2")).toHaveLength(3);
    expect(sectionSeatsOf(hall, "A|1|1")).toHaveLength(8);
  });

  it("picks a rectangle by seat centres", () => {
    const keys = seatsInRect(hall, normalizeRect({ x: 190, y: 90 }, { x: 125, y: 150 }));
    expect(keys.sort()).toEqual(["A|1|1", "A|1|2", "A|1|3", "A|2|1", "A|2|2", "A|2|3"]);
  });

  it("picks a lasso, and needs three points", () => {
    const tri = [
      { x: 100, y: 90 },
      { x: 200, y: 90 },
      { x: 100, y: 110 },
    ];
    expect(seatsInPolygon(hall, tri)).toContain("A|1|1");
    expect(seatsInPolygon(hall, tri.slice(0, 2))).toEqual([]);
    expect(pointInPolygon({ x: 1, y: 1 }, tri)).toBe(false);
  });

  it("picks a range of rows inside one section only", () => {
    expect(rowRange(hall, hall.rowOfSeat.get("A|1|1")!, hall.rowOfSeat.get("A|2|1")!)).toHaveLength(8);
    expect(rowRange(hall, hall.rowOfSeat.get("A|1|1")!, hall.rowOfSeat.get("B|1|1")!)).toEqual([]);
  });
});
