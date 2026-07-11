/**
 * Locale utilities for the Arena Tickets widget.
 */

export const SUPPORTED_LOCALES = ['en', 'ru', 'de', 'fr', 'es', 'uk'] as const;
export type SupportedLocale = (typeof SUPPORTED_LOCALES)[number];

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
 */
export const THEME_CSS_VARS = [
  '--arena-font-family',
  '--arena-color-primary',
  '--arena-color-secondary',
  '--arena-bg',
  '--arena-accent',
  '--arena-radius',
  '--arena-border-color',
] as const;

export type ThemeCssVar = (typeof THEME_CSS_VARS)[number];
