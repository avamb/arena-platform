// @vitest-environment jsdom
/**
 * "Advertising -> Meta": the five states, the choice of dataset and ad account (PATCH), the disconnect question
 * (DELETE only after the confirm button), the connect start with a mocked Facebook SDK and the messages per reason.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/components/ScreenHelp", () => ({ ScreenHelp: () => null }));
vi.mock("@/lib/api/metaApi", () => ({
  fetchMetaIntegration: vi.fn(),
  selectMetaAssets: vi.fn(),
  disconnectMeta: vi.fn(),
  startMetaConnect: vi.fn(),
}));

import { disconnectMeta, fetchMetaIntegration, selectMetaAssets, startMetaConnect, type MetaConnection, type MetaIntegration } from "@/lib/api/metaApi";
import { I18nProvider } from "@/lib/i18n";
import { REASON_KEY, META_REASONS, metaReturn, type MetaReturn } from "@/lib/metaConnect";
import { en } from "@/lib/i18n/en";
import { MetaAdsBody } from "@/screens/MetaAdsScreen";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const roots: Root[] = [];
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function conn(over: Partial<MetaConnection> = {}): MetaConnection {
  return {
    status: "active",
    meta_business_id: "BIZ1",
    config_id: "cfg",
    scopes: ["ads_read", "business_management"],
    last_error: null,
    connected_by: "0b6f0e0e-0000-4000-8000-000000000001",
    connected_at: "2026-11-20T10:00:00Z",
    checked_at: null,
    token_expires_at: null,
    selected_dataset_id: "D1",
    selected_ad_account_id: null,
    assets: [
      { kind: "dataset", meta_id: "D1", name: "Main pixel", currency: null, timezone: null, selected: true },
      { kind: "dataset", meta_id: "D2", name: "Second pixel", currency: null, timezone: null, selected: false },
      { kind: "ad_account", meta_id: "act_7", name: "Ads EU", currency: "EUR", timezone: "Europe/Madrid", selected: false },
      { kind: "page", meta_id: "P1", name: "My page", currency: null, timezone: null, selected: false },
    ],
    ...over,
  };
}
const integration = (enabled: boolean, connection: MetaConnection | null): MetaIntegration => ({ enabled, connection });

async function mount(returned: MetaReturn = null, navigate?: (url: string) => void, meId?: string): Promise<HTMLElement> {
  const el = document.createElement("div");
  document.body.appendChild(el);
  const r = createRoot(el);
  roots.push(r);
  await act(async () => {
    r.render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <I18nProvider locale="en">
          <MetaAdsBody orgId="org1" returned={returned} navigate={navigate} meId={meId} />
        </I18nProvider>
      </QueryClientProvider>,
    );
    await sleep(30);
  });
  for (let i = 0; i < 20 && id(el, "loading") !== null; i++) await act(async () => void (await sleep(25)));
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
  for (const fn of [fetchMetaIntegration, selectMetaAssets, disconnectMeta, startMetaConnect]) vi.mocked(fn).mockReset();
});
afterEach(() => {
  while (roots.length > 0) act(() => (roots.pop() as Root).unmount());
  document.body.innerHTML = "";
});

describe("states", () => {
  it("feature off: a short text and no connect button", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(false, null));
    const el = await mount();
    expect(id(el, "meta-off")?.textContent).toBe(en["meta.off"]);
    expect(id(el, "meta-connect")).toBeNull();
  });

  it("not connected: the Connect button", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, null));
    const el = await mount();
    expect(id(el, "meta-not-connected")).not.toBeNull();
    expect(id(el, "meta-connect")?.textContent).toBe("Connect Meta");
  });

  it("connected: business, who and when, scopes, the three lists and both selects", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn()));
    const el = await mount();
    expect(id(el, "meta-status")?.getAttribute("data-status")).toBe("active");
    expect(id(el, "meta-business")?.textContent).toBe("BIZ1");
    expect(id(el, "meta-connected-by")).toBeNull();
    expect(id(el, "meta-connected-at")?.textContent).not.toBe("");
    expect(id(el, "meta-scopes")?.textContent).toBe("ads_read, business_management");
    expect(id(el, "meta-assets-dataset")?.textContent).toContain("Second pixel");
    expect(id(el, "meta-assets-ad_account")?.textContent).toContain("Ads EU");
    expect(id(el, "meta-assets-page")?.textContent).toContain("My page");
    expect((id(el, "meta-pick-dataset") as HTMLSelectElement).value).toBe("D1");
    expect((id(el, "meta-pick-ad-account") as HTMLSelectElement).value).toBe("");
    expect(id(el, "meta-connect")).toBeNull();
  });

  it("error: shows last_error and Connect again", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn({ status: "error", last_error: "token_invalid" })));
    const el = await mount();
    expect(id(el, "meta-last-error")?.textContent).toContain("token_invalid");
    expect(id(el, "meta-connect")?.textContent).toBe("Connect again");
    expect(id(el, "meta-pickers")).toBeNull();
  });

  it("revoked: says so and offers Connect again", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn({ status: "revoked" })));
    const el = await mount();
    expect(id(el, "meta-revoked")).not.toBeNull();
    expect(id(el, "meta-connect")?.textContent).toBe("Connect again");
  });
});

describe("who connected", () => {
  it("says «you» for the signed-in user", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn()));
    const el = await mount(null, undefined, "0b6f0e0e-0000-4000-8000-000000000001");
    expect(id(el, "meta-connected-by")?.textContent).toBe(en["meta.connectedByYou"]);
  });
});

describe("choice of dataset and ad account", () => {
  it("sends a PATCH with the chosen dataset and shows the stored answer", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn()));
    vi.mocked(selectMetaAssets).mockResolvedValue(integration(true, conn({ selected_dataset_id: "D2" })));
    const el = await mount();
    const select = id(el, "meta-pick-dataset") as HTMLSelectElement;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(select), "value")?.set;
      setter?.call(select, "D2");
      select.dispatchEvent(new Event("change", { bubbles: true }));
      await sleep(30);
    });
    expect(selectMetaAssets).toHaveBeenCalledWith("org1", { dataset_id: "D2" });
    expect((id(el, "meta-pick-dataset") as HTMLSelectElement).value).toBe("D2");
    expect(id(el, "meta-saved")).not.toBeNull();
  });

  it("sends the ad account alone when only it changes", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn()));
    vi.mocked(selectMetaAssets).mockResolvedValue(integration(true, conn({ selected_ad_account_id: "act_7" })));
    const el = await mount();
    const select = id(el, "meta-pick-ad-account") as HTMLSelectElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(Object.getPrototypeOf(select), "value")?.set?.call(select, "act_7");
      select.dispatchEvent(new Event("change", { bubbles: true }));
      await sleep(30);
    });
    expect(selectMetaAssets).toHaveBeenCalledWith("org1", { ad_account_id: "act_7" });
  });
});

describe("disconnect", () => {
  it("asks first: nothing is deleted until the confirm button, and Cancel deletes nothing", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn()));
    vi.mocked(disconnectMeta).mockResolvedValue(integration(true, conn({ status: "revoked" })));
    const el = await mount();
    await click(id(el, "meta-disconnect"));
    expect(id(el, "meta-disconnect-confirm")).not.toBeNull();
    expect(disconnectMeta).not.toHaveBeenCalled();
    await click(id(el, "meta-disconnect-confirm-cancel"));
    expect(id(el, "meta-disconnect-confirm")).toBeNull();
    expect(disconnectMeta).not.toHaveBeenCalled();
    await click(id(el, "meta-disconnect"));
    await click(id(el, "meta-disconnect-confirm-submit"));
    expect(disconnectMeta).toHaveBeenCalledTimes(1);
    expect(disconnectMeta).toHaveBeenCalledWith("org1");
    expect(id(el, "meta-disconnect-confirm")).toBeNull();
    expect(id(el, "meta-status")?.getAttribute("data-status")).toBe("revoked");
  });
});

describe("connect", () => {
  const started = { app_id: "1", config_id: "c", graph_version: "v25.0", state: "S", login_url: "https://fb.example/login" };

  it("starts the connect and sends the browser to login_url", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, null));
    vi.mocked(startMetaConnect).mockResolvedValue(started);
    const navigate = vi.fn();
    const el = await mount(null, navigate);
    await click(id(el, "meta-connect"));
    expect(startMetaConnect).toHaveBeenCalledWith("org1");
    expect(navigate).toHaveBeenCalledWith("https://fb.example/login");
  });

  it("with an empty login_url shows the error and goes nowhere", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, null));
    vi.mocked(startMetaConnect).mockResolvedValue({ ...started, login_url: "" });
    const navigate = vi.fn();
    const el = await mount(null, navigate);
    await click(id(el, "meta-connect"));
    expect(navigate).not.toHaveBeenCalled();
    expect(id(el, "meta-not-configured")?.textContent).toBe(en["meta.notConfigured"]);
  });
});

describe("the return from the callback", () => {
  it("shows the success", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn()));
    const el = await mount(metaReturn("connected", undefined));
    expect(id(el, "meta-returned-ok")?.textContent).toBe(en["meta.result.connected"]);
  });

  it("shows a message of its own for every reason", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, null));
    for (const reason of [...META_REASONS, "other"]) {
      const el = await mount(metaReturn("error", reason));
      const box = id(el, "meta-returned-error");
      expect(box?.textContent, reason).toBe(en[REASON_KEY[reason === "other" ? "unknown" : (reason as (typeof META_REASONS)[number])]]);
    }
  });
});
