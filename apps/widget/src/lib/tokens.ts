/**
 * Arena Tickets Widget — CSS custom-property design token system.
 *
 * This module documents every CSS custom property (design token) that the
 * widget exposes for theming. All tokens are resolved on the `:host` Shadow
 * DOM root and cascade into sub-components. Host-page styles that set these
 * properties on `<arena-tickets>` or any ancestor are honoured automatically.
 *
 * # Usage
 *
 * ```css
 * arena-tickets {
 *   --arena-accent:       #e11d48;   /* rose brand *\/
 *   --arena-radius:       12px;
 *   --arena-bg:           #fff1f2;
 *   --arena-border-color: #fecdd3;
 * }
 * ```
 *
 * Or via inline style attribute:
 * ```html
 * <arena-tickets style="--arena-accent:#e11d48; --arena-radius:12px;"></arena-tickets>
 * ```
 *
 * # RTL Support
 *
 * Add `dir="rtl"` to the `<arena-tickets>` element (or any ancestor `<html>`)
 * to activate right-to-left layout:
 *
 * ```html
 * <arena-tickets locale="he" dir="rtl" feed-token="…"></arena-tickets>
 * <!-- or let the widget set it automatically from locale="he" -->
 * ```
 *
 * # WCAG 2.2 AA Compliance
 *
 * All default token values are chosen to meet WCAG 2.2 AA contrast requirements:
 *  - `--arena-color-primary` (#1a1a1a) on white → ≥18:1
 *  - `--arena-color-secondary` (#6b7280) on white → ≥4.6:1
 *  - White text on `--arena-accent` (#4f46e5) → ≥6.3:1
 *  - `--arena-focus-ring` provides a clearly visible 3px offset outline
 *
 * When overriding token values, ensure the new combination maintains at least
 * 4.5:1 contrast ratio for text (3:1 for large text and UI components).
 */

// ─── Token Definitions ────────────────────────────────────────────────────────

/** Default value for every CSS custom property the widget exposes. */
export const TOKEN_DEFAULTS = {
  /**
   * Font family applied to all widget text.
   * Falls back to the system UI font stack.
   * @default system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif
   */
  '--arena-font-family': "system-ui, -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",

  /**
   * Primary text colour. Must achieve ≥4.5:1 contrast against `--arena-bg`.
   * @default #1a1a1a
   */
  '--arena-color-primary': '#1a1a1a',

  /**
   * Secondary / muted text colour (labels, hints, captions).
   * Must achieve ≥4.5:1 contrast against `--arena-bg`.
   * @default #6b7280
   */
  '--arena-color-secondary': '#6b7280',

  /**
   * Widget background colour.
   * Use `transparent` to inherit the host-page background.
   * @default transparent
   */
  '--arena-bg': 'transparent',

  /**
   * Accent / brand colour used for:
   *  - Primary action buttons (background)
   *  - Selected session chip (background)
   *  - Active focus rings
   *  - Hovered chip borders
   *
   * White text is rendered on this colour; ensure ≥4.5:1 contrast.
   * @default #4f46e5  (indigo-600)
   */
  '--arena-accent': '#4f46e5',

  /**
   * Border radius applied to cards, buttons, inputs, and chip elements.
   * Set to `0` for a sharp-cornered look; increase for a pill style.
   * @default 8px
   */
  '--arena-radius': '8px',

  /**
   * Colour used for borders and dividers.
   * @default #e5e7eb  (gray-200)
   */
  '--arena-border-color': '#e5e7eb',

  /**
   * Focus ring colour rendered as a 3px offset outline on all interactive
   * elements. Defaults to the accent colour when unset.
   *
   * To customise focus rings without changing the accent colour:
   * ```css
   * arena-tickets { --arena-focus-ring: #0ea5e9; }
   * ```
   * @default var(--arena-accent, #4f46e5)
   */
  '--arena-focus-ring': 'var(--arena-accent, #4f46e5)',
} as const satisfies Record<string, string>;

/** Union of every design-token property name. */
export type DesignToken = keyof typeof TOKEN_DEFAULTS;

// ─── Token Groups ─────────────────────────────────────────────────────────────

/** Tokens that control typography. */
export const TYPOGRAPHY_TOKENS: ReadonlyArray<DesignToken> = [
  '--arena-font-family',
  '--arena-color-primary',
  '--arena-color-secondary',
] as const;

/** Tokens that control colour / palette. */
export const COLOR_TOKENS: ReadonlyArray<DesignToken> = [
  '--arena-bg',
  '--arena-accent',
  '--arena-border-color',
  '--arena-focus-ring',
] as const;

/** Tokens that control shape / spacing. */
export const SHAPE_TOKENS: ReadonlyArray<DesignToken> = [
  '--arena-radius',
] as const;

/** All token names in a flat array — useful for iteration. */
export const ALL_TOKENS: ReadonlyArray<DesignToken> = [
  ...TYPOGRAPHY_TOKENS,
  ...COLOR_TOKENS,
  ...SHAPE_TOKENS,
] as const;

// ─── Pre-built Theme Presets ─────────────────────────────────────────────────

/**
 * Indigo theme preset (matches the default accent colour).
 * Apply via: `arena-tickets { ...arenaThemeIndigo }` or spread into a style attr.
 */
export const arenaThemeIndigo: Partial<Record<DesignToken, string>> = {
  '--arena-accent': '#4f46e5',
  '--arena-bg': '#eef2ff',
  '--arena-radius': '12px',
  '--arena-border-color': '#c7d2fe',
  '--arena-color-primary': '#312e81',
} as const;

/**
 * Rose theme preset.
 */
export const arenaThemeRose: Partial<Record<DesignToken, string>> = {
  '--arena-accent': '#e11d48',
  '--arena-bg': '#fff1f2',
  '--arena-border-color': '#fecdd3',
  '--arena-color-primary': '#881337',
} as const;

/**
 * Neutral / greyscale theme preset — suitable for contexts where brand
 * colour should not appear (e.g. a white-label embed).
 */
export const arenaThemeNeutral: Partial<Record<DesignToken, string>> = {
  '--arena-accent': '#374151',
  '--arena-bg': '#f9fafb',
  '--arena-border-color': '#d1d5db',
  '--arena-color-primary': '#111827',
  '--arena-color-secondary': '#6b7280',
  '--arena-radius': '6px',
} as const;

/** All built-in theme presets keyed by name. */
export const ARENA_THEMES = {
  indigo: arenaThemeIndigo,
  rose: arenaThemeRose,
  neutral: arenaThemeNeutral,
} as const;

export type ArenaThemeName = keyof typeof ARENA_THEMES;
