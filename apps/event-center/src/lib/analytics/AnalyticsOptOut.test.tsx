import { describe, expect, it } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import { AnalyticsOptOutView } from "@/lib/analytics/AnalyticsOptOut";
import { CATALOGS, I18nProvider, LOCALES } from "@/lib/i18n";

function html(locale: (typeof LOCALES)[number], checked: boolean): string {
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <AnalyticsOptOutView checked={checked} onChange={() => undefined} />
    </I18nProvider>,
  );
}

describe("the opt-out toggle", () => {
  it("is labelled in every language of the event center", () => {
    expect(LOCALES).toEqual(["en", "ru", "es"]);
    expect(html("en", true)).toContain("Help us make the service better");
    expect(html("ru", true)).toContain("Помогать улучшать сервис");
    expect(html("es", true)).toContain("Ayudar a mejorar el servicio");
    for (const l of LOCALES) {
      expect(CATALOGS[l]["analytics.optOut.label"].trim().length).toBeGreaterThan(0);
      expect(CATALOGS[l]["analytics.optOut.hint"].trim().length).toBeGreaterThan(0);
    }
  });

  it("is a labelled checkbox that is ticked while the person helps (the default)", () => {
    const on = html("en", true);
    expect(on).toMatch(/<input[^>]*type="checkbox"[^>]*checked/);
    expect(on).toMatch(/<label for="[^"]+">Help us make the service better<\/label>/);
    expect(html("en", false)).not.toMatch(/<input[^>]*checked/);
  });
});
