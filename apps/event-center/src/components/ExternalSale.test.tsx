/**
 * "Who sells the tickets?" (spec 63, L-3): the wizard question on every date, the partner fields, the
 * card of a date, and the labels the event screen and the session card show. Presses are proven by
 * the reducer tests and the live twin.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/endpoints", () => ({ updateSessionSalesMode: vi.fn() }));

import { ExternalLabel, ExternalLine } from "@/components/ExternalSaleFields";
import { SessionSalesMode } from "@/components/SessionSalesMode";
import { WizardDatesSection } from "@/components/WizardDatesSection";
import { I18nProvider } from "@/lib/i18n";
import { newDraft, reduceDraft, type DraftAction } from "@/lib/wizard/draft";

const errors = { errorOf: () => undefined, generalOf: () => [], touch: () => undefined };

function wizard(actions: DraftAction[], locale: "en" | "ru" | "es" | "uk" = "en"): string {
  const draft = actions.reduce((d, a) => reduceDraft(d, a), newDraft("dr1", new Date(), "Admission"));
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <WizardDatesSection draft={draft} dispatch={() => undefined} errors={errors} defaultCategoryName="Admission" />
    </I18nProvider>,
  );
}

describe("the wizard question", () => {
  it("asks who sells the tickets first, and shows the tickets editor for the platform", () => {
    const html = wizard([]);
    expect(html).toContain("Who sells the tickets?");
    expect(html).toContain("We do, on the site and in the widget");
    expect(html).toContain('data-testid="wizard-date-0-sales-platform"');
    expect(html).toContain('data-testid="wizard-tickets-0"');
    expect(html).not.toContain("wizard-date-0-external");
  });

  it("for a partner shows the link, the name, the price and the currency, and no tickets", () => {
    const html = wizard([{ type: "dateSales", key: "d1", sales: "external" }]);
    for (const f of ["url", "seller", "price", "currency"]) expect(html).toContain(`data-testid="wizard-date-0-external-${f}"`);
    expect(html).toContain('data-testid="wizard-date-0-partner-note"');
    expect(html).not.toContain('data-testid="wizard-tickets-0"');
    expect(html).not.toContain("wizard-date-0-capacity");
  });

  it("speaks Russian, Spanish and Ukrainian too", () => {
    expect(wizard([], "ru")).toContain("Кто продаёт билеты?");
    expect(wizard([], "es")).toContain("¿Quién vende las entradas?");
    expect(wizard([], "uk")).toContain("Хто продає квитки?");
  });
});

describe("the labels", () => {
  const ext = { url: "https://www.eventim.de/event/1", seller_name: "Eventim" };

  it("names the partner and links to the box office", () => {
    const html = renderToStaticMarkup(
      <I18nProvider locale="ru">
        <ExternalLabel external={ext} />
      </I18nProvider>,
    );
    expect(html).toContain("Продаёт партнёр: Eventim");
    expect(html).toContain('href="https://www.eventim.de/event/1"');
    expect(html).toContain('rel="noopener noreferrer"');
  });

  it("never links a box office that is not https", () => {
    const html = renderToStaticMarkup(
      <I18nProvider locale="en">
        <ExternalLabel external={{ url: "javascript:alert(1)", seller_name: "X" }} />
      </I18nProvider>,
    );
    expect(html).not.toContain("href=");
  });

  it("is the one line of the screens a partner date empties", () => {
    const html = renderToStaticMarkup(
      <I18nProvider locale="ru">
        <ExternalLine />
      </I18nProvider>,
    );
    expect(html).toContain("Продажи ведёт партнёр");
  });
});

describe("the session card", () => {
  function card(session: { sales_mode: string; external: { url: string; seller_name: string; price_from: number | null; currency: string | null } | null }, cancelled = false) {
    return renderToStaticMarkup(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="en">
          <SessionSalesMode orgId="o" eventId="e" sessionId="s" session={{ ...session, currency: "EUR" }} cancelled={cancelled} />
        </I18nProvider>
      </QueryClientProvider>,
    );
  }

  it("shows the choice and the lock rule for a platform session", () => {
    const html = card({ sales_mode: "platform", external: null });
    expect(html).toContain('data-testid="session-sales-mode-choice"');
    expect(html).toContain("only while it has no orders, holds, seats or categories");
    expect(html).not.toContain("session-sales-mode-external");
  });

  it("fills the box office of a partner session", () => {
    const html = card({ sales_mode: "external", external: { url: "https://www.eventim.de/event/1", seller_name: "Eventim", price_from: 39, currency: "EUR" } });
    expect(html).toContain('value="https://www.eventim.de/event/1"');
    expect(html).toContain('value="Eventim"');
    expect(html).toContain('value="39"');
    expect(html).toContain('value="EUR"');
  });

  it("changes nothing on a canceled session", () => {
    const html = card({ sales_mode: "platform", external: null }, true);
    expect(html).toContain("session-sales-mode-cancelled");
    expect(html).not.toContain("session-sales-mode-save");
  });
});
