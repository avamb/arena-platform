// @vitest-environment jsdom
/**
 * A-07 and A-15: the stock edited in the products list. One counted variant: a number field in the row that saves
 * when the person leaves it or presses Enter. Several: the total and an "Stock" disclosure with a field per variant.
 * A refused write (409) shows the number somebody else left and what was typed. The clicks against a real API are
 * the Playwright smoke (tests/smoke/shop.mock.spec.ts).
 */
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/shopApi", () => ({
  replaceShopVariants: vi.fn(),
  fetchShopProduct: vi.fn(),
}));

import { ShopStockCell } from "@/components/ShopStockCell";
import { ApiError } from "@/lib/api/http";
import { fetchShopProduct, replaceShopVariants } from "@/lib/api/shopApi";
import { I18nProvider } from "@/lib/i18n";
import type { ShopProductView } from "@/lib/shop";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ORG = "11111111-1111-4111-8111-111111111111";
const roots: Root[] = [];
let client: QueryClient;

function variant(id: string, stock: number, over: Record<string, unknown> = {}) {
  return { id, title: "", sku: null, price_minor: 2000, compare_at_minor: null, stock, track_stock: true, held: 0, available: stock, position: 0, option1: null, option2: null, option3: null, ...over };
}

function product(variants: ReturnType<typeof variant>[], over: Partial<ShopProductView> = {}): ShopProductView {
  return { id: "p1", org_id: ORG, slug: "riesling", title: "Riesling", body_html: "", status: "active", kind: "physical", option_names: [], images: [], variants, created_at: "2026-10-01T10:00:00Z", updated_at: "2026-10-01T10:00:00Z", ...over } as unknown as ShopProductView;
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  vi.mocked(replaceShopVariants).mockReset();
  vi.mocked(fetchShopProduct).mockReset();
});

afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** The products list: the cell is drawn from the cached list, so a saved product reaches it as it does in the app. */
function Row({ fallback }: { fallback: ShopProductView }) {
  const list = useQuery({ queryKey: ["shopProducts", ORG, ""], queryFn: () => Promise.resolve({ products: [fallback] }), staleTime: Infinity });
  const p = list.data?.products[0] ?? fallback;
  return <ShopStockCell orgId={ORG} product={p} currency="EUR" />;
}

async function mount(p: ShopProductView): Promise<HTMLElement> {
  client.setQueryData(["shopProducts", ORG, ""], { products: [p] });
  const el = document.createElement("div");
  document.body.appendChild(el);
  const root = createRoot(el);
  roots.push(root);
  await act(async () => {
    root.render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="en">
          <Row fallback={p} />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(0);
  });
  return el;
}

function input(el: HTMLElement, index = 0): HTMLInputElement {
  return el.querySelectorAll<HTMLInputElement>('[data-testid="shop-stock-input"]')[index];
}

async function typeInto(field: HTMLInputElement, text: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")?.set?.call(field, text);
    field.dispatchEvent(new Event("input", { bubbles: true }));
    await sleep(0);
  });
}

async function leave(field: HTMLInputElement) {
  await act(async () => {
    field.focus();
    field.blur();
    await sleep(20);
  });
}

const status = (el: HTMLElement, i = 0) => el.querySelectorAll('[data-testid="shop-stock-status"]')[i]?.textContent ?? "";

