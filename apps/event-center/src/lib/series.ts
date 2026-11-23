/**
 * Pure rules of the tour screens (spec 63, L-2). A tour is an "event series" in the API: several events under one
 * name, one public address and one set of page materials. The screen checks the SHAPE of what a person typed so a
 * typo is caught before a call, and the API decides the rest (a taken address, a picture of another organization).
 * No React here.
 */
import { ApiError } from "@/lib/api/http";
import type { Series, SeriesUpdateRequest } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n";
import { slugFromInput } from "@/lib/promoters";

export const SERIES_NAME_MAX = 200;
export const SERIES_SLUG_MAX = 120;

function runes(s: string): number {
  return [...s].length;
}

/** The address alphabet of a tour: 1-120 characters of a-z, 0-9 and single inner hyphens. */
export function validSeriesSlug(s: string): boolean {
  if (s.length < 1 || s.length > SERIES_SLUG_MAX) return false;
  let prevHyphen = true;
  for (let i = 0; i < s.length; i += 1) {
    const c = s[i];
    if ((c >= "a" && c <= "z") || (c >= "0" && c <= "9")) {
      prevHyphen = false;
    } else if (c === "-") {
      if (prevHyphen || i === s.length - 1) return false;
      prevHyphen = true;
    } else {
      return false;
    }
  }
  return true;
}

const VIDEO_HOSTS = new Set([
  "youtube.com",
  "www.youtube.com",
  "m.youtube.com",
  "youtube-nocookie.com",
  "www.youtube-nocookie.com",
  "youtu.be",
  "vimeo.com",
  "www.vimeo.com",
  "player.vimeo.com",
]);

/** True for an https link of YouTube or Vimeo, the two sites the tour page can show. */
export function validVideoUrl(raw: string): boolean {
  const s = raw.trim();
  if (s === "" || /\s/.test(s)) return false;
  let url: URL;
  try {
    url = new URL(s);
  } catch {
    return false;
  }
  return url.protocol === "https:" && VIDEO_HOSTS.has(url.hostname.toLowerCase());
}

/** What a person edits in the form of a tour. Texts are ART ("" = none). */
export interface SeriesDraft {
  name: string;
  slug: string;
  descriptionHtml: string;
  partnersHtml: string;
  posterMediaId: string | null;
  heroMediaId: string | null;
  videoUrl: string;
  hideChildren: boolean;
  position: string;
}

export function draftOf(s: Series): SeriesDraft {
  return {
    name: s.name,
    slug: s.slug,
    descriptionHtml: s.description_html ?? "",
    partnersHtml: s.partners_text_html ?? "",
    posterMediaId: s.poster_media_id,
    heroMediaId: s.hero_media_id,
    videoUrl: s.hero_video_url ?? "",
    hideChildren: s.hide_children_in_lists,
    position: String(s.position),
  };
}

export type DraftField = "name" | "slug" | "videoUrl" | "position";
export type DraftProblems = Partial<Record<DraftField, MessageKey>>;

export function parsePosition(raw: string): number | null {
  const s = raw.trim();
  if (!/^-?\d{1,6}$/.test(s)) return null;
  return Number(s);
}

/** The reasons a draft cannot be saved, by field. Empty = it can. */
export function draftProblems(d: SeriesDraft): DraftProblems {
  const out: DraftProblems = {};
  const name = d.name.trim();
  if (name === "") out.name = "series.err.nameEmpty";
  else if (runes(name) > SERIES_NAME_MAX) out.name = "series.err.nameLong";
  if (!validSeriesSlug(slugFromInput(d.slug))) out.slug = "series.err.slugInvalid";
  if (d.videoUrl.trim() !== "" && !validVideoUrl(d.videoUrl)) out.videoUrl = "series.err.video";
  if (parsePosition(d.position) === null) out.position = "series.err.position";
  return out;
}

/** ONE PATCH carrying only what changed. A text or a picture that was emptied goes as null (clear). */
export function changesOf(base: Series, d: SeriesDraft): SeriesUpdateRequest {
  const patch: SeriesUpdateRequest = {};
  const name = d.name.trim().split(/\s+/).filter(Boolean).join(" ");
  if (name !== base.name) patch.name = name;
  const slug = slugFromInput(d.slug);
  if (slug !== base.slug) patch.slug = slug;
  if (d.descriptionHtml !== (base.description_html ?? "")) patch.description_html = d.descriptionHtml === "" ? null : d.descriptionHtml;
  if (d.partnersHtml !== (base.partners_text_html ?? "")) patch.partners_text_html = d.partnersHtml === "" ? null : d.partnersHtml;
  if (d.posterMediaId !== base.poster_media_id) patch.poster_media_id = d.posterMediaId;
  if (d.heroMediaId !== base.hero_media_id) patch.hero_media_id = d.heroMediaId;
  const video = d.videoUrl.trim();
  if (video !== (base.hero_video_url ?? "")) patch.hero_video_url = video === "" ? null : video;
  if (d.hideChildren !== base.hide_children_in_lists) patch.hide_children_in_lists = d.hideChildren;
  const position = parsePosition(d.position);
  if (position !== null && position !== base.position) patch.position = position;
  return patch;
}

export function hasChanges(base: Series, d: SeriesDraft): boolean {
  return Object.keys(changesOf(base, d)).length > 0;
}

/** The refusals of POST and PATCH of a tour that have words of their own. */
export function seriesErrorKey(err: unknown): MessageKey | null {
  if (!(err instanceof ApiError)) return null;
  switch (err.code) {
    case "series.slug_taken":
      return "series.err.slugTaken";
    case "series.not_found":
      return "series.err.gone";
    case "series.invalid": {
      const field = err.details?.field;
      if (field === "name") return "series.err.nameEmpty";
      if (field === "slug") return "series.err.slugInvalid";
      if (field === "hero_video_url") return "series.err.video";
      if (field === "poster_media_id" || field === "hero_media_id") return "series.err.media";
      return "series.err.invalid";
    }
  }
  return null;
}

/** The refusal of the event's PATCH when the tour cannot be linked. */
export function eventSeriesErrorKey(err: unknown): MessageKey | null {
  if (err instanceof ApiError && err.code === "event.invalid_series") return "series.err.eventInvalid";
  return seriesErrorKey(err);
}

/** Active tours by position then name, or the archived ones by name. */
export function filterSeries(all: readonly Series[], archived: boolean): Series[] {
  return all
    .filter((s) => (s.archived_at !== null) === archived)
    .sort((a, b) => (archived ? 0 : a.position - b.position) || a.name.localeCompare(b.name));
}

/** The tours an event can be linked to: the active ones, and the tour it is already in (even an archived one) so the select is honest. */
export function linkableSeries(all: readonly Series[], currentId: string | null | undefined): Series[] {
  return all
    .filter((s) => s.archived_at === null || s.id === currentId)
    .sort((a, b) => (a.archived_at === null ? 0 : 1) - (b.archived_at === null ? 0 : 1) || a.position - b.position || a.name.localeCompare(b.name));
}
