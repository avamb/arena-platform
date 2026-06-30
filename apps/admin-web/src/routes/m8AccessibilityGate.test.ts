/**
 * Wave M-8 (#301) executable gate — Accessibility & touch quality.
 *
 * The Mobile-Responsive Admin spec asks for:
 *   - A Playwright config in `apps/admin-web/tests/mobile/` that runs
 *     a mobile smoke set at 360x640 and 768x1024.
 *   - CI failing if any organizer/agent route renders horizontal
 *     scroll at 360 px or has a primary tap target below 44x44 CSS px.
 *   - Lighthouse accessibility score >= 90 on the three highest-
 *     traffic routes (orders, tickets, events).
 *   - No new native-app work — responsive web only.
 *
 * Playwright + Lighthouse only run in the dedicated CI job. This
 * vitest gate is what runs on every push: it pins the SHAPE of the
 * Playwright config, the Lighthouse config, and the cross-route
 * mobile contract so a stale config can not ship green.
 */
import { describe, expect, it } from "vitest";
import { readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  M8_GATE_THRESHOLDS,
  M8_LIGHTHOUSE_ACCESSIBILITY_MIN,
  M8_LIGHTHOUSE_ROUTES,
  M8_SMOKE_ROUTES,
  M8_TAP_TARGET_MIN_PX,
  M8_VIEWPORTS,
} from "../../tests/mobile/gateThresholds";
import { M7_TAP_TARGET_PX } from "./webhooks";

const HERE = dirname(fileURLToPath(import.meta.url));
const TESTS_MOBILE = resolve(HERE, "..", "..", "tests", "mobile");

async function readMobileFile(name: string): Promise<string> {
  return await readFile(resolve(TESTS_MOBILE, name), "utf8");
}

