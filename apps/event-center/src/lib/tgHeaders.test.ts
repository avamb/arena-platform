/**
 * The nginx files of the Telegram mini app location (spec 44, RT-12), read as
 * text: the container smoke (scripts/check-headers-tg.sh) proves the running
 * server, this proves the files before an image is built.
 */
import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const root = path.resolve(__dirname, "../..");
const read = (name: string) => readFileSync(path.join(root, name), "utf8").replace(/\r\n/g, "\n");
const main = read("security-headers.conf.template");
const tg = read("security-headers-tg.conf.template");
const nginx = read("nginx.conf.template");

/** The add_header lines by header name, lowercased. */
function headers(text: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const m of text.matchAll(/^add_header\s+(\S+)\s+"(.*)"\s+always;$/gm)) out.set(m[1].toLowerCase(), m[2]);
  return out;
}

/** The body of `location <header> { ... }` (no nested braces in this file). */
function block(header: string): string {
  const start = nginx.indexOf(header);
  expect(start, `${header} is missing`).toBeGreaterThan(-1);
  return nginx.slice(nginx.indexOf("{", start) + 1, nginx.indexOf("}", start));
}

describe("security-headers-tg.conf.template", () => {
  const m = headers(main);
  const t = headers(tg);

  it("has no X-Frame-Options and lists the Telegram web clients in frame-ancestors", () => {
    expect(t.has("x-frame-options")).toBe(false);
    expect(m.get("x-frame-options")).toBe("DENY");
    const csp = t.get("content-security-policy") ?? "";
    expect(csp).toContain("frame-ancestors https://web.telegram.org");
    const ancestors = /frame-ancestors ([^;]+)/.exec(csp)?.[1].split(" ") ?? [];
    expect(ancestors.length).toBeGreaterThan(0);
    for (const origin of ancestors) expect(origin).toMatch(/^https:\/\/[a-z]+\.telegram\.org$/);
    expect(csp).not.toContain("frame-ancestors 'none'");
  });

  it("is the main policy in everything else", () => {
    const strip = (csp: string) => csp.replace(/frame-ancestors [^;]+;\s*/, "");
    expect(strip(t.get("content-security-policy") ?? "")).toBe(strip(m.get("content-security-policy") ?? ""));
    expect(t.get("content-security-policy")).toContain("script-src 'self';");
    expect(t.get("content-security-policy")).not.toContain("unsafe-");
    for (const name of ["x-content-type-options", "referrer-policy", "cross-origin-opener-policy", "permissions-policy"]) {
      expect(t.get(name), name).toBe(m.get(name));
    }
    expect([...t.keys()].sort()).toEqual([...m.keys()].filter((k) => k !== "x-frame-options").sort());
  });
});

describe("nginx.conf.template", () => {
  it("serves /tg/ with the tg headers and a named fallback", () => {
    const tgLocation = block("location ^~ /tg/");
    expect(tgLocation).toContain("include /etc/nginx/snippets/security-headers-tg.conf;");
    expect(tgLocation).toContain("try_files $uri @tg_index;");
    expect(tgLocation).not.toContain("security-headers.conf");
    expect(tgLocation).not.toContain("/index.html");
  });

  it("@tg_index rewrites to the index with break and carries the tg headers", () => {
    const named = block("location @tg_index");
    expect(named).toContain("include /etc/nginx/snippets/security-headers-tg.conf;");
    expect(named).toContain("rewrite ^ /index.html break;");
    expect(named).not.toContain("security-headers.conf");
  });

  it("keeps the main headers everywhere else", () => {
    expect(block("location / ")).toContain("include /etc/nginx/snippets/security-headers.conf;");
    expect(block("location = /index.html")).toContain("include /etc/nginx/snippets/security-headers.conf;");
    const withTg = [...nginx.matchAll(/security-headers-tg\.conf;/g)].length;
    expect(withTg).toBe(2);
  });
});
