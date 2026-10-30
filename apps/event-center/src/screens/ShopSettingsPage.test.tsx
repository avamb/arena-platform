// @vitest-environment jsdom
/**
 * SA-12 and SA-23 on the settings screen: the version the form was loaded with goes back with the save, a refused
 * save (somebody saved first) offers to reload and loses nothing silently, the question on leaving appears only for
 * unsaved work, and a shop without a chosen sales channel is told where its money goes.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/screens/OrgLayout", () => ({
  useOrg: () => ({ orgId: "11111111-1111-4111-8111-111111111111", access: { can: () => true, data: { platform_superadmin: false } } }),
}));
vi.mock("@/lib/api/endpoints", () => ({ listCountries: vi.fn().mockResolvedValue({ countries: [{ iso2: "IL", name: "Israel" }] }) }));
vi.mock("@/lib/api/shopApi", () => ({
  fetchShopSettings: vi.fn(),
  saveShopSettings: vi.fn(),
  listSalesChannels: vi.fn(),
  listShopProducts: vi.fn(),
}));

import { listCountries } from "@/lib/api/endpoints";
import { ApiError } from "@/lib/api/http";
import { fetchShopSettings, listSalesChannels, listShopProducts, saveShopSettings } from "@/lib/api/shopApi";
import type { ShopSettings } from "@/lib/api/types";
import { I18nProvider } from "@/lib/i18n";
import { SettingsPage } from "@/screens/ShopSettingsScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;

const roots: Root[] = [];
let router: ReturnType<typeof createRouter>;

function settings(over: Partial<ShopSettings> = {}): ShopSettings {
  return {
    org_id: "o",
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
    enabled: true,
    updated_at: "2026-10-30T10:00:00.123456Z",
    ...over,
  } as ShopSettings;
}

beforeEach(() => {
  for (const fn of [fetchShopSettings, saveShopSettings, listSalesChannels, listShopProducts]) vi.mocked(fn).mockReset();
  vi.mocked(listSalesChannels).mockResolvedValue({ channels: [{ id: "c1", name: "AllPay site" }, { id: "c2", name: "Tickets widget" }] } as never);
  vi.mocked(listShopProducts).mockResolvedValue({ products: [{ id: "p" }] } as never);
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const byId = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLInputElement;

async function mount(first: ShopSettings): Promise<HTMLElement> {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const page = createRoute({ getParentRoute: () => rootRoute, path: "/", component: () => <SettingsPage settings={first} /> });
  const other = createRoute({ getParentRoute: () => rootRoute, path: "/other", component: () => <p>other</p> });
  router = createRouter({ routeTree: rootRoute.addChildren([page, other]), history: createMemoryHistory({ initialEntries: ["/"] }) });
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

/** Lets the queries that were asked for answer. */
async function settle() {
  await act(async () => {
    await sleep(100);
  });
}

