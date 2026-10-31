// @vitest-environment jsdom
/**
 * C-06: cancelling an UNPAID shop order moves no money, so neither the word dialog nor the password question may
 * talk about a refund or a payment. The password is still asked (cancel is on the step-up list).
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/shopApi", () => ({
  resendShopOrderEmail: vi.fn(),
  cancelShopOrder: vi.fn(),
  fetchShopOrder: vi.fn(),
  refundShopOrder: vi.fn(),
  resolveShopOrder: vi.fn(),
  shipShopOrder: vi.fn(),
  fetchCourierText: vi.fn(),
}));

import { ShopOrderActions } from "@/components/ShopOrderActions";
import { StepUpProvider } from "@/components/StepUpDialog";
import { fetchShopOrder } from "@/lib/api/shopApi";
import type { ShopOrder } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ORG = "11111111-1111-4111-8111-111111111111";
const ID = "22222222-2222-4222-8222-222222222222";
const roots: Root[] = [];

function order(over: Record<string, unknown> = {}): ShopOrder {
  return {
    id: ID,
    org_id: ORG,
    number: 1042,
    status: "manual_review",
    review_reason: "late_payment_no_stock",
    buyer: { name: "Anna", email: "anna@example.test", phone: "+34 600" },
    ship: { address1: "Calle Mayor 1", city: "Madrid", country: "ES" },
    delivery_kind: "shipping",
    delivery_note: "",
    currency: "EUR",
    subtotal: 4000,
    discount: 0,
    shipping_fee: 490,
    total: 5000,
    refunded_minor: 0,
    tracking_note: "",
    staff_note: { text: "", by: "", at: null },
    items: [{ variant_id: "v1", product_title: "Riesling", variant_title: "Default Title", qty: 2, unit_price_minor: 2000, total_minor: 4000 }],
    payment: { id: "p1", provider: "allpay", state: "succeeded", amount: 5000, currency: "EUR" },
    shipped_at: null,
    ...over,
  } as unknown as ShopOrder;
}

beforeEach(() => vi.mocked(fetchShopOrder).mockReset());
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

async function mount(locale: "en" | "ru" | "es"): Promise<HTMLElement> {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale={locale}>
          <StepUpProvider>
            <ShopOrderActions orgId={ORG} order={order({ status: "pending_payment", payment: null })} canManage canRefund country="ES" />
          </StepUpProvider>
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(0);
  });
  return el;
}

describe("cancelling an unpaid order (C-06)", () => {
  const cases = [
    { locale: "en" as const, word: "CANCEL ORDER", intro: "The order closes and its goods go back on sale. Confirm your password." },
    { locale: "ru" as const, word: "ОТМЕНИТЬ ЗАКАЗ", intro: "Заказ закроется, товар вернётся в продажу. Подтвердите пароль." },
    { locale: "es" as const, word: "CANCELAR PEDIDO", intro: "El pedido se cierra y sus productos vuelven a la venta. Confirma tu contraseña." },
  ];
  for (const c of cases) {
    it(`says what really happens, without money, in ${c.locale}`, async () => {
      const el = await mount(c.locale);
      await act(async () => {
        byId(el, "shop-order-cancel").click();
        await sleep(10);
      });
      const dialog = byId(el, "confirm-word");
      expect(dialog.textContent?.toLowerCase()).not.toMatch(/refund|money|devoluci|dinero|возврат|деньг/);
      const input = dialog.querySelector("input") as HTMLInputElement;
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(input, c.word);
        input.dispatchEvent(new Event("input", { bubbles: true }));
        await sleep(0);
      });
      await act(async () => {
        byId(el, "confirm-word-submit").click();
        await sleep(30);
      });
      const step = document.querySelector('[data-testid="step-up"]') as HTMLElement;
      expect(step).not.toBeNull();
      expect(step.textContent).toContain(c.intro);
      expect(step.textContent).not.toMatch(/refund or a payment|reembolso o un pago|возврат или оплат/i);
    });
  }
});
