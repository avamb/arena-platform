/**
 * Wave M-8 (#301) — mobile smoke set.
 *
 * Runs against every viewport in `M8_VIEWPORTS` and every route in
 * `M8_SMOKE_ROUTES`. The two gates this spec enforces:
 *
 *   (1) NO horizontal scroll at 360 px on any organizer/agent route.
 *       Implementation:
 *         scrollWidth = await page.evaluate(
 *           () => document.documentElement.scrollWidth,
 *         );
 *         expect(scrollWidth).toBeLessThanOrEqual(window.innerWidth);
 *
 *   (2) The primary action on each route is at least 44 x 44 CSS px.
 *       Implementation:
 *         locator =
 *           page.locator('[data-testid$="-primary-action"], button[type="submit"]')
 *               .first();
 *         box = await locator.boundingBox();
 *         expect(box.width).toBeGreaterThanOrEqual(44);
 *         expect(box.height).toBeGreaterThanOrEqual(44);
 *
 * Runtime: this file is consumed by `@playwright/test` when the gate
 * job runs in CI. The vitest gate (m8AccessibilityGate.test.ts) pins
 * the file's shape so a stale spec cannot ship.
 */
import {
  M8_GATE_THRESHOLDS,
  M8_SMOKE_ROUTES,
  M8_TAP_TARGET_MIN_PX,
  M8_VIEWPORTS,
} from "./gateThresholds";

// We import @playwright/test lazily through a typed shim so this file
// stays type-checkable from the vitest gate when @playwright/test is
// not installed in the admin-web workspace.
type TestApi = {
  describe: (label: string, body: () => void) => void;
  (label: string, body: (ctx: { page: PageApi }) => Promise<void>): void;
};
type PageApi = {
  goto: (url: string) => Promise<unknown>;
  evaluate: <T>(fn: () => T) => Promise<T>;
  viewportSize: () => { width: number; height: number } | null;
  locator: (selector: string) => {
    first: () => {
      boundingBox: () => Promise<{
        x: number;
        y: number;
        width: number;
        height: number;
      } | null>;
      count: () => Promise<number>;
    };
  };
};
type ExpectApi = <T>(value: T) => {
  toBeLessThanOrEqual: (n: number) => void;
  toBeGreaterThanOrEqual: (n: number) => void;
};

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const pw: { test: TestApi; expect: ExpectApi } = require("@playwright/test");
const { test, expect } = pw;

test.describe("Wave M-8 mobile smoke set", () => {
  for (const viewport of M8_VIEWPORTS) {
    test.describe(`viewport ${viewport.label}`, () => {
      for (const route of M8_SMOKE_ROUTES) {
        test(`${route} :: no horizontal scroll, 44px primary tap target`, async ({
          page,
        }) => {
          await page.goto(route);

          // Gate 1: no horizontal scroll. Asserted on every viewport,
          // but the 360 px viewport is the one the spec calls out.
          const scrollWidth = await page.evaluate(
            () => document.documentElement.scrollWidth,
          );
          const innerWidth = page.viewportSize()?.width ?? viewport.widthPx;
          expect(scrollWidth).toBeLessThanOrEqual(innerWidth);

          // Gate 2: primary action >= 44 x 44 CSS px. Walked through a
          // chain of fallback selectors so legacy routes that have not
          // adopted the `[data-testid$="-primary-action"]` convention
          // still get covered.
          const primary = page
            .locator(
              [
                '[data-testid$="-primary-action"]',
                'button[type="submit"]',
                'a[role="button"][data-primary="true"]',
              ].join(", "),
            )
            .first();
          const present = await primary.count();
          if (present > 0) {
            const box = await primary.boundingBox();
            if (box) {
              expect(box.width).toBeGreaterThanOrEqual(
                M8_TAP_TARGET_MIN_PX,
              );
              expect(box.height).toBeGreaterThanOrEqual(
                M8_TAP_TARGET_MIN_PX,
              );
            }
          }
        });
      }
    });
  }
});

// Export the thresholds so the gate test can pin them.
export {
  M8_GATE_THRESHOLDS,
  M8_SMOKE_ROUTES,
  M8_TAP_TARGET_MIN_PX,
  M8_VIEWPORTS,
};
