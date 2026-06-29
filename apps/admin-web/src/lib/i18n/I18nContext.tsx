/**
 * React context + provider for SuperAdmin UI i18n (Feature #251).
 *
 * - Default locale: ru (per Feature #251 / spec).
 * - Persists selected locale in localStorage under `arena.admin.locale`.
 * - `useTranslation()` returns `{ t, locale, setLocale, locales }`.
 * - Components consume `t(key, params?)` to render translated strings.
 *
 * No external i18n framework is used to keep the bundle small and
 * deterministic. The shape mirrors react-i18next's `useTranslation`
 * for easy migration if/when we adopt that library.
 */

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import {
  DEFAULT_LOCALE,
  SUPPORTED_LOCALES,
  isLocaleCode,
  translate,
  type LocaleCode,
} from "@/lib/i18n/catalog";

const STORAGE_KEY = "arena.admin.locale";

export interface TranslationApi {
  /** Current active locale code. */
  readonly locale: LocaleCode;
  /** Translate a key with optional `{param}` interpolation. */
  readonly t: (
    key: string,
    params?: Readonly<Record<string, string | number>>,
  ) => string;
  /** Switch to a new locale; persisted to localStorage. */
  readonly setLocale: (next: LocaleCode) => void;
  /** Available locales (RU/EN). */
  readonly locales: readonly LocaleCode[];
}

const I18nContext = createContext<TranslationApi | null>(null);

/** Read the initial locale: localStorage > default. */
export function readInitialLocale(
  storage?: Pick<Storage, "getItem">,
): LocaleCode {
  const store =
    storage ??
    (typeof window === "undefined" ? undefined : window.localStorage);
  if (store === undefined) {
    return DEFAULT_LOCALE;
  }
  try {
    const stored = store.getItem(STORAGE_KEY);
    if (isLocaleCode(stored)) {
      return stored;
    }
  } catch {
    // ignore storage errors (e.g., privacy mode)
  }
  return DEFAULT_LOCALE;
}

/** Persist a locale to storage. Silently swallows storage errors. */
export function persistLocale(
  locale: LocaleCode,
  storage?: Pick<Storage, "setItem">,
): void {
  const store =
    storage ??
    (typeof window === "undefined" ? undefined : window.localStorage);
  if (store === undefined) {
    return;
  }
  try {
    store.setItem(STORAGE_KEY, locale);
  } catch {
    // ignore (e.g., quota / privacy mode)
  }
}

export interface I18nProviderProps {
  readonly children: ReactNode;
  /** Optional initial locale override (primarily for tests). */
  readonly initialLocale?: LocaleCode;
}

export function I18nProvider({
  children,
  initialLocale,
}: I18nProviderProps): JSX.Element {
  const [locale, setLocaleState] = useState<LocaleCode>(
    () => initialLocale ?? readInitialLocale(),
  );

  // Sync <html lang="..."> for accessibility + SEO crawlers.
  useEffect(() => {
    if (typeof document !== "undefined") {
      document.documentElement.lang = locale;
    }
  }, [locale]);

  const setLocale = useCallback((next: LocaleCode) => {
    setLocaleState(next);
    persistLocale(next);
  }, []);

  const t = useCallback(
    (
      key: string,
      params?: Readonly<Record<string, string | number>>,
    ) => translate(locale, key, params),
    [locale],
  );

  const value = useMemo<TranslationApi>(
    () => ({
      locale,
      t,
      setLocale,
      locales: SUPPORTED_LOCALES,
    }),
    [locale, t, setLocale],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

/**
 * Access the translation API. Returns a no-op fallback (resolving against
 * the default locale) when no I18nProvider is mounted, so leaf components
 * never throw in isolated tests.
 */
export function useTranslation(): TranslationApi {
  const ctx = useContext(I18nContext);
  if (ctx !== null) {
    return ctx;
  }
  return {
    locale: DEFAULT_LOCALE,
    t: (key, params) => translate(DEFAULT_LOCALE, key, params),
    setLocale: () => {
      // no-op; provider not mounted
    },
    locales: SUPPORTED_LOCALES,
  };
}

/** Test-only helper exposed for unit tests. */
export const __INTERNAL__ = {
  STORAGE_KEY,
};
