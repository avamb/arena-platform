/**
 * Translations of an event's texts (spec 63 section 16): the rows of the language table and the rules of the
 * buttons. No React here.
 *
 * A language row is one of `supported_locales` except the language of the original (`content_locale`); the
 * languages of the organization's sites come first. A translation is "ok", "stale" (the original changed after it
 * was made, it is still served to buyers) or "none". A translation made by hand is never replaced by the machine
 * (owner decision 2026-11-22): the machine job skips it, and the screen never offers to overwrite it.
 */
import type { ContentTranslationItem, ContentTranslationList } from "@/lib/api/types";

export type TranslationState = "ok" | "stale" | "none";

export interface TranslationRow {
  locale: string;
  state: TranslationState;
  /** Who wrote the stored translation; null when there is none. */
  source: "manual" | "machine" | null;
  /** True for a language of one of the organization's sites. */
  site: boolean;
  item: ContentTranslationItem | null;
}

export function translationRows(list: ContentTranslationList): TranslationRow[] {
  const site = new Set(list.site_locales);
  const byLocale = new Map(list.translations.map((x) => [x.locale, x]));
  const targets = list.supported_locales.filter((l) => l !== list.content_locale);
  const ordered = [...targets.filter((l) => site.has(l)), ...targets.filter((l) => !site.has(l))];
  return ordered.map((locale) => {
    const item = byLocale.get(locale) ?? null;
    return {
      locale,
      state: item === null ? "none" : item.translation_stale ? "stale" : "ok",
      source: item === null ? null : item.source,
      site: site.has(locale),
      item,
    };
  });
}

/** The languages the machine button translates: the languages of the sites, else the chosen one, else none. */
export function machineTargets(list: ContentTranslationList, chosen: string | null): string[] {
  const rows = translationRows(list).map((r) => r.locale);
  const site = list.site_locales.filter((l) => rows.includes(l));
  if (site.length > 0) return site;
  return chosen !== null && rows.includes(chosen) ? [chosen] : [];
}

/** The language name in the language of the screen, with the code; the code alone when the browser has no name. */
export function languageLabel(code: string, uiLocale: string): string {
  try {
    const name = new Intl.DisplayNames([uiLocale], { type: "language" }).of(code);
    if (name !== undefined && name !== code) return `${name} (${code})`;
  } catch {
    // An unknown code: show it as it is.
  }
  return code;
}

/** An empty or blank text goes to the server as null (it clears that text). */
export function blankToNull(value: string): string | null {
  return value.trim() === "" ? null : value;
}

export interface TranslationDraft {
  name: string;
  short: string;
  description: string;
  report: string;
}

export function draftOf(item: ContentTranslationItem | null): TranslationDraft {
  return {
    name: item?.name ?? "",
    short: item?.short_description ?? "",
    description: item?.description_html ?? "",
    report: item?.report_text_html ?? "",
  };
}

/** The body of PUT .../translations/{locale}: the row is replaced as a whole. */
export function draftBody(d: TranslationDraft) {
  return {
    name: blankToNull(d.name.trim()),
    short_description: blankToNull(d.short.trim()),
    description_html: blankToNull(d.description),
    report_text_html: blankToNull(d.report),
  };
}

export function sameDraft(a: TranslationDraft, b: TranslationDraft): boolean {
  return a.name.trim() === b.name.trim() && a.short.trim() === b.short.trim() && a.description === b.description && a.report === b.report;
}

/** The original texts as a draft, for "put the original text into the editor" (the name stays as it is). */
export function originalDraft(list: ContentTranslationList, current: TranslationDraft): TranslationDraft {
  return {
    name: current.name,
    short: list.original.short_description ?? "",
    description: list.original.description_html ?? "",
    report: list.original.report_text_html ?? "",
  };
}
