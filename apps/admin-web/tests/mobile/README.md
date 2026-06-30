# Wave M-8 — Mobile-Responsive Admin quality gate

This folder is the **CI quality gate** for the Mobile-Responsive Admin
work tracked in Wave M-1..M-7. It contains:

| File | Role |
| --- | --- |
| `gateThresholds.ts` | Single source of truth for the gate constants (viewports, routes, tap-target, Lighthouse score). |
| `playwright.config.ts` | Playwright config that runs `smoke.spec.ts` at 360x640 and 768x1024. |
| `smoke.spec.ts` | Mobile smoke set: no horizontal scroll at 360 px, primary action >= 44 x 44 CSS px. |
| `lighthouse.config.json` | Lighthouse CI config: accessibility >= 90 on `/orders`, `/tickets`, `/events`. |

## Why a separate folder

Vitest (the in-process unit/contract runner) owns `apps/admin-web/src/`
tests. Playwright (browser-driven, real Chromium) needs a separate
`testDir` so the two runners do not stomp on each other. The vitest
gate `apps/admin-web/src/routes/m8AccessibilityGate.test.ts` pins the
contents of this folder so a stale Playwright config can not ship
green: every push runs the vitest gate, which fails if the Playwright
config drifts away from `M8_GATE_THRESHOLDS`.

## Running locally

```bash
# 1. Install @playwright/test (admin-web does not depend on it by default
#    so unit-test runs stay fast).
cd apps/admin-web
npx --yes playwright install --with-deps chromium

# 2. Start the admin-web dev server in another terminal.
npm run dev

# 3. Run the mobile smoke set.
npx playwright test --config tests/mobile/playwright.config.ts

# 4. Run the Lighthouse accessibility gate (requires @lhci/cli).
npx --yes @lhci/cli@0.13 autorun \
  --config=tests/mobile/lighthouse.config.json
```

## Scope

M-8 is **responsive web only**. No native-app work (Detox, Appium,
React Native) is introduced.
