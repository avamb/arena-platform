import { describe, expect, it } from "vitest";
import type { MeOrganizationMembership } from "@/lib/api/types";
import { isShopOrdersPath, shopModeFor, shopNavTarget, shopRedirect, showShopNav } from "@/lib/shopModule";

const ORG = "11111111-1111-4111-8111-111111111111";
const OTHER = "22222222-2222-4222-8222-222222222222";

function membership(over: Partial<MeOrganizationMembership>): MeOrganizationMembership {
  return {
    id: "m",
    org_id: ORG,
    org_name: "Teatr",
    org_display_number: 1,
    role: "organizer",
    status: "active",
    joined_at: "2026-01-01T00:00:00Z",
    archived: false,
    modules: [],
    shop_mode: null,
    ...over,
  } as MeOrganizationMembership;
}

describe("shopModeFor", () => {
  it("is full while the shop module is connected", () => {
    expect(shopModeFor([membership({ modules: ["shop"], shop_mode: "full" })], ORG)).toBe("full");
    // `modules` alone is enough, a missing shop_mode does not hide the shop.
    expect(shopModeFor([membership({ modules: ["shop"], shop_mode: null })], ORG)).toBe("full");
  });

  it("is orders_only after the module ended while orders remain", () => {
    expect(shopModeFor([membership({ modules: [], shop_mode: "orders_only" })], ORG)).toBe("orders_only");
  });

  it("is none without the module and without orders left", () => {
    expect(shopModeFor([membership({ modules: [], shop_mode: null })], ORG)).toBe("none");
    expect(shopModeFor([], ORG)).toBe("none");
    expect(shopModeFor(undefined, ORG)).toBe("none");
  });

  it("reads only the memberships of this organization", () => {
    const rows = [membership({ org_id: OTHER, modules: ["shop"], shop_mode: "full" }), membership({ org_id: ORG })];
    expect(shopModeFor(rows, ORG)).toBe("none");
    expect(shopModeFor(rows, OTHER)).toBe("full");
  });

  it("takes the best of several memberships in one organization", () => {
    const rows = [membership({ id: "a", modules: [], shop_mode: "orders_only" }), membership({ id: "b", role: "org_admin", modules: ["shop"], shop_mode: "full" })];
    expect(shopModeFor(rows, ORG)).toBe("full");
  });

  it("ignores a mode the web does not know", () => {
    expect(shopModeFor([membership({ shop_mode: "something_new" })], ORG)).toBe("none");
  });
});

describe("the menu item", () => {
  it("shows for the shop modes when the person can read the shop", () => {
    expect(showShopNav("full", true)).toBe(true);
    expect(showShopNav("orders_only", true)).toBe(true);
  });

  it("is hidden without the module and without shop.read", () => {
    expect(showShopNav("none", true)).toBe(false);
    expect(showShopNav("full", false)).toBe(false);
    expect(showShopNav("orders_only", false)).toBe(false);
  });

  it("leads to the orders when only the orders are left", () => {
    expect(shopNavTarget("orders_only")).toBe("orders");
    expect(shopNavTarget("full")).toBe("home");
  });
});

describe("the orders-only routing", () => {
  const base = `/o/${ORG}/shop`;

  it("tells the order screens from the rest", () => {
    expect(isShopOrdersPath(`${base}/orders`)).toBe(true);
    expect(isShopOrdersPath(`${base}/orders/`)).toBe(true);
    expect(isShopOrdersPath(`${base}/orders/abc`)).toBe(true);
    expect(isShopOrdersPath(`${base}/orders?view=to_ship`)).toBe(true);
    expect(isShopOrdersPath(`${base}/products`)).toBe(false);
    expect(isShopOrdersPath(`${base}/settings`)).toBe(false);
    expect(isShopOrdersPath(`${base}`)).toBe(false);
    expect(isShopOrdersPath(`${base}/ordersx`)).toBe(false);
    expect(isShopOrdersPath(`/o/${ORG}/events`)).toBe(false);
  });

  it("sends the catalog, the settings and the shop home to the orders", () => {
    for (const path of [`${base}`, `${base}/products`, `${base}/products/new`, `${base}/products/p1`, `${base}/settings`]) {
      expect(shopRedirect("orders_only", path), path).toBe("orders");
    }
  });

  it("keeps the order screens where they are", () => {
    expect(shopRedirect("orders_only", `${base}/orders`)).toBeNull();
    expect(shopRedirect("orders_only", `${base}/orders/abc`)).toBeNull();
  });

  it("never redirects the full shop", () => {
    expect(shopRedirect("full", `${base}/products`)).toBeNull();
    expect(shopRedirect("full", `${base}/settings`)).toBeNull();
  });
});
