/**
 * PR-07 — admin-web production artifact smoke tests (vitest layer).
 *
 * In YOLO mode the browser/container harness is not available, so the
 * production-readiness checks enforced here are logic/file-level:
 *
 *   1. SOURCE-MAP GUARD: vite.config.ts disables source maps by default;
 *      they require an explicit VITE_ENABLE_SOURCEMAPS=true opt-in.
 *
 *   2. CODE-SPLIT CONFIG: vite.config.ts declares manualChunks splitting
 *      vendors from app code so the initial payload is minimal.
 *
 *   3. PRODUCTION-PLACEHOLDER FILTER: hideProductionPlaceholders() removes
 *      Reports/Content/POS from the sidebar in non-development builds.
 *      In development mode all entries are preserved.
 *
 *   4. PLACEHOLDER NAV ENTRY COVERAGE: the three productionPlaceholder
 *      entries (reports, notifications_content, pos) are pinned so a
 *      future rename cannot silently drop the flag.
 *
 *   5. NGINX CONFIG FILE: nginx.conf exists and contains the expected
 *      directives (port 8080, SPA fallback, /health, immutable cache).
 *
 *   6. DOCKERFILE EXISTS: apps/admin-web/Dockerfile is present and
 *      references nginx:1.27-alpine runtime and non-root USER directive.
 *
 * The browser smoke ("login + /v1/me against built artifact") is not
 * automated in YOLO mode because it requires a running container and
 * a live backend. That step is documented in deploy/DOKPLOY.md §Admin
 * Service and must be executed manually during staging deploys.
 */
import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

import {
  NAV_ENTRIES,
  hideProductionPlaceholders,
  type NavEntry,
} from "@/lib/auth/navConfig";

const __dirname = dirname(fileURLToPath(import.meta.url));
// __dirname = apps/admin-web/src/smoke
// ../.. = apps/admin-web
const ADMIN_WEB_ROOT = join(__dirname, "../..");

// ---------------------------------------------------------------------------
// 1. Source-map guard
// ---------------------------------------------------------------------------

describe("PR-07 source-map guard", () => {
  it("vite.config.ts sets sourcemap to process.env.VITE_ENABLE_SOURCEMAPS === 'true' (not always true)", () => {
    const viteCfg = readFileSync(join(ADMIN_WEB_ROOT, "vite.config.ts"), "utf8");
    // Must NOT have unconditional `sourcemap: true`
    expect(viteCfg).not.toMatch(/sourcemap:\s*true/);
    // Must have the env-guarded expression
    expect(viteCfg).toMatch(/VITE_ENABLE_SOURCEMAPS/);
    expect(viteCfg).toMatch(/sourcemap:/);
  });
});

// ---------------------------------------------------------------------------
// 2. Code-split config
// ---------------------------------------------------------------------------

describe("PR-07 code-split config", () => {
  it("vite.config.ts declares manualChunks for vendor splitting", () => {
    const viteCfg = readFileSync(join(ADMIN_WEB_ROOT, "vite.config.ts"), "utf8");
    expect(viteCfg).toMatch(/manualChunks/);
    expect(viteCfg).toMatch(/vendor-react/);
    expect(viteCfg).toMatch(/vendor-router/);
    expect(viteCfg).toMatch(/vendor-query/);
  });

  it("vite.config.ts has rollupOptions output section", () => {
    const viteCfg = readFileSync(join(ADMIN_WEB_ROOT, "vite.config.ts"), "utf8");
    expect(viteCfg).toMatch(/rollupOptions/);
  });
});

// ---------------------------------------------------------------------------
// 3. Production placeholder filter
// ---------------------------------------------------------------------------

describe("PR-07 hideProductionPlaceholders", () => {
  const PLACEHOLDER_IDS = ["reports", "notifications_content", "pos"];

  it("in development mode: returns all entries unchanged", () => {
    const result = hideProductionPlaceholders(NAV_ENTRIES, true);
    expect(result).toHaveLength(NAV_ENTRIES.length);
    for (const id of PLACEHOLDER_IDS) {
      expect(result.some((e: NavEntry) => e.id === id)).toBe(true);
    }
  });

  it("in production mode: removes reports, notifications_content, and pos entries", () => {
    const result = hideProductionPlaceholders(NAV_ENTRIES, false);
    for (const id of PLACEHOLDER_IDS) {
      expect(
        result.some((e: NavEntry) => e.id === id),
        `${id} must be hidden in production`,
      ).toBe(false);
    }
  });

  it("in production mode: retains all non-placeholder entries", () => {
    const result = hideProductionPlaceholders(NAV_ENTRIES, false);
    const nonPlaceholders = NAV_ENTRIES.filter(
      (e) => !e.productionPlaceholder,
    );
    expect(result).toHaveLength(nonPlaceholders.length);
    for (const entry of nonPlaceholders) {
      expect(
        result.some((e: NavEntry) => e.id === entry.id),
        `non-placeholder entry ${entry.id} must be retained`,
      ).toBe(true);
    }
  });

  it("in production mode: sidebar retains workspace, networks, users, organizations, events/venues/orders/tickets/refunds, channels, payments, audit, observability, geo, webhooks", () => {
    const result = hideProductionPlaceholders(NAV_ENTRIES, false);
    const ids = result.map((e: NavEntry) => e.id);
    const mustKeep = [
      "workspace",
      "networks",
      "users",
      "organizations",
      "events_sessions",
      "venues",
      "orders",
      "tickets",
      "refunds",
      "channels",
      "payments",
      "audit",
      "observability",
      "geo",
      "webhooks",
    ];
    for (const id of mustKeep) {
      expect(ids).toContain(id);
    }
  });
});

