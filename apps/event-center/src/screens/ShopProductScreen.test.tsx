// @vitest-environment jsdom
/**
 * A-02: the product card has ONE Save. It sends what changed in a fixed order (details, variants, picture
 * descriptions), reports one result, keeps the 409 stock conflict handling, names what a failed save had already
 * stored, and while anything is unsaved a move to another screen asks first. The editor of the description is a
 * plain text area here (its own tests are in richText.test.tsx). The clicks against a real API are the Playwright
 * smoke (tests/smoke/shop.mock.spec.ts).
 */
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { RouterProvider, createMemoryHistory, createRootRoute, createRoute, createRouter } from "@tanstack/react-router";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/components/RichTextField", async () => {
  const React = await import("react");
  return {
    RichTextField: ({ value, onChange, testId }: { value: string; onChange: (v: string) => void; testId?: string }) =>
      React.createElement("textarea", { "data-testid": testId, value, onChange: (e: { target: { value: string } }) => onChange(e.target.value) }),
  };
});

vi.mock("@/lib/api/shopApi", () => ({
  patchShopProduct: vi.fn(),
  replaceShopVariants: vi.fn(),
  fetchShopProduct: vi.fn(),
  attachShopImage: vi.fn(),
  detachShopImage: vi.fn(),
  uploadShopImage: vi.fn(),
  createShopProduct: vi.fn(),
  deleteShopProduct: vi.fn(),
  uploadShopDigitalFile: vi.fn(),
  fetchShopSettings: vi.fn(),
}));

import { ApiError } from "@/lib/api/http";
import { attachShopImage, createShopProduct, fetchShopProduct, patchShopProduct, replaceShopVariants } from "@/lib/api/shopApi";
import { I18nProvider } from "@/lib/i18n";
import type { ShopProductView } from "@/lib/shop";
import { ProductForm } from "@/screens/ShopProductScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
window.scrollTo = () => undefined;
Element.prototype.scrollIntoView = () => undefined;

const ORG = "11111111-1111-4111-8111-111111111111";
const roots: Root[] = [];

function variant(id: string, stock: number, over: Record<string, unknown> = {}) {
  return { id, title: "", sku: null, price_minor: 2000, compare_at_minor: null, stock, track_stock: true, held: 0, available: stock, position: 0, option1: null, option2: null, option3: null, ...over };
}

function product(over: Partial<ShopProductView> = {}): ShopProductView {
  return {
    id: "p1",
    org_id: ORG,
    slug: "riesling",
    title: "Riesling",
    body_html: "<p>Dry</p>",
    status: "active",
    kind: "physical",
    option_names: [],
    images: [{ media_id: "m1", url: "", position: 0, alt: "old" }],
    variants: [variant("v1", 12)],
    digital_media_id: null,
    created_at: "2026-10-01T10:00:00Z",
    updated_at: "2026-10-01T10:00:00Z",
    ...over,
  } as unknown as ShopProductView;
}

