/** Every error of the delivery-step route has plain words in every language (spec 61 section 7b). */
import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import { apiErrorStem, describeApiError } from "@/lib/errors";
import { LOCALES, translate, type Locale, type MessageKey } from "@/lib/i18n";

const err = (status: number, code: string) => new ApiError(status, code, "raw server text");
const tFor = (locale: Locale) => (key: MessageKey, params?: Record<string, string | number>) => translate(locale, key, params);

const CODES: Array<[number, string, string]> = [
  [409, "shop.fulfillment_order_closed", "shopFulfillmentClosed"],
  [409, "shop.fulfillment_order_unpaid", "shopFulfillmentUnpaid"],
  [409, "shop.fulfillment_order_in_review", "shopFulfillmentInReview"],
  [409, "shop.fulfillment_invalid_step", "shopFulfillmentInvalidStep"],
  [400, "shop.invalid_fulfillment_step", "shopFulfillmentBadStep"],
  [400, "shop.fulfillment_reason_required", "shopFulfillmentReason"],
];

describe("the errors of the delivery step route", () => {
  for (const [status, code, stem] of CODES) {
    it(`${code} -> ${stem}, with a title and a next step in every language`, () => {
      expect(apiErrorStem(err(status, code))).toBe(stem);
      for (const locale of LOCALES) {
        const text = describeApiError(err(status, code), tFor(locale));
        expect(text.title.length, `${locale} title`).toBeGreaterThan(5);
        expect(text.next.length, `${locale} next`).toBeGreaterThan(5);
        expect(text.title).not.toContain("raw server text");
      }
    });
  }

  it("says in plain words what to do next, without the code", () => {
    const closed = describeApiError(err(409, "shop.fulfillment_order_closed"), tFor("en"));
    expect(closed.title).toBe("This order was refunded, canceled or expired.");
    expect(closed.next).toContain("Do not send it");
    const invalid = describeApiError(err(409, "shop.fulfillment_invalid_step"), tFor("ru"));
    expect(invalid.next).toContain("Обновите заказ");
    expect(describeApiError(err(409, "shop.fulfillment_order_in_review"), tFor("es")).title).toContain("decisión");
  });

  it("keeps the older not-shippable answer", () => {
    expect(apiErrorStem(err(409, "shop.order_not_shippable"))).toBe("shopNotShippable");
  });
});
