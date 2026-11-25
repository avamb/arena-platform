import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { QuickPayError, StripeQuickPay } from "@/components/StripeQuickPay";
import { ApiError } from "@/lib/api/http";
import { I18nProvider, LOCALES, type Locale } from "@/lib/i18n";
import { hasPublishableKey, publishableKeyProblem, stripeQuickPayConfigs } from "@/lib/orgData";
import { PaymentsSection } from "@/screens/OrgDataScreen";

function render(node: React.ReactNode, locale: Locale = "ru"): string {
  return renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <I18nProvider locale={locale}>{node}</I18nProvider>
    </QueryClientProvider>,
  );
}

const cfg = (over: Record<string, unknown> = {}) => ({
  id: "c1",
  provider: "stripe",
  mode: "live" as const,
  is_active: true,
  verification_status: "ok",
  secret_fields_set: ["api_key"] as string[],
  ...over,
});

describe("publishableKeyProblem", () => {
  it("checks the prefix against the mode", () => {
    expect(publishableKeyProblem("live", "pk_live_abc")).toBeNull();
    expect(publishableKeyProblem("live", "  pk_live_abc  ")).toBeNull();
    expect(publishableKeyProblem("live", "pk_test_abc")).toBe("prefixLive");
    expect(publishableKeyProblem("test", "pk_test_abc")).toBeNull();
    expect(publishableKeyProblem("test", "pk_live_abc")).toBe("prefixTest");
    expect(publishableKeyProblem("test", "sk_test_abc")).toBe("prefixTest");
    expect(publishableKeyProblem("live", "   ")).toBe("empty");
  });
});

describe("quick pay helpers", () => {
  it("sees a stored key by its name", () => {
    expect(hasPublishableKey(["api_key", "publishable_key"])).toBe(true);
    expect(hasPublishableKey(["api_key"])).toBe(false);
    expect(hasPublishableKey(undefined)).toBe(false);
  });
  it("keeps only active Stripe configs", () => {
    const list = [cfg(), cfg({ id: "c2", provider: "flitt" }), cfg({ id: "c3", is_active: false })];
    expect(stripeQuickPayConfigs(list).map((c) => c.id)).toEqual(["c1"]);
    expect(stripeQuickPayConfigs(undefined)).toEqual([]);
  });
});

describe("StripeQuickPay", () => {
  it("offers the form without a key for a writer", () => {
    const html = render(<StripeQuickPay orgId="o" config={cfg()} canWrite />);
    expect(html).toContain('data-on="false"');
    expect(html).toContain('data-testid="quick-pay-form"');
    expect(html).toContain('data-testid="quick-pay-hint"');
    expect(html).not.toContain("quick-pay-on");
  });
  it("shows the status and a replace action when a key is stored", () => {
    const html = render(<StripeQuickPay orgId="o" config={cfg({ secret_fields_set: ["publishable_key"] })} canWrite />);
    expect(html).toContain('data-testid="quick-pay-on"');
    expect(html).toContain('data-testid="quick-pay-replace"');
    expect(html).not.toContain("quick-pay-form");
  });
  it("without payment_config.write shows only the status", () => {
    const on = render(<StripeQuickPay orgId="o" config={cfg({ secret_fields_set: ["publishable_key"] })} canWrite={false} />);
    expect(on).toContain("quick-pay-on");
    expect(on).not.toContain("quick-pay-replace");
    expect(on).not.toContain("quick-pay-form");
    const off = render(<StripeQuickPay orgId="o" config={cfg()} canWrite={false} />);
    expect(off).not.toContain("quick-pay-form");
    expect(off).not.toContain("quick-pay-on");
  });
  for (const locale of LOCALES) {
    it(`has no raw keys in ${locale}`, () => {
      expect(render(<StripeQuickPay orgId="o" config={cfg()} canWrite />, locale)).not.toContain("orgData.");
    });
  }
});

describe("QuickPayError", () => {
  it("maps the account mismatch code to its own text in every language", () => {
    const err = new ApiError(422, "payment_config.publishable_key_account_mismatch", "raw server text");
    const ru = render(<QuickPayError error={err} />, "ru");
    expect(ru).toContain("Этот ключ от другого аккаунта Stripe, чем секретный ключ.");
    for (const locale of LOCALES) {
      const html = render(<QuickPayError error={err} />, locale);
      expect(html).not.toContain("orgData.");
      expect(html).not.toContain("raw server text");
    }
    const other = new ApiError(422, "payment_config.other", "raw server text");
    expect(render(<QuickPayError error={other} />)).toContain("raw server text");
  });
});

describe("PaymentsSection", () => {
  it("shows the block for each active Stripe config only", () => {
    const html = render(<PaymentsSection orgId="o" canWrite configs={[cfg(), cfg({ id: "c2", provider: "flitt" })]} />);
    expect(html.match(/data-testid="quick-pay"/g)).toHaveLength(1);
  });
});
