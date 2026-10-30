/**
 * The words a person types to confirm an action that cannot be undone. They are
 * EXACTLY the bot's words (apps/backend/internal/platform/i18n/locales/*.toml,
 * the keys named in BOT_KEYS), and `confirmWords.test.ts` reads those files so
 * the two cannot drift apart.
 *
 * Rules, the same as the bot's `normalizePhrase` (eventbot/typed_words.go):
 * - every action has its OWN word: the word of an order does not cancel a
 *   session (owner decision 2026-10-14);
 * - the English word is accepted in every language;
 * - case, extra spaces, quotes, a closing full stop and Spanish accents do not matter.
 */
import type { Locale } from "@/lib/i18n";

export const CONFIRM_KINDS = ["cancel_order", "cancel_session", "delete_event", "refund", "annul_invitation", "delete_promo"] as const;
export type ConfirmKind = (typeof CONFIRM_KINDS)[number];

export const CONFIRM_WORDS: Record<ConfirmKind, Record<Locale, string>> = {
  cancel_order: { en: "CANCEL ORDER", ru: "ОТМЕНИТЬ ЗАКАЗ", es: "CANCELAR PEDIDO" },
  cancel_session: { en: "CANCEL SESSION", ru: "ОТМЕНИТЬ СЕАНС", es: "CANCELAR SESIÓN" },
  delete_event: { en: "DELETE", ru: "УДАЛИТЬ", es: "ELIMINAR" },
  refund: { en: "REFUND", ru: "ВЕРНУТЬ", es: "DEVOLVER" },
  annul_invitation: { en: "ANNUL", ru: "ОТМЕНИТЬ ПРИГЛАШЕНИЕ", es: "ANULAR" },
  // 4-3: the bot's promo delete word is the same word as the event's.
  delete_promo: { en: "DELETE", ru: "УДАЛИТЬ", es: "ELIMINAR" },
};

/** The bot catalog key that holds each word. Used by the drift test. */
export const BOT_KEYS: Record<ConfirmKind, string> = {
  cancel_order: "bot.ord.cancel_word",
  cancel_session: "bot.ses.cancel_word",
  delete_event: "bot.evs.delete_word",
  refund: "bot.ref.word",
  annul_invitation: "bot.inv.revoke_word",
  delete_promo: "bot.promo.delete_word",
};

/**
 * Kinds that may share one word, because the two actions never stand on the
 * same screen: deleting an event and deleting a promo code (the bot uses the
 * same word for both). Every other pair must differ.
 */
export const SHARED_WORD_KINDS: readonly (readonly [ConfirmKind, ConfirmKind])[] = [["delete_event", "delete_promo"]];

/** True when two different kinds are allowed to take the same word. */
export function mayShareWord(a: ConfirmKind, b: ConfirmKind): boolean {
  return SHARED_WORD_KINDS.some(([x, y]) => (a === x && b === y) || (a === y && b === x));
}

/** The word to show for an action in the person's language. */
export function confirmWord(kind: ConfirmKind, locale: Locale): string {
  return CONFIRM_WORDS[kind][locale];
}

const ACCENTS: Record<string, string> = { á: "a", é: "e", í: "i", ó: "o", ú: "u", ü: "u", ñ: "n", ё: "е" };

/**
 * Folds case, accents and spacing and drops the quotes and the closing
 * punctuation a person may add around the phrase (the bot's `normalizePhrase`).
 */
export function normalizePhrase(text: string): string {
  const quotes = "\"'«»“”.!";
  let s = text.trim();
  let start = 0;
  let end = s.length;
  while (start < end && quotes.includes(s[start])) start++;
  while (end > start && quotes.includes(s[end - 1])) end--;
  s = s.slice(start, end);
  s = s.split(/\s+/).filter((p) => p !== "").join(" ").toLowerCase();
  return Array.from(s)
    .map((c) => ACCENTS[c] ?? c)
    .join("");
}

/** True when `typed` confirms this action: the phrase of the person's language or the English one. */
export function isConfirmWord(kind: ConfirmKind, locale: Locale, typed: string): boolean {
  const got = normalizePhrase(typed);
  if (got === "") return false;
  return got === normalizePhrase(CONFIRM_WORDS[kind][locale]) || got === normalizePhrase(CONFIRM_WORDS[kind].en);
}

export type ConfirmAttempt = "ok" | "empty" | "mismatch";

/** What a press of the confirm button means: nothing typed, a wrong text, or a go. */
export function attemptConfirm(kind: ConfirmKind, locale: Locale, typed: string): ConfirmAttempt {
  if (normalizePhrase(typed) === "") return "empty";
  return isConfirmWord(kind, locale, typed) ? "ok" : "mismatch";
}