async function typeInto(field: HTMLInputElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

async function press(el: HTMLElement, id: string) {
  await act(async () => {
    byId(el, id).click();
    await sleep(40);
  });
}

describe("the settings screen", () => {
  it("sends the version it was loaded with, and shows the stored settings after the save", async () => {
    vi.mocked(saveShopSettings).mockResolvedValue(settings({ shipping_fee_minor: 3000, updated_at: "2026-10-30T10:05:00.000001Z" }));
    const el = await mount(settings());
    await typeInto(byId(el, "shop-settings-fee"), "30.00");
    await press(el, "shop-settings-save");
    expect(saveShopSettings).toHaveBeenCalledTimes(1);
    expect(vi.mocked(saveShopSettings).mock.calls[0][1]).toMatchObject({ shipping_fee_minor: 3000, expected_updated_at: "2026-10-30T10:00:00.123456Z" });
    expect(byId(el, "shop-settings-saved")).not.toBeNull();
    // The next save carries the version the first one produced.
    await typeInto(byId(el, "shop-settings-fee"), "31.00");
    vi.mocked(saveShopSettings).mockResolvedValue(settings({ shipping_fee_minor: 3100, updated_at: "2026-10-30T10:06:00.000001Z" }));
    await press(el, "shop-settings-save");
    expect(vi.mocked(saveShopSettings).mock.calls[1][1]).toMatchObject({ expected_updated_at: "2026-10-30T10:05:00.000001Z" });
  });

  it("offers to reload when somebody saved first, keeps the typed text until then, and reloads on request", async () => {
    vi.mocked(saveShopSettings).mockRejectedValue(new ApiError(409, "shop.settings_changed", "changed"));
    vi.mocked(fetchShopSettings).mockResolvedValue(settings({ shipping_fee_minor: 9900, updated_at: "2026-10-30T10:30:00.000000Z" }));
    const el = await mount(settings());
    await typeInto(byId(el, "shop-settings-fee"), "30.00");
    await press(el, "shop-settings-save");
    expect(byId(el, "shop-settings-error").textContent).toContain("Someone saved these settings while you were editing.");
    expect(byId(el, "shop-settings-fee").value).toBe("30.00");
    expect(fetchShopSettings).not.toHaveBeenCalled();
    await press(el, "shop-settings-reload");
    expect(fetchShopSettings).toHaveBeenCalledTimes(1);
    expect(byId(el, "shop-settings-fee").value).toBe("99.00");
    expect(byId(el, "shop-settings-reloaded")).not.toBeNull();
    expect(byId(el, "shop-settings-error")).toBeNull();
    // The reloaded version is the one the next save sends.
    vi.mocked(saveShopSettings).mockReset();
    vi.mocked(saveShopSettings).mockResolvedValue(settings({ updated_at: "2026-10-30T10:31:00.000000Z" }));
    await typeInto(byId(el, "shop-settings-fee"), "5.00");
    await press(el, "shop-settings-save");
    expect(vi.mocked(saveShopSettings).mock.calls[0][1]).toMatchObject({ expected_updated_at: "2026-10-30T10:30:00.000000Z" });
  });

  it("says why the currency cannot change and why the provider refuses one (the server's words)", async () => {
    vi.mocked(saveShopSettings).mockRejectedValue(new ApiError(422, "shop.currency_unsupported", "no"));
    const el = await mount(settings());
    await typeInto(byId(el, "shop-settings-fee"), "30.00");
    await press(el, "shop-settings-save");
    expect(byId(el, "shop-settings-error").textContent).toContain("The payment provider cannot take this currency.");
    expect(byId(el, "shop-settings-reload")).toBeNull();
    vi.mocked(saveShopSettings).mockRejectedValue(new ApiError(409, "shop.currency_locked", "no"));
    await press(el, "shop-settings-save");
    expect(byId(el, "shop-settings-error").textContent).toContain("The currency cannot be changed now.");
  });

  it("asks before the page is left with unsaved settings, and not otherwise", async () => {
    await mount(settings());
    await act(async () => {
      void router.navigate({ to: "/other" });
      await sleep(50);
    });
    expect(router.state.location.pathname).toBe("/other");
    for (const r of roots.splice(0)) act(() => r.unmount());
    document.body.innerHTML = "";

    const el = await mount(settings());
    await typeInto(byId(el, "shop-settings-fee"), "30.00");
    await act(async () => {
      void router.navigate({ to: "/other" });
      await sleep(50);
    });
    expect(router.state.location.pathname).toBe("/");
    expect(byId(el, "unsaved-dialog").textContent).toContain("There are unsaved changes. Leave?");
  });

  it("warns that the money goes to the first suitable channel when none is chosen (SA-23)", async () => {
    const el = await mount(settings({ sales_channel_id: null }));
    await settle();
    expect(byId(el, "shop-settings-channel-warning").textContent).toContain("No sales channel is chosen");
    const select = byId(el, "shop-settings-channel") as unknown as HTMLSelectElement;
    await act(async () => {
      select.value = "c1";
      select.dispatchEvent(new Event("change", { bubbles: true }));
      await sleep(0);
    });
    expect(byId(el, "shop-settings-channel-warning")).toBeNull();
  });

  it("says that a shop with no channel at all cannot be paid for", async () => {
    vi.mocked(listSalesChannels).mockResolvedValue({ channels: [] } as never);
    const el = await mount(settings({ sales_channel_id: null }));
    await settle();
    expect(byId(el, "shop-settings-channel-warning").textContent).toContain("no sales channel yet");
  });
});

describe("a helper list that fails to load (SB-13)", () => {
  it("shows an error with a retry instead of staying on 'checking' for good, and keeps the form usable", async () => {
    vi.mocked(listShopProducts).mockRejectedValueOnce(new ApiError(500, "internal", "boom")).mockResolvedValue({ products: [{ id: "p" }] } as never);
    const el = await mount(settings());
    await settle();
    const box = byId(el, "shop-settings-products-error");
    expect(box).not.toBeNull();
    expect(box.textContent).toContain("It stays as it is");
    expect(byId(el, "shop-settings-currency-locked")).toBeNull();
    // The rest of the form is not blocked.
    expect((byId(el, "shop-settings-save") as unknown as HTMLButtonElement).disabled).toBe(false);
    expect((byId(el, "shop-settings-fee") as unknown as HTMLInputElement).disabled).toBe(false);
    await act(async () => {
      (box.querySelector("button") as HTMLButtonElement).click();
      await sleep(80);
    });
    expect(listShopProducts).toHaveBeenCalledTimes(2);
    expect(byId(el, "shop-settings-products-error")).toBeNull();
    expect(byId(el, "shop-settings-currency-locked")).not.toBeNull();
  });

  it("keeps the stored country selected and offers a retry when the country list fails", async () => {
    vi.mocked(listCountries).mockRejectedValueOnce(new ApiError(500, "internal", "boom"));
    const el = await mount(settings({ country_iso2: "IL" }));
    await settle();
    expect(byId(el, "shop-settings-countries-error")).not.toBeNull();
    expect((byId(el, "shop-settings-country") as unknown as HTMLSelectElement).value).toBe("IL");
  });

  it("says that the channels did not load, not that there are no rights", async () => {
    vi.mocked(listSalesChannels).mockRejectedValue(new ApiError(500, "internal", "boom"));
    const el = await mount(settings({ sales_channel_id: null }));
    await settle();
    expect(byId(el, "shop-settings-channels-error")).not.toBeNull();
    expect(byId(el, "shop-settings-channel-locked")).toBeNull();
    expect(byId(el, "shop-settings-channel-warning")).toBeNull();
  });

  it("sends one PUT when the save is asked for twice in a row", async () => {
    let release: (v: ShopSettings) => void = () => undefined;
    vi.mocked(saveShopSettings).mockImplementation(() => new Promise<ShopSettings>((r) => (release = r)));
    const el = await mount(settings());
    await settle();
    await typeInto(byId(el, "shop-settings-label"), "Express");
    const form = byId(el, "shop-settings-form") as unknown as HTMLFormElement;
    await act(async () => {
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await sleep(10);
    });
    expect(saveShopSettings).toHaveBeenCalledTimes(1);
    await act(async () => {
      release(settings({ shipping_label: "Express" }));
      await sleep(10);
    });
  });
});