describe("ShopStockCell: one variant", () => {
  it("shows the number in a field a thumb can hit", async () => {
    const el = await mount(product([variant("v1", 12)]));
    expect(input(el).value).toBe("12");
    expect(input(el).getAttribute("aria-label")).toBe("Stock of Riesling");
    expect(input(el).getAttribute("inputmode")).toBe("numeric");
    expect(el.querySelector('[data-testid="shop-stock-details"]')).toBeNull();
  });

  it("saves when the person leaves the field, sending every variant back with the stock the person saw", async () => {
    const p = product([variant("v1", 12)]);
    vi.mocked(replaceShopVariants).mockResolvedValue(product([variant("v1", 20)], { updated_at: "2026-10-02T10:00:00Z" }));
    const el = await mount(p);
    await typeInto(input(el), "20");
    await leave(input(el));
    expect(replaceShopVariants).toHaveBeenCalledTimes(1);
    const [org, id, body] = vi.mocked(replaceShopVariants).mock.calls[0];
    expect([org, id]).toEqual([ORG, "p1"]);
    expect(body).toHaveLength(1);
    expect(body[0]).toMatchObject({ id: "v1", stock: 20, expected_stock: 12, track_stock: true });
    expect(status(el)).toBe("Saved");
    // The list now holds the saved product, so every cell shows what the server has.
    const cached = client.getQueryData<{ products: ShopProductView[] }>(["shopProducts", ORG, ""]);
    expect(cached?.products[0].variants[0].stock).toBe(20);
    expect(input(el).value).toBe("20");
  });

  it("saves on Enter too", async () => {
    vi.mocked(replaceShopVariants).mockResolvedValue(product([variant("v1", 7)], { updated_at: "2026-10-02T10:00:00Z" }));
    const el = await mount(product([variant("v1", 12)]));
    await typeInto(input(el), "7");
    await act(async () => {
      input(el).dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      await sleep(20);
    });
    expect(replaceShopVariants).toHaveBeenCalledTimes(1);
    expect(vi.mocked(replaceShopVariants).mock.calls[0][2][0]).toMatchObject({ stock: 7, expected_stock: 12 });
  });

  it("sends nothing when the number did not change, and names a number that is not one", async () => {
    const el = await mount(product([variant("v1", 12)]));
    await typeInto(input(el), "12");
    await leave(input(el));
    expect(replaceShopVariants).not.toHaveBeenCalled();
    await typeInto(input(el), "twelve");
    await leave(input(el));
    expect(replaceShopVariants).not.toHaveBeenCalled();
    expect(status(el)).toBe("Type a whole number, 0 or more.");
    expect(input(el).getAttribute("aria-invalid")).toBe("true");
  });

  it("on a 409 shows the new number, the one the person saw and the one they typed, and writes nothing over it", async () => {
    vi.mocked(replaceShopVariants).mockRejectedValue(new ApiError(409, "shop.stock_changed", "moved", undefined, { variants: [{ id: "v1", expected_stock: 12, stock: 9 }] }));
    vi.mocked(fetchShopProduct).mockResolvedValue(product([variant("v1", 9)], { updated_at: "2026-10-02T10:00:00Z" }));
    const el = await mount(product([variant("v1", 12)]));
    await typeInto(input(el), "20");
    await leave(input(el));
    expect(fetchShopProduct).toHaveBeenCalledTimes(1);
    expect(input(el).value).toBe("9");
    const line = el.querySelector('[data-testid="shop-stock-moved-line"]')?.textContent ?? "";
    expect(line).toContain("Someone changed the stock first.");
    expect(line).toContain("you saw 12, now 9");
    expect(line).toContain("You typed 20");
  });

  it("shows a refusal that is not a conflict as the error it is", async () => {
    vi.mocked(replaceShopVariants).mockRejectedValue(new ApiError(500, "shop.failed", "boom"));
    const el = await mount(product([variant("v1", 12)]));
    await typeInto(input(el), "3");
    await leave(input(el));
    expect(el.querySelector('[data-testid="shop-stock-error"]')).not.toBeNull();
  });
});

describe("ShopStockCell: several variants and none", () => {
  it("shows the total and puts a field per variant under a disclosure", async () => {
    const p = product([variant("v1", 12, { option1: "2021" }), variant("v2", 3, { option1: "2022", position: 1 })], { option_names: ["Vintage"] });
    const el = await mount(p);
    expect(el.querySelector('[data-testid="shop-stock-total"]')?.textContent).toBe("Total: 15");
    const details = el.querySelector('[data-testid="shop-stock-details"]') as HTMLDetailsElement;
    expect(details.open).toBe(false);
    expect(details.querySelector("summary")?.textContent).toBe("Stock");
    expect(el.querySelectorAll('[data-testid="shop-stock-input"]')).toHaveLength(2);
    expect(input(el, 1).getAttribute("aria-label")).toBe("Stock of Riesling, 2022");
  });

  it("changes ONE variant of several and sends the other back as it is", async () => {
    vi.mocked(replaceShopVariants).mockResolvedValue(product([variant("v1", 12, { option1: "2021" }), variant("v2", 8, { option1: "2022", position: 1 })], { option_names: ["Vintage"], updated_at: "2026-10-02T10:00:00Z" }));
    const p = product([variant("v1", 12, { option1: "2021" }), variant("v2", 3, { option1: "2022", position: 1 })], { option_names: ["Vintage"] });
    const el = await mount(p);
    await typeInto(input(el, 1), "8");
    await leave(input(el, 1));
    const body = vi.mocked(replaceShopVariants).mock.calls[0][2];
    expect(body.map((v) => [v.id, v.stock, v.expected_stock])).toEqual([
      ["v1", 12, 12],
      ["v2", 8, 3],
    ]);
  });

  it("has nothing to edit when no variant counts its stock", async () => {
    const el = await mount(product([variant("v1", 0, { track_stock: false })]));
    expect(el.querySelector("input")).toBeNull();
    expect(el.textContent).toBe("Not tracked");
  });
});