beforeEach(() => {
  for (const fn of [patchShopProduct, replaceShopVariants, fetchShopProduct, attachShopImage, createShopProduct]) vi.mocked(fn).mockReset();
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** The page: the product comes from its query, so a saved product reaches the form as it does in the app. */
function Page({ first, canManage }: { first: ShopProductView; canManage: boolean }) {
  const q = useQuery({ queryKey: ["shopProduct", ORG, first.id], queryFn: () => Promise.resolve(first), initialData: first, staleTime: Infinity });
  return <ProductForm orgId={ORG} currency="EUR" product={q.data} canManage={canManage} canViewMedia={false} />;
}

async function mount(p: ShopProductView, canManage = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute();
  const card = createRoute({
    getParentRoute: () => rootRoute,
    path: "/",
    component: () => <Page first={p} canManage={canManage} />,
  });
  const other = createRoute({ getParentRoute: () => rootRoute, path: "/other", component: () => <p>other</p> });
  const router = createRouter({ routeTree: rootRoute.addChildren([card, other]), history: createMemoryHistory({ initialEntries: ["/"] }) });
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
  return { el, router, client };
}

const q = (el: HTMLElement, id: string) => el.querySelector('[data-testid="' + id + '"]') as HTMLInputElement;
const status = (el: HTMLElement) => q(el, "shop-savebar-status").textContent ?? "";

async function typeInto(field: HTMLInputElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

async function save(el: HTMLElement) {
  await act(async () => {
    q(el, "shop-product-save").click();
    await sleep(50);
  });
}

describe("the product card: one Save", () => {
  it("has ONE save button, disabled until something changed, and none for a reader", async () => {
    const { el } = await mount(product());
    expect(el.querySelectorAll('[data-testid="shop-product-save"]')).toHaveLength(1);
    expect(q(el, "shop-variants-save")).toBeNull();
    expect(q(el, "shop-image-0-alt-save")).toBeNull();
    expect(q(el, "shop-product-save").disabled).toBe(true);
    expect(status(el)).toBe("");
    await typeInto(q(el, "shop-product-title"), "Riesling 2021");
    expect(q(el, "shop-product-save").disabled).toBe(false);
    expect(status(el)).toBe("There are unsaved changes.");
    const reader = await mount(product(), false);
    expect(reader.el.querySelector('[data-testid="shop-savebar"]')).toBeNull();
    expect(reader.el.querySelector('[data-testid="shop-product-save"]')).toBeNull();
  });

  it("sends the details, then the variants, then the picture descriptions, and says one thing", async () => {
    const order: string[] = [];
    vi.mocked(patchShopProduct).mockImplementation(async () => {
      order.push("patch");
      return product({ title: "Riesling 2021", updated_at: "2026-10-02T10:00:00Z" });
    });
    vi.mocked(replaceShopVariants).mockImplementation(async () => {
      order.push("variants");
      return product({ title: "Riesling 2021", variants: [variant("v1", 20)], updated_at: "2026-10-02T10:00:01Z" });
    });
    vi.mocked(attachShopImage).mockImplementation(async () => {
      order.push("image");
      return product({ title: "Riesling 2021", variants: [variant("v1", 20)], images: [{ media_id: "m1", url: "", position: 0, alt: "Bottle" }], updated_at: "2026-10-02T10:00:02Z" } as Partial<ShopProductView>);
    });
    const { el } = await mount(product());
    await typeInto(q(el, "shop-product-title"), "Riesling 2021");
    await typeInto(q(el, "shop-variant-stock-0"), "20");
    await typeInto(q(el, "shop-image-0-alt"), "Bottle");
    await save(el);
    expect(order).toEqual(["patch", "variants", "image"]);
    expect(vi.mocked(patchShopProduct).mock.calls[0][2]).toMatchObject({ title: "Riesling 2021" });
    expect(vi.mocked(replaceShopVariants).mock.calls[0][2][0]).toMatchObject({ id: "v1", stock: 20, expected_stock: 12 });
    expect(vi.mocked(attachShopImage).mock.calls[0]).toEqual([ORG, "p1", "m1", 0, "Bottle"]);
    expect(status(el)).toBe("Everything is saved.");
    expect(q(el, "shop-product-save").disabled).toBe(true);
  });

  it("sends only the part that changed", async () => {
    vi.mocked(replaceShopVariants).mockResolvedValue(product({ variants: [variant("v1", 20)], updated_at: "2026-10-02T10:00:01Z" }));
    const { el } = await mount(product());
    await typeInto(q(el, "shop-variant-stock-0"), "20");
    await save(el);
    expect(patchShopProduct).not.toHaveBeenCalled();
    expect(attachShopImage).not.toHaveBeenCalled();
    expect(replaceShopVariants).toHaveBeenCalledTimes(1);
  });

  it("on a 409 stops, reloads the numbers, says what is saved already and writes nothing over the new stock", async () => {
    vi.mocked(patchShopProduct).mockResolvedValue(product({ title: "Riesling 2021", updated_at: "2026-10-02T10:00:00Z" }));
    vi.mocked(replaceShopVariants).mockRejectedValue(new ApiError(409, "shop.stock_changed", "moved", undefined, { variants: [{ id: "v1", expected_stock: 12, stock: 9 }] }));
    vi.mocked(fetchShopProduct).mockResolvedValue(product({ title: "Riesling 2021", variants: [variant("v1", 9)], updated_at: "2026-10-02T10:00:02Z" }));
    const { el } = await mount(product());
    await typeInto(q(el, "shop-product-title"), "Riesling 2021");
    await typeInto(q(el, "shop-variant-stock-0"), "20");
    await typeInto(q(el, "shop-image-0-alt"), "Bottle");
    await save(el);
    expect(attachShopImage).not.toHaveBeenCalled();
    expect(fetchShopProduct).toHaveBeenCalledTimes(1);
    expect(status(el)).toContain("Not everything was saved.");
    expect(status(el)).toContain("Already saved: the product.");
    expect(q(el, "shop-stock-changed")).not.toBeNull();
    expect(q(el, "shop-stock-moved").textContent).toContain("you saw 12, now 9");
    expect(q(el, "shop-stock-moved").textContent).toContain("You typed 20");
    expect(q(el, "shop-variant-stock-0").value).toBe("9");
    // The description the person typed is still there, waiting for the next Save.
    expect(q(el, "shop-image-0-alt").value).toBe("Bottle");
    expect(q(el, "shop-product-save").disabled).toBe(false);
  });

  it("names a wrong field before anything is sent", async () => {
    const { el } = await mount(product());
    await typeInto(q(el, "shop-product-title"), "Riesling 2021");
    await typeInto(q(el, "shop-variant-price-0"), "lots");
    await save(el);
    expect(patchShopProduct).not.toHaveBeenCalled();
    expect(replaceShopVariants).not.toHaveBeenCalled();
    expect(q(el, "shop-variant-price-0-error")).not.toBeNull();
    expect(q(el, "shop-product-fix")).not.toBeNull();
  });

  it("asks before the person leaves with unsaved changes, and not otherwise", async () => {
    const clean = await mount(product());
    await act(async () => {
      void clean.router.navigate({ to: "/other" });
      await sleep(50);
    });
    expect(clean.router.state.location.pathname).toBe("/other");
    for (const r of roots.splice(0)) act(() => r.unmount());
    document.body.innerHTML = "";

    const { el, router } = await mount(product());
    await typeInto(q(el, "shop-product-title"), "Riesling 2021");
    await act(async () => {
      void router.navigate({ to: "/other" });
      await sleep(50);
    });
    expect(router.state.location.pathname).toBe("/");
    expect(q(el, "unsaved-dialog").textContent).toContain("There are unsaved changes. Leave?");
  });
});

describe("leaving with unsaved changes (M-09)", () => {
  async function dirtyAndLeave(canManage = true) {
    const m = await mount(product(), canManage);
    await typeInto(q(m.el, "shop-product-title"), "Riesling 2021");
    await act(async () => {
      void m.router.navigate({ to: "/other" });
      await sleep(50);
    });
    return m;
  }

  it("offers three ways out, and hides the Save bar while the question is open", async () => {
    const { el, router } = await dirtyAndLeave();
    const buttons = Array.from(el.querySelectorAll('[data-testid="unsaved-dialog"] button')).map((b) => b.getAttribute("data-testid"));
    expect(buttons).toEqual(["unsaved-stay", "unsaved-save-leave", "unsaved-leave"]);
    const bar = q(el, "shop-savebar");
    expect(bar.hidden).toBe(true);
    expect(bar.getAttribute("aria-hidden")).toBe("true");
    await act(async () => {
      q(el, "unsaved-stay").click();
      await sleep(20);
    });
    expect(q(el, "shop-savebar").hidden).toBe(false);
    expect(router.state.location.pathname).toBe("/");
  });

  it("'Save and leave' runs the one Save and then goes", async () => {
    vi.mocked(patchShopProduct).mockResolvedValue(product({ title: "Riesling 2021" }));
    const { el, router } = await dirtyAndLeave();
    await act(async () => {
      q(el, "unsaved-save-leave").click();
      await sleep(80);
    });
    expect(patchShopProduct).toHaveBeenCalledTimes(1);
    expect(router.state.location.pathname).toBe("/other");
  });

  it("'Save and leave' stays when the save failed, and says what is wrong", async () => {
    vi.mocked(patchShopProduct).mockRejectedValue(new ApiError(500, "internal", "boom", undefined));
    const { el, router } = await dirtyAndLeave();
    await act(async () => {
      q(el, "unsaved-save-leave").click();
      await sleep(80);
    });
    expect(router.state.location.pathname).toBe("/");
    expect(q(el, "unsaved-dialog")).toBeNull();
    expect(q(el, "shop-savebar").hidden).toBe(false);
    expect(status(el)).toContain("Not everything was saved");
  });
});

describe("a new product of another kind has no shelf (M-01)", () => {
  async function mountNew() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const rootRoute = createRootRoute();
    const card = createRoute({
      getParentRoute: () => rootRoute,
      path: "/",
      component: () => <ProductForm orgId={ORG} currency="EUR" product={undefined} canManage canViewMedia={false} />,
    });
    const router = createRouter({ routeTree: rootRoute.addChildren([card]), history: createMemoryHistory({ initialEntries: ["/"] }) });
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
    return el;
  }
  async function pickKind(el: HTMLElement, kind: string) {
    await act(async () => {
      const sel = q(el, "shop-product-kind") as unknown as HTMLSelectElement;
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")?.set?.call(sel, kind);
      sel.dispatchEvent(new Event("change", { bubbles: true }));
      await sleep(0);
    });
  }

  it("physical counts stock by default, a service does not and hides the stock field", async () => {
    const el = await mountNew();
    expect(q(el, "shop-variant-track-0").checked).toBe(true);
    expect(q(el, "shop-variant-stock-0")).not.toBeNull();
    await pickKind(el, "service");
    expect(q(el, "shop-variant-track-0").checked).toBe(false);
    expect(q(el, "shop-variant-stock-0")).toBeNull();
    await pickKind(el, "physical");
    expect(q(el, "shop-variant-track-0").checked).toBe(true);
  });

  it("creates a service with the tracking flag sent as false", async () => {
    vi.mocked(createShopProduct).mockResolvedValue(product({ kind: "service" } as Partial<ShopProductView>));
    const el = await mountNew();
    await pickKind(el, "service");
    await typeInto(q(el, "shop-product-title"), "Sommelier consultation");
    await typeInto(q(el, "shop-variant-price-0"), "20");
    await act(async () => {
      (el.querySelector('[data-testid="shop-product-form"]') as HTMLFormElement).dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
      await sleep(80);
    });
    expect(createShopProduct).toHaveBeenCalledTimes(1);
    const body = vi.mocked(createShopProduct).mock.calls[0][1] as { kind: string; variants: { track_stock: boolean }[] };
    expect(body.kind).toBe("service");
    expect(body.variants[0].track_stock).toBe(false);
  });
});