describe("Wave M-8 (#301) — Accessibility & touch quality gate", () => {
  describe("gateThresholds constants", () => {
    it("pins the 44 CSS px tap-target minimum (WCAG 2.5.5 AAA / Apple HIG)", () => {
      expect(M8_TAP_TARGET_MIN_PX).toBe(44);
    });

    it("matches the M-7 tap-target constant so the platform-wide contract is consistent", () => {
      // If M-7 ever raises the tap target, M-8 must move in lockstep.
      expect(M8_TAP_TARGET_MIN_PX).toBe(M7_TAP_TARGET_PX);
    });

    it("pins the Lighthouse accessibility floor at 90", () => {
      expect(M8_LIGHTHOUSE_ACCESSIBILITY_MIN).toBe(90);
    });

    it("publishes exactly the two viewports the spec calls out", () => {
      expect(M8_VIEWPORTS.length).toBe(2);
      const labels = M8_VIEWPORTS.map((v) => v.label);
      expect(labels).toContain("360x640");
      expect(labels).toContain("768x1024");
      const v360 = M8_VIEWPORTS.find((v) => v.label === "360x640");
      const v768 = M8_VIEWPORTS.find((v) => v.label === "768x1024");
      expect(v360?.widthPx).toBe(360);
      expect(v360?.heightPx).toBe(640);
      expect(v768?.widthPx).toBe(768);
      expect(v768?.heightPx).toBe(1024);
    });

    it("smoke routes cover every organizer/agent surface Waves M-2..M-7 converted", () => {
      // These are the routes touched by M-3 (auth), M-4 (lists),
      // M-5 (forms), M-6 (image upload via events) and M-7 (webhooks
      // + tickets). If a new mobile-organizer/agent route is added,
      // it MUST be added here too.
      const required = [
        "/orders",
        "/tickets",
        "/events",
        "/refunds",
        "/channels",
        "/payments",
        "/webhooks",
        "/organizations",
        "/venues",
        "/login",
        "/password-reset",
        "/accept-invite",
      ];
      for (const route of required) {
        expect(
          M8_SMOKE_ROUTES.includes(route),
          `M8_SMOKE_ROUTES missing ${route}`,
        ).toBe(true);
      }
    });

    it("Lighthouse routes are the three highest-traffic surfaces called out in the spec", () => {
      expect([...M8_LIGHTHOUSE_ROUTES].sort()).toEqual(
        ["/events", "/orders", "/tickets"],
      );
    });

    it("M8_GATE_THRESHOLDS aggregates every threshold the runner needs", () => {
      expect(M8_GATE_THRESHOLDS.tapTargetMinPx).toBe(44);
      expect(M8_GATE_THRESHOLDS.lighthouseAccessibilityMin).toBe(90);
      expect(M8_GATE_THRESHOLDS.viewports).toBe(M8_VIEWPORTS);
      expect(M8_GATE_THRESHOLDS.smokeRoutes).toBe(M8_SMOKE_ROUTES);
      expect(M8_GATE_THRESHOLDS.lighthouseRoutes).toBe(M8_LIGHTHOUSE_ROUTES);
    });
  });

  describe("playwright.config.ts", () => {
    it("lives at apps/admin-web/tests/mobile/playwright.config.ts", async () => {
      const src = await readMobileFile("playwright.config.ts");
      expect(src.length).toBeGreaterThan(0);
    });

    it("declares both 360x640 and 768x1024 projects from the gate constants", async () => {
      const src = await readMobileFile("playwright.config.ts");
      // The config must derive its projects from M8_VIEWPORTS so the
      // single source of truth wins.
      expect(src.includes("M8_VIEWPORTS")).toBe(true);
      expect(src.includes("M8_VIEWPORTS.map(")).toBe(true);
      // hasTouch must be enabled or the tap-target gate is meaningless.
      expect(src.includes("hasTouch: true")).toBe(true);
    });

    it("imports the gate thresholds (no inline magic numbers)", async () => {
      const src = await readMobileFile("playwright.config.ts");
      expect(
        src.includes('from "./gateThresholds"'),
      ).toBe(true);
      // No inline 44 / 90 / 360 / 768 literals masquerading as the
      // gate threshold. We tolerate them in comments; strip comments
      // before checking.
      const code = src
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .split(/\r?\n/)
        .map((line) => {
          const idx = line.indexOf("//");
          return idx === -1 ? line : line.slice(0, idx);
        })
        .join("\n");
      // The widthPx/heightPx reads from viewport.* are the only place
      // the numeric literals can appear, and they are read off the
      // imported constant, not re-declared inline. Sanity check:
      expect(/widthPx:\s*360/.test(code)).toBe(false);
      expect(/heightPx:\s*640/.test(code)).toBe(false);
    });
  });

  describe("smoke.spec.ts", () => {
    it("exists in apps/admin-web/tests/mobile/", async () => {
      const src = await readMobileFile("smoke.spec.ts");
      expect(src.length).toBeGreaterThan(0);
    });

    it("walks every route in M8_SMOKE_ROUTES at every viewport in M8_VIEWPORTS", async () => {
      const src = await readMobileFile("smoke.spec.ts");
      expect(src.includes("for (const viewport of M8_VIEWPORTS)")).toBe(true);
      expect(src.includes("for (const route of M8_SMOKE_ROUTES)")).toBe(true);
    });

    it("asserts document.documentElement.scrollWidth <= innerWidth (horizontal-scroll gate)", async () => {
      const src = await readMobileFile("smoke.spec.ts");
      expect(
        src.includes("document.documentElement.scrollWidth"),
      ).toBe(true);
      expect(src.includes("toBeLessThanOrEqual(innerWidth)")).toBe(true);
    });

    it("asserts primary action width/height >= M8_TAP_TARGET_MIN_PX (tap-target gate)", async () => {
      const src = await readMobileFile("smoke.spec.ts");
      expect(
        src.includes("toBeGreaterThanOrEqual(\n                M8_TAP_TARGET_MIN_PX,\n              )") ||
        src.includes("toBeGreaterThanOrEqual(M8_TAP_TARGET_MIN_PX)") ||
        /toBeGreaterThanOrEqual\([\s\n]*M8_TAP_TARGET_MIN_PX/.test(src),
      ).toBe(true);
      // Selector must include the canonical `*-primary-action` testid
      // pattern and a sensible fallback to `button[type="submit"]`.
      expect(src.includes('[data-testid$="-primary-action"]')).toBe(true);
      expect(src.includes('button[type="submit"]')).toBe(true);
    });
  });

  describe("lighthouse.config.json", () => {
    it("exists and is valid JSON", async () => {
      const src = await readMobileFile("lighthouse.config.json");
      expect(() => JSON.parse(src)).not.toThrow();
    });

    it("targets the three highest-traffic routes from M8_LIGHTHOUSE_ROUTES", async () => {
      const src = await readMobileFile("lighthouse.config.json");
      const cfg = JSON.parse(src) as {
        ci: { collect: { url: string[] } };
      };
      const urls = cfg.ci.collect.url.map((u) => new URL(u).pathname);
      for (const route of M8_LIGHTHOUSE_ROUTES) {
        expect(urls.includes(route), `lighthouse.config.json missing ${route}`).toBe(true);
      }
    });

    it("pins accessibility minScore >= 0.9 (the M8_LIGHTHOUSE_ACCESSIBILITY_MIN threshold)", async () => {
      const src = await readMobileFile("lighthouse.config.json");
      const cfg = JSON.parse(src) as {
        ci: {
          assert: {
            assertions: {
              "categories:accessibility": [string, { minScore: number }];
            };
          };
        };
      };
      const [level, opts] = cfg.ci.assert.assertions["categories:accessibility"];
      expect(level).toBe("error");
      expect(opts.minScore * 100).toBeGreaterThanOrEqual(
        M8_LIGHTHOUSE_ACCESSIBILITY_MIN,
      );
    });

    it("emulates a 360 px mobile form factor (the smallest supported phone in portrait)", async () => {
      const src = await readMobileFile("lighthouse.config.json");
      const cfg = JSON.parse(src) as {
        ci: {
          collect: {
            settings: {
              screenEmulation: { mobile: boolean; width: number };
            };
          };
        };
      };
      expect(cfg.ci.collect.settings.screenEmulation.mobile).toBe(true);
      expect(cfg.ci.collect.settings.screenEmulation.width).toBe(360);
    });
  });

  describe("scope guard — responsive web only", () => {
    it("no native-app runner is introduced in tests/mobile/", async () => {
      // Detox / Appium / React Native imports would mean the gate
      // strayed outside responsive-web scope.
      const forbidden = [
        /\bdetox\b/i,
        /\bappium\b/i,
        /\breact-native\b/i,
        /\bExpo\b/,
      ];
      for (const file of [
        "playwright.config.ts",
        "smoke.spec.ts",
        "lighthouse.config.json",
        "gateThresholds.ts",
      ] as const) {
        const src = await readMobileFile(file);
        for (const re of forbidden) {
          expect(
            re.test(src),
            `${file} unexpectedly references ${re}`,
          ).toBe(false);
        }
      }
    });

    it("admin-web package.json does NOT pull in a native-app SDK", async () => {
      const pkgRaw = await readFile(
        resolve(HERE, "..", "..", "package.json"),
        "utf8",
      );
      const pkg = JSON.parse(pkgRaw) as {
        dependencies?: Record<string, string>;
        devDependencies?: Record<string, string>;
      };
      const all = {
        ...(pkg.dependencies ?? {}),
        ...(pkg.devDependencies ?? {}),
      };
      for (const name of Object.keys(all)) {
        expect(
          /detox|appium|react-native|expo/i.test(name),
          `admin-web unexpectedly depends on native-app package ${name}`,
        ).toBe(false);
      }
    });
  });
});
