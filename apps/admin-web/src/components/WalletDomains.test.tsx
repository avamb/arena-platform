import { renderToStaticMarkup } from "react-dom/server";
import { createElement } from "react";
import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import {
  WalletDomainsTable,
  walletDomainsQueryKey,
  type WalletDomain,
  type WalletDomainsEnvelope,
} from "./WalletDomains";
import { SiteDomainsForm, parseSiteDomains } from "./SiteDomainsEditor";

function dom(over: Partial<WalletDomain> = {}): WalletDomain {
  return {
    domain: "arenasoldout.com",
    source: "platform",
    enabled: true,
    apple_pay_status: "active",
    google_pay_status: "inactive",
    link_status: null,
    status_detail: null,
    last_error: null,
    checked_at: "2026-11-22T10:00:00Z",
    ...over,
  };
}

describe("WalletDomainsTable", () => {
  it("renders active, inactive and not checked", () => {
    const html = renderToStaticMarkup(createElement(WalletDomainsTable, { configId: "c1", domains: [dom()] }));
    expect(html).toContain('data-testid="wallet-domain-row-arenasoldout.com"');
    expect(html).toMatch(/wallet-apple-arenasoldout.com[^>]*>active</);
    expect(html).toMatch(/wallet-google-arenasoldout.com[^>]*>inactive</);
    expect(html).toMatch(/wallet-link-arenasoldout.com[^>]*>not checked</);
  });
  it("shows status detail and last error under the row", () => {
    const html = renderToStaticMarkup(
      createElement(WalletDomainsTable, {
        configId: "c1",
        domains: [dom({ status_detail: "Waiting for Stripe", last_error: "domain not reachable" })],
      }),
    );
    expect(html).toContain("Waiting for Stripe");
    expect(html).toContain("domain not reachable");
  });
  it("shows a note for an empty list", () => {
    const html = renderToStaticMarkup(createElement(WalletDomainsTable, { configId: "c1", domains: [] }));
    expect(html).toContain("wallet-domains-empty-c1");
    expect(html).toContain("after a successful check");
  });
  it("is refreshed by writing the verify answer into the query cache", () => {
    const qc = new QueryClient();
    const env: WalletDomainsEnvelope = { payment_method_domains: [dom({ domain: "shop.example.com", source: "site" })] };
    qc.setQueryData(walletDomainsQueryKey("o1", "c1"), env);
    const got = qc.getQueryData<WalletDomainsEnvelope>(walletDomainsQueryKey("o1", "c1"));
    expect(got?.payment_method_domains[0]?.domain).toBe("shop.example.com");
  });
});

describe("SiteDomainsForm", () => {
  it("parses one host per line", () => {
    expect(parseSiteDomains(" a.com \r\n\n b.com\n")).toEqual(["a.com", "b.com"]);
  });
  it("renders the server error", () => {
    const html = renderToStaticMarkup(
      createElement(SiteDomainsForm, {
        text: "bad host",
        onText: () => undefined,
        onSave: () => undefined,
        saving: false,
        error: "channel.invalid_site_domain",
        saved: false,
      }),
    );
    expect(html).toContain("channels-site-domains-error");
    expect(html).toContain("channel.invalid_site_domain");
  });
});
