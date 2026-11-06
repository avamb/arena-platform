// @vitest-environment jsdom
/**
 * Spec 61, section 5: the door of the shop area. Without the module the screens say "The shop is not connected",
 * and the shop menu item is not drawn. After the module ended (orders_only) only the orders are open: a banner
 * on top, the nav holds the orders alone, and every other shop address goes to the orders.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRoute, createRouter, useRouterState } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/screens/OrgLayout", () => ({
  useOrg: () => ({ orgId: "11111111-1111-4111-8111-111111111111", orgName: "Teatr", access: { can: () => true, data: { platform_superadmin: false } } }),
}));

import { I18nProvider } from "@/lib/i18n";
import type { ShopMode } from "@/lib/shopModule";
import { ShopNav } from "@/components/ShopNav";
import { ShopGateView } from "@/screens/ShopGate";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;

const ORG = "11111111-1111-4111-8111-111111111111";
const roots: Root[] = [];

afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

function Inside({ mode }: { mode: ShopMode }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  return (
    <ShopGateView mode={mode} pathname={pathname} orgId={ORG}>
      <ShopNav orgId={ORG} />
      <p data-testid="inside">{pathname}</p>
    </ShopGateView>
  );
}

async function mount(mode: ShopMode, at: string) {
  const rootRoute = createRootRoute({ component: () => <Inside mode={mode} /> });
  const paths = ["/o/$orgId/shop/products", "/o/$orgId/shop/orders", "/o/$orgId/shop/orders/$orderId", "/o/$orgId/shop/settings"];
  const routes = paths.map((path) => createRoute({ getParentRoute: () => rootRoute, path }));
  const router = createRouter({ routeTree: rootRoute.addChildren(routes), history: createMemoryHistory({ initialEntries: [at] }) });
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={new QueryClient()}>
        <I18nProvider locale="en">
          <RouterProvider router={router} />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(150);
  });
  return { el, router };
}

describe("the shop gate", () => {
  it("says the shop is not connected, and draws neither the screen nor the nav", async () => {
    const { el } = await mount("none", `/o/${ORG}/shop/products`);
    expect(byId(el, "shop-not-connected").textContent).toContain("The shop is not connected for this organization.");
    expect(byId(el, "inside")).toBeNull();
    expect(byId(el, "shop-nav")).toBeNull();
  });

  it("lets the full shop through, with products, orders and settings in the nav", async () => {
    const { el, router } = await mount("full", `/o/${ORG}/shop/settings`);
    expect(byId(el, "inside").textContent).toBe(`/o/${ORG}/shop/settings`);
    expect(byId(el, "shop-orders-only")).toBeNull();
    for (const id of ["shop-nav-products", "shop-nav-orders", "shop-nav-settings"]) expect(byId(el, id), id).not.toBeNull();
    expect(router.state.location.pathname).toBe(`/o/${ORG}/shop/settings`);
  });

  it("in orders-only mode sends the catalog and the settings to the orders", async () => {
    for (const at of [`/o/${ORG}/shop/products`, `/o/${ORG}/shop/settings`]) {
      const { el, router } = await mount("orders_only", at);
      expect(router.state.location.pathname, at).toBe(`/o/${ORG}/shop/orders`);
      expect(byId(el, "inside").textContent).toBe(`/o/${ORG}/shop/orders`);
      for (const r of roots.splice(0)) act(() => r.unmount());
      document.body.innerHTML = "";
    }
  });

  it("in orders-only mode keeps the orders and an order card, with the banner and a nav of the orders alone", async () => {
    const { el, router } = await mount("orders_only", `/o/${ORG}/shop/orders/ab12`);
    expect(router.state.location.pathname).toBe(`/o/${ORG}/shop/orders/ab12`);
    expect(byId(el, "shop-orders-only").textContent).toContain("The shop is switched off and the storefront is closed.");
    expect(byId(el, "shop-orders-only").textContent).toContain("Orders and refunds are still available.");
    expect(byId(el, "shop-nav-orders")).not.toBeNull();
    expect(byId(el, "shop-nav-products")).toBeNull();
    expect(byId(el, "shop-nav-settings")).toBeNull();
  });
});
