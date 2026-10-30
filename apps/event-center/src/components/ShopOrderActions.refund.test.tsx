// @vitest-environment jsdom
/**
 * SA-01, SA-19 and SA-35 on the order card. A refund is ONE operation with ONE Idempotency-Key: a lost answer keeps
 * the key (a retry with the same amount and reason cannot return the money twice), a refusal drops it, and a failure
 * reads the order again and shows what the server says is left. The courier text is the one the SERVER builds.
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
  fetchCourierText: vi.fn(),
}));
// The password question is not what these tests are about: the money request just goes out.
vi.mock("@/lib/stepUp", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/stepUp")>()),
  withStepUp: async (_org: string, fn: (headers: Record<string, string>) => Promise<unknown>) => fn({ "X-Step-Up-Token": "t" }),
}));

import { ShopOrderActions } from "@/components/ShopOrderActions";
import { StepUpProvider } from "@/components/StepUpDialog";
import { ApiError } from "@/lib/api/http";
import { fetchCourierText, fetchShopOrder, refundShopOrder, shipShopOrder } from "@/lib/api/shopApi";
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

const refundAnswer = { order: order({ status: "partially_refunded", refunded_minor: 1000 }), refund: { id: "r1", state: "succeeded", amount: 1000, currency: "EUR" } };

beforeEach(() => {
  for (const fn of [fetchCourierText, fetchShopOrder, refundShopOrder, shipShopOrder]) vi.mocked(fn).mockReset();
  vi.mocked(fetchShopOrder).mockResolvedValue(order());
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
  sessionStorage.clear();
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

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
            <ShopOrderActions orgId={ORG} order={o} canManage canRefund country="ES" />
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
    b.click();
    await sleep(30);
  });
}

async function typeInto(field: HTMLInputElement | HTMLTextAreaElement, text: string) {
  const proto = field instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(proto, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

/** Opens the refund form, types the amount, goes through the typed word and presses the final button. */
async function refund(el: HTMLElement, amount: string) {
  if (byId(el, "shop-refund-form") === null) await press(el, "shop-order-refund");
  await typeInto(byId(el, "shop-refund-amount") as HTMLInputElement, amount);
  await press(el, "shop-refund-continue");
  const dialog = byId(el, "confirm-word");
  await typeInto(dialog.querySelector("input") as HTMLInputElement, "REFUND");
  await press(el, "confirm-word-submit");
}

