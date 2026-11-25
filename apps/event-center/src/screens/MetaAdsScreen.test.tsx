// @vitest-environment jsdom
/**
 * "Advertising -> Meta": the five states, the choice of dataset and ad account (PATCH), the disconnect question
 * (DELETE only after the confirm button), the connect start (redirect to login_url), the finish
 * from the callback's fragment and the messages per reason.
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/components/ScreenHelp", () => ({ ScreenHelp: () => null }));
vi.mock("@/screens/OrgLayout", () => ({ useOrg: () => ({ orgId: "org1", access: { can: () => false } }) }));
vi.mock("@/lib/auth", () => ({ useMe: () => ({ data: undefined }) }));
vi.mock("@tanstack/react-router", () => ({ useSearch: () => ({ meta: "finish" }) }));
vi.mock("@/lib/api/metaApi", () => ({
  fetchMetaIntegration: vi.fn(),
  selectMetaAssets: vi.fn(),
  disconnectMeta: vi.fn(),
  startMetaConnect: vi.fn(),
  finishMetaConnect: vi.fn(),
  fetchMetaPage: vi.fn(),
}));

import {
  disconnectMeta,
  fetchMetaIntegration,
  finishMetaConnect,
  fetchMetaPage,
  selectMetaAssets,
  startMetaConnect,
  type MetaConnection,
  type MetaIntegration,
} from "@/lib/api/metaApi";
import { ApiError } from "@/lib/api/http";
import { I18nProvider } from "@/lib/i18n";
import { META_FINISH_KEY, REASON_KEY, META_REASONS, metaReturn, type MetaReturn } from "@/lib/metaConnect";
import { en } from "@/lib/i18n/en";
import { MetaAdsBody, MetaAdsScreen } from "@/screens/MetaAdsScreen";

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

async function mount(
  returned: MetaReturn = null,
  navigate?: (url: string) => void,
  meId?: string,
  stored?: { code: string; state: string; org: string },
): Promise<HTMLElement> {
  if (stored !== undefined) sessionStorage.setItem(META_FINISH_KEY, JSON.stringify(stored));
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
  for (const fn of [fetchMetaIntegration, selectMetaAssets, disconnectMeta, startMetaConnect, finishMetaConnect, fetchMetaPage]) vi.mocked(fn).mockReset();
});
afterEach(() => {
  sessionStorage.clear();
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
    // The refetch after a disconnect answers the disconnected state.
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn({ status: "revoked" })));
    await click(id(el, "meta-disconnect-confirm-submit"));
    expect(disconnectMeta).toHaveBeenCalledTimes(1);
    expect(disconnectMeta).toHaveBeenCalledWith("org1");
    expect(id(el, "meta-disconnect-confirm")).toBeNull();
    expect(id(el, "meta-status")?.getAttribute("data-status")).toBe("revoked");
    expect(id(el, "meta-revoked")?.textContent).toBe(en["meta.revokedText"]);
    expect(id(el, "meta-connect")?.textContent).toBe("Connect again");
    expect(id(el, "meta-disconnect")).toBeNull();
    expect(id(el, "meta-scopes")).toBeNull();
    expect(id(el, "meta-business")).toBeNull();
    expect(id(el, "meta-assets-dataset")).toBeNull();
    expect(id(el, "meta-page-card")).toBeNull();
    expect(el.textContent).not.toContain("ads_read, business_management");
  });
});

describe("connect", () => {
  const started = { app_id: "1", config_id: "c", graph_version: "v25.0", login_url: "https://fb.example/login" };

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
  it("finish: consumes the stored entry once, posts code and state once and shows the success", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, null));
    vi.mocked(finishMetaConnect).mockResolvedValue(integration(true, conn()));
    const el = await mount(metaReturn("finish", undefined), undefined, undefined, { code: "C1", state: "ST1", org: "org1" });
    expect(sessionStorage.getItem(META_FINISH_KEY)).toBeNull();
    expect(finishMetaConnect).toHaveBeenCalledTimes(1);
    expect(finishMetaConnect).toHaveBeenCalledWith("org1", { code: "C1", state: "ST1" });
    expect(id(el, "meta-returned-ok")?.textContent).toBe(en["meta.result.connected"]);
    expect(id(el, "meta-status")?.getAttribute("data-status")).toBe("active");
    // A second visit to ?meta=finish finds nothing: no second post.
    const el2 = await mount(metaReturn("finish", undefined));
    expect(finishMetaConnect).toHaveBeenCalledTimes(1);
    expect(id(el2, "meta-returned-error")?.getAttribute("data-reason")).toBe("no_code");
  });

  it("finish for another organization: the entry is dropped and nothing is posted", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, null));
    const el = await mount(metaReturn("finish", undefined), undefined, undefined, { code: "C9", state: "ST9", org: "other" });
    expect(finishMetaConnect).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(META_FINISH_KEY)).toBeNull();
    expect(id(el, "meta-returned-error")?.getAttribute("data-reason")).toBe("no_code");
  });

  it("while the finish is in flight the Connect button is hidden", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, null));
    vi.mocked(finishMetaConnect).mockReturnValue(new Promise(() => undefined));
    const el = await mount(metaReturn("finish", undefined), undefined, undefined, { code: "C3", state: "ST3", org: "org1" });
    expect(id(el, "meta-finishing")).not.toBeNull();
    expect(id(el, "meta-connect")).toBeNull();
  });

  it("finish refused: shows the message of the reason", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, null));
    vi.mocked(finishMetaConnect).mockRejectedValue(new ApiError(403, "meta.state_mismatch", "x"));
    const el = await mount(metaReturn("finish", undefined), undefined, undefined, { code: "C2", state: "ST2", org: "org1" });
    const box = id(el, "meta-returned-error");
    expect(box?.getAttribute("data-reason")).toBe("state_mismatch");
    expect(box?.textContent).toBe(en["meta.reason.state_mismatch"]);
  });

  it("finish without a stored entry posts nothing", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, null));
    const el = await mount(metaReturn("finish", undefined));
    expect(finishMetaConnect).not.toHaveBeenCalled();
    expect(id(el, "meta-returned-error")?.getAttribute("data-reason")).toBe("no_code");
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

describe("the Facebook Page card", () => {
  const withPage = (over: Partial<MetaConnection> = {}) =>
    conn({
      assets: [
        { kind: "dataset", meta_id: "D1", name: "Main pixel", currency: null, timezone: null, selected: true },
        { kind: "page", meta_id: "P1", name: "My page", currency: null, timezone: null, selected: true },
        { kind: "page", meta_id: "P2", name: "Other page", currency: null, timezone: null, selected: false },
      ],
      ...over,
    });
  const preview = {
    meta_id: "P1",
    name: "Live name",
    category: "Concert venue",
    link: "https://www.facebook.com/livepage",
    fan_count: 12345,
    picture_data_url: "data:image/png;base64,iVBORw0KGgo=",
  };

  it("shows name, category, likes, link and picture of the selected page", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, withPage()));
    vi.mocked(fetchMetaPage).mockResolvedValue(preview);
    const el = await mount();
    for (let i = 0; i < 20 && id(el, "meta-page-name") === null; i++) await act(async () => void (await sleep(25)));
    expect(fetchMetaPage).toHaveBeenCalledWith("org1");
    expect(id(el, "meta-page-name")?.textContent).toBe("Live name");
    expect(id(el, "meta-page-category")?.textContent).toBe("Concert venue");
    expect(id(el, "meta-page-fans")?.textContent).toBe("12,345");
    expect(id(el, "meta-page-link")?.getAttribute("href")).toBe("https://www.facebook.com/livepage");
    expect(id(el, "meta-page-picture")?.getAttribute("src")).toBe(preview.picture_data_url);
    expect(id(el, "meta-page-error")).toBeNull();
    expect((id(el, "meta-pick-page") as HTMLSelectElement).value).toBe("P1");
  });

  it("no card and no request without a selected page", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn()));
    const el = await mount();
    expect(id(el, "meta-page-card")).toBeNull();
    expect(fetchMetaPage).not.toHaveBeenCalled();
  });

  it("no card for a connection in error", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, withPage({ status: "error" })));
    const el = await mount();
    expect(id(el, "meta-page-card")).toBeNull();
    expect(fetchMetaPage).not.toHaveBeenCalled();
  });

  it("on an error shows one neutral line and nothing of Meta's answer", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, withPage()));
    vi.mocked(fetchMetaPage).mockRejectedValue(new ApiError(502, "meta.graph_error", "Meta did not show the page"));
    const el = await mount();
    for (let i = 0; i < 20 && id(el, "meta-page-error") === null; i++) await act(async () => void (await sleep(25)));
    expect(id(el, "meta-page-error")?.textContent).toBe(en["meta.page.unavailable"]);
    expect(id(el, "meta-page-preview")).toBeNull();
  });

  it("sends a PATCH with the chosen page", async () => {
    vi.mocked(fetchMetaIntegration).mockResolvedValue(integration(true, conn()));
    vi.mocked(selectMetaAssets).mockResolvedValue(integration(true, conn()));
    const el = await mount();
    const select = id(el, "meta-pick-page") as HTMLSelectElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(Object.getPrototypeOf(select), "value")?.set?.call(select, "P1");
      select.dispatchEvent(new Event("change", { bubbles: true }));
      await sleep(30);
    });
    expect(selectMetaAssets).toHaveBeenCalledWith("org1", { page_id: "P1" });
  });
});

describe("a non-owner", () => {
  it("renders the owner-only note and leaves no code in the address", async () => {
    window.history.replaceState(null, "", "/o/org1/ads/meta?meta=finish");
    const el = document.createElement("div");
    document.body.appendChild(el);
    const r = createRoot(el);
    roots.push(r);
    await act(async () => {
      r.render(
        <QueryClientProvider client={new QueryClient()}>
          <I18nProvider locale="en">
            <MetaAdsScreen />
          </I18nProvider>
        </QueryClientProvider>,
      );
      await sleep(30);
    });
    expect(id(el, "meta-owner-only")).not.toBeNull();
    expect(finishMetaConnect).not.toHaveBeenCalled();
    expect(window.location.hash).toBe("");
    expect(window.location.href).not.toContain("code=");
  });
});
