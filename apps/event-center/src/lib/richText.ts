/**
 * Formatted texts in the web (spec 44, RT-11): the description of an event,
 * the organizer's text of the buyer letters and the message about a moved or
 * canceled session are kept as "text Arena" (ART), the small HTML subset the
 * server's `richtext.Sanitize` accepts. This file is the ONE place where the
 * web counts and converts them, with the editor library's DOM-light entry
 * (`@arena/rich-editor/text`, no editor inside): the limits are the profile's
 * VISIBLE characters, never the length of the HTML.
 *
 * `measure`, `sanitize` and `artToPlainText` read HTML through a template
 * element, so they need a DOM (a browser or jsdom); a text the library refuses
 * as hostile (too large, nested too deeply) counts as "over every limit".
 */
import { PROFILES, artToPlainText, fromLegacy, measure, type ProfileName } from "@arena/rich-editor/text";

export type { ProfileName };

/** The format flag the web sends: it always writes ART. */
export const ART_FORMAT = "art";

export interface TextCount {
  /** Canonical ART. */
  art: string;
  /** Visible characters, counted the way the API counts them. */
  visible: number;
  links: number;
  /** Characters of the canonical HTML. */
  htmlLength: number;
}

/** The limits of a profile, as the server checks them. */
export function limitsOf(profile: ProfileName): { maxVisible: number; maxHtml: number; maxLinks: number } {
  const p = PROFILES[profile];
  return { maxVisible: p.maxVisible, maxHtml: p.maxHtml, maxLinks: p.maxLinks };
}

/** Counts an ART text; null when the library refuses it. */
export function countText(art: string, profile: ProfileName): TextCount | null {
  try {
    const m = measure(art, PROFILES[profile]);
    return { art: m.html, visible: m.visible, links: m.links, htmlLength: m.htmlLength };
  } catch {
    return null;
  }
}

/** The visible characters of an ART text (`Infinity` for one the library refuses). */
export function visibleLength(art: string, profile: ProfileName): number {
  if (art === "") return 0;
  return countText(art, profile)?.visible ?? Number.POSITIVE_INFINITY;
}

/** True when an ART text is over the VISIBLE limit of its profile. */
export function overVisibleLimit(art: string, profile: ProfileName): boolean {
  return visibleLength(art, profile) > PROFILES[profile].maxVisible;
}

/** True when an ART text is over the other limits of its profile: the stored HTML or the links. */
export function overMarkupLimit(art: string, profile: ProfileName): boolean {
  if (art === "") return false;
  const c = countText(art, profile);
  if (c === null) return true;
  const p = PROFILES[profile];
  return c.htmlLength > p.maxHtml || c.links > p.maxLinks;
}

/** The old plain-text form ("**bold**", "- " lists, blank lines) as canonical ART. "" for a blank text. */
export function artOfLegacy(text: string, profile: ProfileName): string {
  return fromLegacy(text, PROFILES[profile]);
}

/**
 * A text read from the API as ART: the stored format says how. `art` is taken as it is, anything else
 * (`text`, an empty or an unknown flag, the old plain text) goes through the converter, so a text is never
 * shown with its tags and never guessed from them.
 */
export function artOfStored(text: string | null | undefined, format: string | null | undefined, profile: ProfileName): string {
  if (text === null || text === undefined || text === "") return "";
  return format === ART_FORMAT ? text : artOfLegacy(text, profile);
}

/** The text a reader sees: paragraphs separated by a blank line, list items as lines. "" when unreadable. */
export function plainOfArt(art: string, profile: ProfileName): string {
  if (art === "") return "";
  try {
    return artToPlainText(art, PROFILES[profile]);
  } catch {
    return "";
  }
}

/** True when two ART texts show the same words (the markup may differ). */
export function sameVisibleText(a: string, b: string, profile: ProfileName): boolean {
  return plainOfArt(a, profile).trim() === plainOfArt(b, profile).trim();
}

/** The visible text of a description with the `description_html` / `description_format` fields of an event. */
export function descriptionArtOf(e: {
  description?: string | null;
  description_html?: string | null;
  description_format?: string | null;
}): string {
  // `description_html` is `richtext.Render` of the stored text, safe HTML for display: reading it back through
  // the description profile gives ART whichever format was stored. Else the raw value with its flag.
  if (e.description_html !== null && e.description_html !== undefined && e.description_html !== "") {
    const c = countText(e.description_html, "description");
    if (c !== null) return c.art;
  }
  return artOfStored(e.description, e.description_format, "description");
}
