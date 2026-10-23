/** The scope list and the warning panel of a promo code, rendered on the server. */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { PromoScopeList, PromoWarnings } from "@/components/PromoScope";
import type { PromoCodeItem } from "@/lib/api/types";
import { I18nProvider, LOCALES } from "@/lib/i18n";

const session = {
  session_id: "s1",
  event_id: "e1",
  event_name: "White wines",
  start_at: "2026-11-05T19:00:00Z",
  sales_end_at: "2026-11-05T18:00:00Z",
  timezone: "Europe/Madrid",
  status: "scheduled",
  sales_start_at: null,
};
const item = (over: Partial<PromoCodeItem>) =>
  ({ id: "1", code: "RED30", applies_to_session_ids: ["s1"], sessions: [session], warnings: [], ...over }) as unknown as PromoCodeItem;
const render = (node: React.ReactNode, locale: (typeof LOCALES)[number] = "en") =>
  renderToStaticMarkup(<I18nProvider locale={locale}>{node}</I18nProvider>);

describe("PromoScopeList", () => {
  it("names the event and the dates instead of an id", () => {
    const html = render(<PromoScopeList item={item({})} />);
    expect(html).toContain("White wines");
    expect(html).toContain('data-testid="promo-scope-row"');
    expect(html).not.toContain(">s1<");
  });

  it("says plainly that a code without sessions works on every session", () => {
    const html = render(<PromoScopeList item={item({ applies_to_session_ids: [], sessions: [] })} />);
    expect(html).toContain('data-testid="promo-scope-all"');
    expect(html).not.toContain("promo-scope-list");
  });
});

describe("PromoWarnings", () => {
  it("draws nothing when there is nothing to warn about", () => {
    expect(render(<PromoWarnings item={item({})} />)).toBe("");
  });

  it("lists every warning in the three languages with the event name", () => {
    const warnings = [
      { code: "promo.warning.valid_until_before_sales_end", session_id: "s1", event_name: "White wines", start_at: session.start_at },
      { code: "promo.warning.session_cancelled", session_id: "s1", event_name: "White wines", start_at: session.start_at },
    ];
    for (const locale of LOCALES) {
      const html = render(<PromoWarnings item={item({ warnings })} />, locale);
      expect(html).toContain('data-testid="promo-warnings"');
      expect(html.match(/data-testid="promo-warning"/g)).toHaveLength(2);
      expect(html).toContain("White wines");
    }
  });
});
