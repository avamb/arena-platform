/**
 * Languages of the event center: en, ru, es from day one. Hebrew joins
 * with its own catalog; `dir` already follows the language, so a
 * right-to-left catalog needs no layout change.
 */
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { en, type MessageKey } from "@/lib/i18n/en";
import { es } from "@/lib/i18n/es";
import { ru } from "@/lib/i18n/ru";

export const LOCALES = ["en", "ru", "es"] as const;
export type Locale = (typeof LOCALES)[number];

export const CATALOGS: Record<Locale, Record<MessageKey, string>> = { en, ru, es };

/** Name of each language in that language, for the switcher. */
export const LOCALE_NAMES: Record<Locale, string> = { en: "English", ru: "Русский", es: "Español" };

const RTL_LOCALES: readonly string[] = ["he", "ar"];
const STORAGE_KEY = "arena.events.locale";

export function isLocale(value: string): value is Locale {
  return (LOCALES as readonly string[]).includes(value);
}

/** Picks the first supported language of the browser list, else English. */
export function pickLocale(candidates: readonly string[]): Locale {
  for (const c of candidates) {
    const base = c.toLowerCase().split("-")[0];
    if (isLocale(base)) return base;
  }
  return "en";
}

export type Params = Record<string, string | number>;

/** Replaces `{name}` placeholders. A missing value stays visible as `{name}`. */
export function interpolate(text: string, params?: Params): string {
  if (params === undefined) return text;
  return text.replace(/\{([a-zA-Z_]+)\}/g, (match, name: string) =>
    Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : match,
  );
}

export function translate(locale: Locale, key: MessageKey, params?: Params): string {
  return interpolate(CATALOGS[locale][key] ?? en[key], params);
}

function initialLocale(): Locale {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored !== null && isLocale(stored)) return stored;
  } catch {
    // Storage can be blocked. The browser language decides then.
  }
  if (typeof navigator !== "undefined") {
    return pickLocale(navigator.languages ?? [navigator.language]);
  }
  return "en";
}

export interface I18n {
  locale: Locale;
  setLocale: (l: Locale) => void;
  t: (key: MessageKey, params?: Params) => string;
}

const Ctx = createContext<I18n | null>(null);

export function I18nProvider({ children, locale: fixed }: { children: ReactNode; locale?: Locale }) {
  const [locale, setLocaleState] = useState<Locale>(fixed ?? initialLocale);
  const setLocale = useCallback((l: Locale) => {
    setLocaleState(l);
    try {
      localStorage.setItem(STORAGE_KEY, l);
    } catch {
      // The choice then lasts until the page reloads.
    }
  }, []);
  useEffect(() => {
    if (typeof document === "undefined") return;
    document.documentElement.lang = locale;
    document.documentElement.dir = RTL_LOCALES.includes(locale) ? "rtl" : "ltr";
  }, [locale]);
  const value = useMemo<I18n>(
    () => ({ locale, setLocale, t: (key, params) => translate(locale, key, params) }),
    [locale, setLocale],
  );
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useI18n(): I18n {
  const v = useContext(Ctx);
  if (v === null) throw new Error("useI18n outside I18nProvider");
  return v;
}

export type { MessageKey };
