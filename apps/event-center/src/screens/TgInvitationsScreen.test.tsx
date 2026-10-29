/** The invitations mini app page, rendered on the server (no DOM in vitest): the states that need no hall. */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { I18nProvider, LOCALES, type Locale } from "@/lib/i18n";
import { TgInvitationsView, type TgInvitationsState } from "@/screens/TgInvitationsScreen";

function render(state: TgInvitationsState, locale: Locale = "en"): string {
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <TgInvitationsView state={state} onClose={() => undefined} />
    </I18nProvider>,
  );
}

describe("TgInvitationsView", () => {
  it("says it is working while the code is exchanged, with the close button there", () => {
    const html = render({ phase: "working" });
    expect(html).toContain('data-state="working"');
    expect(html).toContain('role="status"');
    expect(html).toContain('data-testid="tg-inv-close"');
  });

  it("shows a refused link as an alert that says to press the bot button again", () => {
    const html = render({ phase: "error", key: "tgInv.error.invalid" }, "ru");
    expect(html).toContain('role="alert"');
    expect(html).toContain("Эта ссылка больше не работает");
    expect(html).toContain("Нажмите кнопку пригласительных в боте ещё раз");
  });

  it("says calmly that there is no access to a date the person cannot see", () => {
    const html = render({ phase: "noAccess" }, "en");
    expect(html).toContain('data-testid="tg-inv-no-access"');
    expect(html).toContain("No access to this date");
    expect(html).not.toContain("tg-inv-error");
  });

  it("uses the mini app classes, which carry the theme and the safe areas, and no style attribute", () => {
    const html = render({ phase: "working" });
    expect(html).toContain('class="tg-page"');
    expect(html).toContain('class="tg-card tg-inv"');
    expect(html).not.toMatch(/\sstyle=/);
  });

  for (const locale of LOCALES) {
    it(`has no missing text in ${locale}`, () => {
      for (const state of [
        { phase: "working" },
        { phase: "noAccess" },
        { phase: "error", key: "tgInv.error.missing" },
        { phase: "error", key: "tgInv.error.invalid" },
      ] as TgInvitationsState[]) {
        const html = render(state, locale);
        expect(html).not.toContain("tgInv.");
        expect(html).not.toContain("tgEdit.");
        expect(html).toContain('data-testid="tg-inv-close"');
      }
    });
  }
});
