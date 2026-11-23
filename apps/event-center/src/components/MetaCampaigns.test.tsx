// @vitest-environment jsdom
/**
 * "Advertising -> Meta", section "Campaigns": the list with status badges, Pause only for ACTIVE and Resume only
 * for PAUSED, the POST only after the confirm button, the row updated from the answer, the notices, the reload and
 * 409 meta.reconnect_required refreshing the connection query.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/api/metaCampaignsApi", () => ({
  fetchMetaCampaigns: vi.fn(),
  setMetaCampaignStatus: vi.fn(),
}));

import { MetaCampaigns } from "@/components/MetaCampaigns";
import { ApiError } from "@/lib/api/http";
import { fetchMetaCampaigns, setMetaCampaignStatus, type MetaCampaign, type MetaCampaignList } from "@/lib/api/metaCampaignsApi";
import { I18nProvider } from "@/lib/i18n";
import { en } from "@/lib/i18n/en";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const camp = (id: string, name: string, status: string, effective = status): MetaCampaign => ({
  id,
  name,
  status,
  effective_status: effective,
});
const list = (...campaigns: MetaCampaign[]): MetaCampaignList => ({ ad_account_id: "act_7", campaigns });

async function mount(client = new QueryClient({ defaultOptions: { queries: { retry: false } } })): Promise<HTMLElement> {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const r = createRoot(el);
  roots.push(r);
  await act(async () => {
    r.render(
      <QueryClientProvider client={client}>
        <I18nProvider locale="en">
          <MetaCampaigns orgId="org1" adAccountId="act_7" />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(30);
  });
  for (let i = 0; i < 40 && id(el, "meta-campaigns-list") === null && id(el, "meta-campaigns-none") === null && id(el, "meta-campaigns-error") === null; i++) {
    await act(async () => void (await sleep(25)));
  }
  return el;
}

const id = (el: HTMLElement, testId: string) => el.querySelector(`[data-testid="${testId}"]`) as HTMLElement | null;
async function click(node: HTMLElement | null) {
  await act(async () => {
    node?.click();
    await sleep(30);
  });
}

beforeEach(() => {
  vi.mocked(fetchMetaCampaigns).mockReset();
  vi.mocked(setMetaCampaignStatus).mockReset();
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

describe("list", () => {
  it("shows name and status, Pause for ACTIVE, Resume for PAUSED, nothing for other statuses", async () => {
    vi.mocked(fetchMetaCampaigns).mockResolvedValue(
      list(camp("1", "Spring", "ACTIVE"), camp("2", "Summer", "PAUSED"), camp("3", "Old", "ARCHIVED"), camp("4", "", "ACTIVE", "WITH_ISSUES")),
    );
    const el = await mount();
    expect(fetchMetaCampaigns).toHaveBeenCalledWith("org1");
    expect(id(el, "meta-campaign-1")?.textContent).toContain("Spring");
    expect(id(el, "meta-campaign-1")?.textContent).toContain(en["meta.campaigns.status.active"]);
    expect(id(el, "meta-campaign-2")?.textContent).toContain(en["meta.campaigns.status.paused"]);
    expect(id(el, "meta-campaign-pause-1")?.textContent).toBe(en["meta.campaigns.pause"]);
    expect(id(el, "meta-campaign-resume-1")).toBeNull();
    expect(id(el, "meta-campaign-resume-2")?.textContent).toBe(en["meta.campaigns.resume"]);
    expect(id(el, "meta-campaign-pause-2")).toBeNull();
    expect(id(el, "meta-campaign-pause-3")).toBeNull();
    expect(id(el, "meta-campaign-resume-3")).toBeNull();
    expect(id(el, "meta-campaign-4")?.textContent).toContain(en["meta.campaigns.unnamed"]);
    expect(id(el, "meta-campaign-4")?.textContent).toContain("Delivery: WITH_ISSUES");
  });

  it("an empty account says so", async () => {
    vi.mocked(fetchMetaCampaigns).mockResolvedValue(list());
    const el = await mount();
    expect(id(el, "meta-campaigns-none")?.textContent).toBe(en["meta.campaigns.none"]);
  });

  it("Reload reads the list again", async () => {
    vi.mocked(fetchMetaCampaigns).mockResolvedValueOnce(list(camp("1", "Spring", "ACTIVE")));
    vi.mocked(fetchMetaCampaigns).mockResolvedValueOnce(list(camp("1", "Spring", "PAUSED")));
    const el = await mount();
    await click(id(el, "meta-campaigns-reload"));
    expect(fetchMetaCampaigns).toHaveBeenCalledTimes(2);
    expect(id(el, "meta-campaign-1")?.getAttribute("data-status")).toBe("PAUSED");
  });

  it("a list failure with reconnect_required refreshes the connection query", async () => {
    vi.mocked(fetchMetaCampaigns).mockRejectedValue(new ApiError(409, "meta.reconnect_required", "x"));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const spy = vi.spyOn(client, "invalidateQueries");
    const el = await mount(client);
    expect(id(el, "meta-campaigns-error")?.textContent).toBe(en["meta.campaigns.error.reconnect"]);
    expect(spy).toHaveBeenCalledWith({ queryKey: ["metaIntegration", "org1"] });
  });
});

describe("pause and resume", () => {
  it("Pause asks first, Cancel sends nothing", async () => {
    vi.mocked(fetchMetaCampaigns).mockResolvedValue(list(camp("1", "Spring", "ACTIVE")));
    const el = await mount();
    await click(id(el, "meta-campaign-pause-1"));
    const dialog = id(el, "meta-campaign-confirm");
    expect(dialog?.querySelector("h2")?.textContent).toBe("Pause campaign Spring?");
    expect(dialog?.textContent).toContain(en["meta.campaigns.pause.explain"]);
    await click(id(el, "meta-campaign-confirm-cancel"));
    expect(id(el, "meta-campaign-confirm")).toBeNull();
    expect(setMetaCampaignStatus).not.toHaveBeenCalled();
  });

  it("confirmed Pause posts PAUSED and updates the row from the answer", async () => {
    vi.mocked(fetchMetaCampaigns).mockResolvedValue(list(camp("1", "Spring", "ACTIVE")));
    vi.mocked(setMetaCampaignStatus).mockResolvedValue({ campaign: camp("1", "Spring", "PAUSED", "ACTIVE"), changed: true });
    const el = await mount();
    await click(id(el, "meta-campaign-pause-1"));
    await click(id(el, "meta-campaign-confirm-submit"));
    expect(setMetaCampaignStatus).toHaveBeenCalledWith("org1", "1", "PAUSED");
    expect(id(el, "meta-campaign-confirm")).toBeNull();
    expect(id(el, "meta-campaign-1")?.getAttribute("data-status")).toBe("PAUSED");
    expect(id(el, "meta-campaign-resume-1")).not.toBeNull();
    expect(id(el, "meta-campaigns-notice")?.textContent).toBe("Campaign Spring is paused.");
  });

  it("confirmed Resume posts ACTIVE, the title names the campaign", async () => {
    vi.mocked(fetchMetaCampaigns).mockResolvedValue(list(camp("2", "Summer", "PAUSED")));
    vi.mocked(setMetaCampaignStatus).mockResolvedValue({ campaign: camp("2", "Summer", "ACTIVE", "PAUSED"), changed: true });
    const el = await mount();
    await click(id(el, "meta-campaign-resume-2"));
    expect(id(el, "meta-campaign-confirm")?.querySelector("h2")?.textContent).toBe("Resume campaign Summer?");
    await click(id(el, "meta-campaign-confirm-submit"));
    expect(setMetaCampaignStatus).toHaveBeenCalledWith("org1", "2", "ACTIVE");
    expect(id(el, "meta-campaigns-notice")?.textContent).toBe("Campaign Summer is active again.");
  });

  it("an unchanged answer says nothing changed", async () => {
    vi.mocked(fetchMetaCampaigns).mockResolvedValue(list(camp("1", "Spring", "ACTIVE")));
    vi.mocked(setMetaCampaignStatus).mockResolvedValue({ campaign: camp("1", "Spring", "PAUSED"), changed: false });
    const el = await mount();
    await click(id(el, "meta-campaign-pause-1"));
    await click(id(el, "meta-campaign-confirm-submit"));
    expect(id(el, "meta-campaigns-notice")?.getAttribute("data-kind")).toBe("unchanged");
  });

  it("409 reconnect_required shows the text and refreshes the connection query", async () => {
    vi.mocked(fetchMetaCampaigns).mockResolvedValue(list(camp("1", "Spring", "ACTIVE")));
    vi.mocked(setMetaCampaignStatus).mockRejectedValue(new ApiError(409, "meta.reconnect_required", "x"));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const spy = vi.spyOn(client, "invalidateQueries");
    const el = await mount(client);
    await click(id(el, "meta-campaign-pause-1"));
    await click(id(el, "meta-campaign-confirm-submit"));
    expect(id(el, "meta-campaign-confirm")).toBeNull();
    expect(id(el, "meta-campaigns-failure")?.textContent).toBe(en["meta.campaigns.error.reconnect"]);
    expect(spy).toHaveBeenCalledWith({ queryKey: ["metaIntegration", "org1"] });
    expect(id(el, "meta-campaign-1")?.getAttribute("data-status")).toBe("ACTIVE");
  });

  it.each([
    [409, "meta.campaign_not_changeable", "meta.campaigns.error.notChangeable"],
    [404, "meta.campaign_not_found", "meta.campaigns.error.notFound"],
    [403, "meta.permission_denied", "meta.campaigns.error.permission"],
    [429, "meta.rate_limited", "meta.campaigns.error.rateLimited"],
    [502, "meta.graph_error", "meta.campaigns.error.graph"],
  ] as const)("%i %s has its own text", async (status, code, key) => {
    vi.mocked(fetchMetaCampaigns).mockResolvedValue(list(camp("1", "Spring", "ACTIVE")));
    vi.mocked(setMetaCampaignStatus).mockRejectedValue(new ApiError(status, code, "x"));
    const el = await mount();
    await click(id(el, "meta-campaign-pause-1"));
    await click(id(el, "meta-campaign-confirm-submit"));
    expect(id(el, "meta-campaigns-failure")?.textContent).toBe(en[key]);
    expect(id(el, "meta-campaigns-notice")).toBeNull();
  });
});
