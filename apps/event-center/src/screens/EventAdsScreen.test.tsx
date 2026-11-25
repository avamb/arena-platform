// @vitest-environment jsdom
/**
 * "Advertising" of an event: the sync states, the two columns, MER in one currency and "—" in two, the campaigns table,
 * the untagged share, the template with its copy button, and the refresh button that stays locked until not_before.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, ...rest }: { children: React.ReactNode; to: string }) => (
    <a href={to} {...(rest as object)}>
      {children}
    </a>
  ),
  useParams: () => ({ eventId: "ev1" }),
}));
vi.mock("@/lib/api/adsApi", () => ({ fetchEventMetaAds: vi.fn(), refreshOrgMetaAds: vi.fn() }));

import { fetchEventMetaAds, refreshOrgMetaAds, type MetaAdsEventReport } from "@/lib/api/adsApi";
import { I18nProvider } from "@/lib/i18n";
import { en } from "@/lib/i18n/en";
import { EventAdsBody } from "@/screens/EventAdsScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const TEMPLATE = "utm_source=facebook&utm_medium=paid_social&utm_campaign={{campaign.id}}&utm_term={{adset.id}}&utm_content={{ad.id}}";

function report(over: Partial<MetaAdsEventReport> = {}): MetaAdsEventReport {
  return {
    sync: {
      status: "ok",
      ad_account_id: "act_7",
      ad_account_name: "Ads EU",
      currency: "EUR",
      timezone: "Europe/Madrid",
      last_success_at: "2026-11-23T08:00:00Z",
      last_error: null,
      backfill_done: true,
    },
    period: { from: "2026-10-25", to: "2026-11-23" },
    meta: { spend_minor: 20000, currency: "EUR", impressions: 9000, clicks: 400, link_clicks: 300, purchases: 12, purchase_value_minor: 36000 },
    ours: { checkouts_started: 40, paid_orders: 10, revenue_net_minor: 50000, currency: "EUR" },
    mer: 2.5,
    untagged: { paid_orders: 20, untagged_orders: 5 },
    campaigns: [
      {
        campaign_id: "c1",
        campaign_name: "Spring",
        effective_status: "ACTIVE",
        spend_minor: 15000,
        currency: "EUR",
        impressions: 5000,
        clicks: 200,
        link_clicks: 150,
        meta_purchases: 7,
        meta_purchase_value_minor: 20000,
        paid_orders: 6,
        revenue_net_minor: 30000,
        revenue_currency: "EUR",
        shared: true,
        events: [],
      },
    ],
    url_parameters_template: TEMPLATE,
    shared_spend: true,
    ...over,
  };
}

async function mount(canConnect = true): Promise<HTMLElement> {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const r = createRoot(el);
  roots.push(r);
  await act(async () => {
    r.render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <I18nProvider locale="en">
          <EventAdsBody orgId="org1" eventId="ev1" canConnect={canConnect} />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(40);
  });
  for (let i = 0; i < 40 && el.querySelector('[data-testid="loading"]') !== null; i++) await act(async () => void (await sleep(25)));
  return el;
}

const id = (el: HTMLElement, testId: string) => el.querySelector(`[data-testid="${testId}"]`) as HTMLElement | null;
async function click(node: HTMLElement | null) {
  await act(async () => {
    node?.click();
    await sleep(40);
  });
}

beforeEach(() => {
  vi.mocked(fetchEventMetaAds).mockReset();
  vi.mocked(refreshOrgMetaAds).mockReset();
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

describe("sync states", () => {
  it("not connected: a link to the Meta settings for the owner, a plain ask for others", async () => {
    const r = report({ sync: { ...report().sync, status: "not_connected", ad_account_id: null, ad_account_name: null, currency: null, last_success_at: null } });
    vi.mocked(fetchEventMetaAds).mockResolvedValue(r);
    const owner = await mount(true);
    expect(id(owner, "ads-sync")?.dataset.status).toBe("not_connected");
    expect(id(owner, "ads-connect-link")?.getAttribute("href")).toBe("/o/$orgId/ads/meta");
    expect(id(owner, "ads-funnel")).toBeNull();
    expect(id(owner, "ads-template-value")?.textContent).toBe(TEMPLATE);
    const other = await mount(false);
    expect(id(other, "ads-connect-link")).toBeNull();
    expect(other.textContent).toContain(en["ads.sync.notConnectedAsk"]);
  });

  it("no ad account: says so and links to the settings", async () => {
    vi.mocked(fetchEventMetaAds).mockResolvedValue(report({ sync: { ...report().sync, status: "no_ad_account", ad_account_id: null, ad_account_name: null } }));
    const el = await mount();
    expect(id(el, "ads-sync")?.dataset.status).toBe("no_ad_account");
    expect(id(el, "ads-sync")?.textContent).toContain(en["ads.sync.noAccount"]);
    expect(id(el, "ads-connect-link")).not.toBeNull();
    expect(id(el, "ads-refresh")).toBeNull();
  });

  it("error: shows the code and keeps the numbers", async () => {
    vi.mocked(fetchEventMetaAds).mockResolvedValue(report({ sync: { ...report().sync, status: "error", last_error: "graph_rate_limited" } }));
    const el = await mount();
    expect(id(el, "ads-sync")?.dataset.status).toBe("error");
    expect(id(el, "ads-sync-code")?.textContent).toContain("graph_rate_limited");
    expect(id(el, "ads-funnel")).not.toBeNull();
  });
});

describe("the report", () => {
  it("same currency: two columns, MER, campaigns, untagged share, shared mark", async () => {
    vi.mocked(fetchEventMetaAds).mockResolvedValue(report());
    const el = await mount();
    expect(id(el, "ads-meta-spend")?.textContent).toBe("€200.00");
    expect(id(el, "ads-meta-clicks")?.textContent).toBe("300");
    expect(id(el, "ads-ours-checkout")?.textContent).toBe("40");
    expect(id(el, "ads-meta-paid")?.textContent).toBe("12");
    expect(id(el, "ads-ours-paid")?.textContent).toBe("10");
    expect(id(el, "ads-meta-revenue")?.textContent).toBe("€360.00");
    expect(id(el, "ads-ours-revenue")?.textContent).toBe("€500.00");
    expect(id(el, "ads-mer")?.textContent).toBe("2.50");
    expect(id(el, "ads-currency-note")).toBeNull();
    expect(id(el, "ads-why")?.textContent).toBe(en["ads.why"]);
    expect(id(el, "ads-shared-spend")).not.toBeNull();
    expect(id(el, "ads-campaign")?.textContent).toContain("Spring");
    expect(id(el, "ads-campaign-shared")?.textContent).toBe(en["ads.shared"]);
    expect(id(el, "ads-untagged-text")?.textContent).toContain("5 of 20");
    expect(id(el, "ads-untagged-text")?.textContent).toContain("25%");
    expect(id(el, "ads-period")?.textContent).toBe("From 2026-10-25 to 2026-11-23");
  });

  it("different currencies: both amounts, MER is a dash, with a note", async () => {
    vi.mocked(fetchEventMetaAds).mockResolvedValue(report({ ours: { checkouts_started: 40, paid_orders: 10, revenue_net_minor: 90000, currency: "ILS" }, mer: 1.1 }));
    const el = await mount();
    expect(id(el, "ads-meta-revenue")?.textContent).toBe("€360.00");
    expect(id(el, "ads-ours-revenue")?.textContent).toContain("900.00");
    expect(id(el, "ads-mer")?.textContent).toBe("—");
    expect(id(el, "ads-currency-note")?.textContent).toContain("EUR");
    expect(id(el, "ads-currency-note")?.textContent).toContain("ILS");
  });

  it("the preset asks again with a window of that length", async () => {
    vi.mocked(fetchEventMetaAds).mockResolvedValue(report());
    const el = await mount();
    await click(id(el, "ads-preset-7"));
    const last = vi.mocked(fetchEventMetaAds).mock.calls.at(-1);
    expect(last?.[2]?.from).toMatch(/^\d{4}-\d{2}-\d{2}$/);
    expect(last?.[2]?.to).toMatch(/^\d{4}-\d{2}-\d{2}$/);
  });

  it("the template has a copy button", async () => {
    vi.mocked(fetchEventMetaAds).mockResolvedValue(report());
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const el = await mount();
    await click(id(el, "ads-template-copy"));
    expect(writeText).toHaveBeenCalledWith(TEMPLATE);
    expect(id(el, "ads-template-copy")?.textContent).toBe(en["ads.template.copied"]);
  });
});

describe("refresh", () => {
  it("queued: shows the queued state", async () => {
    vi.mocked(fetchEventMetaAds).mockResolvedValue(report());
    vi.mocked(refreshOrgMetaAds).mockResolvedValue({ queued: true, not_before: new Date(Date.now() + 600_000).toISOString() });
    const el = await mount();
    await click(id(el, "ads-refresh"));
    expect(refreshOrgMetaAds).toHaveBeenCalledWith("org1");
    expect(id(el, "ads-refresh-queued")?.textContent).toBe(en["ads.refresh.queued"]);
    expect((id(el, "ads-refresh") as HTMLButtonElement).disabled).toBe(true);
  });

  it("throttled: not queued, the button stays locked until not_before", async () => {
    vi.mocked(fetchEventMetaAds).mockResolvedValue(report());
    vi.mocked(refreshOrgMetaAds).mockResolvedValue({ queued: false, not_before: new Date(Date.now() + 600_000).toISOString() });
    const el = await mount();
    expect((id(el, "ads-refresh") as HTMLButtonElement).disabled).toBe(false);
    await click(id(el, "ads-refresh"));
    expect(id(el, "ads-refresh-queued")).toBeNull();
    expect(id(el, "ads-refresh-throttled")).not.toBeNull();
    expect((id(el, "ads-refresh") as HTMLButtonElement).disabled).toBe(true);
  });
});
