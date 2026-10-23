import { defineConfig } from "@playwright/test";
import base from "./playwright.live.config";

// Start the tickets-page production container on the port named below before
// this suite. The API and Event Center are still the real local live harness.
// No third-party player traffic is necessary for the deterministic checks.
process.env.ARENA_LIVE_PUBLIC_UI_PORT ??= "18095";
export default defineConfig(base, {
  testDir: ".",
  testMatch: ["live/materials.spec.ts", "materials/public.spec.ts"],
  retries: 0,
  reporter: [["list"], ["junit", { outputFile: "../../.live/materials-release.xml" }]],
});
