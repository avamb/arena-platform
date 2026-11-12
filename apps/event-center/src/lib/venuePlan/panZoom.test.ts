import { describe, expect, it } from "vitest";
import { MAX_SCALE, RESET, clampView, isReset, panBy, transformOf, wheelFactor, zoomAt, zoomStep } from "@/lib/venuePlan/panZoom";

const box = { w: 400, h: 300 };

describe("zoomAt", () => {
  it("keeps the point under the cursor where it is", () => {
    const v = zoomAt(RESET, 2, 100, 50, box);
    expect(v.scale).toBe(2);
    // The content point under (100, 50) was (100, 50): it must still show at (100, 50).
    expect(100 * v.scale + v.x).toBeCloseTo(100);
    expect(50 * v.scale + v.y).toBeCloseTo(50);
  });

  it("never goes below 1 or above the maximum", () => {
    expect(zoomAt(RESET, 0.1, 10, 10, box)).toEqual(RESET);
    expect(zoomAt({ scale: MAX_SCALE, x: -100, y: -100 }, 2, 10, 10, box).scale).toBe(MAX_SCALE);
  });

  it("zooming out to 1 returns to the whole content", () => {
    const zoomed = zoomAt(RESET, 3, 390, 290, box);
    expect(zoomAt(zoomed, 1 / 3, 200, 150, box)).toEqual(RESET);
  });
});

describe("buttons and wheel", () => {
  it("+ zooms in around the middle and - zooms out", () => {
    const up = zoomStep(RESET, 1, box);
    expect(up.scale).toBeCloseTo(1.5);
    expect(200 * up.scale + up.x).toBeCloseTo(200);
    expect(zoomStep(up, -1, box)).toEqual(RESET);
  });

  it("a wheel turn up zooms in, down zooms out", () => {
    expect(wheelFactor(-100)).toBeGreaterThan(1);
    expect(wheelFactor(100)).toBeLessThan(1);
    expect(wheelFactor(0)).toBe(1);
  });
});

describe("panBy", () => {
  it("moves a zoomed view and stops at the content's edge", () => {
    const zoomed = { scale: 2, x: 0, y: 0 };
    expect(panBy(zoomed, -50, -20, box)).toEqual({ scale: 2, x: -50, y: -20 });
    expect(panBy(zoomed, 80, 80, box)).toEqual(zoomed);
    expect(panBy(zoomed, -9999, -9999, box)).toEqual({ scale: 2, x: -400, y: -300 });
  });

  it("does not move the whole content", () => {
    expect(panBy(RESET, -30, 30, box)).toEqual(RESET);
  });
});

describe("clampView and the transform", () => {
  it("pulls a lost view back", () => {
    expect(clampView({ scale: 2, x: 500, y: -900 }, box)).toEqual({ scale: 2, x: 0, y: -300 });
    expect(clampView({ scale: 0.2, x: 10, y: 10 }, box)).toEqual(RESET);
  });

  it("writes a CSS transform", () => {
    expect(transformOf({ scale: 2, x: -10, y: -20 })).toBe("translate(-10px, -20px) scale(2)");
    expect(isReset(RESET)).toBe(true);
    expect(isReset({ scale: 2, x: 0, y: 0 })).toBe(false);
  });
});