describe("the refund of a shop order: one operation, one Idempotency-Key (SA-01)", () => {
  it("sends the key with the refund and starts a new operation after a success", async () => {
    vi.mocked(refundShopOrder).mockResolvedValue(refundAnswer as never);
    const el = await mount(order());
    await refund(el, "10");
    expect(refundShopOrder).toHaveBeenCalledTimes(1);
    const [, , body, headers, key] = vi.mocked(refundShopOrder).mock.calls[0];
    expect(body).toEqual({ amount_minor: 1000 });
    expect(headers).toEqual({ "X-Step-Up-Token": "t" });
    expect(key).toMatch(/^web-/);
    expect(byId(el, "shop-order-note").textContent).toContain("The money went back");
    // A second refund of the same amount is a NEW operation after the first one was answered.
    await refund(el, "10");
    expect(vi.mocked(refundShopOrder).mock.calls[1][4]).not.toBe(key);
  });

  it("keeps the key when the answer is lost, so the retry cannot return the money twice", async () => {
    vi.mocked(refundShopOrder).mockRejectedValueOnce(new ApiError(0, "network", "lost")).mockResolvedValueOnce(refundAnswer as never);
    const el = await mount(order());
    await refund(el, "10");
    expect(byId(el, "shop-refund-failure")).not.toBeNull();
    expect(byId(el, "shop-refund-retry-safe")).not.toBeNull();
    // The order was read again so the card shows its real state.
    expect(fetchShopOrder).toHaveBeenCalledWith(ORG, ID);
    await refund(el, "10");
    const keys = vi.mocked(refundShopOrder).mock.calls.map((c) => c[4]);
    expect(keys).toHaveLength(2);
    expect(keys[1]).toBe(keys[0]);
  });

  it("keeps the key across a page reload: it sits in the tab's storage, not in memory (SB-12)", async () => {
    vi.mocked(refundShopOrder).mockRejectedValueOnce(new ApiError(0, "network", "lost")).mockResolvedValueOnce(refundAnswer as never);
    const first = await mount(order());
    await refund(first, "10");
    expect(Object.keys(sessionStorage).some((k) => k.startsWith("arena.events.shoprefund."))).toBe(true);
    // A reload: the old card is gone, a new one is mounted from scratch.
    while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
    document.body.innerHTML = "";
    const second = await mount(order());
    await refund(second, "10");
    const keys = vi.mocked(refundShopOrder).mock.calls.map((c) => c[4]);
    expect(keys[1]).toBe(keys[0]);
    // The answer came: nothing is left in the storage.
    expect(Object.keys(sessionStorage).some((k) => k.startsWith("arena.events.shoprefund."))).toBe(false);
  });

  it("changes the key when the amount changes", async () => {
    vi.mocked(refundShopOrder).mockRejectedValueOnce(new ApiError(504, "gateway", "slow")).mockResolvedValueOnce(refundAnswer as never);
    const el = await mount(order());
    await refund(el, "10");
    await refund(el, "12");
    const keys = vi.mocked(refundShopOrder).mock.calls.map((c) => c[4]);
    expect(keys[1]).not.toBe(keys[0]);
  });

  it("drops the key after a refusal: nothing was recorded, the next try is a new operation", async () => {
    vi.mocked(refundShopOrder)
      .mockRejectedValueOnce(new ApiError(409, "shop.order_changed", "changed"))
      .mockResolvedValueOnce(refundAnswer as never);
    const el = await mount(order());
    await refund(el, "10");
    expect(byId(el, "shop-refund-retry-safe")).toBeNull();
    await refund(el, "10");
    const keys = vi.mocked(refundShopOrder).mock.calls.map((c) => c[4]);
    expect(keys[1]).not.toBe(keys[0]);
  });

  it("shows what the server says is left, not the card's own figure (SA-19)", async () => {
    vi.mocked(refundShopOrder).mockRejectedValueOnce(new ApiError(422, "shop.invalid_refund_amount", "too much", undefined, { refundable_minor: 1500 }));
    const el = await mount(order());
    await refund(el, "40");
    expect(byId(el, "shop-refund-left-now").textContent).toContain("15");
    expect(byId(el, "shop-refund-error").textContent).toContain("The amount does not fit.");
    // The form now judges the amount against the server's number.
    await typeInto(byId(el, "shop-refund-amount") as HTMLInputElement, "20");
    await press(el, "shop-refund-continue");
    expect(byId(el, "shop-refund-form").textContent).toContain("more than what is left");
    expect(byId(el, "confirm-word")).toBeNull();
  });

  it("says in the shop's own words that nothing is left, not something about tickets", async () => {
    vi.mocked(refundShopOrder).mockRejectedValueOnce(new ApiError(409, "shop.nothing_to_refund", "all"));
    const el = await mount(order());
    await refund(el, "10");
    const text = byId(el, "shop-refund-error").textContent ?? "";
    expect(text).toContain("nothing left to refund on this order");
    expect(text.toLowerCase()).not.toContain("ticket");
  });
});

describe("shipping an order whose goods are gone (SA-19)", () => {
  it("lists the lines that are short, by name", async () => {
    vi.mocked(shipShopOrder).mockRejectedValue(new ApiError(409, "shop.out_of_stock", "short", undefined, { items: [{ variant_id: "v1", ordered: 2, available: 1 }] }));
    const el = await mount(order({ status: "manual_review" }));
    await press(el, "shop-order-ship");
    await press(el, "shop-ship-submit");
    expect(byId(el, "shop-order-error").textContent).toContain("Some goods of this order are not on the shelf.");
    expect(byId(el, "shop-order-short").textContent).toContain("Riesling: ordered 2, free 1");
  });
});

describe("the courier text (SA-35)", () => {
  it("is the text of the server", async () => {
    vi.mocked(fetchCourierText).mockResolvedValue("SERVER TEXT");
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const el = await mount(order());
    await press(el, "shop-order-courier");
    expect(fetchCourierText).toHaveBeenCalledWith(ORG, ID);
    expect(writeText).toHaveBeenCalledWith("SERVER TEXT");
  });

  it("builds its own only when the server cannot be reached", async () => {
    vi.mocked(fetchCourierText).mockRejectedValue(new ApiError(0, "network", "down"));
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const el = await mount(order());
    await press(el, "shop-order-courier");
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(String(writeText.mock.calls[0][0])).toContain("Calle Mayor 1");
  });

  it("shows the refusal of the server instead of guessing a text", async () => {
    vi.mocked(fetchCourierText).mockRejectedValue(new ApiError(404, "shop.order_not_found", "gone"));
    const writeText = vi.fn();
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const el = await mount(order());
    await press(el, "shop-order-courier");
    expect(writeText).not.toHaveBeenCalled();
    expect(byId(el, "shop-order-error")).not.toBeNull();
  });
});
