// @vitest-environment jsdom
/**
 * Spec 61, section 3.4 and 5: the storefront switch on the settings screen. The organization opens and closes its
 * own storefront through PUT .../shop/storefront. Opening a shop that is not ready is refused with the list of what
 * is missing, which the screen shows in plain words with a link to fix it. The switch never rewrites the form.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const state = vi.hoisted(() => ({ perms: new Set<string>() }));

vi.mock("@/screens/OrgLayout", () => ({
  useOrg: () => ({
    orgId: "11111111-1111-4111-8111-111111111111",
    access: { can: (p: string) => state.perms.has(p), data: { platform_superadmin: false } },
  }),
}));
vi.mock("@/lib/api/endpoints", () => ({ listCountries: vi.fn().mockResolvedValue({ countries: [{ iso2: "IL", name: "Israel" }] }) }));
vi.mock("@/lib/api/shopApi", () => ({
  fetchShopSettings: vi.fn(),
  saveShopSettings: vi.fn(),
  setShopStorefront: vi.fn(),
  listSalesChannels: vi.fn(),
  listShopProducts: vi.fn(),
}));

import { ApiError } from "@/lib/api/http";
import { listSalesChannels, listShopProducts, saveShopSettings, setShopStorefront } from "@/lib/api/shopApi";
import type { ShopSettings } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";
import { SettingsPage } from "@/screens/ShopSettingsScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;

const ORG = "11111111-1111-4111-8111-111111111111";
const roots: Root[] = [];

function settings(over: Partial<ShopSettings> = {}): ShopSettings {
  return {
    org_id: ORG,
    configured: true,
    currency: "ILS",
    shipping_fee_minor: 2500,
    shipping_label: "Courier",
    country_iso2: "IL",
    age_gate_min_age: null,
    announcement_text: "",
    contact_email: null,
    logistics_note: "",
    sales_channel_id: "c1",
    shop_domain: null,
    max_line_qty: 24,
    enabled: false,
    updated_at: "2026-11-05T10:00:00.000001Z",
    module_active: true,
    shop_mode: "full",
    can_open: true,
    open_blockers: [],
    ...over,
  } as ShopSettings;
}

beforeEach(() => {
  state.perms = new Set(["shop.read", "shop.manage", "org.update"]);
  for (const fn of [saveShopSettings, setShopStorefront, listSalesChannels, listShopProducts]) vi.mocked(fn).mockReset();
  vi.mocked(listSalesChannels).mockResolvedValue({ channels: [{ id: "c1", name: "Own site" }] } as never);
  vi.mocked(listShopProducts).mockResolvedValue({ products: [{ id: "p" }] } as never);
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLElement;

async function mount(first: ShopSettings): Promise<HTMLElement> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const page = createRoute({ getParentRoute: () => rootRoute, path: "/", component: () => <SettingsPage settings={first} /> });
  const products = createRoute({ getParentRoute: () => rootRoute, path: "/o/$orgId/shop/products", component: () => <p>products</p> });
  const router = createRouter({ routeTree: rootRoute.addChildren([page, products]), history: createMemoryHistory({ initialEntries: ["/"] }) });
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
    await sleep(150);
  });
  return el;
}

async function press(el: HTMLElement, id: string) {
  await act(async () => {
    byId(el, id).click();
    await sleep(60);
  });
}

async function typeInto(field: HTMLElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

describe("the storefront switch", () => {
  it("opens a ready storefront through PUT .../shop/storefront and shows it open", async () => {
    vi.mocked(setShopStorefront).mockResolvedValue(settings({ enabled: true, updated_at: "2026-11-05T10:01:00.000001Z" }));
    const el = await mount(settings());
    expect(byId(el, "shop-storefront-status").textContent).toBe("Closed");
    expect(byId(el, "shop-storefront-toggle").textContent).toBe("Open the storefront");
    expect(byId(el, "shop-storefront-blockers")).toBeNull();
    await press(el, "shop-storefront-toggle");
    expect(setShopStorefront).toHaveBeenCalledWith(ORG, true);
    expect(byId(el, "shop-storefront-status").textContent).toBe("Open to buyers");
    expect(byId(el, "shop-storefront-toggle").textContent).toBe("Close the storefront");
  });

  it("closes an open storefront", async () => {
    vi.mocked(setShopStorefront).mockResolvedValue(settings({ enabled: false }));
    const el = await mount(settings({ enabled: true }));
    expect(byId(el, "shop-storefront-status").textContent).toBe("Open to buyers");
    await press(el, "shop-storefront-toggle");
    expect(setShopStorefront).toHaveBeenCalledWith(ORG, false);
    expect(byId(el, "shop-storefront-status").textContent).toBe("Closed");
  });

  it("lists what blocks the opening, with a link to the products, and does not call the API", async () => {
    const el = await mount(settings({ can_open: false, open_blockers: ["no_active_product", "payment_not_configured"] }));
    const list = byId(el, "shop-storefront-blockers");
    expect(list.textContent).toContain("There is no product on sale.");
    expect(list.textContent).toContain("Online payment is not connected for the shop.");
    expect(list.textContent).not.toContain("settings are not saved");
    expect(byId(el, "shop-storefront-fix-products").getAttribute("href")).toContain("/shop/products");
    expect(byId(el, "shop-storefront-fix-payments")).not.toBeNull();
    const toggle = byId(el, "shop-storefront-toggle") as HTMLButtonElement;
    expect(toggle.disabled).toBe(true);
    await press(el, "shop-storefront-toggle");
    expect(setShopStorefront).not.toHaveBeenCalled();
  });

  it("offers the payment link only to who can open the organization data", async () => {
    state.perms.delete("org.update");
    const el = await mount(settings({ can_open: false, open_blockers: ["payment_not_configured"] }));
    expect(byId(el, "shop-storefront-blocker-payment_not_configured")).not.toBeNull();
    expect(byId(el, "shop-storefront-fix-payments")).toBeNull();
  });

  it("shows the blockers of a refused opening (422 shop.cannot_open) and keeps the storefront closed", async () => {
    vi.mocked(setShopStorefront).mockRejectedValue(new ApiError(422, "shop.cannot_open", "no", undefined, { blockers: ["settings_not_saved", "no_active_product"] }));
    const el = await mount(settings());
    await press(el, "shop-storefront-toggle");
    expect(byId(el, "shop-storefront-error").textContent).toContain("You cannot open the storefront.");
    expect(byId(el, "shop-storefront-blocker-settings_not_saved")).not.toBeNull();
    expect(byId(el, "shop-storefront-blocker-no_active_product")).not.toBeNull();
    expect(byId(el, "shop-storefront-status").textContent).toBe("Closed");
    expect((byId(el, "shop-storefront-toggle") as HTMLButtonElement).disabled).toBe(true);
  });

  it("says the shop is not connected when the module ended under an open screen (404 shop.module_not_enabled)", async () => {
    vi.mocked(setShopStorefront).mockRejectedValue(new ApiError(404, "shop.module_not_enabled", "no"));
    const el = await mount(settings());
    await press(el, "shop-storefront-toggle");
    expect(byId(el, "shop-storefront-error").textContent).toContain("We did not connect the shop for this organization.");
  });

  it("is only a statement without shop.manage", async () => {
    state.perms = new Set(["shop.read"]);
    const el = await mount(settings({ enabled: true }));
    expect(byId(el, "shop-storefront-status").textContent).toBe("Open to buyers");
    expect(byId(el, "shop-storefront-toggle")).toBeNull();
  });

  it("does not touch the form, and the next save carries the version the switch produced", async () => {
    vi.mocked(setShopStorefront).mockResolvedValue(settings({ enabled: true, updated_at: "2026-11-05T10:05:00.000001Z" }));
    vi.mocked(saveShopSettings).mockResolvedValue(settings({ enabled: true, shipping_fee_minor: 3000, updated_at: "2026-11-05T10:06:00.000001Z" }));
    const el = await mount(settings());
    await typeInto(byId(el, "shop-settings-fee"), "30.00");
    await press(el, "shop-storefront-toggle");
    // The typed text stays.
    expect((byId(el, "shop-settings-fee") as HTMLInputElement).value).toBe("30.00");
    await press(el, "shop-settings-save");
    const sent = vi.mocked(saveShopSettings).mock.calls[0][1];
    expect(sent).toMatchObject({ shipping_fee_minor: 3000, expected_updated_at: "2026-11-05T10:05:00.000001Z" });
    // The settings form never sends the switch.
    expect(sent).not.toHaveProperty("enabled");
  });
});
