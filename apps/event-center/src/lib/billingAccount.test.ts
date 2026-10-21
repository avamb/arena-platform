import { describe, expect, it } from "vitest";
import type { BillingAccountInvoice, BillingPackOffer } from "@/lib/api/types";
import {
  EMPTY_PAYER,
  awaitsBankTransfer,
  canPayByCard,
  invoiceKindKey,
  invoiceStateKey,
  isEmptyBalance,
  isInvoiceStateFilter,
  ledgerKey,
  ledgerSign,
  ledgerSigned,
  lineKindKey,
  methodKey,
  packsByCurrency,
  parseQuotePrice,
  payerFromOrg,
  payerRequest,
  quoteCurrencies,
  validatePayer,
} from "@/lib/billingAccount";
import { en } from "@/lib/i18n/en";

const pack = (currency: string, face: number, tier: number): BillingPackOffer => ({ currency, face_minor: face, tier }) as unknown as BillingPackOffer;
const inv = (o: Partial<BillingAccountInvoice>): BillingAccountInvoice => o as BillingAccountInvoice;

describe("packs", () => {
  it("groups by currency, never mixing them, cheapest first", () => {
    const g = packsByCurrency([pack("USD", 100, 1), pack("EUR", 5200, 2), pack("EUR", 1650, 1)]);
    expect(g.map((x) => x.currency)).toEqual(["EUR", "USD"]);
    expect(g[0].packs.map((p) => p.face_minor)).toEqual([1650, 5200]);
  });
  it("no packs, no groups", () => {
    expect(packsByCurrency([])).toEqual([]);
  });
  it("an empty balance has nothing and no lots", () => {
    expect(isEmptyBalance({ balance_minor: 0, lots: [] })).toBe(true);
    expect(isEmptyBalance({ balance_minor: 5, lots: [] })).toBe(false);
  });
  it("quote currencies are unique and sorted", () => {
    expect(quoteCurrencies([{ currency: "USD" }, { currency: "EUR" }, { currency: "EUR" }])).toEqual(["EUR", "USD"]);
  });
});

describe("invoice labels all exist in the catalog", () => {
  it("states, kinds, methods and lines", () => {
    for (const s of ["issued", "paid", "void", "draft", "weird"]) expect(en).toHaveProperty([invoiceStateKey(s)]);
    for (const k of ["credit_pack", "usage", "credit_note", "subscription", "manual", "weird"]) expect(en).toHaveProperty([invoiceKindKey(k)]);
    for (const m of ["bank_transfer", "stripe_card", "weird"]) expect(en).toHaveProperty([methodKey(m) as string]);
    for (const l of ["pack", "card_fee", "usage", "credit_note", "manual", "weird"]) expect(en).toHaveProperty([lineKindKey(l)]);
    expect(methodKey(null)).toBeNull();
    expect(methodKey("")).toBeNull();
  });
  it("state filters are the states the API takes", () => {
    expect(isInvoiceStateFilter("issued")).toBe(true);
    expect(isInvoiceStateFilter("draft")).toBe(false);
    expect(isInvoiceStateFilter("")).toBe(false);
  });
  it("every journal entry type has a label", () => {
    const types = ["purchase", "bonus_grant", "consume", "reversal", "reversal_dropped", "expire", "hold", "release", "adjustment_credit", "adjustment_debit", "weird"];
    for (const ty of types) expect(en).toHaveProperty([ledgerKey(ty)]);
  });
});

describe("what can be paid how", () => {
  it("card is offered for an issued card invoice only", () => {
    expect(canPayByCard({ state: "issued", payment_method: "stripe_card" })).toBe(true);
    expect(canPayByCard({ state: "paid", payment_method: "stripe_card" })).toBe(false);
    expect(canPayByCard({ state: "issued", payment_method: "bank_transfer" })).toBe(false);
    expect(canPayByCard({ state: "void", payment_method: "stripe_card" })).toBe(false);
  });
  it("a bank transfer waits only while issued and with a reference", () => {
    expect(awaitsBankTransfer({ state: "issued", payment_method: "bank_transfer", payment_reference: "ARN-AAAA-BBBB" })).toBe(true);
    expect(awaitsBankTransfer({ state: "issued", payment_method: "bank_transfer", payment_reference: null })).toBe(false);
    expect(awaitsBankTransfer({ state: "paid", payment_method: "bank_transfer", payment_reference: "ARN-AAAA-BBBB" })).toBe(false);
    expect(awaitsBankTransfer(inv({ state: "issued", payment_method: "stripe_card", payment_reference: "X" }))).toBe(false);
  });
});

