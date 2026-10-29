/**
 * The variants editor rendered on the server: what it shows for a loaded product, how it
 * names a mistake, and what it hides for a person who cannot change the shop. Typing and
 * clicking are covered by the rules in lib/shop.test.ts (the rows it edits) and the live twin.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ShopVariantsEditor, type ShopVariantsEditorProps } from "@/components/ShopVariantsEditor";
import { I18nProvider, type Locale } from "@/lib/i18n";
import { checkVariants, emptyRow, rowFromVariant, type VariantRow } from "@/lib/shop";

const loaded: VariantRow[] = [
  rowFromVariant({ id: "v1", title: "Red / L", sku: "RED-L", price_minor: 1250, compare_at_minor: 1500, stock: 7, track_stock: true, held: 2, available: 5, position: 0, option1: "Red", option2: "L", option3: null }, "EUR"),
  rowFromVariant({ id: "v2", title: "Blue / L", sku: null, price_minor: 1300, compare_at_minor: null, stock: 0, track_stock: false, held: 0, available: null, position: 1, option1: "Blue", option2: "L", option3: null }, "EUR"),
];

function render(over: Partial<ShopVariantsEditorProps> = {}, locale: Locale = "en"): string {
  const props: ShopVariantsEditorProps = {
    rows: loaded,
    onChange: () => undefined,
    currency: "EUR",
    optionNames: ["Color", "Size"],
    optionCount: 2,
    check: null,
    stats: { v1: { held: 2, available: 5 } },
    ...over,
  };
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <ShopVariantsEditor {...props} />
    </I18nProvider>,
  );
}

describe("ShopVariantsEditor", () => {
  it("shows a column for each option, named after it, with the saved numbers", () => {
    const html = render();
    expect(html.match(/data-testid="shop-variant-row"/g)).toHaveLength(2);
    expect(html).toContain(">Color<");
    expect(html).toContain(">Size<");
    expect(html).toContain('value="Red"');
    expect(html).toContain('value="12.50"');
    expect(html).toContain('value="15.00"');
    expect(html).toContain("In unpaid orders: 2");
    expect(html).toContain("Available: 5");
    expect(html).toContain("Price (EUR)");
  });

  it("hides the stock field of a variant that does not count it", () => {
    const html = render();
    expect(html).toContain('data-testid="shop-variant-stock-0"');
    expect(html).not.toContain('data-testid="shop-variant-stock-1"');
    expect(html).toContain("sells without a limit");
  });

  it("shows the name field when there are no options and several variants", () => {
    const html = render({ rows: [{ ...emptyRow(), price: "5" }, { ...emptyRow(), price: "6" }], optionCount: 0, optionNames: [] });
    expect(html).toContain('data-testid="shop-variant-title-0"');
    expect(html).toContain('data-testid="shop-variant-title-1"');
    expect(html).not.toContain("shop-variant-option1-0");
    expect(html).not.toContain("shop-variant-single");
  });

  it("says the only version instead of an empty name field for a single option-less variant", () => {
    const html = render({ rows: [{ ...emptyRow(), price: "5" }], optionCount: 0, optionNames: [] });
    expect(html).toContain('data-testid="shop-variant-single"');
    expect(html).toContain("The only version");
    expect(html).not.toContain('data-testid="shop-variant-title-0"');
    expect(render({ rows: [{ ...emptyRow(), price: "5" }], optionCount: 0, optionNames: [] }, "ru")).toContain("Единственная версия");
  });

  it("never prints the technical Default Title, even when the product came with that option value", () => {
    const imported = rowFromVariant(
      { id: "v9", title: "Default Title", sku: null, price_minor: 20000, compare_at_minor: null, stock: 82, track_stock: true, held: 0, available: 82, position: 0, option1: "Default Title", option2: null, option3: null },
      "ILS",
    );
    const html = render({ rows: [imported], optionCount: 0, optionNames: [], currency: "ILS" });
    expect(html).not.toContain("Default Title");
    expect(html).toContain("The only version");
  });

  it("brings the option field back once the person names an option", () => {
    const html = render({ rows: [{ ...emptyRow(), price: "5" }], optionCount: 1, optionNames: ["Size"] });
    expect(html).toContain('data-testid="shop-variant-option1-0"');
    expect(html).not.toContain("shop-variant-single");
  });

  it("says what is wrong, in words, after a failed check", () => {
    const bad: VariantRow = { ...emptyRow(), options: ["Red", "", ""], price: "", stock: "x" };
    const check = checkVariants([bad], "EUR", 1);
    const html = render({ rows: [bad], optionCount: 1, optionNames: ["Color"], check });
    expect(html).toContain("Type a price, for example 12.50.");
    expect(html).toContain("Type a whole number, 0 or more.");
  });

  it("says that a product needs at least one variant", () => {
    const html = render({ rows: [], check: checkVariants([], "EUR", 0) });
    expect(html).toContain("Add at least one variant.");
  });

  it("is read only for a person who cannot change the shop", () => {
    const html = render({ disabled: true });
    expect(html).not.toContain('data-testid="shop-variant-add"');
    expect(html).not.toContain("shop-variant-remove-0");
    expect(html).toContain("<fieldset disabled");
  });

  it("speaks Russian and Spanish", () => {
    expect(render({}, "ru")).toContain("Цена (EUR)");
    expect(render({}, "es")).toContain("Precio (EUR)");
  });
});
