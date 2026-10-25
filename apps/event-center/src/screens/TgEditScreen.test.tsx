/** The mini app page, rendered on the server (no DOM in vitest). */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { I18nProvider, LOCALES, type Locale } from "@/lib/i18n";
import { TgEditView, type TgEditState } from "@/screens/TgEditScreen";

function render(state: TgEditState, locale: Locale = "en"): string {
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <TgEditView state={state} onClose={() => undefined} />
    </I18nProvider>,
  );
}

describe("TgEditView", () => {
  it("shows the placeholder and the close button once signed in", () => {
    const html = render({ phase: "ready" }, "ru");
    expect(html).toContain("Редактор появится в следующем обновлении");
    expect(html).toContain('data-testid="tg-edit-close"');
    expect(html).toContain("Закрыть");
    expect(html).toContain('data-state="ready"');
  });

  it("says it is working while the code is exchanged, with the close button there", () => {
    const html = render({ phase: "working" });
    expect(html).toContain('data-state="working"');
    expect(html).toContain('role="status"');
    expect(html).not.toContain("tg-edit-placeholder");
    expect(html).toContain('data-testid="tg-edit-close"');
  });

  it("shows a refused link as an alert, not the placeholder", () => {
    const html = render({ phase: "error", key: "tgEdit.error.invalid" }, "ru");
    expect(html).toContain('role="alert"');
    expect(html).toContain("Ссылка больше не работает");
    expect(html).not.toContain("tg-edit-placeholder");
  });

  for (const locale of LOCALES) {
    it(`has no missing text in ${locale}`, () => {
      for (const state of [{ phase: "working" }, { phase: "ready" }, { phase: "error", key: "tgEdit.error.missing" }] as TgEditState[]) {
        const html = render(state, locale);
        expect(html).not.toContain("tgEdit.");
        expect(html).toContain('data-testid="tg-edit-close"');
      }
    });
  }
});
