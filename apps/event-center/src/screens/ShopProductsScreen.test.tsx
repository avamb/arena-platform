/** The products table: a thumbnail of the first picture (E-09), the count as a small badge, labels for the phone (E-10). */
import { describe, expect, it } from "vitest";
import { ShopProductsTable } from "@/screens/ShopProductsScreen";
import type { ShopProductView } from "@/lib/shop";
import { renderInRouter } from "@/test-utils/renderInRouter";

function product(over: Partial<ShopProductView> = {}): ShopProductView {
  return {
    id: "p1",
    org_id: "o",
    title: "Red dry",
    slug: "red-dry",
    status: "active",
    body_html: "",
    created_at: "2026-10-01T10:00:00Z",
    updated_at: "2026-10-01T10:00:00Z",
    variants: [{ id: "v1", title: "", price_minor: 20000, stock: 82, track_stock: true, held: 0, position: 0 }],
    images: [],
    ...over,
  } as unknown as ShopProductView;
}

const img = (n: number) => ({ media_id: `m${n}`, url: "", position: n, alt: "" });

describe("ShopProductsTable", () => {
  it("draws a thumbnail slot for a product with a picture and the media right", async () => {
    const html = await renderInRouter(<ShopProductsTable orgId="org1" currency="ILS" canViewMedia products={[product({ images: [img(0)] })]} />);
    expect(html).toContain('data-testid="shop-product-thumb"');
    // One picture: the thumbnail says it all, no count badge.
    expect(html).not.toContain("shop-product-picture-count");
  });

  it("keeps the count as a small badge when there are several pictures", async () => {
    const html = await renderInRouter(<ShopProductsTable orgId="org1" currency="ILS" canViewMedia products={[product({ images: [img(0), img(1), img(2)] })]} />);
    expect(html).toContain('data-testid="shop-product-thumb"');
    expect(html).toContain('data-testid="shop-product-picture-count"');
    expect(html).toContain('title="Pictures: 3"');
    expect(html).toMatch(/shop-product-picture-count"[^>]*>3</);
  });

  it("shows only the count without the media right, and 0 for a product with no picture", async () => {
    const without = await renderInRouter(<ShopProductsTable orgId="org1" currency="ILS" products={[product({ images: [img(0)] })]} />);
    expect(without).toContain('data-testid="shop-product-picture-count"');
    const none = await renderInRouter(<ShopProductsTable orgId="org1" currency="ILS" canViewMedia products={[product()]} />);
    expect(none).not.toContain("shop-product-thumb");
    expect(none).toMatch(/shop-product-picture-count"[^>]*>0</);
  });

  it("names every cell for the phone layout and keeps the status in a badge", async () => {
    const html = await renderInRouter(<ShopProductsTable orgId="org1" currency="ILS" products={[product()]} />, "ru");
    expect(html.match(/data-label="/g)).toHaveLength(6);
    expect(html).toContain("badge badge-ok");
    expect(html).toContain("В продаже");
  });
});
