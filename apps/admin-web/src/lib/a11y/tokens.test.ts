/**
 * SAUI-13 — assert the accessibility token contract.
 *
 * The numbers and palette here are the only knobs the rest of the shell
 * may dial when changing focus visibility or status colours. Pinning
 * them in a test gives the next reviewer a loud trip-wire if the values
 * regress (e.g. someone halves the focus ring width "for aesthetics").
 */
import { describe, expect, it } from "vitest";
import {
  FOCUS_RING_COLOR,
  FOCUS_RING_OFFSET,
  FOCUS_RING_WIDTH,
  STATE_GLYPHS,
  STATE_TOKENS,
  TYPOGRAPHY,
  focusVisibleStyle,
  visuallyHiddenStyle,
} from "./tokens";

describe("focus ring tokens", () => {
  it("pins the high-contrast indicator parameters", () => {
    expect(FOCUS_RING_COLOR).toBe("#1d4ed8");
    expect(FOCUS_RING_WIDTH).toBeGreaterThanOrEqual(2);
    expect(FOCUS_RING_OFFSET).toBeGreaterThanOrEqual(2);
  });

  it("focusVisibleStyle wires the ring into outline (no border) so the layout never shifts", () => {
    expect(focusVisibleStyle.outline).toBe("3px solid #1d4ed8");
    expect(focusVisibleStyle.outlineOffset).toBe(2);
  });
});

describe("visuallyHiddenStyle", () => {
  it("renders the element off-screen without removing it from the accessibility tree", () => {
    expect(visuallyHiddenStyle.position).toBe("absolute");
    expect(visuallyHiddenStyle.width).toBe(1);
    expect(visuallyHiddenStyle.height).toBe(1);
    expect(visuallyHiddenStyle.overflow).toBe("hidden");
    expect(visuallyHiddenStyle.whiteSpace).toBe("nowrap");
    // Critical: it must NOT use display:none -- that would hide it from
    // screen readers as well.
    expect(visuallyHiddenStyle.display).toBeUndefined();
  });
});

describe("STATE_TOKENS palette", () => {
  it("pairs a background with a foreground for every semantic state", () => {
    for (const [name, pair] of Object.entries(STATE_TOKENS)) {
      expect(pair.background).toMatch(/^#[0-9a-f]{6}$/i);
      expect(pair.foreground).toMatch(/^#[0-9a-f]{6}$/i);
      expect(pair.background.toLowerCase()).not.toBe(
        pair.foreground.toLowerCase(),
      );
      // Guard against silent renames that drop a state from the palette.
      expect(["success", "warn", "error", "info", "neutral"]).toContain(name);
    }
  });

  it("covers every state the support consoles need", () => {
    expect(Object.keys(STATE_TOKENS).sort()).toEqual([
      "error",
      "info",
      "neutral",
      "success",
      "warn",
    ]);
  });

  it("is frozen so a renderer cannot accidentally mutate the palette at runtime", () => {
    expect(Object.isFrozen(STATE_TOKENS)).toBe(true);
  });
});

describe("STATE_GLYPHS", () => {
  it("provides a non-colour glyph for every state", () => {
    for (const key of Object.keys(STATE_TOKENS)) {
      expect(
        STATE_GLYPHS[key as keyof typeof STATE_GLYPHS],
      ).toBeDefined();
    }
  });
  it("is frozen", () => {
    expect(Object.isFrozen(STATE_GLYPHS)).toBe(true);
  });
});

describe("TYPOGRAPHY", () => {
  it("keeps body text >= 14px so dense tables clear WCAG 1.4.4 at 200% zoom", () => {
    expect(TYPOGRAPHY.bodySize).toBeGreaterThanOrEqual(14);
  });
  it("uses a comfortable line-height for dense screens", () => {
    expect(TYPOGRAPHY.bodyLineHeight).toBeGreaterThanOrEqual(1.4);
  });
  it("declares system + monospace font stacks", () => {
    expect(TYPOGRAPHY.fontFamily).toContain("system-ui");
    expect(TYPOGRAPHY.monoFamily).toContain("ui-monospace");
  });
  it("is frozen", () => {
    expect(Object.isFrozen(TYPOGRAPHY)).toBe(true);
  });
});
