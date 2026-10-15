/**
 * Words for a LetterStatus (4-1). One text per kind and state, the same sentences
 * as the bot's `bot.ord.letter_*`. The optional parts (when, to whom, why) are
 * language fragments, so a language can put its own preposition in front of
 * them: a missing part drops out with its preposition.
 */
import { formatDateTime } from "@/lib/format";
import type { MessageKey } from "@/lib/i18n";
import type { Params } from "@/lib/i18n";
import { en } from "@/lib/i18n/en";
import type { LetterState, LetterStatus } from "@/lib/letters";

export type LetterKind = "tickets" | "refund";
type Translate = (key: MessageKey, params?: Params) => string;

/** Which fragment carries the address in each state: a comma, brackets, "to" or "reach". */
function addressFragment(state: LetterState): MessageKey {
  switch (state) {
    case "queued":
      return "letter.frag.toComma";
    case "opened":
    case "delayed":
      return "letter.frag.toParen";
    case "bounced":
      return "letter.frag.reach";
    default:
      return "letter.frag.to";
  }
}

/** True when the catalog has a sentence for this kind and state. */
export function hasLetterText(kind: LetterKind, state: LetterState): boolean {
  return `letter.${kind}.${state}` in en;
}

/** The line for one letter. "" when there is nothing to say (no text for the state). */
export function letterLine(t: Translate, kind: LetterKind, s: LetterStatus, tz: string, locale: string): string {
  if (!hasLetterText(kind, s.state)) return "";
  const time = s.at !== null ? formatDateTime(s.at, tz, locale) : "";
  const when = time !== "" ? t("letter.frag.when", { time }) : "";
  const to = s.to !== "" ? t(addressFragment(s.state), { to: s.to }) : "";
  const why = s.why !== "" ? t("letter.frag.why", { why: s.why }) : "";
  const last = time !== "" ? t("letter.frag.last", { time }) : "";
  const lastTry = time !== "" ? t("letter.frag.lastTry", { time }) : "";
  return t(`letter.${kind}.${s.state}` as MessageKey, { when, to, why, last, lastTry, done: s.done, total: s.total });
}
