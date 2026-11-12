// @vitest-environment jsdom
/**
 * The delivery step on the orders list (spec 61 section 7b.4): a column with the step badge and the "waiting N
 * days" mark, and a filter by step whose options carry the numbers of the counts. The step goes into the address
 * and into the body of the search, never the buyer's text.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/screens/OrgLayout", () => ({
  useOrg: () => ({ orgId: "11111111-1111-4111-8111-111111111111", access: { can: () => true, data: { platform_superadmin: false } } }),
}));
vi.mock("@/lib/api/shopApi", () => ({
  searchShopOrders: vi.fn(),
  fetchShopOrderCounts: vi.fn(),
  fetchShopSettings: vi.fn(),
}));

import { fetchShopOrderCounts, fetchShopSettings, searchShopOrders } from "@/lib/api/shopApi";
import { I18nProvider } from "@/lib/i18n";
import { parseShopOrdersSearch } from "@/lib/shop";
import { ShopOrdersScreen } from "@/screens/ShopOrdersScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;

const roots: Root[] = [];
const ORG = "11111111-1111-4111-8111-111111111111";
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

function o(n: number, step: string | null, paidAt: string | null) {
  return {
    id: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`,
    number: 1000 + n,
    status: "paid",
    buyer: { name: `Buyer ${n}`, email: `b${n}@example.test`, phone: "" },
    currency: "EUR",
    total: 1000,
    delivery_kind: step === null ? "none" : "shipping",
    created_at: "2026-10-30T10:00:00Z",
    paid_at: paidAt,
    shipped_at: null,
    fulfillment_step: step,
  };
}

beforeEach(() => {
  for (const fn of [searchShopOrders, fetchShopOrderCounts, fetchShopSettings]) vi.mocked(fn).mockReset();
  vi.mocked(fetchShopOrderCounts).mockResolvedValue({
    all: 9, to_ship: 5, unpaid: 0, shipped: 1, refunds: 0,
    steps: { waiting: 3, packed: 2, handed_over: 1, delivered: 0, failed: 4 },
  } as never);
  vi.mocked(fetchShopSettings).mockResolvedValue({ currency: "EUR" } as never);
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

async function mount(entry = `/o/${ORG}/shop/orders`): Promise<HTMLElement> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const page = createRoute({ getParentRoute: () => rootRoute, path: "/o/$orgId/shop/orders", validateSearch: parseShopOrdersSearch, component: ShopOrdersScreen });
  const router = createRouter({ routeTree: rootRoute.addChildren([page]), history: createMemoryHistory({ initialEntries: [entry] }) });
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="en">
          <RouterProvider router={router} />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(50);
  });
  for (let i = 0; i < 30; i++) {
    await act(async () => {
      await sleep(25);
    });
    if (el.querySelector('[data-testid="shop-orders-table"]') !== null) break;
  }
  return el;
}

describe("the delivery step on the list", () => {
  it("shows a column with the step, and how long a late order has waited", async () => {
    const old = new Date(Date.now() - 5 * 24 * 3_600_000).toISOString();
    const fresh = new Date(Date.now() - 3_600_000).toISOString();
    vi.mocked(searchShopOrders).mockResolvedValue({ orders: [o(3, "waiting", old), o(2, "packed", fresh), o(1, null, null)], next_offset: null } as never);
    const el = await mount();
    const cells = [...el.querySelectorAll('[data-testid="shop-order-step-cell"]')].map((c) => c.textContent);
    expect(cells[0]).toContain("Not packed");
    expect(cells[0]).toContain("wait time: 5 days");
    expect(cells[1]).toContain("Packed");
    expect(cells[1]).not.toContain("days");
    expect(cells[2]).toBe("—");
    expect(el.textContent).toContain("Delivery step");
  });

  it("filters by step: the options carry the counts and the choice goes into the search body", async () => {
    vi.mocked(searchShopOrders).mockResolvedValue({ orders: [o(1, "failed", null)], next_offset: null } as never);
    const el = await mount();
    const select = byId(el, "shop-orders-step") as HTMLSelectElement;
    expect([...select.options].map((x) => x.textContent)).toEqual([
      "All steps",
      "Not packed (3)",
      "Packed (2)",
      "Given to courier (1)",
      "Delivered (0)",
      "Not delivered (4)",
    ]);
    expect(vi.mocked(searchShopOrders).mock.calls[0][1]).not.toHaveProperty("fulfillment_step");
    await act(async () => {
      select.value = "failed";
      select.dispatchEvent(new Event("change", { bubbles: true }));
      await sleep(100);
    });
    const last = vi.mocked(searchShopOrders).mock.calls.at(-1);
    expect(last?.[1]).toMatchObject({ fulfillment_step: "failed" });
    expect((byId(el, "shop-orders-step") as HTMLSelectElement).value).toBe("failed");
    expect(byId(el, "shop-orders-reset")).not.toBeNull();
  });

  it("opens with the step the address names", async () => {
    vi.mocked(searchShopOrders).mockResolvedValue({ orders: [o(1, "packed", null)], next_offset: null } as never);
    await mount(`/o/${ORG}/shop/orders?step=packed`);
    expect(vi.mocked(searchShopOrders).mock.calls[0][1]).toMatchObject({ fulfillment_step: "packed" });
  });
});