// ---------------------------------------------------------------------------
// 4. Placeholder nav entry coverage
// ---------------------------------------------------------------------------

describe("PR-07 productionPlaceholder entries pinned", () => {
  const placeholders = NAV_ENTRIES.filter((e) => e.productionPlaceholder === true);

  it("exactly 3 entries are marked productionPlaceholder", () => {
    expect(placeholders).toHaveLength(3);
  });

  it("the placeholder ids are reports, notifications_content, pos", () => {
    const ids = placeholders.map((e) => e.id).sort();
    expect(ids).toEqual(["notifications_content", "pos", "reports"]);
  });

  it("the placeholder routes are /reports, /content, /pos", () => {
    const paths = placeholders.map((e) => e.to).sort();
    expect(paths).toEqual(["/content", "/pos", "/reports"]);
  });

  it("placeholders have a non-always permission rule (still gated when accessed directly)", () => {
    for (const e of placeholders) {
      expect(e.permission).not.toBe("always");
    }
  });
});

// ---------------------------------------------------------------------------
// 5. nginx.conf exists and contains expected directives
// ---------------------------------------------------------------------------

describe("PR-07 nginx.conf", () => {
  let nginxConf: string;

  it("apps/admin-web/nginx.conf exists", () => {
    nginxConf = readFileSync(join(ADMIN_WEB_ROOT, "nginx.conf"), "utf8");
    expect(nginxConf.length).toBeGreaterThan(100);
  });

  it("listens on port 8080 (non-root)", () => {
    nginxConf ??= readFileSync(join(ADMIN_WEB_ROOT, "nginx.conf"), "utf8");
    expect(nginxConf).toMatch(/listen\s+8080/);
  });

  it("has SPA fallback try_files $uri /index.html", () => {
    nginxConf ??= readFileSync(join(ADMIN_WEB_ROOT, "nginx.conf"), "utf8");
    expect(nginxConf).toMatch(/try_files.*\$uri.*index\.html/);
  });

  it("has /health endpoint returning 200", () => {
    nginxConf ??= readFileSync(join(ADMIN_WEB_ROOT, "nginx.conf"), "utf8");
    expect(nginxConf).toMatch(/location.*\/health/);
    expect(nginxConf).toMatch(/return\s+200/);
  });

  it("has immutable cache headers for static assets", () => {
    nginxConf ??= readFileSync(join(ADMIN_WEB_ROOT, "nginx.conf"), "utf8");
    expect(nginxConf).toMatch(/immutable/);
    expect(nginxConf).toMatch(/expires\s+1y/);
  });

  it("disables cache on index.html (SPA entry point)", () => {
    nginxConf ??= readFileSync(join(ADMIN_WEB_ROOT, "nginx.conf"), "utf8");
    expect(nginxConf).toMatch(/index\.html/);
    expect(nginxConf).toMatch(/no-store/);
  });
});

// ---------------------------------------------------------------------------
// 6. Dockerfile exists with expected content
// ---------------------------------------------------------------------------

describe("PR-07 apps/admin-web/Dockerfile", () => {
  let dockerfile: string;

  it("Dockerfile exists", () => {
    dockerfile = readFileSync(join(ADMIN_WEB_ROOT, "Dockerfile"), "utf8");
    expect(dockerfile.length).toBeGreaterThan(100);
  });

  it("uses nginx:1.27-alpine (or compatible) as the runtime base", () => {
    dockerfile ??= readFileSync(join(ADMIN_WEB_ROOT, "Dockerfile"), "utf8");
    expect(dockerfile).toMatch(/FROM\s+nginx:/);
  });

  it("runs as non-root USER nginx", () => {
    dockerfile ??= readFileSync(join(ADMIN_WEB_ROOT, "Dockerfile"), "utf8");
    expect(dockerfile).toMatch(/USER\s+nginx/);
  });

  it("has a HEALTHCHECK directive", () => {
    dockerfile ??= readFileSync(join(ADMIN_WEB_ROOT, "Dockerfile"), "utf8");
    expect(dockerfile).toMatch(/HEALTHCHECK/);
  });

  it("uses multi-stage build (FROM ... AS build, FROM ... AS runtime)", () => {
    dockerfile ??= readFileSync(join(ADMIN_WEB_ROOT, "Dockerfile"), "utf8");
    expect(dockerfile).toMatch(/AS\s+build/);
    expect(dockerfile).toMatch(/AS\s+runtime/);
  });

  it("runs npm ci (not npm install) for deterministic installs", () => {
    dockerfile ??= readFileSync(join(ADMIN_WEB_ROOT, "Dockerfile"), "utf8");
    expect(dockerfile).toMatch(/npm ci/);
    expect(dockerfile).not.toMatch(/npm install/);
  });

  it("accepts VITE_API_BASE_URL as a build ARG (no secret baked in)", () => {
    dockerfile ??= readFileSync(join(ADMIN_WEB_ROOT, "Dockerfile"), "utf8");
    expect(dockerfile).toMatch(/ARG\s+VITE_API_BASE_URL/);
    // The default must be empty or a placeholder — not a real URL baked in.
    // (VITE_API_BASE_URL without a default forces the build arg to be
    // supplied explicitly, preventing accidental wrong-backend deploys.)
    expect(dockerfile).not.toMatch(/VITE_API_BASE_URL=https?:\/\/[^$]/);
  });
});
