/**
 * Locale utilities for the Arena Tickets widget.
 */

export const SUPPORTED_LOCALES = ['en', 'ru', 'de', 'fr', 'es', 'uk', 'he'] as const;
export type SupportedLocale = (typeof SUPPORTED_LOCALES)[number];

/**
 * Locales that use right-to-left text direction (ISO 639-1 codes).
 * Used to set `dir="rtl"` on the widget host element.
 */
export const RTL_LOCALES = ['he', 'ar', 'fa', 'ur'] as const;
export type RtlLocale = (typeof RTL_LOCALES)[number];

/**
 * Returns true when the given locale code requires right-to-left layout.
 * Checks the first two characters of the locale (language subtag).
 *
 * @example
 * isRtlLocale('he')     // true
 * isRtlLocale('he-IL')  // true
 * isRtlLocale('en')     // false
 */
export function isRtlLocale(locale: string): boolean {
  const lang = locale.trim().toLowerCase().slice(0, 2);
  return (RTL_LOCALES as readonly string[]).some((rtl) => rtl === lang);
}

/**
 * Normalise a raw locale string from an HTML attribute.
 * Returns 'en' for absent / blank values.
 * Lowercases and trims; truncates to first 5 chars (e.g. 'en-US' → 'en-us').
 */
export function parseLocale(raw: string | null | undefined): string {
  if (!raw || raw.trim() === '') return 'en';
  return raw.trim().toLowerCase().slice(0, 5);
}

/**
 * Normalise a raw feed-token attribute value.
 * Returns empty string when absent.
 */
export function parseFeedToken(raw: string | null | undefined): string {
  return raw?.trim() ?? '';
}

/**
 * Normalise a raw session-id attribute value.
 * Returns empty string when absent.
 */
export function parseSessionId(raw: string | null | undefined): string {
  return raw?.trim() ?? '';
}

/**
 * Build a CSS variable declaration string from a theme map.
 * Used to inject custom CSS properties into the Shadow DOM host.
 *
 * @param vars - Record of CSS variable name to value, e.g. { '--arena-accent': '#e11d48' }
 * @returns A valid inline style fragment like '--arena-accent:#e11d48;'
 */
export function buildThemeStyle(vars: Record<string, string>): string {
  return Object.entries(vars)
    .filter(([k, v]) => k.startsWith('--') && v.trim() !== '')
    .map(([k, v]) => `${k}:${v}`)
    .join(';');
}

/**
 * CSS variable names that the widget honours for theming.
 *
 * All variables are defined on the `:host` element (Shadow DOM) and cascade
 * into the widget's internals. Set them on the `<arena-tickets>` element or
 * any ancestor via CSS:
 *
 * ```css
 * arena-tickets {
 *   --arena-accent: #e11d48;
 *   --arena-radius: 12px;
 * }
 * ```
 */
export const THEME_CSS_VARS = [
  /** Widget font family (default: system-ui, -apple-system, sans-serif). */
  '--arena-font-family',
  /** Primary text colour (default: #1a1a1a). */
  '--arena-color-primary',
  /** Secondary / muted text colour (default: #6b7280). */
  '--arena-color-secondary',
  /** Widget background colour (default: transparent). */
  '--arena-bg',
  /** Accent / brand colour used for buttons, links, and selected states (default: #6366f1). */
  '--arena-accent',
  /** Border radius for cards, buttons, and inputs (default: 8px). */
  '--arena-radius',
  /** Border and divider colour (default: #e5e7eb). */
  '--arena-border-color',
  /** Focus ring colour — defaults to the accent colour when unset. */
  '--arena-focus-ring',
] as const;

export type ThemeCssVar = (typeof THEME_CSS_VARS)[number];
