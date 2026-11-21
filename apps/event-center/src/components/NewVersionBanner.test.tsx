// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@/lib/i18n";
import { NewVersionBanner } from "@/components/NewVersionBanner";
import { fetchDeployedBuild, isNewBuild, parseVersionFile } from "@/lib/appVersion";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

describe("appVersion", () => {
  it("reads only a well-formed build id", () => {
    expect(parseVersionFile({ build: "mh2k3x9a" })).toBe("mh2k3x9a");
    expect(parseVersionFile({ build: "" })).toBeNull();
    expect(parseVersionFile({ build: "<script>" })).toBeNull();
    expect(parseVersionFile({ build: 7 })).toBeNull();
    expect(parseVersionFile(null)).toBeNull();
    expect(parseVersionFile("mh2k3x9a")).toBeNull();
  });

  it("calls a build new only when the server names another one", () => {
    expect(isNewBuild("a1", "b2")).toBe(true);
    expect(isNewBuild("a1", "a1")).toBe(false);
    expect(isNewBuild("a1", null)).toBe(false);
    expect(isNewBuild("dev", "b2")).toBe(false);
  });

  it("asks /version.json past every cache and gives null on any failure", async () => {
    const ok = vi.fn(async () => new Response(JSON.stringify({ build: "b2" }), { status: 200 }));
    expect(await fetchDeployedBuild(ok as unknown as typeof fetch, () => 42)).toBe("b2");
    expect(ok).toHaveBeenCalledWith("/version.json?t=42", { cache: "no-store", credentials: "omit" });
    const missing = vi.fn(async () => new Response("nope", { status: 404 }));
    expect(await fetchDeployedBuild(missing as unknown as typeof fetch)).toBeNull();
    const offline = vi.fn(async () => {
      throw new TypeError("Failed to fetch");
    });
    expect(await fetchDeployedBuild(offline as unknown as typeof fetch)).toBeNull();
    const html = vi.fn(async () => new Response("<!doctype html>", { status: 200 }));
    expect(await fetchDeployedBuild(html as unknown as typeof fetch)).toBeNull();
  });
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(props: Parameters<typeof NewVersionBanner>[0]) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <I18nProvider>
        <NewVersionBanner {...props} />
      </I18nProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

describe("NewVersionBanner", () => {
  it("offers a reload when another build is deployed, and reloads only on the press", async () => {
    const reload = vi.fn();
    await mount({ enabled: true, running: "a1", fetchDeployed: async () => "b2", reload });
    const banner = host!.querySelector('[data-testid="new-version"]');
    expect(banner).not.toBeNull();
    expect(banner!.getAttribute("role")).toBe("status");
    expect(reload).not.toHaveBeenCalled();
    act(() => (host!.querySelector('[data-testid="new-version-reload"]') as HTMLButtonElement).click());
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("stays away while the same build runs, outside production, and after Later", async () => {
    await mount({ enabled: true, running: "a1", fetchDeployed: async () => "a1" });
    expect(host!.querySelector('[data-testid="new-version"]')).toBeNull();
    act(() => root!.unmount());
    host!.remove();

    const asked = vi.fn(async () => "b2");
    await mount({ enabled: false, running: "a1", fetchDeployed: asked });
    expect(asked).not.toHaveBeenCalled();
    expect(host!.querySelector('[data-testid="new-version"]')).toBeNull();
    act(() => root!.unmount());
    host!.remove();

    await mount({ enabled: true, running: "a1", fetchDeployed: async () => "b2" });
    act(() => (host!.querySelector('[data-testid="new-version-later"]') as HTMLButtonElement).click());
    expect(host!.querySelector('[data-testid="new-version"]')).toBeNull();
  });
});
