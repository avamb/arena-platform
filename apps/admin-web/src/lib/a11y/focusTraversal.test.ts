/**
 * Pure-logic tests for the SAUI-13 focus-traversal helpers.
 *
 * The hooks layer (`./hooks.ts`) is intentionally a thin React wrapper
 * around these helpers, so exercising the helpers gives us deterministic
 * coverage of the keyboard contract without requiring a DOM environment.
 */
import { describe, expect, it } from "vitest";
import {
  FOCUSABLE_SELECTOR,
  isElementFocusable,
  isEscapeKey,
  isTabKey,
  nextTrapIndex,
  type FocusableLike,
} from "./focusTraversal";

describe("FOCUSABLE_SELECTOR", () => {
  it("includes the canonical interactive elements", () => {
    expect(FOCUSABLE_SELECTOR).toContain("a[href]");
    expect(FOCUSABLE_SELECTOR).toContain("button:not([disabled])");
    expect(FOCUSABLE_SELECTOR).toContain("input:not([disabled])");
    expect(FOCUSABLE_SELECTOR).toContain("select:not([disabled])");
    expect(FOCUSABLE_SELECTOR).toContain("textarea:not([disabled])");
    expect(FOCUSABLE_SELECTOR).toContain("[tabindex]:not([tabindex='-1'])");
  });

  it("excludes disabled controls via :not", () => {
    // The selector relies on :not([disabled]) -- if someone removes it
    // we would silently start trapping focus on disabled buttons. Pin
    // both the input and button rules here to fail loudly on regression.
    expect(FOCUSABLE_SELECTOR).toContain("button:not([disabled])");
    expect(FOCUSABLE_SELECTOR).toContain("input:not([disabled])");
  });

  it("excludes negative tabindex", () => {
    expect(FOCUSABLE_SELECTOR).toMatch(/\[tabindex\]:not\(\[tabindex='-1'\]\)/);
  });
});

describe("isElementFocusable", () => {
  it("treats a bare element as focusable", () => {
    const el: FocusableLike = {};
    expect(isElementFocusable(el)).toBe(true);
  });

  it("rejects elements with explicit negative tabindex", () => {
    expect(isElementFocusable({ tabIndex: -1 })).toBe(false);
  });

  it("accepts tabIndex 0 and positive values", () => {
    expect(isElementFocusable({ tabIndex: 0 })).toBe(true);
    expect(isElementFocusable({ tabIndex: 2 })).toBe(true);
  });

  it("rejects elements carrying the inert attribute", () => {
    const el: FocusableLike = {
      hasAttribute: (name) => name === "inert",
    };
    expect(isElementFocusable(el)).toBe(false);
  });

  it("ignores hasAttribute checks for unrelated attributes", () => {
    const el: FocusableLike = {
      hasAttribute: (name) => name === "data-testid",
    };
    expect(isElementFocusable(el)).toBe(true);
  });

  it("rejects elements with null offsetParent (display:none ancestor)", () => {
    expect(isElementFocusable({ offsetParent: null })).toBe(false);
  });

  it("treats elements without an offsetParent key as focusable (SSR)", () => {
    // The test runtime is Node and the shim object literal does not
    // include the offsetParent key at all -- this models server-side
    // rendering where offsetParent is undefined. We should fall through
    // to focusable rather than excluding everything.
    const el: FocusableLike = { tabIndex: 0 };
    expect(isElementFocusable(el)).toBe(true);
  });

  it("tolerates a defined offsetParent value", () => {
    expect(isElementFocusable({ offsetParent: {} })).toBe(true);
  });
});

describe("nextTrapIndex", () => {
  const six = ["a", "b", "c", "d", "e", "f"] as const;

  it("returns -1 for an empty trap (forward or backward)", () => {
    expect(nextTrapIndex([], -1, false)).toBe(-1);
    expect(nextTrapIndex([], -1, true)).toBe(-1);
    expect(nextTrapIndex([], 3, false)).toBe(-1);
  });

  it("focuses the first element when current is outside (forward)", () => {
    expect(nextTrapIndex(six, -1, false)).toBe(0);
  });

  it("focuses the last element when current is outside (backward)", () => {
    expect(nextTrapIndex(six, -1, true)).toBe(six.length - 1);
  });

  it("advances forward one step", () => {
    expect(nextTrapIndex(six, 0, false)).toBe(1);
    expect(nextTrapIndex(six, 3, false)).toBe(4);
  });

  it("wraps forward from the last element to the first", () => {
    expect(nextTrapIndex(six, six.length - 1, false)).toBe(0);
  });

  it("advances backward one step", () => {
    expect(nextTrapIndex(six, 4, true)).toBe(3);
    expect(nextTrapIndex(six, 1, true)).toBe(0);
  });

  it("wraps backward from the first element to the last", () => {
    expect(nextTrapIndex(six, 0, true)).toBe(six.length - 1);
  });

  it("treats current >= length as wrap forward", () => {
    expect(nextTrapIndex(six, six.length, false)).toBe(0);
    expect(nextTrapIndex(six, six.length + 5, false)).toBe(0);
  });

  it("handles a single-element trap (forward + backward wrap to same)", () => {
    expect(nextTrapIndex(["only"], 0, false)).toBe(0);
    expect(nextTrapIndex(["only"], 0, true)).toBe(0);
  });
});

describe("isEscapeKey", () => {
  it("accepts the modern DOM Level 3 spelling", () => {
    expect(isEscapeKey("Escape")).toBe(true);
  });
  it("accepts the legacy IE/Edge alias", () => {
    expect(isEscapeKey("Esc")).toBe(true);
  });
  it("rejects unrelated keys", () => {
    expect(isEscapeKey("Enter")).toBe(false);
    expect(isEscapeKey("Tab")).toBe(false);
    expect(isEscapeKey("e")).toBe(false);
    expect(isEscapeKey("")).toBe(false);
  });
});

describe("isTabKey", () => {
  it("accepts Tab", () => {
    expect(isTabKey("Tab")).toBe(true);
  });
  it("rejects unrelated keys", () => {
    expect(isTabKey("Escape")).toBe(false);
    expect(isTabKey(" ")).toBe(false);
    expect(isTabKey("ArrowDown")).toBe(false);
  });
});
