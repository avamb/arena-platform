/**
 * locale.ts — resolves the page's active locale.
 *
 * The PAGE's own supported set is wider than the embedded widget's: `es`
 * was added for a Spain-based organizer's promoter page even though the
 * <arena-tickets> widget (apps/widget/src/utils.ts `parseLocale`) only
 * understands en/ru/cs/he. `toWidgetLocale` is the one place that maps a
 * page locale onto the widget's smaller set — never hand-roll that mapping
 * elsewhere.
 *
 * Precedence: `?lang=` query parameter -> `navigator.language` (mapped onto
 * the supported set by matching the language subtag, e.g. "ru-RU" -> "ru")
 * -> "en".
 */

export const PAGE_LOCALES = ['en', 'ru', 'cs', 'he', 'es'] as const;
export type PageLocale = (typeof PAGE_LOCALES)[number];

/** The <arena-tickets> widget's own supported set — do not widen this
 * without widening the widget itself first. */
export const WIDGET_LOCALES = ['en', 'ru', 'cs', 'he'] as const;
export type WidgetLocale = (typeof WIDGET_LOCALES)[number];

const DEFAULT_LOCALE: PageLocale = 'en';

const RTL_LOCALES: ReadonlySet<PageLocale> = new Set(['he']);

export function isSupportedLocale(value: string): value is PageLocale {
  return (PAGE_LOCALES as readonly string[]).includes(value);
}

export function isRtlLocale(locale: PageLocale): boolean {
  return RTL_LOCALES.has(locale);
}

/** Maps a page locale onto the widget's supported set. `es` (and any other
 * future page-only locale) falls back to `en`; every widget-supported
 * locale passes through unchanged. Single source of truth for this mapping
 * — the widget's own locale attribute must always be set through this. */
export function toWidgetLocale(locale: PageLocale): WidgetLocale {
  return (WIDGET_LOCALES as readonly string[]).includes(locale) ? (locale as WidgetLocale) : 'en';
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
): PageLocale {
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
