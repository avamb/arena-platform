// @vitest-environment jsdom
/**
 * SB-14 on the orders list: states of loading, error with a retry, and an empty list, and a "Show more" that never
 * prints the same order twice when a new order arrived between two presses (the list is read by offset).
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

import { ApiError } from "@/lib/api/http";
import { fetchShopOrderCounts, fetchShopSettings, searchShopOrders } from "@/lib/api/shopApi";
import { I18nProvider } from "@/lib/i18n";
import { ShopOrdersScreen } from "@/screens/ShopOrdersScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;

const roots: Root[] = [];

function o(n: number) {
  return {
    id: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}`,
    number: 1000 + n,
    status: "paid",
    buyer: { name: `Buyer ${n}`, email: `b${n}@example.test`, phone: "" },
    currency: "EUR",
    total: 1000,
    delivery_kind: "shipping",
    created_at: "2026-10-30T10:00:00Z",
    shipped_at: null,
  };
}

beforeEach(() => {
  for (const fn of [searchShopOrders, fetchShopOrderCounts, fetchShopSettings]) vi.mocked(fn).mockReset();
  // No country: the zone (a part of the list's query key) does not change when the settings arrive.
  vi.mocked(fetchShopOrderCounts).mockResolvedValue({ to_ship: 0, unpaid: 0 } as never);
  vi.mocked(fetchShopSettings).mockResolvedValue({ currency: "EUR" } as never);
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

async function mount(): Promise<HTMLElement> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const page = createRoute({ getParentRoute: () => rootRoute, path: "/o/$orgId/shop/orders", component: ShopOrdersScreen });
  const router = createRouter({ routeTree: rootRoute.addChildren([page]), history: createMemoryHistory({ initialEntries: ["/o/11111111-1111-4111-8111-111111111111/shop/orders"] }) });
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
  await idle(el);
  return el;
}

/** Waits until the screen stops saying that it loads (the router and the queries need a few turns). */
async function idle(el: HTMLElement) {
  for (let i = 0; i < 40; i++) {
    await act(async () => {
      await sleep(25);
    });
    if (el.querySelector('[data-testid="loading"]') === null && !(el.textContent ?? "").includes("Loading more orders")) return;
  }
}

const rowCount = (el: HTMLElement) => el.querySelectorAll('[data-testid="shop-order-row"]').length;

describe("the orders list (SB-14)", () => {
  it("does not print an order twice when a new order moved the next page by one", async () => {
    vi.mocked(searchShopOrders)
      .mockResolvedValueOnce({ orders: [o(3), o(2)], next_offset: 2 } as never)
      // A new order (#4) arrived: offset 2 now starts with the row that was last on the first page.
      .mockResolvedValueOnce({ orders: [o(2), o(1)], next_offset: null } as never);
    const el = await mount();
    expect(rowCount(el)).toBe(2);
    await act(async () => {
      byId(el, "shop-orders-more").click();
    });
    await idle(el);
    expect(rowCount(el)).toBe(3);
    expect(byId(el, "shop-orders-count").textContent).toContain("3");
    expect(byId(el, "shop-orders-more")).toBeNull();
  });

  it("shows an error with a retry when the first page fails, and an empty state for an empty shop", async () => {
    vi.mocked(searchShopOrders).mockRejectedValueOnce(new ApiError(500, "internal", "boom")).mockResolvedValueOnce({ orders: [], next_offset: null } as never);
    const el = await mount();
    const box = byId(el, "error");
    expect(box).not.toBeNull();
    await act(async () => {
      (box.querySelector("button") as HTMLButtonElement).click();
    });
    await idle(el);
    expect(byId(el, "error")).toBeNull();
    expect(el.textContent).toContain("There are no orders yet.");
  });

  it("retries the NEXT page, not the whole list, when 'Show more' fails", async () => {
    vi.mocked(searchShopOrders)
      .mockResolvedValueOnce({ orders: [o(2), o(1)], next_offset: 2 } as never)
      .mockRejectedValueOnce(new ApiError(500, "internal", "boom"))
      .mockResolvedValueOnce({ orders: [o(0)], next_offset: null } as never);
    const el = await mount();
    await act(async () => {
      byId(el, "shop-orders-more").click();
    });
    await idle(el);
    expect(rowCount(el)).toBe(2); // what is loaded stays on the screen
    await act(async () => {
      (byId(el, "error").querySelector("button") as HTMLButtonElement).click();
    });
    await idle(el);
    expect(rowCount(el)).toBe(3);
    expect(vi.mocked(searchShopOrders).mock.calls).toHaveLength(3);
  });
});
