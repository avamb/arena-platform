/**
 * The billing balance, pack and invoice components, rendered on the server
 * (no DOM here). What needs a click lives in src/lib/billingPacks.ts, which
 * has its own tests. These prove the first render says the right things in
 * all three languages.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { BillingFeeQuote } from "@/components/BillingFeeQuote";
import { BillingInvoiceCard } from "@/components/BillingInvoiceCard";
import { BillingPacks } from "@/components/BillingPacks";
import { PaymentReference } from "@/components/PaymentReference";
import type { BillingAccountInvoice, BillingPackOffer } from "@/lib/api/types";
import { I18nProvider, LOCALES, type Locale } from "@/lib/i18n";

function render(node: React.ReactNode, locale: Locale = "en"): string {
  const client = new QueryClient();
  return renderToStaticMarkup(
    <QueryClientProvider client={client}>
      <I18nProvider locale={locale}>{node}</I18nProvider>
    </QueryClientProvider>,
  );
}

const PACKS: BillingPackOffer[] = [
  {
    id: "p-1",
    account_id: "a-1",
    currency: "EUR",
    tier: 2,
    face_minor: 5200,
    unseated_rate_minor: 52,
    seated_surcharge_minor: 35,
    tickets_unseated: 100,
    card_fee_minor: 190,
    card_payment_available: true,
  },
  {
    id: "p-0",
    account_id: "a-1",
    currency: "EUR",
    tier: 1,
    face_minor: 1650,
    unseated_rate_minor: 55,
    seated_surcharge_minor: 35,
    tickets_unseated: 30,
    card_fee_minor: null,
    card_payment_available: false,
  },
];

function invoice(o: Partial<BillingAccountInvoice>): BillingAccountInvoice {
  return {
    id: "i-1",
    org_id: "o-1",
    account_id: "a-1",
    kind: "credit_pack",
    state: "issued",
    number: "INV-2026-0001",
    billing_period: "2026-10",
    payment_method: "bank_transfer",
    payment_reference: "ARN-ABCD-EFGH",
    currency: "EUR",
    subtotal_minor: 5200,
    card_fee_minor: 0,
    tax_minor: 0,
    total_minor: 5200,
    period_from: null,
    period_to: null,
    issued_at: "2026-10-20T10:00:00Z",
    due_at: "2026-11-03T10:00:00Z",
    paid_at: null,
    paid_amount_minor: null,
    paid_via: null,
    voided_at: null,
    payment_url: null,
    pack_id: "p-1",
    lines: [
      { id: "l-1", kind: "pack", rule_type: null, description: "Credit pack tier 2", quantity: 1, unit_amount_minor: 5200, total_amount_minor: 5200, currency: "EUR" },
    ],
    ...o,
  };
}

describe("BillingPacks", () => {
  it("lists the packs per currency, cheapest first, with what a card adds", () => {
    const html = render(<BillingPacks orgId="o-1" packs={PACKS} canPurchase />);
    expect(html).toContain('data-testid="billing-packs-EUR"');
    expect(html.indexOf("€16.50")).toBeLessThan(html.indexOf("€52.00"));
    expect(html).toContain("payment system fee of €1.90");
    expect(html).toContain("Card payment is not set up yet");
    expect(html).toContain('data-testid="billing-pack-buy"');
  });

  it("shows no order buttons to a person who may not buy", () => {
    const html = render(<BillingPacks orgId="o-1" packs={PACKS} canPurchase={false} />);
    expect(html).not.toContain('data-testid="billing-pack-buy"');
    expect(html).toContain('data-testid="billing-packs-owner-only"');
  });

  it("says plainly when there are no packs", () => {
    const html = render(<BillingPacks orgId="o-1" packs={[]} canPurchase />);
    expect(html).toContain('data-testid="billing-packs-none"');
    expect(html).not.toContain('data-testid="billing-packs-owner-only"');
  });

  it("opens no order panel before a pack is chosen", () => {
    expect(render(<BillingPacks orgId="o-1" packs={PACKS} canPurchase />)).not.toContain('data-testid="billing-order"');
  });

  for (const locale of LOCALES) {
    it(`renders in ${locale}`, () => {
      const html = render(<BillingPacks orgId="o-1" packs={PACKS} canPurchase />, locale);
      expect(html).toContain('data-testid="billing-pack"');
      expect(html).not.toContain("{");
    });
  }
});

describe("PaymentReference", () => {
  it("shows the reference as big selectable text with a copy button", () => {
    const html = render(<PaymentReference value="ARN-ABCD-EFGH" />);
    expect(html).toContain('data-testid="payment-reference"');
    expect(html).toContain("ARN-ABCD-EFGH");
    expect(html).toContain('data-testid="payment-reference-copy"');
  });
});

describe("BillingInvoiceCard", () => {
  it("an issued bank invoice shows the reference, the instruction and no card button", () => {
    const html = render(<BillingInvoiceCard orgId="o-1" invoice={invoice({})} canPay />);
    expect(html).toContain("Invoice INV-2026-0001");
    expect(html).toContain("Waiting for payment");
    expect(html).toContain("ARN-ABCD-EFGH");
    expect(html).toContain("Write the payment reference in the transfer");
    expect(html).toContain("Credits appear only after the payment is confirmed");
    expect(html).not.toContain('data-testid="billing-invoice-pay"');
  });

  it("an issued card invoice offers Pay by card, but only to who may pay", () => {
    const card = invoice({ payment_method: "stripe_card", payment_reference: null, card_fee_minor: 190, total_minor: 5390 });
    expect(render(<BillingInvoiceCard orgId="o-1" invoice={card} canPay />)).toContain('data-testid="billing-invoice-pay"');
    expect(render(<BillingInvoiceCard orgId="o-1" invoice={card} canPay={false} />)).not.toContain('data-testid="billing-invoice-pay"');
  });

  it("a paid invoice has no payment button and says paid", () => {
    const paid = invoice({ state: "paid", paid_at: "2026-10-21T08:00:00Z", payment_method: "stripe_card" });
    const html = render(<BillingInvoiceCard orgId="o-1" invoice={paid} canPay />);
    expect(html).toContain("Paid");
    expect(html).not.toContain('data-testid="billing-invoice-pay"');
    expect(html).not.toContain("Waiting for");
  });

  it("the open card lists the lines and the totals in the invoice's own currency", () => {
    const withFee = invoice({ payment_method: "stripe_card", card_fee_minor: 190, total_minor: 5390, lines: [
      { id: "l-1", kind: "pack", rule_type: null, description: "Credit pack tier 2", quantity: 1, unit_amount_minor: 5200, total_amount_minor: 5200, currency: "EUR" },
      { id: "l-2", kind: "card_fee", rule_type: null, description: "Payment system fee", quantity: 1, unit_amount_minor: 190, total_amount_minor: 190, currency: "EUR" },
    ] });
    const html = render(<BillingInvoiceCard orgId="o-1" invoice={withFee} canPay defaultOpen />);
    expect(html).toContain('data-testid="billing-invoice-lines"');
    expect(html).toContain("€52.00");
    expect(html).toContain("€1.90");
    expect(html).toContain("€53.90");
  });

  it("a draft without a number says it is not issued", () => {
    const html = render(<BillingInvoiceCard orgId="o-1" invoice={invoice({ state: "draft", number: null })} canPay />);
    expect(html).toContain("Invoice not issued yet");
  });
});

describe("BillingFeeQuote", () => {
  it("asks for a price and shows no result at first", () => {
    const html = render(<BillingFeeQuote orgId="o-1" currencies={["EUR"]} />);
    expect(html).toContain('data-testid="billing-fee-price"');
    expect(html).toContain('data-testid="billing-fee-seated"');
    expect(html).not.toContain('data-testid="billing-fee-result"');
    expect(html).not.toContain('data-testid="billing-fee-currency"');
  });
  it("offers a currency choice only when there are several accounts", () => {
    expect(render(<BillingFeeQuote orgId="o-1" currencies={["EUR", "USD"]} />)).toContain('data-testid="billing-fee-currency"');
  });
});
