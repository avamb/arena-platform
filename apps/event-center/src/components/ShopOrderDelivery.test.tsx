/**
 * The delivery block of an order card and the step cell of the list, rendered on the server: the current step,
 * the history (who, from where, when in the shop's zone, tracking number, reason) and the "waiting N days" badge.
 */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ShopOrderDelivery, ShopStepBadge } from "@/components/ShopOrderDelivery";
import { I18nProvider, type Locale } from "@/lib/i18n";
import type { ShopOrder } from "@/lib/api/types";

const NOW = new Date("2026-11-06T10:00:00Z");

function order(over: Record<string, unknown> = {}): ShopOrder {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    status: "shipped",
    delivery_kind: "shipping",
    fulfillment_step: "failed",
    paid_at: "2026-11-01T10:00:00Z",
    shipped_at: "2026-11-02T10:00:00Z",
    events: [
      { id: "e1", step: "packed", from_step: "waiting", tracking: "", reason: "", note: "", actor_user_id: "u1", actor_telegram_id: null, actor_name: "Marina", via: "event_center_web", created_at: "2026-11-02T08:20:00Z" },
      { id: "e2", step: "handed_over", from_step: "packed", tracking: "TRACK-9", reason: "", note: "", actor_user_id: null, actor_telegram_id: 77, actor_name: "Ivan", via: "delivery_group", created_at: "2026-11-02T13:05:00Z" },
      { id: "e3", step: "failed", from_step: "handed_over", tracking: "", reason: "not_home", note: "", actor_user_id: null, actor_telegram_id: 77, actor_name: "Ivan", via: "delivery_group", created_at: "2026-11-03T09:00:00Z" },
      { id: "e4", step: "failed", from_step: "handed_over", tracking: "", reason: "gate <closed>", note: "called twice", actor_user_id: "u1", actor_telegram_id: null, actor_name: "Marina", via: "telegram_bot", created_at: "2026-11-03T10:00:00Z" },
    ],
    ...over,
  } as unknown as ShopOrder;
}

function render(node: JSX.Element, locale: Locale = "en"): string {
  return renderToStaticMarkup(<I18nProvider locale={locale}>{node}</I18nProvider>);
}

describe("ShopOrderDelivery", () => {
  it("names the step and lists the history with who, where from, when, the number and the reason", () => {
    const html = render(<ShopOrderDelivery order={order()} country="ES" />);
    expect(html).toContain('data-testid="shop-order-delivery"');
    expect(html).toContain("Not delivered");
    expect(html.match(/data-testid="shop-order-step"/g)).toHaveLength(4);
    expect(html).toContain("Packed · Marina · event center");
    expect(html).toContain("Handed to courier · Ivan · delivery group");
    expect(html).toContain("Tracking number: TRACK-9");
    expect(html).toContain("Reason: Not at home");
    expect(html).toContain("Telegram bot");
    expect(html).toContain("called twice");
  });

  it("shows a typed reason as it was written, escaped", () => {
    const html = render(<ShopOrderDelivery order={order()} country="ES" />);
    expect(html).toContain("Reason: gate &lt;closed&gt;");
    expect(html).not.toContain("gate <closed>");
  });

  it("reads the time in the zone of the shop", () => {
    const es = render(<ShopOrderDelivery order={order()} country="ES" />);
    const il = render(<ShopOrderDelivery order={order()} country="IL" />);
    expect(es).toContain("09:20");
    expect(il).toContain("10:20");
  });

  it("speaks the language of the person", () => {
    const ru = render(<ShopOrderDelivery order={order()} country="ES" />, "ru");
    expect(ru).toContain("Доставка");
    expect(ru).toContain("Не доставлен");
    expect(ru).toContain("Передан курьеру");
    expect(ru).toContain("Причина: Нет дома");
  });

  it("says so when no step was recorded yet, and shows nothing for a digital order", () => {
    expect(render(<ShopOrderDelivery order={order({ events: [], fulfillment_step: "waiting", status: "paid", shipped_at: null })} />)).toContain('data-testid="shop-order-steps-empty"');
    expect(render(<ShopOrderDelivery order={order({ delivery_kind: "none", fulfillment_step: null })} />)).toBe("");
    expect(render(<ShopOrderDelivery order={order({ status: "pending_payment", fulfillment_step: null, shipped_at: null })} />)).toBe("");
  });
});

describe("ShopStepBadge", () => {
  it("shows the step and, past two days, how long an order has waited", () => {
    const waiting = order({ status: "paid", fulfillment_step: "waiting", shipped_at: null, paid_at: "2026-11-01T10:00:00Z" });
    const html = render(<ShopStepBadge order={waiting} now={NOW} />);
    expect(html).toContain("Waiting to be packed");
    expect(html).toContain("waiting 5 days");
    const fresh = render(<ShopStepBadge order={{ ...waiting, paid_at: "2026-11-06T08:00:00Z" } as ShopOrder} now={NOW} />);
    expect(fresh).toContain("Waiting to be packed");
    expect(fresh).not.toContain("waiting 0");
    expect(fresh).not.toContain("days");
  });

  it("does not call a handed-over or delivered order late", () => {
    for (const step of ["handed_over", "delivered", "failed"]) {
      expect(render(<ShopStepBadge order={order({ fulfillment_step: step })} now={NOW} />)).not.toContain("days");
    }
  });

  it("is a dash for an order with nothing to deliver", () => {
    expect(render(<ShopStepBadge order={order({ delivery_kind: "none", fulfillment_step: null })} now={NOW} />)).toContain("—");
  });
});
