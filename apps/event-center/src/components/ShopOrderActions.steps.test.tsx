// @vitest-environment jsdom
/**
 * The delivery steps on the order card (spec 61 section 7b): the buttons of the allowed next steps, "Handed to
 * courier" with an optional tracking number (the old "Mark shipped", one call), "Not delivered" with a reason
 * picker, the card refreshed from the answer, a stale step read again, and the delivered letter on resend.
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
  setShopOrderFulfillment: vi.fn(),
}));

import { ShopOrderActions } from "@/components/ShopOrderActions";
import { StepUpProvider } from "@/components/StepUpDialog";
import { ApiError } from "@/lib/api/http";
import { fetchShopOrder, resendShopOrderEmail, setShopOrderFulfillment } from "@/lib/api/shopApi";
import type { ShopOrder } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ORG = "11111111-1111-4111-8111-111111111111";
const ID = "22222222-2222-4222-8222-222222222222";
const roots: Root[] = [];
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function order(over: Record<string, unknown> = {}): ShopOrder {
  return {
    id: ID,
    org_id: ORG,
    number: 1042,
    status: "paid",
    source: "web",
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
    payment: { id: "p", provider: "stripe", state: "succeeded", amount: 4490, currency: "EUR" },
    shipped_at: null,
    fulfillment_step: "waiting",
    ...over,
  } as unknown as ShopOrder;
}

beforeEach(() => {
  for (const fn of [resendShopOrderEmail, fetchShopOrder, setShopOrderFulfillment]) vi.mocked(fn).mockReset();
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

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

const get = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement | null;

async function press(el: HTMLElement, id: string) {
  const b = get(el, id);
  expect(b, id).not.toBeNull();
  await act(async () => {
    (b as HTMLElement).click();
    await sleep(20);
  });
}

async function type(el: HTMLElement, id: string, value: string) {
  const input = get(el, id) as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement | null;
  expect(input, id).not.toBeNull();
  const proto = input instanceof HTMLSelectElement ? HTMLSelectElement.prototype : input instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set as (v: string) => void;
  await act(async () => {
    setter.call(input, value);
    (input as HTMLElement).dispatchEvent(new Event(input instanceof HTMLSelectElement ? "change" : "input", { bubbles: true }));
    await sleep(0);
  });
}

async function submit(el: HTMLElement, id: string) {
  await press(el, id);
}

const note = (el: HTMLElement) => get(el, "shop-order-note")?.textContent ?? "";
const buttons = (el: HTMLElement) => ["packed", "ship", "delivered", "failed"].filter((k) => get(el, "shop-order-" + k) !== null);

describe("the buttons follow the step", () => {
  const cases: Array<[string, Record<string, unknown>, string[]]> = [
    ["waiting", {}, ["packed", "ship", "failed"]],
    ["packed", { fulfillment_step: "packed" }, ["ship", "failed"]],
    ["handed over", { fulfillment_step: "handed_over", status: "shipped", shipped_at: "2026-11-01T10:00:00Z" }, ["ship", "delivered", "failed"]],
    ["delivered", { fulfillment_step: "delivered", status: "shipped", shipped_at: "2026-11-01T10:00:00Z" }, []],
    ["failed", { fulfillment_step: "failed" }, ["ship"]],
    ["refunded", { status: "refunded", fulfillment_step: "waiting" }, []],
    ["unpaid", { status: "pending_payment", fulfillment_step: null }, []],
    ["digital", { delivery_kind: "none", fulfillment_step: null }, []],
  ];
  for (const [name, over, want] of cases) {
    it(`${name}: ${want.join(", ") || "no step buttons"}`, async () => {
      const el = await mount(order(over));
      expect(buttons(el)).toEqual(want);
    });
  }

  it("shows none to someone who cannot manage the shop", async () => {
    const el = await mount(order(), false);
    expect(buttons(el)).toEqual([]);
  });

  it("calls the step that is the allowed one \"Handed to courier\", and \"Change the note\" while it is with the courier", async () => {
    expect(get(await mount(order()), "shop-order-ship")?.textContent).toBe("Given to courier");
    const shipped = await mount(order({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "2026-11-01T10:00:00Z" }));
    expect(get(shipped, "shop-order-ship")?.textContent).toBe("Change the note");
  });
});

describe("a step is one call and refreshes the card", () => {
  it("packed: sends the step and says what it is now", async () => {
    vi.mocked(setShopOrderFulfillment).mockResolvedValue(order({ fulfillment_step: "packed" }));
    const el = await mount(order());
    await press(el, "shop-order-packed");
    expect(setShopOrderFulfillment).toHaveBeenCalledWith(ORG, ID, { step: "packed" });
    expect(note(el)).toBe("The delivery step is: Packed.");
  });

  it("delivered: sends the step", async () => {
    vi.mocked(setShopOrderFulfillment).mockResolvedValue(order({ fulfillment_step: "delivered", status: "shipped" }));
    const el = await mount(order({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "2026-11-01T10:00:00Z" }));
    await press(el, "shop-order-delivered");
    expect(setShopOrderFulfillment).toHaveBeenCalledWith(ORG, ID, { step: "delivered" });
    expect(note(el)).toContain("Delivered");
  });

  it("handed to courier: sends the tracking number the person typed, trimmed", async () => {
    vi.mocked(setShopOrderFulfillment).mockResolvedValue(order({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "x", tracking_note: "TRACK-1" }));
    const el = await mount(order());
    await press(el, "shop-order-ship");
    await type(el, "shop-ship-note", "  TRACK-1 ");
    await submit(el, "shop-ship-submit");
    expect(setShopOrderFulfillment).toHaveBeenCalledWith(ORG, ID, { step: "handed_over", tracking: "TRACK-1" });
    expect(note(el)).toBe("The order went to the courier.");
  });

  it("handed to courier without a number: the step alone, nothing to clear", async () => {
    vi.mocked(setShopOrderFulfillment).mockResolvedValue(order({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "x" }));
    const el = await mount(order());
    await press(el, "shop-order-ship");
    await submit(el, "shop-ship-submit");
    expect(setShopOrderFulfillment).toHaveBeenCalledWith(ORG, ID, { step: "handed_over" });
  });

  it("changing the number of a handed-over order sends it even when it is emptied", async () => {
    vi.mocked(setShopOrderFulfillment).mockResolvedValue(order({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "x" }));
    const el = await mount(order({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "x", tracking_note: "OLD" }));
    await press(el, "shop-order-ship");
    await type(el, "shop-ship-note", "");
    await submit(el, "shop-ship-submit");
    expect(setShopOrderFulfillment).toHaveBeenCalledWith(ORG, ID, { step: "handed_over", tracking: "" });
    expect(note(el)).toBe("We saved the shipping number.");
  });
});

describe("not delivered asks why", () => {
  it("refuses to send without a reason", async () => {
    const el = await mount(order({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "x" }));
    await press(el, "shop-order-failed");
    await submit(el, "shop-failed-submit");
    expect(setShopOrderFulfillment).not.toHaveBeenCalled();
    expect(get(el, "shop-failed-reason-error")?.textContent).toBe("Select a cause or write one.");
  });

  it("sends the code of a reason chosen from the list", async () => {
    vi.mocked(setShopOrderFulfillment).mockResolvedValue(order({ fulfillment_step: "failed" }));
    const el = await mount(order({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "x" }));
    await press(el, "shop-order-failed");
    const options = [...(get(el, "shop-failed-reason") as HTMLSelectElement).options].map((o) => o.textContent);
    expect(options).toEqual(["Select a cause", "No answer by phone", "Not at home", "Incorrect address", "Buyer did not accept the order", "Other"]);
    await type(el, "shop-failed-reason", "wrong_address");
    await submit(el, "shop-failed-submit");
    expect(setShopOrderFulfillment).toHaveBeenCalledWith(ORG, ID, { step: "failed", reason: "wrong_address" });
    expect(get(el, "shop-failed-form")).toBeNull();
  });

  it("asks for a text with \"Other\" and sends it", async () => {
    vi.mocked(setShopOrderFulfillment).mockResolvedValue(order({ fulfillment_step: "failed" }));
    const el = await mount(order());
    await press(el, "shop-order-failed");
    expect(get(el, "shop-failed-text")).toBeNull();
    await type(el, "shop-failed-reason", "other");
    await submit(el, "shop-failed-submit");
    expect(setShopOrderFulfillment).not.toHaveBeenCalled();
    await type(el, "shop-failed-text", "The gate was locked");
    await submit(el, "shop-failed-submit");
    expect(setShopOrderFulfillment).toHaveBeenCalledWith(ORG, ID, { step: "failed", reason: "The gate was locked" });
  });
});

describe("a step that moved meanwhile", () => {
  it("says what happened and reads the order again", async () => {
    vi.mocked(setShopOrderFulfillment).mockRejectedValue(new ApiError(409, "shop.fulfillment_invalid_step", "bad step", undefined, { fulfillment_step: "packed", allowed: ["handed_over", "failed"] }));
    vi.mocked(fetchShopOrder).mockResolvedValue(order({ fulfillment_step: "packed" }));
    const el = await mount(order());
    await press(el, "shop-order-packed");
    expect(fetchShopOrder).toHaveBeenCalledWith(ORG, ID);
    expect(get(el, "shop-order-error")?.textContent).toContain("The order is not at the step that this action must have.");
    expect(note(el)).toBe("");
  });

  it("explains a refunded order in plain words", async () => {
    vi.mocked(setShopOrderFulfillment).mockRejectedValue(new ApiError(409, "shop.fulfillment_order_closed", "closed"));
    vi.mocked(fetchShopOrder).mockResolvedValue(order({ status: "refunded" }));
    const el = await mount(order());
    await press(el, "shop-order-packed");
    expect(get(el, "shop-order-error")?.textContent).toContain("We refunded, canceled or closed this order.");
  });
});

describe("the repeat e-mail offers the delivered letter", () => {
  it("only for a delivered order, and sends that kind", async () => {
    vi.mocked(resendShopOrderEmail).mockResolvedValue({ queued: true, kind: "delivered" });
    const el = await mount(order({ fulfillment_step: "delivered", status: "shipped", shipped_at: "2026-11-01T10:00:00Z" }));
    await press(el, "shop-order-resend");
    await press(el, "shop-resend-delivered");
    expect(resendShopOrderEmail).toHaveBeenCalledWith(ORG, ID, "delivered");
    const handed = await mount(order({ fulfillment_step: "handed_over", status: "shipped", shipped_at: "2026-11-01T10:00:00Z" }));
    await press(handed, "shop-order-resend");
    expect(get(handed, "shop-resend-delivered")).toBeNull();
    expect(get(handed, "shop-resend-shipped")).not.toBeNull();
  });
});
