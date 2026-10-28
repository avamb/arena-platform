/** The orders table of the shop rendered on the server: its columns, a row, and a phone-sized label for every cell. */
import { describe, expect, it } from "vitest";
import { ShopOrdersTable } from "@/components/ShopOrdersTable";
import type { ShopOrder } from "@/lib/api/types";
import { renderInRouter } from "@/test-utils/renderInRouter";

function order(over: Partial<ShopOrder> = {}): ShopOrder {
  return {
    id: "11111111-1111-4111-8111-111111111111",
    org_id: "o",
    number: 1042,
    status: "paid",
    source: "web",
    buyer: { name: "Anna Smith", email: "anna@example.com" },
    ship: { address1: "Main street 1", city: "Madrid", country: "ES" },
    delivery_kind: "shipping",
    delivery_note: "",
    currency: "EUR",
    subtotal: 4000,
    discount: 0,
    shipping_fee: 490,
    total: 4490,
    refunded_minor: 0,
    tracking_note: "",
    items: [],
    payment: null,
    created_at: "2026-10-05T10:00:00Z",
    updated_at: "2026-10-05T10:00:00Z",
    ...over,
  };
}

describe("ShopOrdersTable", () => {
  it("shows number, date, buyer, total, status and delivery for each order", async () => {
    const second = order({ id: "22222222-2222-4222-8222-222222222222", number: 1043, status: "manual_review", delivery_kind: "pickup", total: 1000 });
    const html = await renderInRouter(<ShopOrdersTable orgId="org1" orders={[order(), second]} />);
    expect(html.match(/data-testid="shop-order-row"/g)).toHaveLength(2);
    for (const label of ["Number", "Date", "Buyer", "Total", "Status", "Delivery"]) expect(html).toContain(`>${label}</th>`);
    expect(html).toContain("№1042");
    expect(html).toContain("Anna Smith");
    expect(html).toContain("anna@example.com");
    expect(html).toContain("Paid");
    expect(html).toContain("Needs a manual check");
    expect(html).toContain("Shipping");
    expect(html).toContain("Pickup");
    expect(html).toMatch(/44[.,]90/);
    // every cell names its column for the phone layout
    expect(html.match(/data-label="/g)?.length).toBe(12);
  });

  it("links each number to the card of the order", async () => {
    const html = await renderInRouter(<ShopOrdersTable orgId="org1" orders={[order()]} />);
    expect(html).toContain('href="/o/org1/shop/orders/11111111-1111-4111-8111-111111111111"');
  });

  it("marks the status with a word, never colour only", async () => {
    const html = await renderInRouter(<ShopOrdersTable orgId="org1" orders={[order({ status: "expired" })]} />, "ru");
    expect(html).toContain("badge-bad");
    expect(html).toContain("Бронь истекла");
  });
});
