/**
 * locale.ts — resolves the page's active locale.
 *
 * `toWidgetLocale` is the one place that maps a page locale onto the set the
 * <arena-tickets> widget is told about — never hand-roll that mapping
 * elsewhere. `es` is passed through even though the widget has no complete
 * Spanish UI table yet: it falls back to English for the strings it lacks,
 * exactly as a customer embed with locale="es" does, but the legal wording
 * around the pay button (apps/widget/src/lib/checkout.ts PAYMENT_COMMIT_I18N)
 * is picked by language subtag and must reach a Spanish buyer in Spanish.
 *
 * Precedence: `?lang=` query parameter -> the remembered explicit choice
 * (see main.ts) -> `navigator.language` (mapped onto
 * the supported set by matching the language subtag, e.g. "ru-RU" -> "ru")
 * -> "en".
 */

export const PAGE_LOCALES = ['en', 'ru', 'cs', 'he', 'es'] as const;
export type PageLocale = (typeof PAGE_LOCALES)[number];

/** The locales handed to the <arena-tickets> widget. `es` has no full UI
 * table in the widget (English fallback) but does have its own payment and
 * withdrawal wording. */
export const WIDGET_LOCALES = ['en', 'ru', 'cs', 'he', 'es'] as const;
export type WidgetLocale = (typeof WIDGET_LOCALES)[number];

const DEFAULT_LOCALE: PageLocale = 'en';

const RTL_LOCALES: ReadonlySet<PageLocale> = new Set(['he']);

export function isSupportedLocale(value: string): value is PageLocale {
  return (PAGE_LOCALES as readonly string[]).includes(value);
}

export function isRtlLocale(locale: PageLocale): boolean {
  return RTL_LOCALES.has(locale);
}

/** Maps a page locale onto the set handed to the widget. Every page locale
 * is passed through today; a future page-only locale falls back to `en`. Single source of truth for this mapping
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
  remembered: string | null = null,
): PageLocale {
  const params = typeof search === 'string' ? new URLSearchParams(search) : search;
  const queryLang = params.get('lang');
  if (queryLang) {
    const subtag = languageSubtag(queryLang);
    if (isSupportedLocale(subtag)) return subtag;
  }

  // The language the buyer last chose explicitly. The payment provider sends
  // the buyer back to a URL arena builds, and arena strips every query
  // parameter but its own checkout token from it — so `?lang=ru` does not
  // survive the round trip and the order page would flip to the browser's
  // language mid-purchase.
  if (remembered) {
    const subtag = languageSubtag(remembered);
    if (isSupportedLocale(subtag)) return subtag;
  }

  for (const lang of navigatorLanguages) {
    const subtag = languageSubtag(lang);
    if (isSupportedLocale(subtag)) return subtag;
  }

  return DEFAULT_LOCALE;
}