describe("the credit journal sign", () => {
  it("follows the entry type, the amount is always positive", () => {
    for (const t of ["purchase", "bonus_grant", "reversal", "release", "adjustment_credit"]) expect(ledgerSign(t)).toBe(1);
    for (const t of ["consume", "expire", "hold", "adjustment_debit"]) expect(ledgerSign(t)).toBe(-1);
    expect(ledgerSign("reversal_dropped")).toBe(0);
    expect(ledgerSign("something new")).toBe(0);
    expect(ledgerSigned({ entry_type: "consume", amount_minor: 59 })).toBe(-59);
    expect(ledgerSigned({ entry_type: "purchase", amount_minor: 1650 })).toBe(1650);
    expect(ledgerSigned({ entry_type: "reversal_dropped", amount_minor: 59 })).toBe(0);
  });
});

describe("the commission price input", () => {
  it("reads major units as minor units of the currency", () => {
    expect(parseQuotePrice("20", "EUR")).toEqual({ ok: true, priceMinor: 2000 });
    expect(parseQuotePrice("20.5", "EUR")).toEqual({ ok: true, priceMinor: 2050 });
    expect(parseQuotePrice("20,50", "EUR")).toEqual({ ok: true, priceMinor: 2050 });
    expect(parseQuotePrice(" 0 ", "EUR")).toEqual({ ok: true, priceMinor: 0 });
    expect(parseQuotePrice("1 200.05", "EUR")).toEqual({ ok: true, priceMinor: 120005 });
  });
  it("uses the currency's own fraction digits", () => {
    expect(parseQuotePrice("500", "JPY")).toEqual({ ok: true, priceMinor: 500 });
    expect(parseQuotePrice("500.5", "JPY")).toEqual({ ok: false, problem: "invalid" });
  });
  it("refuses instead of rounding", () => {
    expect(parseQuotePrice("20.999", "EUR")).toEqual({ ok: false, problem: "invalid" });
    expect(parseQuotePrice("abc", "EUR")).toEqual({ ok: false, problem: "invalid" });
    expect(parseQuotePrice("-5", "EUR")).toEqual({ ok: false, problem: "invalid" });
    expect(parseQuotePrice("", "EUR")).toEqual({ ok: false, problem: "empty" });
    expect(parseQuotePrice("   ", "EUR")).toEqual({ ok: false, problem: "empty" });
  });
  it("stops at the API's limit", () => {
    expect(parseQuotePrice("10000000", "EUR")).toEqual({ ok: true, priceMinor: 1_000_000_000 });
    expect(parseQuotePrice("10000001", "EUR")).toEqual({ ok: false, problem: "tooBig" });
  });
});

describe("the payer form", () => {
  it("needs the legal name and nothing else", () => {
    expect(validatePayer(EMPTY_PAYER)).toEqual([{ field: "legal_name", code: "required" }]);
    expect(validatePayer({ ...EMPTY_PAYER, legal_name: "Acme S.L." })).toEqual([]);
  });
  it("checks the API's limits", () => {
    const f = { ...EMPTY_PAYER, legal_name: "A".repeat(201), address: "B".repeat(501), tax_id: "C".repeat(51), country: "ESP", email: "nope" };
    expect(validatePayer(f).map((p) => `${p.field}:${p.code}`).sort()).toEqual([
      "address:tooLong",
      "country:country",
      "email:email",
      "legal_name:tooLong",
      "tax_id:tooLong",
    ]);
  });
  it("sends trimmed text and the country in capitals", () => {
    expect(payerRequest({ legal_name: " Acme ", address: " Calle 1 ", country: "es", tax_id: " B123 ", email: " a@b.co " })).toEqual({
      legal_name: "Acme",
      address: "Calle 1",
      country: "ES",
      tax_id: "B123",
      email: "a@b.co",
    });
  });
  it("starts from the organization's legal data", () => {
    expect(
      payerFromOrg({
        legal_name: "Acme S.L.",
        tax_id: "B123",
        contact_email: "hi@acme.test",
        legal_address_line1: "Calle 1",
        legal_address_postal_code: "28001",
        legal_address_city: "Madrid",
        legal_address_country: "es",
      }),
    ).toEqual({ legal_name: "Acme S.L.", address: "Calle 1, 28001 Madrid", country: "ES", tax_id: "B123", email: "hi@acme.test" });
    expect(payerFromOrg({})).toEqual(EMPTY_PAYER);
  });
});
