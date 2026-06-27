/**
 * Pure DOM-free helpers used by the accessibility hooks in
 * `./hooks.ts` (SAUI-13).
 *
 * Centralising these as small, exported functions keeps the focus-trap
 * + escape-close behaviour testable under the project's Node-only Vitest
 * environment: the hooks themselves require React + a DOM, but every
 * decision point is delegated here.
 *
 * The selectors and decision rules are intentionally conservative and
 * mirror the WCAG 2.2 keyboard guidance:
 *
 *   - Hidden (display: none / visibility: hidden), `inert`, and
 *     explicitly negative tabindex elements are NOT in the trap order.
 *   - Disabled form controls are skipped.
 *   - The CSS selector matches the canonical "focusable" set used by
 *     reach/dialog and Radix' FocusScope.
 */
export const FOCUSABLE_SELECTOR =
  [
    "a[href]",
    "area[href]",
    "input:not([disabled]):not([type='hidden'])",
    "select:not([disabled])",
    "textarea:not([disabled])",
    "button:not([disabled])",
    "iframe",
    "object",
    "embed",
    "[contenteditable='true']",
    "[tabindex]:not([tabindex='-1'])",
    "audio[controls]",
    "video[controls]",
    "summary",
    "details > summary:first-of-type",
  ].join(",");

/**
 * Subset of HTMLElement we rely on. Kept narrow so tests can supply a
 * plain object shim under the Node test environment without dragging in
 * jsdom.
 */
export interface FocusableLike {
  readonly tabIndex?: number;
  readonly hasAttribute?: (name: string) => boolean;
  // optional offsetParent — null when the element is detached or hidden
  // via display:none. We only check existence; we never call into it.
  readonly offsetParent?: unknown;
}

/**
 * Decide whether an element should be considered focusable for the
 * tab-trap order.
 *
 * Rules:
 *   - explicit tabIndex < 0 disqualifies, even if the CSS selector
 *     matched the element earlier (think custom buttons with
 *     tabIndex=-1 used for programmatic focus only);
 *   - elements carrying the `inert` boolean attribute are excluded
 *     because their entire subtree is non-interactive;
 *   - elements with no offsetParent are hidden (display:none ancestor)
 *     and excluded -- but we tolerate environments where offsetParent is
 *     not provided (server/test) and assume focusable in that case.
 */
export function isElementFocusable(el: FocusableLike): boolean {
  if (typeof el.tabIndex === "number" && el.tabIndex < 0) {
    return false;
  }
  if (typeof el.hasAttribute === "function" && el.hasAttribute("inert")) {
    return false;
  }
  if ("offsetParent" in el && el.offsetParent === null) {
    return false;
  }
  return true;
}

/**
 * Decide the next focus target inside a trap.
 *
 * @param ordered  the focusable elements in DOM order
 * @param current  the currently focused element, or -1 if focus is
 *                 outside the trap
 * @param backward true when Shift+Tab was pressed
 *
 * Returns the index of the element that should receive focus next.
 * Returns -1 if the trap is empty (the caller should keep focus on the
 * container itself).
 */
export function nextTrapIndex(
  ordered: readonly unknown[],
  current: number,
  backward: boolean,
): number {
  if (ordered.length === 0) {
    return -1;
  }
  if (current < 0) {
    return backward ? ordered.length - 1 : 0;
  }
  if (backward) {
    return current <= 0 ? ordered.length - 1 : current - 1;
  }
  return current >= ordered.length - 1 ? 0 : current + 1;
}

/**
 * True when the key event represents Escape. We accept both the DOM Level 3
 * `key` ("Escape") and the legacy `"Esc"` alias for older browsers that still
 * report it during keyup. We never look at keyCode.
 */
export function isEscapeKey(key: string): boolean {
  return key === "Escape" || key === "Esc";
}

/**
 * True when the key event represents a Tab keypress (with or without
 * Shift). Centralised so we never accidentally pattern-match on
 * `key === "Tab"` in one place and `keyCode === 9` somewhere else.
 */
export function isTabKey(key: string): boolean {
  return key === "Tab";
}
