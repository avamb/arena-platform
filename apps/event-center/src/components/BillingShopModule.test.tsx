import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { I18nProvider, type Locale } from "@/lib/i18n";
import type { ShopModuleLine } from "@/lib/billingModules";
import { ShopModuleLineView } from "@/components/BillingShopModule";

function render(line: ShopModuleLine, locale: Locale = "en"): string {
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <ShopModuleLineView line={line} />
    </I18nProvider>,
  );
}

describe("the shop module line of the billing screen", () => {
  it("says since when the module is connected and that it is free", () => {
    const html = render({ state: "active", since: "2026-11-01", until: null, price: { kind: "free" } });
    expect(html).toContain("Shop module");
    expect(html).toContain("Connected since 1 Nov 2026");
    expect(html).toContain("Monthly price: Free");
  });

  it("shows the price per month", () => {
    const html = render({ state: "active", since: "2026-11-01", until: null, price: { kind: "amount", minor: 1900, currency: "EUR" } });
    expect(html).toContain("19.00");
    expect(html).toContain("per month");
  });

  it("says the price list sets the price when the amount is not known", () => {
    expect(render({ state: "active", since: "2026-11-01", until: null, price: { kind: "unknown" } })).toContain("set by the price list");
  });

  it("says when a disconnected module had its last day, with no price", () => {
    const html = render({ state: "ended", since: "2026-01-01", until: "2026-03-31", price: { kind: "unknown" } });
    expect(html).toContain("Disconnected. The last day was 31 Mar 2026.");
    expect(html).not.toContain("Monthly price");
  });

  it("is read only: no button, no form", () => {
    const html = render({ state: "active", since: "2026-11-01", until: null, price: { kind: "free" } });
    expect(html).not.toContain("<button");
    expect(html).not.toContain("<input");
  });

  it("speaks Russian and Spanish", () => {
    const line: ShopModuleLine = { state: "active", since: "2026-11-01", until: null, price: { kind: "free" } };
    expect(render(line, "ru")).toContain("Бесплатно");
    expect(render(line, "es")).toContain("Gratis");
  });
});
