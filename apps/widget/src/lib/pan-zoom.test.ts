/**
 * pan-zoom.test.ts — unit tests for pure pan/zoom transform utilities.
 */

import { describe, it, expect } from 'vitest';
import {
  IDENTITY,
  MIN_SCALE,
  MAX_SCALE,
  toCSSTransform,
  toSVGTransform,
  fitTransform,
  wheelZoom,
  pan,
  pinchZoom,
  pointerDist,
  pointerMid,
} from './pan-zoom.js';

// ─── Serialisation ────────────────────────────────────────────────────────────

describe('toCSSTransform', () => {
  it('formats identity as no-op translate + scale 1', () => {
    expect(toCSSTransform(IDENTITY)).toBe('translate(0px, 0px) scale(1)');
  });

  it('formats translated/scaled state', () => {
    expect(toCSSTransform({ x: 50, y: -20, scale: 2 })).toBe(
      'translate(50px, -20px) scale(2)',
    );
  });
});

describe('toSVGTransform', () => {
  it('formats identity', () => {
    expect(toSVGTransform(IDENTITY)).toBe('translate(0 0) scale(1)');
  });

  it('formats translated/scaled state', () => {
    expect(toSVGTransform({ x: 10, y: 20, scale: 1.5 })).toBe(
      'translate(10 20) scale(1.5)',
    );
  });
});

// ─── fitTransform ─────────────────────────────────────────────────────────────

describe('fitTransform', () => {
  it('returns IDENTITY for zero canvas dimensions', () => {
    expect(fitTransform(0, 600, 800, 600)).toEqual(IDENTITY);
    expect(fitTransform(800, 0, 800, 600)).toEqual(IDENTITY);
  });

  it('returns IDENTITY for zero container dimensions', () => {
    expect(fitTransform(800, 600, 0, 600)).toEqual(IDENTITY);
    expect(fitTransform(800, 600, 800, 0)).toEqual(IDENTITY);
  });

  it('scales down when canvas is larger than container', () => {
    const result = fitTransform(1600, 1200, 800, 600, 0);
    expect(result.scale).toBeCloseTo(0.5, 5);
  });

  it('centers the result in the container', () => {
    // Square canvas, landscape container → centered horizontally.
    const result = fitTransform(400, 400, 800, 400, 0);
    // scale = min(800/400, 400/400) = 1; x = (800 - 400*1)/2 = 200; y = 0
    expect(result.scale).toBeCloseTo(1, 5);
    expect(result.x).toBeCloseTo(200, 5);
    expect(result.y).toBeCloseTo(0, 5);
  });

  it('respects padding parameter', () => {
    const noPad = fitTransform(800, 600, 800, 600, 0);
    const withPad = fitTransform(800, 600, 800, 600, 8);
    // With padding the scale must be smaller.
    expect(withPad.scale).toBeLessThan(noPad.scale);
  });

  it('uses default padding of 8', () => {
    const result = fitTransform(800, 600, 800, 600);
    const withPad = fitTransform(800, 600, 800, 600, 8);
    expect(result.scale).toBeCloseTo(withPad.scale, 5);
  });
});

// ─── wheelZoom ────────────────────────────────────────────────────────────────

describe('wheelZoom', () => {
  it('zooms in when deltaY is negative', () => {
    const result = wheelZoom(IDENTITY, -100, 0, 0);
    expect(result.scale).toBeGreaterThan(1);
  });

  it('zooms out when deltaY is positive', () => {
    const result = wheelZoom(IDENTITY, 100, 0, 0);
    expect(result.scale).toBeLessThan(1);
  });

  it('clamps scale to MIN_SCALE', () => {
    let state = IDENTITY;
    for (let i = 0; i < 100; i++) state = wheelZoom(state, 100, 0, 0);
    expect(state.scale).toBeGreaterThanOrEqual(MIN_SCALE);
  });

  it('clamps scale to MAX_SCALE', () => {
    let state = IDENTITY;
    for (let i = 0; i < 100; i++) state = wheelZoom(state, -100, 0, 0);
    expect(state.scale).toBeLessThanOrEqual(MAX_SCALE);
  });

  it('zooms around pointer position (pointer at origin)', () => {
    // Pointer at (0,0) — translate should remain (0,0) on zoom.
    const result = wheelZoom({ x: 0, y: 0, scale: 1 }, -100, 0, 0);
    expect(result.x).toBeCloseTo(0, 5);
    expect(result.y).toBeCloseTo(0, 5);
  });

  it('adjusts translate when zooming around off-center pointer', () => {
    const result = wheelZoom({ x: 0, y: 0, scale: 1 }, -100, 100, 100);
    // After zoom in, the point (100,100) should still be under the pointer.
    expect(result.x).not.toBeCloseTo(0, 1); // should shift
  });
});

