/**
 * Barrel export for the SAUI-13 accessibility primitives.
 *
 * Consumers should import from `@/lib/a11y` rather than reaching into
 * `./hooks`, `./tokens`, or `./focusTraversal` directly. The barrel
 * keeps the public surface narrow and makes future refactors painless.
 */
export {
  useEscapeClose,
  useFocusOnMount,
  useFocusRestore,
  useFocusTrap,
} from "./hooks";
export {
  FOCUSABLE_SELECTOR,
  isElementFocusable,
  isEscapeKey,
  isTabKey,
  nextTrapIndex,
  type FocusableLike,
} from "./focusTraversal";
export {
  FOCUS_RING_COLOR,
  FOCUS_RING_WIDTH,
  FOCUS_RING_OFFSET,
  STATE_GLYPHS,
  STATE_TOKENS,
  TYPOGRAPHY,
  focusVisibleStyle,
  visuallyHiddenStyle,
} from "./tokens";
