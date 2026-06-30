/**
 * Wave M-8 (#301) — single source of truth for the mobile quality
 * gate thresholds.
 *
 * This module is intentionally framework-free (no Playwright, no
 * Lighthouse imports) so it can be loaded from:
 *
 *   - playwright.config.ts          (Playwright run, mobile smoke set)
 *   - lighthouse.config.json        (read indirectly via the smoke spec)
 *   - smoke.spec.ts                 (the Playwright smoke set itself)
 *   - apps/admin-web/src/routes/m8AccessibilityGate.test.ts
 *     (the executable vitest gate enforced on every push)
 *
 * Drift between the Playwright config, the Lighthouse config, and the
 * vitest gate is what M-8 is trying to prevent. By forcing every
 * surface to import from here, the gate test pins the contract and
 * a stale Playwright config can no longer ship green.
 */

/**
 * 44 CSS px is the minimum tap-target size required by WCAG 2.5.5
 * (Target Size, Level AAA) and the Apple Human Interface Guidelines.
 * This is the same constant Wave M-7 pinned as `M7_TAP_TARGET_PX` on
 * webhooks.tsx; M-8 re-pins it here so the responsive-quality gate is
 * self-contained.
 */
export const M8_TAP_TARGET_MIN_PX = 44;

/**
 * Lighthouse accessibility score required on every route listed in
 * `M8_LIGHTHOUSE_ROUTES`. Below this threshold CI fails.
 */
export const M8_LIGHTHOUSE_ACCESSIBILITY_MIN = 90;

/**
 * The two viewports the smoke set runs against:
 *   - 360 x 640  smallest supported phone in portrait
 *   - 768 x 1024 small tablet in portrait, the desktop/mobile cut
 */
export const M8_VIEWPORTS: ReadonlyArray<{
  readonly label: string;
  readonly widthPx: number;
  readonly heightPx: number;
}> = Object.freeze([
  Object.freeze({ label: "360x640", widthPx: 360, heightPx: 640 }),
  Object.freeze({ label: "768x1024", widthPx: 768, heightPx: 1024 }),
]);

/**
 * Organizer / agent routes the M-8 smoke set walks at 360 x 640.
 * Any route added to the mobile-organizer / mobile-agent navigation
 * MUST be added here too so the horizontal-scroll + tap-target gates
 * cover it.
 *
 * Order matches the priority in 13_backend_go_initial_specification_ru.md
 * and the wave breakdown in the M-series backlog (M-2..M-7).
 */
export const M8_SMOKE_ROUTES: ReadonlyArray<string> = Object.freeze([
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
]);

/**
 * The three highest-traffic routes the Lighthouse accessibility gate
 * scores. Kept narrower than `M8_SMOKE_ROUTES` so Lighthouse runs stay
 * cheap on every push.
 */
export const M8_LIGHTHOUSE_ROUTES: ReadonlyArray<string> = Object.freeze([
  "/orders",
  "/tickets",
  "/events",
]);

/**
 * A single object the Playwright config re-exports so a downstream
 * runner (or CI shell script) can `JSON.stringify` the thresholds
 * without having to know each constant by name.
 */
export const M8_GATE_THRESHOLDS = Object.freeze({
  tapTargetMinPx: M8_TAP_TARGET_MIN_PX,
  lighthouseAccessibilityMin: M8_LIGHTHOUSE_ACCESSIBILITY_MIN,
  viewports: M8_VIEWPORTS,
  smokeRoutes: M8_SMOKE_ROUTES,
  lighthouseRoutes: M8_LIGHTHOUSE_ROUTES,
});

export type M8GateThresholds = typeof M8_GATE_THRESHOLDS;