// ─── pan ─────────────────────────────────────────────────────────────────────

describe('pan', () => {
  it('translates by dx, dy', () => {
    const result = pan(IDENTITY, 10, 20);
    expect(result.x).toBe(10);
    expect(result.y).toBe(20);
    expect(result.scale).toBe(1);
  });

  it('accumulates translations', () => {
    const s1 = pan(IDENTITY, 10, 0);
    const s2 = pan(s1, 5, 3);
    expect(s2.x).toBe(15);
    expect(s2.y).toBe(3);
  });

  it('does not change scale', () => {
    const state = { x: 0, y: 0, scale: 2 };
    const result = pan(state, 10, 10);
    expect(result.scale).toBe(2);
  });
});

// ─── pinchZoom ────────────────────────────────────────────────────────────────

describe('pinchZoom', () => {
  it('zooms in when distance increases', () => {
    const result = pinchZoom(IDENTITY, 100, 200, 400, 300);
    expect(result.scale).toBeGreaterThan(1);
  });

  it('zooms out when distance decreases', () => {
    const result = pinchZoom(IDENTITY, 200, 100, 400, 300);
    expect(result.scale).toBeLessThan(1);
  });

  it('returns unchanged state when prevDist is 0', () => {
    const result = pinchZoom(IDENTITY, 0, 200, 400, 300);
    expect(result).toEqual(IDENTITY);
  });

  it('clamps scale to MIN_SCALE', () => {
    let state = IDENTITY;
    for (let i = 0; i < 100; i++) state = pinchZoom(state, 1000, 1, 400, 300);
    expect(state.scale).toBeGreaterThanOrEqual(MIN_SCALE);
  });

  it('clamps scale to MAX_SCALE', () => {
    let state = IDENTITY;
    for (let i = 0; i < 100; i++) state = pinchZoom(state, 1, 1000, 400, 300);
    expect(state.scale).toBeLessThanOrEqual(MAX_SCALE);
  });
});

// ─── pointerDist ─────────────────────────────────────────────────────────────

describe('pointerDist', () => {
  it('returns 0 for same point', () => {
    expect(pointerDist({ x: 5, y: 5 }, { x: 5, y: 5 })).toBe(0);
  });

  it('returns correct 3-4-5 distance', () => {
    expect(pointerDist({ x: 0, y: 0 }, { x: 3, y: 4 })).toBeCloseTo(5, 5);
  });

  it('is symmetric', () => {
    const a = { x: 10, y: 20 };
    const b = { x: 30, y: 40 };
    expect(pointerDist(a, b)).toBeCloseTo(pointerDist(b, a), 10);
  });
});

// ─── pointerMid ──────────────────────────────────────────────────────────────

describe('pointerMid', () => {
  it('returns midpoint of two points', () => {
    const mid = pointerMid({ x: 0, y: 0 }, { x: 10, y: 10 });
    expect(mid.x).toBeCloseTo(5, 5);
    expect(mid.y).toBeCloseTo(5, 5);
  });

  it('handles negative coordinates', () => {
    const mid = pointerMid({ x: -10, y: -20 }, { x: 10, y: 20 });
    expect(mid.x).toBeCloseTo(0, 5);
    expect(mid.y).toBeCloseTo(0, 5);
  });
});
