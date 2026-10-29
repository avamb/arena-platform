/** The card of a shop order: the unpaid-in-time hint (E-02), the time of the shop (E-03), the country in words (E-06). Also the unknown-page text (E-01). */
import { describe, expect, it } from "vitest";
import { ShopOrderCard } from "@/screens/ShopOrderScreen";
import type { ShopOrder } from "@/lib/api/types";
import { CATALOGS, LOCALES } from "@/lib/i18n";
import { renderInRouter } from "@/test-utils/renderInRouter";

function order(over: Partial<ShopOrder> = {}): ShopOrder {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    org_id: "o",
    number: 1000001786,
    status: "expired",
    source: "web",
    buyer: { name: "Dana Levi", email: "dana@example.com", phone: "+972501234567" },
    ship: { address1: "Herzl 1", city: "Haifa", postal: "3300000", country: "IL" },
    delivery_kind: "shipping",
    delivery_note: "",
    currency: "ILS",
    subtotal: 74000,
    discount: 0,
    shipping_fee: 0,
    total: 74000,
    refunded_minor: 0,
    tracking_note: "",
    staff_note: { text: "", by: "", at: null },
    items: [],
    payment: null,
    created_at: "2026-10-28T19:36:00Z",
    updated_at: "2026-10-28T19:36:00Z",
    ...over,
  };
}

describe("ShopOrderCard", () => {
  it("says what happened to an order that was not paid in time", async () => {
    const html = await renderInRouter(<ShopOrderCard order={order()} orgId="org1" country="IL" />, "ru");
    expect(html).toContain("Не оплачен в срок");
    expect(html).toContain("Покупатель не оплатил за отведённое время, товар вернулся в продажу.");
    expect(html).not.toContain("Бронь истекла");
  });

  it("prints the country of the address in words, not as a code", async () => {
    const html = await renderInRouter(<ShopOrderCard order={order()} orgId="org1" country="IL" />, "ru");
    expect(html).toContain("3300000 Haifa Израиль");
    expect(html).not.toMatch(/Haifa IL</);
  });

  it("tells the payment deadline in the history, and keeps it after it passed (M-04)", async () => {
    const waiting = await renderInRouter(<ShopOrderCard order={order({ status: "pending_payment", expires_at: "2026-10-29T16:16:00Z" })} orgId="org1" country="IL" />, "ru");
    expect(waiting).toContain("Оплатить до");
    expect(waiting).not.toContain("Бронь закончится");
    const late = await renderInRouter(<ShopOrderCard order={order({ status: "expired", expires_at: "2026-10-29T16:16:00Z" })} orgId="org1" country="IL" />, "ru");
    expect(late).toContain("<li>Не оплачен в срок · чт, 29 окт. 2026 г., 18:16 (Израиль)</li>");
  });

  it("reads the history in the zone of the shop", async () => {
    const html = await renderInRouter(<ShopOrderCard order={order()} orgId="org1" country="IL" />, "ru");
    expect(html).toContain("21:36");
    expect(html).toContain("(Израиль)");
  });
});

describe("the text for an address that does not exist (E-01)", () => {
  it("is short and never blames the reader", () => {
    expect(CATALOGS.en["common.error.notFound"]).toBe("There is no such page.");
    expect(CATALOGS.ru["common.error.notFound"]).toBe("Такой страницы нет.");
    for (const locale of LOCALES) {
      const text = CATALOGS[locale]["common.error.notFound"].toLowerCase();
      for (const word of ["organization", "организац", "organizaci", "deleted", "удалил", "eliminó"]) expect(text, `${locale} ${word}`).not.toContain(word);
    }
  });
});
