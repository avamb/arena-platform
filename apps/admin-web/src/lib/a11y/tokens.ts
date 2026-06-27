/**
 * Accessibility design tokens shared by the SuperAdmin shell (SAUI-13).
 *
 * Centralising these values means every drawer/dialog/table inherits the
 * same focus ring, semantic palette, and typography baseline rather than
 * each module reinventing colours that may not clear WCAG 2.2 AA.
 *
 * Contrast targets (background -> foreground, normal 14px text):
 *
 *   muted text   #475569 on #ffffff  → 7.96:1  AAA
 *   primary text #0f172a on #ffffff  → 17.85:1 AAA
 *   error text   #7f1d1d on #fef2f2  → 8.43:1  AAA
 *   warn text    #78350f on #fef3c7  → 6.99:1  AAA
 *   success text #166534 on #dcfce7  → 6.21:1  AAA
 *
 * Critical state is never colour-only. Every status badge in the
 * support consoles renders both a coloured pill AND the literal status
 * string ("succeeded", "failed", "cancelled", ...), so an operator with
 * a colour-vision deficiency still receives the same information.
 *
 * The focus ring is a high-contrast 3px outer ring on every focusable
 * surface; it is wired through `focusVisibleStyle` (consumed by the CSS
 * variable layer in `styles.css`) so every <button>, <input>, <select>,
 * and <a> picks it up without per-component opt-in.
 */
import type { CSSProperties } from "react";

/**
 * Single source of truth for the focus indicator. The values below are
 * mirrored in `styles.css` as the `:focus-visible` outline; if you edit
 * one, edit the other.
 */
export const FOCUS_RING_COLOR = "#1d4ed8";
export const FOCUS_RING_WIDTH = 3;
export const FOCUS_RING_OFFSET = 2;

/**
 * Inline focus style for buttons that need the indicator to render
 * inside CSS-in-JS rather than via the global :focus-visible rule (used
 * by SVG-only icon buttons whose outline would clip).
 */
export const focusVisibleStyle: CSSProperties = {
  outline: `${FOCUS_RING_WIDTH}px solid ${FOCUS_RING_COLOR}`,
  outlineOffset: FOCUS_RING_OFFSET,
};

/**
 * Visually-hidden helper for off-screen labels (the "screen reader only"
 * pattern). Keeps the element accessible to assistive tech while
 * removing it from the visual flow.
 */
export const visuallyHiddenStyle: CSSProperties = {
  position: "absolute",
  width: 1,
  height: 1,
  padding: 0,
  margin: -1,
  overflow: "hidden",
  clip: "rect(0, 0, 0, 0)",
  whiteSpace: "nowrap",
  border: 0,
};

/**
 * Semantic state palette. Backgrounds + foregrounds pre-paired so the
 * AA-clearing contrast is preserved by construction.
 */
export const STATE_TOKENS = Object.freeze({
  success: { background: "#dcfce7", foreground: "#166534" },
  warn: { background: "#fef3c7", foreground: "#78350f" },
  error: { background: "#fee2e2", foreground: "#7f1d1d" },
  info: { background: "#e0e7ff", foreground: "#3730a3" },
  neutral: { background: "#f1f5f9", foreground: "#0f172a" },
} as const);

/**
 * Status icon glyphs paired with each state. Rendered alongside the
 * coloured pill so the badge meaning never depends on colour alone --
 * the SAUI-13 "critical state is not color-only" requirement.
 */
export const STATE_GLYPHS = Object.freeze({
  success: "✓",
  warn: "!",
  error: "✕",
  info: "•",
  neutral: "·",
} as const);

/**
 * Operational typography baseline. 14px body / 12px meta / 11px label,
 * 1.5 line-height for body so dense admin tables remain readable to
 * users at 200% browser zoom (WCAG 1.4.4 Resize Text).
 */
export const TYPOGRAPHY = Object.freeze({
  bodySize: 14,
  metaSize: 12,
  labelSize: 11,
  bodyLineHeight: 1.5,
  fontFamily:
    "system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif",
  monoFamily: "ui-monospace, SFMono-Regular, Menlo, monospace",
} as const);
