/** The mini app entry page, rendered on the server (no DOM in vitest). */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { I18nProvider, LOCALES, type Locale, type MessageKey } from "@/lib/i18n";
import { TgEnterView } from "@/screens/TgEnterScreen";

function render(error: MessageKey | null, locale: Locale = "en"): string {
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <TgEnterView error={error} onClose={() => undefined} />
    </I18nProvider>,
  );
}

const KEYS: MessageKey[] = [
  "tgEnter.error.invalid",
  "tgEnter.error.expired",
  "tgEnter.error.notLinked",
  "tgEnter.error.unavailable",
  "tgEnter.error.outside",
  "common.error.network",
  "login.error.deactivated",
  "linkLogin.error.superadmin",
  "login.error.rateLimited",
];

describe("TgEnterView", () => {
  it("says it is signing in, with no button yet", () => {
    const html = render(null);
    expect(html).toContain('data-state="working"');
    expect(html).toContain('role="status"');
    expect(html).not.toContain("tg-enter-close");
  });

  it("shows a refusal as an alert in plain words, with a way out", () => {
    const html = render("tgEnter.error.notLinked", "ru");
    expect(html).toContain('role="alert"');
    expect(html).toContain("ещё не привязан");
    expect(html).toContain('data-testid="tg-enter-close"');
  });

  it("uses the mini app classes and no style attribute", () => {
    const html = render(null);
    expect(html).toContain('class="tg-page"');
    expect(html).not.toMatch(/\sstyle=/);
  });

  for (const locale of LOCALES) {
    it(`has no missing text in ${locale}`, () => {
      for (const key of KEYS) {
        const html = render(key, locale);
        expect(html).not.toContain("undefined");
        expect(html).not.toContain(key);
      }
    });
  }
});
