/**
 * The installable app (spec 37, WEB-PWA): the manifest is valid and its icons
 * exist, and the service worker never takes an API answer (personal data)
 * into its cache — it handles only the app shell of its own origin.
 */
import { describe, expect, it } from "vitest";
import { readFileSync, existsSync } from "node:fs";
import path from "node:path";
import vm from "node:vm";

const pub = path.resolve(__dirname, "../../public");

type Policy = (url: string, method: string, origin: string, mode: string) => string;

function loadPolicy(): Policy {
  const sandbox: Record<string, unknown> = {
    URL,
    self: { addEventListener: () => undefined, location: { origin: "https://events.example" } },
  };
  vm.createContext(sandbox);
  vm.runInContext(readFileSync(path.join(pub, "sw.js"), "utf8"), sandbox);
  return (sandbox.self as { arenaCachePolicy: Policy }).arenaCachePolicy;
}

describe("manifest", () => {
  const manifest = JSON.parse(readFileSync(path.join(pub, "manifest.webmanifest"), "utf8"));

  it("is installable", () => {
    expect(manifest.name).toBeTruthy();
    expect(manifest.short_name).toBeTruthy();
    expect(manifest.display).toBe("standalone");
    expect(manifest.start_url).toBe("/orgs");
    const sizes = manifest.icons.map((i: { sizes: string }) => i.sizes);
    expect(sizes).toContain("192x192");
    expect(sizes).toContain("512x512");
    expect(manifest.icons.some((i: { purpose: string }) => i.purpose === "maskable")).toBe(true);
  });

  it("names only icons that exist", () => {
    for (const icon of manifest.icons as { src: string }[]) {
      expect(existsSync(path.join(pub, icon.src))).toBe(true);
    }
  });
});

describe("service worker cache policy", () => {
  const policy = loadPolicy();
  const origin = "https://events.example";

  it("never handles arena-api or any other origin", () => {
    expect(policy("https://api.example/v1/organizations/x/orders/search", "GET", origin, "cors")).toBe("skip");
    expect(policy("https://api.example/v1/me", "GET", origin, "cors")).toBe("skip");
    expect(policy("https://api.example/assets/x.js", "GET", origin, "no-cors")).toBe("skip");
  });

  it("never handles a same-origin API path, a write or itself", () => {
    expect(policy(`${origin}/api/v1/me`, "GET", origin, "cors")).toBe("skip");
    expect(policy(`${origin}/api/v1/auth/login`, "POST", origin, "cors")).toBe("skip");
    expect(policy(`${origin}/assets/index-abc.js`, "POST", origin, "cors")).toBe("skip");
    expect(policy(`${origin}/health`, "GET", origin, "navigate")).toBe("skip");
    expect(policy(`${origin}/sw.js`, "GET", origin, "same-origin")).toBe("skip");
    expect(policy(`${origin}/manifest.webmanifest`, "GET", origin, "cors")).toBe("skip");
  });

  it("caches hashed assets and icons, serves app pages from the network", () => {
    expect(policy(`${origin}/assets/index-abc.js`, "GET", origin, "no-cors")).toBe("asset");
    expect(policy(`${origin}/icons/icon-192.png`, "GET", origin, "no-cors")).toBe("asset");
    expect(policy(`${origin}/o/1/orders?q=x`, "GET", origin, "navigate")).toBe("page");
    expect(policy(`${origin}/reset-password?token=t`, "GET", origin, "navigate")).toBe("page");
  });
});
