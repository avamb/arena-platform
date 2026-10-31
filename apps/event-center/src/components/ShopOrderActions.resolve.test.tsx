// @vitest-environment jsdom
/**
 * The operator's decision on an order that waits for a check ("Fulfil the order" / "Refund and cancel") and the
 * keyboard behaviour of the dialogs of the order card (SB-14): focus goes into the dialog, Escape closes it and gives
 * the focus back, and after a success the focus goes to the result line.
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
vi.mock("@/lib/stepUp", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/stepUp")>()),
  withStepUp: async (_org: string, fn: (headers: Record<string, string>) => Promise<unknown>) => fn({ "X-Step-Up-Token": "t" }),
}));

import { ShopOrderActions } from "@/components/ShopOrderActions";
import { StepUpProvider } from "@/components/StepUpDialog";
import { ApiError } from "@/lib/api/http";
import { fetchShopOrder, refundShopOrder, resolveShopOrder } from "@/lib/api/shopApi";
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

beforeEach(() => {
  for (const fn of [fetchShopOrder, refundShopOrder, resolveShopOrder]) vi.mocked(fn).mockReset();
  vi.mocked(fetchShopOrder).mockResolvedValue(order());
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
  sessionStorage.clear();
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

async function mount(o: ShopOrder, rights: { canManage?: boolean; canRefund?: boolean } = {}): Promise<HTMLElement> {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="en">
          <StepUpProvider>
            <ShopOrderActions orgId={ORG} order={o} canManage={rights.canManage ?? true} canRefund={rights.canRefund ?? true} country="ES" />
          </StepUpProvider>
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(0);
  });
  return el;
}

async function press(el: HTMLElement, testId: string) {
  const b = byId(el, testId);
  expect(b, testId).not.toBeNull();
  await act(async () => {
    b.focus();
    b.click();
    await sleep(30);
  });
}

async function typeInto(field: HTMLInputElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

async function confirm(el: HTMLElement, word: string) {
  await typeInto(byId(el, "confirm-word").querySelector("input") as HTMLInputElement, word);
  await press(el, "confirm-word-submit");
}

const heldKeys = () => Object.keys(sessionStorage).filter((k) => k.startsWith("arena.events.shoprefund."));

describe("the decision on an order that waits for a check", () => {
  it("offers both buttons only for manual_review and only with shop.manage and the refund rights", async () => {
    const review = await mount(order());
    expect(byId(review, "shop-order-resolve-fulfil").textContent).toBe("Fulfil the order");
    expect(byId(review, "shop-order-resolve-refund").textContent).toBe("Refund and cancel");
    const paid = await mount(order({ status: "paid" }));
    expect(byId(paid, "shop-order-resolve-fulfil")).toBeNull();
    const noRefund = await mount(order(), { canRefund: false });
    expect(byId(noRefund, "shop-order-resolve-fulfil")).toBeNull();
    const noManage = await mount(order(), { canManage: false });
    expect(byId(noManage, "shop-order-resolve-refund")).toBeNull();
  });

  it("fulfils with the typed word, the step-up header and an Idempotency-Key, then sends the focus to the result", async () => {
    vi.mocked(resolveShopOrder).mockResolvedValue(order({ status: "paid" }));
    const el = await mount(order());
    await press(el, "shop-order-resolve-fulfil");
    await confirm(el, "wrong");
    expect(resolveShopOrder).not.toHaveBeenCalled();
    await confirm(el, "PROCESS ORDER");
    expect(resolveShopOrder).toHaveBeenCalledTimes(1);
    const [org, id, action, headers, key] = vi.mocked(resolveShopOrder).mock.calls[0];
    expect([org, id, action, headers]).toEqual([ORG, ID, "fulfil", { "X-Step-Up-Token": "t" }]);
    expect(key).toMatch(/^web-/);
    expect(byId(el, "confirm-word")).toBeNull();
    expect(byId(el, "shop-order-note").textContent).toContain("The order is fulfilled.");
    expect(document.activeElement).toBe(byId(el, "shop-order-note"));
    expect(heldKeys()).toEqual([]);
  });

  it("refunds and cancels with the refund word and names the amount in the dialog", async () => {
    vi.mocked(resolveShopOrder).mockResolvedValue(order({ status: "cancelled" }));
    const el = await mount(order());
    await press(el, "shop-order-resolve-refund");
    expect(byId(el, "confirm-word").textContent).toContain("50");
    await confirm(el, "REFUND");
    expect(vi.mocked(resolveShopOrder).mock.calls[0][2]).toBe("refund_and_cancel");
    expect(byId(el, "shop-order-note").textContent).toContain("order is canceled");
  });

  it("keeps the key when the answer is lost, also across a reload, and drops it after a refusal", async () => {
    vi.mocked(resolveShopOrder).mockRejectedValueOnce(new ApiError(0, "network", "lost")).mockResolvedValueOnce(order({ status: "paid" }));
    const first = await mount(order());
    await press(first, "shop-order-resolve-fulfil");
    await confirm(first, "PROCESS ORDER");
    expect(byId(first, "confirm-word")).not.toBeNull(); // the dialog stays and shows the failure
    while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
    document.body.innerHTML = "";
    const second = await mount(order());
    await press(second, "shop-order-resolve-fulfil");
    await confirm(second, "PROCESS ORDER");
    const keys = vi.mocked(resolveShopOrder).mock.calls.map((c) => c[4]);
    expect(keys[1]).toBe(keys[0]);

    vi.mocked(resolveShopOrder).mockReset();
    vi.mocked(resolveShopOrder).mockRejectedValueOnce(new ApiError(409, "shop.order_changed", "moved")).mockResolvedValueOnce(order({ status: "paid" }));
    const third = await mount(order());
    await press(third, "shop-order-resolve-fulfil");
    await confirm(third, "PROCESS ORDER");
    expect(fetchShopOrder).toHaveBeenCalledWith(ORG, ID); // the card reads the order again
    await confirm(third, "PROCESS ORDER");
    const retry = vi.mocked(resolveShopOrder).mock.calls.map((c) => c[4]);
    expect(retry[1]).not.toBe(retry[0]);
  });

  it("does not touch the refund of other statuses", async () => {
    const el = await mount(order({ status: "paid" }));
    expect(byId(el, "shop-order-refund")).not.toBeNull();
  });
});

describe("the dialogs of the order card, from the keyboard (SB-14)", () => {
  it("moves the focus into the dialog, closes on Escape and gives the focus back to the button", async () => {
    const el = await mount(order());
    await press(el, "shop-order-resolve-fulfil");
    const dialog = byId(el, "confirm-word");
    expect(dialog.contains(document.activeElement)).toBe(true);
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
      await sleep(0);
    });
    expect(byId(el, "confirm-word")).toBeNull();
    expect(document.activeElement).toBe(byId(el, "shop-order-resolve-fulfil"));
    expect(resolveShopOrder).not.toHaveBeenCalled();
  });

  it("closes the refund dialog on Escape and returns to the Continue button of the form", async () => {
    const el = await mount(order({ status: "paid" }));
    await press(el, "shop-order-refund");
    await typeInto(byId(el, "shop-refund-amount") as HTMLInputElement, "10");
    await press(el, "shop-refund-continue");
    expect(byId(el, "confirm-word")).not.toBeNull();
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
      await sleep(0);
    });
    expect(byId(el, "confirm-word")).toBeNull();
    expect(document.activeElement).toBe(byId(el, "shop-refund-continue"));
    expect(refundShopOrder).not.toHaveBeenCalled();
  });
});
