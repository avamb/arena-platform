import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { BOT_KEYS, CONFIRM_KINDS, CONFIRM_WORDS, attemptConfirm, confirmWord, isConfirmWord, normalizePhrase } from "@/lib/confirmWords";
import { LOCALES } from "@/lib/i18n";

const LOCALES_DIR = new URL("../../../backend/internal/platform/i18n/locales/", import.meta.url);
const TYPED_WORDS_GO = fileURLToPath(new URL("../../../backend/internal/platform/eventbot/typed_words.go", import.meta.url));

/** The value of `["key"]` / `other = "value"` in a bot catalog. */
function botText(locale: string, key: string): string {
  const text = readFileSync(fileURLToPath(new URL(`${locale}.toml`, LOCALES_DIR)), "utf8");
  // A plain search, no regular expression built from the key (nothing to escape).
  const lines = text.split(/\r?\n/);
  const at = lines.indexOf(`["${key}"]`);
  const m = at < 0 ? null : /^other = "([^"]*)"$/.exec(lines[at + 1] ?? "");
  if (m === null) throw new Error(`${locale}.toml has no ${key}`);
  return m[1];
}

describe("the table equals the bot's catalogs", () => {
  for (const kind of CONFIRM_KINDS) {
    for (const locale of LOCALES) {
      it(`${kind} in ${locale}`, () => {
        expect(CONFIRM_WORDS[kind][locale]).toBe(botText(locale, BOT_KEYS[kind]));
      });
    }
  }

  it("the bot source still names the keys of the order and session words", () => {
    const go = readFileSync(TYPED_WORDS_GO, "utf8");
    // gofmt aligns the "=" of a const block, so compare with the spaces folded.
    const declared = go.split(/\r?\n/).map((l) => l.trim().replace(/\s+/g, " "));
    expect(declared).toContain(`ordCancelWordKey = "${BOT_KEYS.cancel_order}"`);
    expect(declared).toContain(`sesCancelWordKey = "${BOT_KEYS.cancel_session}"`);
  });
});

describe("each action has its own word", () => {
  for (const locale of LOCALES) {
    it(`no two words are the same in ${locale}`, () => {
      const seen = new Map<string, string>();
      for (const kind of CONFIRM_KINDS) {
        const n = normalizePhrase(confirmWord(kind, locale));
        expect(seen.get(n), `${kind} repeats ${seen.get(n)}`).toBeUndefined();
        seen.set(n, kind);
      }
    });

    it(`the word of an order does not cancel a session in ${locale}`, () => {
      expect(isConfirmWord("cancel_session", locale, confirmWord("cancel_order", locale))).toBe(false);
      expect(isConfirmWord("cancel_order", locale, confirmWord("cancel_session", locale))).toBe(false);
    });

    it(`a word accepts only its own action in ${locale}`, () => {
      for (const a of CONFIRM_KINDS) {
        for (const b of CONFIRM_KINDS) {
          expect(isConfirmWord(a, locale, confirmWord(b, locale)), `${a} takes the word of ${b}`).toBe(a === b);
          expect(isConfirmWord(a, locale, CONFIRM_WORDS[b].en), `${a} takes the English word of ${b}`).toBe(a === b);
        }
      }
    });
  }
});

describe("typed forms", () => {
  it("accepts the English word in every language", () => {
    expect(isConfirmWord("cancel_session", "ru", "cancel session")).toBe(true);
    expect(isConfirmWord("cancel_order", "es", "Cancel Order")).toBe(true);
    expect(isConfirmWord("refund", "ru", "refund")).toBe(true);
  });

  it("ignores case, extra spaces, quotes, a closing full stop and accents", () => {
    const cases: [Parameters<typeof isConfirmWord>[0], Parameters<typeof isConfirmWord>[1], string, boolean][] = [
      ["cancel_session", "ru", "отменить сеанс", true],
      ["cancel_session", "ru", "  ОТМЕНИТЬ   СЕАНС  ", true],
      ["cancel_order", "ru", "«Отменить заказ».", true],
      ["cancel_session", "es", "cancelar sesion", true],
      ["cancel_session", "es", "CANCELAR SESIÓN", true],
      ["cancel_order", "es", "cancelar pedido!", true],
      ["cancel_order", "en", '"cancel order"', true],
      ["annul_invitation", "es", "anular", true],
    ];
    for (const [kind, locale, typed, want] of cases) expect(isConfirmWord(kind, locale, typed), `${kind} ${locale} ${typed}`).toBe(want);
  });

  it("the old single word and near misses confirm nothing", () => {
    for (const typed of ["", "cancel", "CANCEL", "ОТМЕНИТЬ", "отменить", "CANCELAR", "да", "yes", "сеанс", "заказ", "order", "session"]) {
      for (const locale of LOCALES) {
        expect(isConfirmWord("cancel_session", locale, typed), typed).toBe(false);
        expect(isConfirmWord("cancel_order", locale, typed), typed).toBe(false);
      }
    }
  });

  it("the other language's word does not count except English", () => {
    expect(isConfirmWord("delete_event", "es", "УДАЛИТЬ")).toBe(false);
    expect(isConfirmWord("delete_event", "ru", "ELIMINAR")).toBe(false);
  });

  it("attemptConfirm tells an empty text, a wrong text and a go apart", () => {
    expect(attemptConfirm("refund", "en", "   ")).toBe("empty");
    expect(attemptConfirm("refund", "en", "yes")).toBe("mismatch");
    expect(attemptConfirm("refund", "en", "Refund.")).toBe("ok");
  });

  it("normalizePhrase folds like the bot", () => {
    expect(normalizePhrase("  “Cancelar   SESIÓN”. ")).toBe("cancelar sesion");
    expect(normalizePhrase("ЁЖ")).toBe("еж");
  });
});
