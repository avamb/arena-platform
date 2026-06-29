/**
 * Admin-web responsive breakpoints (Wave M-1).
 *
 * apps/admin-web does NOT ship Tailwind today (see package.json: only Vite +
 * React + TanStack). To keep parity with the Tailwind defaults referenced
 * in the M-series autoforge backlog and so that designers can reason about
 * a single canonical set of breakpoints, this module exports the same
 * `sm / md / lg / xl` thresholds the rest of the platform docs assume.
 *
 * Single source of truth for viewport reasoning:
 *
 *   sm  >= 640 px   small phones in landscape, large phones in portrait
 *   md  >= 768 px   small tablets in portrait -- **THE DESKTOP/MOBILE CUT**
 *   lg  >= 1024 px  tablets in landscape, small laptops
 *   xl  >= 1280 px  standard desktop, the admin shell's design target
 *
 * The `md` threshold (768 px) is the desktop/mobile cut: at or above `md`
 * surfaces render the full operator chrome (sidebar nav, multi-column
 * tables, right-side drawers). Below `md` surfaces collapse to a stacked
 * single-column layout, table rows become cards, and drawers expand to a
 * full-screen sheet with a back button.
 *
 * Consumers should prefer the named helpers (`MIN_WIDTH_*`, `IS_AT_LEAST_MD`)
 * over raw numeric literals. Tests guard the constants against accidental
 * drift.
 */

export const BREAKPOINT_SM_PX = 640;
export const BREAKPOINT_MD_PX = 768;
export const BREAKPOINT_LG_PX = 1024;
export const BREAKPOINT_XL_PX = 1280;

/**
 * `md` (768 px) is the canonical desktop/mobile boundary for admin-web.
 * Below this threshold the UI collapses into a mobile shell; at or above
 * it the full multi-pane desktop layout is rendered. Wave M-2..M-N tickets
 * audit each route against this cut.
 */
export const DESKTOP_MOBILE_CUT_PX = BREAKPOINT_MD_PX;

export type BreakpointName = "sm" | "md" | "lg" | "xl";

export const BREAKPOINTS: Readonly<Record<BreakpointName, number>> = Object.freeze({
  sm: BREAKPOINT_SM_PX,
  md: BREAKPOINT_MD_PX,
  lg: BREAKPOINT_LG_PX,
  xl: BREAKPOINT_XL_PX,
});

/** `(min-width: 640px)` media query string. */
export const MIN_WIDTH_SM = `(min-width: ${BREAKPOINT_SM_PX}px)`;
/** `(min-width: 768px)` media query string. The desktop/mobile cut. */
export const MIN_WIDTH_MD = `(min-width: ${BREAKPOINT_MD_PX}px)`;
/** `(min-width: 1024px)` media query string. */
export const MIN_WIDTH_LG = `(min-width: ${BREAKPOINT_LG_PX}px)`;
/** `(min-width: 1280px)` media query string. */
export const MIN_WIDTH_XL = `(min-width: ${BREAKPOINT_XL_PX}px)`;

/** Resolve a media query string for a named breakpoint. */
export function minWidthQuery(name: BreakpointName): string {
  return `(min-width: ${BREAKPOINTS[name]}px)`;
}

/**
 * Pure helper: is the given viewport width at or above `md`?
 * Useful in tests and SSR fallbacks where matchMedia is not available.
 */
export function isAtLeast(width: number, name: BreakpointName): boolean {
  return width >= BREAKPOINTS[name];
}
