/**
 * The few checks the wizard makes itself, before the server's check answers:
 * the ones that are about the SHAPE of what was typed (a length, a number)
 * and that the server would meet as a database error rather than a field
 * error. Everything else is the server's check (validation.ts).
 */
import type { MessageKey } from "@/lib/i18n/en";
import {
  MAX_DESCRIPTION_RUNES,
  MAX_NAME_RUNES,
  parseCapacity,
  runeLength,
  validCurrency,
  type WizardDraft,
} from "@/lib/wizard/draft";
import type { FormField } from "@/lib/wizard/validation";

export type ClientErrors = Partial<Record<FormField, MessageKey>>;

export function clientErrors(d: WizardDraft): ClientErrors {
  const out: ClientErrors = {};
  if (runeLength(d.event.name.trim()) > MAX_NAME_RUNES) out.name = "wizard.err.nameLong";
  if (runeLength(d.event.description.trim()) > MAX_DESCRIPTION_RUNES) out.description = "wizard.err.descriptionLong";
  if (d.venue.mode === "new" && parseCapacity(d.venue.capacity) === null) out.venueCapacity = "wizard.err.capacity";
  // An empty currency is the server's to report ("choose a country or type it").
  if (d.currency.trim() !== "" && validCurrency(d.currency) === "") out.currency = "wizard.err.currencyShape";
  return out;
}
