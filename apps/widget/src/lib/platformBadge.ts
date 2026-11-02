/**
 * platformBadge.ts — the small "Tickets: Arena Sold Out" line under the
 * widget (spec 08_architecture/52, owner decision В-2).
 *
 * Every widget shows it, unless the event's brand (its promoter, else the
 * organization) pays for white label: then the feed session says
 * `white_label: true` and the line is left out. An older backend sends no
 * flag, so the line is shown.
 *
 * The brand is the same for every date of one event, so a single session
 * with the flag hides the line for the whole widget.
 */

import type { FeedSession } from '../types.js';

/** Where the line leads. */
export const PLATFORM_BADGE_URL = 'https://arenasoldout.com';

export interface PlatformBadgeStrings {
  /** The words before the platform's name, e.g. "Tickets:". */
  prefix: string;
  /** Screen-reader hint: the link opens in a new tab. */
  new_tab: string;
}

export const PLATFORM_BADGE_I18N: Record<string, PlatformBadgeStrings> = {
  en: { prefix: 'Tickets:', new_tab: 'opens in a new tab' },
  ru: { prefix: 'Билеты:', new_tab: 'откроется в новой вкладке' },
  cs: { prefix: 'Vstupenky:', new_tab: 'otevře se na nové kartě' },
  he: { prefix: 'כרטיסים:', new_tab: 'ייפתח בכרטיסייה חדשה' },
  es: { prefix: 'Entradas:', new_tab: 'se abre en una pestaña nueva' },
  de: { prefix: 'Tickets:', new_tab: 'öffnet sich in einem neuen Tab' },
  fr: { prefix: 'Billets :', new_tab: "s'ouvre dans un nouvel onglet" },
};

/** The badge words for a locale ("es-ES" -> es), English when the language has none. */
export function getPlatformBadgeStrings(locale: string): PlatformBadgeStrings {
  const lang = (locale ?? '').trim().toLowerCase().split(/[-_]/)[0] ?? '';
  return PLATFORM_BADGE_I18N[lang] ?? PLATFORM_BADGE_I18N.en!;
}

/**
 * Whether the platform line is shown: false only when a session of the event
 * says white_label is exactly true.
 */
export function showPlatformBadge(
  sessions: ReadonlyArray<Pick<FeedSession, 'white_label'>> | null | undefined,
  selected?: Pick<FeedSession, 'white_label'> | null,
): boolean {
  if (selected?.white_label === true) return false;
  return !(sessions ?? []).some((s) => s?.white_label === true);
}
