/** A-08: the quick lists over the orders: names, the numbers on two of them, the open one, Russian words. */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ShopOrderTabs } from "@/components/ShopOrderTabs";
import { I18nProvider, type Locale } from "@/lib/i18n";
import type { OrderTab } from "@/lib/shop";

function render(active: OrderTab, counts: { all: number; to_ship: number; unpaid: number; shipped: number; refunds: number } | undefined, locale: Locale = "en") {
  return renderToStaticMarkup(
    <I18nProvider locale={locale}>
      <ShopOrderTabs active={active} counts={counts} onPick={() => undefined} />
    </I18nProvider>,
  );
}

describe("ShopOrderTabs", () => {
  it("shows five tabs and the number of orders to ship and of unpaid ones", () => {
    const html = render("all", { all: 40, to_ship: 3, unpaid: 12, shipped: 20, refunds: 1 });
    for (const k of ["all", "to_ship", "unpaid", "shipped", "refunds"]) expect(html).toContain('data-testid="shop-orders-tab-' + k + '"');
    expect(html).toContain(">To ship (3)<");
    expect(html).toContain(">Unpaid (12)<");
    expect(html).toContain(">All<");
    expect(html).toContain(">Shipped<");
    expect(html).toContain(">Refunds<");
  });

  it("marks the open tab, and only that one", () => {
    const html = render("to_ship", { all: 1, to_ship: 1, unpaid: 0, shipped: 0, refunds: 0 });
    expect(html.match(/aria-pressed="true"/g)).toHaveLength(1);
    expect(html).toMatch(/class="tab active" aria-pressed="true" data-testid="shop-orders-tab-to_ship"/);
  });

  it("shows the names alone until the numbers are known", () => {
    const html = render("all", undefined);
    expect(html).toContain(">To ship<");
    expect(html).toContain(">Unpaid<");
    expect(html).not.toMatch(/\(\d+\)/);
  });

  it("speaks Russian: «К отправке (N)», «Не оплачены (N)»", () => {
    const html = render("all", { all: 9, to_ship: 4, unpaid: 2, shipped: 1, refunds: 0 }, "ru");
    expect(html).toContain(">К отправке (4)<");
    expect(html).toContain(">Не оплачены (2)<");
    expect(html).toContain(">Отправлены<");
    expect(html).toContain(">Возвраты<");
    expect(html).toContain(">Все<");
  });

  it("shows a zero too: nothing to ship is an answer", () => {
    expect(render("all", { all: 9, to_ship: 0, unpaid: 0, shipped: 9, refunds: 0 })).toContain(">To ship (0)<");
  });
});
