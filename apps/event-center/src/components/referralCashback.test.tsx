/**
 * The "Refer a friend" cashback (spec 52) on the sales figures and on the
 * order card, rendered on the server in the three languages. The stand's
 * case: 252,50 + 454,50 paid, a refund of 207,05 to a buyer, cashback 45,45
 * to the referrer, the organizer kept 454,50.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { OrderReferral } from "@/components/OrderReferral";
import { SalesFigures } from "@/components/SalesFigures";
import type { OrderDetail, SummaryMoneyItem } from "@/lib/api/types";
import { moneyLines } from "@/lib/events";
import { I18nProvider, LOCALES, type Locale } from "@/lib/i18n";
import { cashbackRowKind } from "@/lib/referral";
import { renderInRouter } from "@/test-utils/renderInRouter";

const render = (node: React.ReactNode, locale: Locale = "en") =>
  renderToStaticMarkup(<I18nProvider locale={locale}>{node}</I18nProvider>);

const places = {
  seats: { total: 0, available: 0, held: 0, sold: 0, sold_upstream: 0, unavailable: 0 },
  ga: { total: 10, available: 7, held: 0, sold: 3, sold_upstream: 0, unavailable: 0 },
};

const money = (over: Partial<SummaryMoneyItem> = {}): SummaryMoneyItem => ({
  currency: "EUR",
  paid_orders: 2,
  paid: 70700,
  service_charge: 0,
  discount: 0,
  refunded: 20705,
  cashback_refunds: 1,
  cashback: 4545,
  net: 45450,
  pending_orders: 0,
  pending: 0,
  ...over,
});

describe("sales figures", () => {
  it("names the cashback on its own and shows the net the server took it off", () => {
    const html = render(<SalesFigures places={places} money={[money()]} />, "ru");
    expect(html).toContain("Возвращено: 207,05");
    expect(html).toContain("Кэшбэк «Приведи друга»: 1 на сумму 45,45");
    expect(html).toContain("Итого: 454,50");
  });

  it("says nothing about cashback when there is none", () => {
    for (const locale of LOCALES) {
      const html = render(<SalesFigures places={places} money={[money({ cashback: 0, cashback_refunds: 0, net: 49995 })]} />, locale);
      expect(html).not.toMatch(/cashback|кэшбэк/i);
    }
  });

  it("keeps a currency whose only movement is cashback", () => {
    const only = money({ paid_orders: 0, paid: 0, refunded: 0, cashback: 100, net: -100 });
    expect(moneyLines([only])).toHaveLength(1);
  });
});

const order = (referral: OrderDetail["referral"]): OrderDetail =>
  ({ venue_timezone: "Europe/Madrid", referral }) as unknown as OrderDetail;

describe("order card", () => {
  it("words each friend's cashback by its state", () => {
    expect(cashbackRowKind("paid")).toBe("paid");
    expect(cashbackRowKind("due")).toBe("pending");
    expect(cashbackRowKind("sent")).toBe("pending");
    for (const s of ["capped", "not_possible", "failed", "cancelled", "something_new"]) {
      expect(cashbackRowKind(s)).toBe("unpaid");
    }
  });

  it("shows the referrer the paid cashback with its date and the friend's order", async () => {
    const html = await renderInRouter(
      <OrderReferral
        orgId="org"
        order={order({
          cashback: [
            {
              friend_order_id: "f1",
              friend_order_number: 1000000501,
              state: "paid",
              amount: 4545,
              currency: "EUR",
              friend_reversal: null,
              paid_at: "2026-11-02T17:30:00Z",
            },
            { friend_order_id: null, friend_order_number: 1000000502, state: "sent", amount: 1000, currency: "EUR", friend_reversal: null, paid_at: null },
          ],
          used_code: null,
        })}
      />,
      "ru",
    );
    expect(html).toContain("Кэшбэк 45,45");
    expect(html).toContain("выплачен");
    expect(html).toContain("Заказ друга №1000000501");
    expect(html).toContain("/o/org/orders/f1");
    expect(html).toContain("в пути");
  });

  it("shows the friend the referrer's code in every language", async () => {
    for (const locale of LOCALES) {
      const html = await renderInRouter(
        <OrderReferral
          orgId="org"
          order={order({ cashback: [], used_code: { code: "ANNA-7K2Q", referrer_order_id: "r1", referrer_order_number: 1000000500 } })}
        />,
        locale,
      );
      expect(html).toContain("ANNA-7K2Q");
      expect(html).toContain("№1000000500");
    }
  });

  it("draws nothing for an order with no part in the programme", async () => {
    expect(await renderInRouter(<OrderReferral orgId="org" order={order(null)} />)).toBe("");
  });

  it("draws nothing for a card from a server that sends no referral field", async () => {
    const old = { venue_timezone: "Europe/Madrid" } as unknown as OrderDetail;
    expect(await renderInRouter(<OrderReferral orgId="org" order={old} />)).toBe("");
  });
});
