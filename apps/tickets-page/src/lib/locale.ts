/**
 * locale.ts — resolves the page's active locale, mirroring the widget's
 * supported set (apps/widget/src/utils.ts `parseLocale`: en, ru, cs, he).
 *
 * Precedence: `?lang=` query parameter -> `navigator.language` (mapped onto
 * the supported set by matching the language subtag, e.g. "ru-RU" -> "ru")
 * -> "en".
 */

export const SUPPORTED_LOCALES = ['en', 'ru', 'cs', 'he'] as const;
export type SupportedLocale = (typeof SUPPORTED_LOCALES)[number];

const DEFAULT_LOCALE: SupportedLocale = 'en';

const RTL_LOCALES: ReadonlySet<SupportedLocale> = new Set(['he']);

export function isSupportedLocale(value: string): value is SupportedLocale {
  return (SUPPORTED_LOCALES as readonly string[]).includes(value);
}

export function isRtlLocale(locale: SupportedLocale): boolean {
  return RTL_LOCALES.has(locale);
}

/** Extracts the language subtag ("ru-RU" -> "ru") and lower-cases it. */
function languageSubtag(tag: string): string {
  return tag.split('-')[0]?.toLowerCase() ?? '';
}

/**
 * resolveLocale picks the active locale from (in order): the `lang` query
 * parameter, the browser's language preference list, then the default.
 * Never throws — an unrecognized/malformed value at any step is skipped.
 */
export function resolveLocale(
  search: string | URLSearchParams,
  navigatorLanguages: readonly string[] = [],
): SupportedLocale {
  const params = typeof search === 'string' ? new URLSearchParams(search) : search;
  const queryLang = params.get('lang');
  if (queryLang) {
    const subtag = languageSubtag(queryLang);
    if (isSupportedLocale(subtag)) return subtag;
  }

  for (const lang of navigatorLanguages) {
    const subtag = languageSubtag(lang);
    if (isSupportedLocale(subtag)) return subtag;
  }

  return DEFAULT_LOCALE;
}
