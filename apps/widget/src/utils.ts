/**
 * Locale utilities for the Arena Tickets widget.
 */

// Single source of truth: the supported-locale set lives next to the
// translation tables in lib/checkout.ts — a locale is "supported" iff it has
// a complete translation table there.
export { SUPPORTED_LOCALES, type SupportedLocale } from './lib/checkout.js';

/**
 * Supported locales that use right-to-left text direction (ISO 639-1 codes).
 * Used to set `dir="rtl"` on the widget host element.
 * Per spec, Hebrew is the only RTL locale the widget ships translations for.
 */
export const RTL_LOCALES = ['he'] as const;
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
