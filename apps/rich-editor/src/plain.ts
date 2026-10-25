// The plain text of an ART text: paragraphs separated by a blank line, list items as "- " or "1. " lines
// (the same text the editor hands to the "Оформить" button as getText()).

import { plainText } from './model'
import { parseHtml } from './sanitize'
import type { Profile } from './profiles'

/** The text a reader sees in ART or any HTML for the profile. It needs a DOM, like `sanitize`. */
export function artToPlainText(html: string, p: Profile): string {
  return plainText(parseHtml(html, p))
}
