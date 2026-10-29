// @vitest-environment jsdom
/**
 * A-11: "Send the e-mail again" on the order card. One letter for a paid order (a single press), a question for a
 * shipped one (the confirmation or the shipping e-mail), "queued" afterwards, a calm line when the API says wait a
 * minute, and no button for a reader, an order nobody paid, or an order without an address.
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
  shipShopOrder: vi.fn(),
}));

import { ShopOrderActions } from "@/components/ShopOrderActions";
import { StepUpProvider } from "@/components/StepUpDialog";
import { ApiError } from "@/lib/api/http";
import { resendShopOrderEmail } from "@/lib/api/shopApi";
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
    status: "paid",
    buyer: { name: "Anna", email: "anna@example.test" },
    ship: { address1: "Calle Mayor 1", city: "Madrid", country: "ES" },
    delivery_kind: "shipping",
    delivery_note: "",
    currency: "EUR",
    subtotal: 4000,
    discount: 0,
    shipping_fee: 490,
    total: 4490,
    refunded_minor: 0,
    tracking_note: "",
    staff_note: { text: "", by: "", at: null },
    items: [],
    payment: null,
    shipped_at: null,
    ...over,
  } as unknown as ShopOrder;
}

beforeEach(() => {
  vi.mocked(resendShopOrderEmail).mockReset();
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

async function mount(o: ShopOrder, canManage = true): Promise<HTMLElement> {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="en">
          <StepUpProvider>
            <ShopOrderActions orgId={ORG} order={o} canManage={canManage} canRefund />
          </StepUpProvider>
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(0);
  });
  return el;
}

async function press(el: HTMLElement, testId: string) {
  const b = el.querySelector('[data-testid="' + testId + '"]') as HTMLElement | null;
  expect(b, testId).not.toBeNull();
  await act(async () => {
    (b as HTMLElement).click();
    await sleep(20);
  });
}

const note = (el: HTMLElement) => el.querySelector('[data-testid="shop-order-note"]')?.textContent ?? "";

describe("the repeat e-mail button", () => {
  it("sends the confirmation of a paid order with one press and says it is queued", async () => {
    vi.mocked(resendShopOrderEmail).mockResolvedValue({ queued: true, kind: "received" });
    const el = await mount(order());
    await press(el, "shop-order-resend");
    expect(resendShopOrderEmail).toHaveBeenCalledWith(ORG, ID, "received");
    expect(el.querySelector('[data-testid="shop-resend-panel"]')).toBeNull();
    expect(note(el)).toBe("The e-mail is queued.");
  });

  it("asks which letter for a shipped order, and sends the one that was chosen", async () => {
    vi.mocked(resendShopOrderEmail).mockResolvedValue({ queued: true, kind: "shipped" });
    const el = await mount(order({ status: "shipped", shipped_at: "2026-10-06T09:00:00Z" }));
    await press(el, "shop-order-resend");
    expect(resendShopOrderEmail).not.toHaveBeenCalled();
    expect(el.querySelector('[data-testid="shop-resend-panel"]')?.textContent).toContain("anna@example.test");
    await press(el, "shop-resend-shipped");
    expect(resendShopOrderEmail).toHaveBeenCalledWith(ORG, ID, "shipped");
    expect(note(el)).toBe("The e-mail is queued.");
  });

  it("says to wait a minute when the API refuses a second request", async () => {
    vi.mocked(resendShopOrderEmail).mockImplementation(async () => {
      throw new ApiError(429, "shop.rate_limited", "wait");
    });
    const el = await mount(order());
    await press(el, "shop-order-resend");
    expect(note(el)).toContain("less than a minute ago");
    expect(el.querySelector('[data-testid="shop-order-error"]')).toBeNull();
  });

  it("shows any other refusal as an error", async () => {
    vi.mocked(resendShopOrderEmail).mockImplementation(async () => {
      throw new ApiError(409, "shop.order_email_unavailable", "no");
    });
    const el = await mount(order());
    await press(el, "shop-order-resend");
    expect(el.querySelector('[data-testid="shop-order-error"]')).not.toBeNull();
  });

  it("has no button for a reader, an unpaid order, or an order with no address", async () => {
    expect((await mount(order(), false)).querySelector('[data-testid="shop-order-resend"]')).toBeNull();
    expect((await mount(order({ status: "pending_payment" }))).querySelector('[data-testid="shop-order-resend"]')).toBeNull();
    expect((await mount(order({ buyer: { name: "A", email: "" } }))).querySelector('[data-testid="shop-order-resend"]')).toBeNull();
  });
});
