/**
 * Page statistics of an event's hosted page (task 2-3, HP-2a).
 *
 * The beacon counts people in `hits` and probable bots in `suspect_hits`. The
 * screen shows everything that happened (hits + suspect_hits) and says how
 * many of it looks like bots, the same wording as the bot's card line. The
 * figures are a guide for the organizer, not proof for an advertising account.
 */
import type { PageStatsCounts, PageStatsResponse } from "@/lib/api/types";

export interface KindFigure {
  /** Everything counted: people and probable bots. */
  readonly total: number;
  /** How many of `total` look like bots. */
  readonly suspect: number;
}

export function kindFigure(c: PageStatsCounts): KindFigure {
  return { total: c.hits + c.suspect_hits, suspect: c.suspect_hits };
}

/** True when the page was opened, played or clicked at least once in the window. */
export function hasPageStats(stats: PageStatsResponse): boolean {
  const t = stats.totals;
  return kindFigure(t.page_view).total + kindFigure(t.video_play).total + kindFigure(t.partner_click).total > 0;
}

export interface SourceRow {
  readonly key: string;
  /** Empty when the visit came without utm values. */
  readonly source: string;
  readonly campaign: string;
  readonly figure: KindFigure;
}

/** The utm table rows: biggest first (the API's order), the no-utm pair last. */
export function sourceRows(stats: PageStatsResponse): SourceRow[] {
  const rows = stats.sources.map((s) => ({
    key: `${s.utm_source}|${s.utm_campaign}`,
    source: s.utm_source,
    campaign: s.utm_campaign,
    figure: kindFigure({ hits: s.hits, suspect_hits: s.suspect_hits }),
  }));
  const direct = rows.filter((r) => r.source === "" && r.campaign === "");
  return [...rows.filter((r) => r.source !== "" || r.campaign !== ""), ...direct];
}
