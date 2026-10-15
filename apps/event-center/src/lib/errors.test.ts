import { describe, expect, it } from "vitest";
import { ApiError } from "@/lib/api/http";
import { API_ERROR_ALL_STEMS, API_ERROR_STEMS, apiErrorStem, describeApiError } from "@/lib/errors";
import { CATALOGS, LOCALES, translate, type Locale, type MessageKey } from "@/lib/i18n";

const err = (status: number, code: string) => new ApiError(status, code, "raw server text");
const tFor = (locale: Locale) => (key: MessageKey, params?: Record<string, string | number>) => translate(locale, key, params);

describe("apiErrorStem", () => {
  it("knows every code of the table", () => {
    for (const [code, stem] of Object.entries(API_ERROR_STEMS)) {
      expect(apiErrorStem(err(409, code)), code).toBe(stem);
      expect(API_ERROR_ALL_STEMS, code).toContain(stem);
    }
  });

  it("maps the codes the screens meet", () => {
    expect(apiErrorStem(err(409, "order.seller_site_order"))).toBe("sellerSiteOrder");
    expect(apiErrorStem(err(422, "session.change_site_unsupported"))).toBe("sessionChangeSite");
    expect(apiErrorStem(err(422, "organization.contact_missing"))).toBe("contactMissing");
    expect(apiErrorStem(err(409, "refund.ticket_scanned"))).toBe("refundScanned");
    expect(apiErrorStem(err(409, "tier.quantity_below_used"))).toBe("tierBelowUsed");
    expect(apiErrorStem(err(422, "promo.minimum_not_reached"))).toBe("promoMinimum");
    expect(apiErrorStem(err(403, "auth.step_up_required"))).toBe("stepUpRequired");
    expect(apiErrorStem(err(403, "permissions.denied"))).toBe("forbidden");
  });

  it("a code beats the status", () => {
    // 403 is "forbidden" by status, but the step-up code asks for the password.
    expect(apiErrorStem(err(403, "auth.step_up_required"))).toBe("stepUpRequired");
    expect(apiErrorStem(err(403, "auth.step_up_wrong_password"))).toBe("stepUpWrong");
  });

  it("falls back by the not_found suffix, then by status", () => {
    expect(apiErrorStem(err(404, "order.not_found"))).toBe("notFound");
    expect(apiErrorStem(err(400, "promo.not_found"))).toBe("notFound");
    expect(apiErrorStem(err(0, ""))).toBe("network");
    expect(apiErrorStem(err(401, "auth.token_expired"))).toBe("unauthorized");
    expect(apiErrorStem(err(403, "something.new"))).toBe("forbidden");
    expect(apiErrorStem(err(404, "x"))).toBe("notFound");
    expect(apiErrorStem(err(409, "x"))).toBe("conflict");
    expect(apiErrorStem(err(422, "x"))).toBe("invalid");
    expect(apiErrorStem(err(400, "x"))).toBe("invalid");
    expect(apiErrorStem(err(429, "x"))).toBe("tooMany");
    expect(apiErrorStem(err(503, "x"))).toBe("server");
    expect(apiErrorStem(err(500, "x"))).toBe("server");
  });

  it("a timed-out call says to check the result first", () => {
    expect(apiErrorStem(err(524, ""))).toBe("slow");
    expect(apiErrorStem(err(504, ""))).toBe("slow");
  });

  it("anything that is not an API error is the general text", () => {
    expect(apiErrorStem(new Error("boom"))).toBe("generic");
    expect(apiErrorStem(null)).toBe("generic");
    expect(apiErrorStem("text")).toBe("generic");
    expect(apiErrorStem(err(302, "x"))).toBe("generic");
  });

  it("a code named like an Object property does not break the lookup", () => {
    expect(apiErrorStem(err(422, "constructor"))).toBe("invalid");
    expect(apiErrorStem(err(422, "__proto__"))).toBe("invalid");
  });
});

describe("describeApiError", () => {
  for (const locale of LOCALES) {
    it(`gives a title and a next step for every stem in ${locale}`, () => {
      for (const stem of API_ERROR_ALL_STEMS) {
        const title = CATALOGS[locale][`apiError.${stem}.title` as MessageKey];
        const next = CATALOGS[locale][`apiError.${stem}.next` as MessageKey];
        expect(title, `${locale} ${stem} title`).toBeTruthy();
        expect(next, `${locale} ${stem} next`).toBeTruthy();
      }
    });
  }

  it("translates through the person's language", () => {
    expect(describeApiError(err(409, "refund.ticket_scanned"), tFor("en")).title).toBe("This ticket was already scanned at the entrance.");
    expect(describeApiError(err(409, "refund.ticket_scanned"), tFor("ru")).title).toBe("Этот билет уже отсканировали на входе.");
    expect(describeApiError(err(409, "refund.ticket_scanned"), tFor("es")).title).toContain("escaneó");
  });

  it("never prints the raw server message", () => {
    const t = describeApiError(err(500, "x"), tFor("en"));
    expect(`${t.title} ${t.next}`).not.toContain("raw server text");
  });

  it("no text uses the bare words the standard forbids", () => {
    for (const locale of LOCALES) {
      for (const stem of API_ERROR_ALL_STEMS) {
        for (const part of ["title", "next"] as const) {
          const text = CATALOGS[locale][`apiError.${stem}.${part}` as MessageKey];
          expect(/\binvalid\b|\berror\b|неверн|ошибк|\binválid|\berror\b/i.test(text), `${locale} ${stem} ${part}: ${text}`).toBe(false);
        }
      }
    }
  });
});
