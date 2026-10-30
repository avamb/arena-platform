// @vitest-environment jsdom
/**
 * SB-14 on the products list: a failed list says so and offers a retry, an empty shop gets its own text, and the
 * address cannot be used to print an invented sentence about a deleted product.
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
  listShopProducts: vi.fn(),
  fetchShopSettings: vi.fn(),
}));

import { ApiError } from "@/lib/api/http";
import { fetchShopSettings, listShopProducts } from "@/lib/api/shopApi";
import { I18nProvider } from "@/lib/i18n";
import { parseProductsSearch } from "@/lib/shop";
import { ShopProductsScreen } from "@/screens/ShopProductsScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;

const roots: Root[] = [];

beforeEach(() => {
  vi.mocked(listShopProducts).mockReset();
  vi.mocked(fetchShopSettings).mockReset();
  vi.mocked(fetchShopSettings).mockResolvedValue({ currency: "EUR" } as never);
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

async function mount(search = ""): Promise<HTMLElement> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const page = createRoute({ getParentRoute: () => rootRoute, path: "/o/$orgId/shop/products", validateSearch: parseProductsSearch, component: ShopProductsScreen });
  const router = createRouter({
    routeTree: rootRoute.addChildren([page]),
    history: createMemoryHistory({ initialEntries: ["/o/11111111-1111-4111-8111-111111111111/shop/products" + search] }),
  });
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

async function idle(el: HTMLElement) {
  for (let i = 0; i < 40; i++) {
    await act(async () => {
      await sleep(25);
    });
    if (el.querySelector('[data-testid="loading"]') === null) return;
  }
}

describe("the products list (SB-14)", () => {
  it("shows an error with a retry, then the empty-shop text", async () => {
    vi.mocked(listShopProducts).mockRejectedValueOnce(new ApiError(500, "internal", "boom")).mockResolvedValueOnce({ products: [] } as never);
    const el = await mount();
    const box = byId(el, "error");
    expect(box).not.toBeNull();
    await act(async () => {
      (box.querySelector("button") as HTMLButtonElement).click();
    });
    await idle(el);
    expect(byId(el, "error")).toBeNull();
    expect(el.textContent).toContain("There are no products yet.");
  });

  it("does not print what the address says was deleted", async () => {
    vi.mocked(listShopProducts).mockResolvedValue({ products: [] } as never);
    const el = await mount("?deleted=Your%20account%20is%20closed");
    const notice = byId(el, "shop-products-notice");
    expect(notice.textContent).toBe("The product is deleted.");
    expect(el.textContent).not.toContain("account is closed");
  });
});
