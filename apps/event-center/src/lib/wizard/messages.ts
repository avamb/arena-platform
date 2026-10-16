/**
 * Words for the answers of the server's check. The check writes its messages
 * in English only; the organizer reads the wizard in en, ru or es, so the
 * codes the sections can raise have their own texts in the catalogs. A code
 * with no text falls back to the server's message, which is better than none.
 */
import type { MessageKey } from "@/lib/i18n/en";

const ERROR_KEYS: Record<string, MessageKey> = {
  "import.action_name_required": "wizard.err.action_name_required",
  "import.invalid_currency": "wizard.err.invalid_currency",
  "import.invalid_promoter": "wizard.err.invalid_promoter",
  "import.invalid_venue": "wizard.err.invalid_venue",
  "import.venue_name_required": "wizard.err.venue_name_required",
  "venue.timezone_required": "wizard.err.timezone_required",
};

const WARNING_KEYS: Record<string, MessageKey> = {
  "import.venue_matched_by_name": "wizard.warn.venue_matched_by_name",
  "import.venue_timezone_kept": "wizard.warn.venue_timezone_kept",
  "import.country_unresolved": "wizard.warn.country_unresolved",
  "import.city_unresolved": "wizard.warn.city_unresolved",
};

/** The catalog key for an error code, or null when the server's message is all there is. */
export function checkErrorKey(code: string): MessageKey | null {
  return Object.prototype.hasOwnProperty.call(ERROR_KEYS, code) ? ERROR_KEYS[code] : null;
}

export function checkWarningKey(code: string): MessageKey | null {
  return Object.prototype.hasOwnProperty.call(WARNING_KEYS, code) ? WARNING_KEYS[code] : null;
}

/** Codes the wizard has a text for, for the test that every one exists in the catalogs. */
export const KNOWN_ERROR_CODES: readonly string[] = Object.keys(ERROR_KEYS);
export const KNOWN_WARNING_CODES: readonly string[] = Object.keys(WARNING_KEYS);
