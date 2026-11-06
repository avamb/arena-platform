// @vitest-environment jsdom
/**
 * P-14 (spec 62 14.6): "Refund outside the platform" on the order card. The
 * person ticks tickets, types the FINAL amount per ticket and the provider's
 * refund number, checks the summary and records it; a ticket that already has a
 * refund is refused with the recorded amount, and the dialog says so.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/endpoints", () => ({ recordExternalRefund: vi.fn() }));
// The password question is not what these tests are about: the money request just goes out.
vi.mock("@/lib/stepUp", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/stepUp")>()),
  withStepUp: async (_org: string, fn: (headers: Record<string, string>) => Promise<unknown>) => fn({ "X-Step-Up-Token": "t" }),
}));

import { ExternalRefund } from "@/components/ExternalRefund";
import { StepUpProvider } from "@/components/StepUpDialog";
import { ApiError } from "@/lib/api/http";
import { recordExternalRefund } from "@/lib/api/endpoints";
import type { OrderDetail } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ORG = "11111111-1111-4111-8111-111111111111";
const roots: Root[] = [];

function order(over: Record<string, unknown> = {}): OrderDetail {
  return {
    id: "22222222-2222-4222-8222-222222222222",
    system_id: 1000000500,
    status: "paid",
    currency: "ILS",
    tickets: [
      { id: "t1", system_ticket_id: 11, price: 5000, currency: "ILS", status: "active" },
      { id: "t2", system_ticket_id: 12, price: 3000, currency: "ILS", status: "active" },
      { id: "t3", system_ticket_id: 13, price: 3000, currency: "ILS", status: "cancelled" },
    ],
    ...over,
  } as unknown as OrderDetail;
}

beforeEach(() => vi.mocked(recordExternalRefund).mockReset());
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (id: string) => document.querySelector('[data-testid="' + id + '"]') as HTMLElement | null;

async function mount(o: OrderDetail, canCreate = true) {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="en">
          <StepUpProvider>
            <ExternalRefund orgId={ORG} order={o} canCreate={canCreate} />
          </StepUpProvider>
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(0);
  });
}

async function press(testId: string) {
  const b = byId(testId);
  expect(b, testId).not.toBeNull();
  await act(async () => {
    (b as HTMLElement).click();
    await sleep(30);
  });
}

async function typeInto(testId: string, text: string) {
  const field = byId(testId) as HTMLInputElement;
  expect(field, testId).not.toBeNull();
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

describe("refund outside the platform", () => {
  it("is not offered without refund.create or for a refunded order", async () => {
    await mount(order(), false);
    expect(byId("extref-open")).toBeNull();
    document.body.innerHTML = "";
    await mount(order({ status: "refunded" }));
    expect(byId("extref-open")).toBeNull();
  });

  it("records the final amount per ticket after the check", async () => {
    vi.mocked(recordExternalRefund).mockResolvedValue({
      order_id: "o",
      order_status: "partially_refunded",
      total_minor: 2050,
      currency: "ILS",
      items: [{ ticket_id: "t1", refund_id: "r1", amount_minor: 2050, currency: "ILS", provider_refund_ref: "AP-77" }],
    });
    await mount(order());
    await press("extref-open");
    // Only active tickets are listed.
    expect(document.querySelectorAll('[data-testid="extref-ticket"]').length).toBe(2);
    await press("extref-check");
    expect(byId("extref-problem")?.textContent).toContain("Tick at least one ticket");

    await act(async () => {
      (document.querySelector('[data-ticket="t1"]') as HTMLInputElement).click();
      await sleep(0);
    });
    // The whole price is prefilled; the person types the sum really returned.
    expect((byId("extref-amount-11") as HTMLInputElement).value).toBe("50.00");
    await typeInto("extref-amount-11", "20.50");
    await press("extref-check");
    expect(byId("extref-problem")?.textContent).toContain("refund number");
    await typeInto("extref-ref", "AP-77");
    await press("extref-check");
    expect(byId("extref-final-total")?.textContent).toContain("20.50");
    expect(recordExternalRefund).not.toHaveBeenCalled();

    await press("extref-record");
    expect(recordExternalRefund).toHaveBeenCalledTimes(1);
    const [org, orderId, body, headers] = vi.mocked(recordExternalRefund).mock.calls[0];
    expect(org).toBe(ORG);
    expect(orderId).toBe("22222222-2222-4222-8222-222222222222");
    expect(body).toEqual({ items: [{ ticket_id: "t1", amount_minor: 2050, provider_refund_ref: "AP-77" }] });
    expect(headers).toEqual({ "X-Step-Up-Token": "t" });
    expect(byId("extref-result")).not.toBeNull();
  });

  it("refuses an amount above the price before anything is sent", async () => {
    await mount(order());
    await press("extref-open");
    await act(async () => {
      (document.querySelector('[data-ticket="t2"]') as HTMLInputElement).click();
      await sleep(0);
    });
    await typeInto("extref-amount-12", "30.01");
    await typeInto("extref-ref", "AP-1");
    await press("extref-check");
    expect(byId("extref-problem")?.textContent).toContain("more than the ticket price");
    expect(recordExternalRefund).not.toHaveBeenCalled();
  });

  it("explains a ticket that already has a refund, with the amount recorded", async () => {
    vi.mocked(recordExternalRefund).mockRejectedValueOnce(
      new ApiError(409, "order.external_refund_exists", "exists", undefined, { ticket_id: "t1", amount_minor: 1000, currency: "ILS" }),
    );
    await mount(order());
    await press("extref-open");
    await act(async () => {
      (document.querySelector('[data-ticket="t1"]') as HTMLInputElement).click();
      await sleep(0);
    });
    await typeInto("extref-ref", "AP-9");
    await press("extref-check");
    await press("extref-record");
    const text = byId("extref-refusal")?.textContent ?? "";
    expect(text).toContain("already recorded for ticket 11");
    expect(text).toContain("10.00");
  });
});
