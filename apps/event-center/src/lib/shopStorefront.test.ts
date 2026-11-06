import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import type { ShopSettings } from "@/lib/api/types";
import { CATALOGS, LOCALES } from "@/lib/i18n";
import { OPEN_BLOCKERS, blockerKey, blockerTarget, blockersOf, blockersOfError, knownBlockers, withBlockers } from "@/lib/shopStorefront";

describe("knownBlockers", () => {
  it("keeps the codes the web knows, in the API's order, once each", () => {
    expect(knownBlockers(["no_active_product", "settings_not_saved", "no_active_product"])).toEqual(["no_active_product", "settings_not_saved"]);
  });

  it("drops a code of a newer server and anything that is not a list", () => {
    expect(knownBlockers(["payment_not_configured", "something_new"])).toEqual(["payment_not_configured"]);
    expect(knownBlockers(undefined)).toEqual([]);
    expect(knownBlockers(null)).toEqual([]);
    expect(knownBlockers("no_active_product")).toEqual([]);
    expect(knownBlockers([1, null, {}])).toEqual([]);
  });

  it("reads the list of the settings, none for a server that does not send it", () => {
    expect(blockersOf({ open_blockers: ["no_active_product"] })).toEqual(["no_active_product"]);
    expect(blockersOf({})).toEqual([]);
  });
});

describe("blockersOfError", () => {
  it("reads details.blockers of a refused opening", () => {
    const e = new ApiError(422, "shop.cannot_open", "no", undefined, { blockers: ["settings_not_saved", "payment_not_configured"] });
    expect(blockersOfError(e)).toEqual(["settings_not_saved", "payment_not_configured"]);
  });

  it("is null for any other failure", () => {
    expect(blockersOfError(new ApiError(422, "shop.invalid_currency", "no", undefined, { blockers: ["settings_not_saved"] }))).toBeNull();
    expect(blockersOfError(new Error("x"))).toBeNull();
  });

  it("is an empty list when the refusal names nothing", () => {
    expect(blockersOfError(new ApiError(422, "shop.cannot_open", "no"))).toEqual([]);
  });
});

describe("texts and links", () => {
  it("has a plain text for every blocker in every language", () => {
    for (const locale of LOCALES) {
      for (const b of OPEN_BLOCKERS) expect(CATALOGS[locale][blockerKey(b)], `${locale} ${b}`).toBeTruthy();
    }
  });

  it("points the product blocker at the products, the payment one at the organization data", () => {
    expect(blockerTarget("no_active_product")).toBe("products");
    expect(blockerTarget("payment_not_configured")).toBe("orgData");
    expect(blockerTarget("settings_not_saved")).toBeNull();
  });
});

describe("withBlockers", () => {
  const settings = { module_active: true, can_open: true, open_blockers: [], enabled: false } as unknown as ShopSettings;

  it("puts the blockers of a refusal on the settings and says it cannot open", () => {
    const next = withBlockers(settings, ["no_active_product"]);
    expect(next.open_blockers).toEqual(["no_active_product"]);
    expect(next.can_open).toBe(false);
    expect(next.enabled).toBe(false);
  });
});
