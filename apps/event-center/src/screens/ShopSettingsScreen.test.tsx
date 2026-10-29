/** The settings form of the shop rendered on the server: what an organizer can change and what only the operator can. */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { ShopSettings } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";
import { checkSettings, settingsDraft } from "@/lib/shop";
import { ShopSettingsForm, type ShopSettingsFormProps } from "@/screens/ShopSettingsScreen";

const settings: ShopSettings = {
  org_id: "o",
  configured: true,
  currency: "EUR",
  shipping_fee_minor: 490,
  shipping_label: "Courier",
  country_iso2: "ES",
  age_gate_min_age: null,
  announcement_text: "",
  contact_email: "shop@example.com",
  logistics_note: "",
  sales_channel_id: "c2",
  shop_domain: "shop.example.com",
  max_line_qty: 24,
  enabled: false,
  updated_at: "2026-10-01T10:00:00Z",
};

function render(over: Partial<ShopSettingsFormProps> = {}, extra: Record<string, unknown> = {}, locale: "en" | "ru" | "es" = "en"): string {
  const props: ShopSettingsFormProps = {
    draft: settingsDraft(settings),
    onDraft: () => undefined,
    problems: {},
    countries: [
      { iso2: "ES", name: "Spain" },
      { iso2: "IL", name: "Israel" },
    ],
    channels: [
      { id: "c1", name: "Web widget" },
      { id: "c2", name: "Own site" },
    ],
    settings: { ...settings, ...extra },
    operator: false,
    operatorDomain: "",
    onOperatorDomain: () => undefined,
    operatorEnabled: false,
    onOperatorEnabled: () => undefined,
    canManage: true,
    busy: false,
    onSubmit: () => undefined,
    ...over,
  };
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <ShopSettingsForm {...props} />
    </I18nProvider>,
  );
}

describe("ShopSettingsForm", () => {
  it("shows the stored values and the channel of the organization", () => {
    const html = render();
    expect(html).toContain('value="EUR"');
    expect(html).toContain('value="4.90"');
    expect(html).toContain('value="Courier"');
    expect(html).toContain('value="shop@example.com"');
    expect(html).toContain("Delivery fee (EUR)");
    expect(html).toMatch(/<option value="c2" selected="">Own site<\/option>/);
    expect(html).toMatch(/<option value="ES" selected="">Spain<\/option>/);
    expect(html).toContain('data-testid="shop-settings-save"');
  });

  it("shows the domain and the open flag read only for an organizer, and says who changes them", () => {
    const html = render({}, { enabled: true });
    expect(html).toContain('data-testid="shop-settings-domain-value"');
    expect(html).toContain("shop.example.com");
    expect(html).toContain("Open to buyers");
    expect(html).toContain("platform operator");
    expect(html).not.toContain('data-testid="shop-settings-domain"');
    expect(html).toMatch(/<fieldset disabled="" data-testid="shop-settings-operator">/);
  });

  it("lets the platform operator change the domain and the flag", () => {
    const html = render({ operator: true, operatorDomain: "shop.example.com", operatorEnabled: true });
    expect(html).toContain('data-testid="shop-settings-domain"');
    expect(html).toContain('data-testid="shop-settings-enabled"');
    expect(html).not.toMatch(/<fieldset disabled="" data-testid="shop-settings-operator">/);
  });

  it("explains that only the owner picks the channel", () => {
    const html = render({ channels: null });
    expect(html).toContain('data-testid="shop-settings-channel-locked"');
    expect(html).not.toContain('data-testid="shop-settings-channel"');
  });

  it("is read only without shop.manage", () => {
    const html = render({ canManage: false });
    expect(html).not.toContain('data-testid="shop-settings-save"');
    expect(html).toMatch(/<fieldset disabled="">/);
  });

  it("types the currency only while the shop has no products", () => {
    const html = render({ productCount: 0 });
    expect(html).toContain('data-testid="shop-settings-currency"');
    expect(html).not.toContain('data-testid="shop-settings-currency-value"');
  });

  it("shows the currency read only, with its symbol and a note, once the shop has products", () => {
    const html = render({ productCount: 39 }, { currency: "ILS" });
    expect(html).not.toContain('data-testid="shop-settings-currency"');
    expect(html).toContain('data-testid="shop-settings-currency-value"');
    expect(html).toContain("ILS · ₪");
    expect(html).toContain("You can change the currency through support while the shop has products.");
    const ru = render({ productCount: 39 }, { currency: "ILS" }, "ru");
    expect(ru).toContain("Валюту можно изменить через поддержку, пока в магазине есть товары.");
    expect(ru).toContain("₪");
  });

  it("keeps the currency closed while the number of products is not known", () => {
    const html = render({ productCount: null });
    expect(html).not.toContain('data-testid="shop-settings-currency"');
    expect(html).toContain("Until then the currency stays as it is.");
  });

  it("says that a currency code does not exist", () => {
    const draft = { ...settingsDraft(settings), currency: "ILX" };
    const html = render({ draft, problems: checkSettings(draft) });
    expect(html).toContain("There is no such currency code. Type a code like EUR or ILS.");
  });

  it("names the countries in the language of the screen and sorts them by that name", () => {
    const html = render({}, {}, "ru");
    expect(html).toContain("Израиль</option>");
    expect(html).toContain("Испания</option>");
    expect(html).not.toContain(">Spain<");
    expect(html.indexOf("Израиль")).toBeLessThan(html.indexOf("Испания"));
  });

  it("does not cut a long number: the age and the limit take what is typed and the check judges it", () => {
    const html = render({ draft: { ...settingsDraft(settings), ageGate: "150", maxLineQty: "150" } });
    expect(html).toContain('value="150"');
    expect(html).not.toMatch(/maxLength="2"/);
    expect(html).toContain('inputMode="numeric"');
  });

  it("names each mistake in words", () => {
    const draft = { ...settingsDraft(settings), currency: "E", ageGate: "200", contactEmail: "nope" };
    const html = render({ draft, problems: checkSettings(draft) });
    expect(html).toContain("Type three letters, for example EUR.");
    expect(html).toContain("Type a number from 1 to 99, or leave it empty.");
    expect(html).toContain("Check the address. It needs the @ sign and a domain.");
  });
});
