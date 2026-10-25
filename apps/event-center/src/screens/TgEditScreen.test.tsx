/** The mini app page, rendered on the server (no DOM in vitest): the states that need no editor. */
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
  it("says it is working while the code is exchanged, with the close button there", () => {
    const html = render({ phase: "working" });
    expect(html).toContain('data-state="working"');
    expect(html).toContain('role="status"');
    expect(html).toContain('data-testid="tg-edit-close"');
  });

  it("shows a refused link as an alert that says to press the bot button again", () => {
    const html = render({ phase: "error", key: "tgEdit.error.invalid" }, "ru");
    expect(html).toContain('role="alert"');
    expect(html).toContain("Ссылка больше не работает");
    expect(html).toContain("Нажмите «✏️ Править в редакторе» в боте ещё раз");
    expect(html).toContain('data-testid="tg-edit-close"');
  });

  it("says calmly that there is no access to an event the person cannot see", () => {
    const html = render({ phase: "noAccess" }, "ru");
    expect(html).toContain('data-testid="tg-edit-no-access"');
    expect(html).toContain("Нет доступа к этому мероприятию");
    expect(html).toContain('data-testid="tg-edit-close"');
    expect(html).not.toContain("tg-edit-error");
  });

  it("uses the mini app classes, which carry the theme and the safe areas, and no style attribute", () => {
    const html = render({ phase: "working" });
    expect(html).toContain('class="tg-page"');
    expect(html).toContain('class="tg-card"');
    expect(html).not.toMatch(/\sstyle=/);
  });

  for (const locale of LOCALES) {
    it(`has no missing text in ${locale}`, () => {
      for (const state of [
        { phase: "working" },
        { phase: "noAccess" },
        { phase: "error", key: "tgEdit.error.missing" },
        { phase: "error", key: "tgEdit.error.invalid" },
      ] as TgEditState[]) {
        const html = render(state, locale);
        expect(html).not.toContain("tgEdit.");
        expect(html).toContain('data-testid="tg-edit-close"');
      }
    });
  }
});
