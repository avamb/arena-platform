// @vitest-environment jsdom
/**
 * M-10: a refund overwrites the status of a shipped order, but the order stays shipped. The "Mark shipped"
 * button must not come back once shipped_at is set; the tracking note can still be changed after a partial
 * refund (the same button, labelled for the note).
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/shopApi", () => ({
  resendShopOrderEmail: vi.fn(),
  cancelShopOrder: vi.fn(),
  fetchShopOrder: vi.fn(),
  refundShopOrder: vi.fn(),
  shipShopOrder: vi.fn(),
}));

import { ShopOrderActions } from "@/components/ShopOrderActions";
import { StepUpProvider } from "@/components/StepUpDialog";
import type { ShopOrder } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ORG = "11111111-1111-4111-8111-111111111111";
const SHIPPED = "2026-10-29T10:00:00Z";
const roots: Root[] = [];

function order(status: string, shippedAt: string | null): ShopOrder {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    org_id: ORG,
    number: 1790,
    status,
    buyer: { name: "Anna", email: "anna@example.test" },
    ship: { address1: "Herzl 1", city: "Haifa", country: "IL" },
    delivery_kind: "shipping",
    delivery_note: "",
    currency: "ILS",
    subtotal: 74000,
    discount: 0,
    shipping_fee: 0,
    total: 74000,
    refunded_minor: status === "partially_refunded" ? 5000 : status === "refunded" ? 74000 : 0,
    tracking_note: "",
    staff_note: { text: "", by: "", at: null },
    items: [],
    payment: { id: "p", provider: "stripe", state: "succeeded", amount: 74000, currency: "ILS" },
    shipped_at: shippedAt,
  } as unknown as ShopOrder;
}

afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

async function mount(o: ShopOrder): Promise<HTMLElement> {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="en">
          <StepUpProvider>
            <ShopOrderActions orgId={ORG} order={o} canManage canRefund />
          </StepUpProvider>
        </I18nProvider>
      </QueryClientProvider>,
    );
  });
  return el;
}

const shipButton = (el: HTMLElement) => el.querySelector('[data-testid="shop-order-ship"]') as HTMLElement | null;

describe("the ship button across status and shipped_at", () => {
  const cases: Array<[string, string | null, string | null]> = [
    // status, shipped_at, label of the button (null: no button)
    ["paid", null, "Mark shipped"],
    ["manual_review", null, "Mark shipped"],
    ["partially_refunded", null, "Mark shipped"],
    ["shipped", SHIPPED, "Change the note"],
    ["partially_refunded", SHIPPED, "Change the note"],
    ["refunded", SHIPPED, null],
    ["refunded", null, null],
    ["pending_payment", null, null],
    ["cancelled", null, null],
    ["expired", null, null],
  ];
  for (const [status, at, label] of cases) {
    it(`${status}, shipped_at ${at === null ? "empty" : "set"}: ${label ?? "no button"}`, async () => {
      const el = await mount(order(status, at));
      const b = shipButton(el);
      if (label === null) expect(b).toBeNull();
      else expect(b?.textContent?.trim()).toBe(label);
    });
  }
});
