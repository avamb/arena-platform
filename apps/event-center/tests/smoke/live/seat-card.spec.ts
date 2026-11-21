/**
 * Twin test "the seat card of the seat manager" (one card for the buyer and the organizer), against a
 * REAL arena-api. The date is the seed's "E2E Hall Show" (see seats.spec.ts): P|1|1 is sold,
 * P|6|1 is a free Standard seat.
 *
 * A mouse over a seat shows sector, row, seat, category and price, and the state (sold) or the
 * change that is not applied yet. Hover only informs: it paints nothing. The long press of a finger is
 * covered by the component test (SeatMapCanvas.test.tsx): a hover cannot be made on a phone.
 *
 * NOT run in the session that wrote it (no stand there): it runs in the live harness like its
 * neighbours (`npm run test:e2e:live`).
 */
import { expect, test, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, person, signInAs } from "./liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const EVENT = "fe0000e2-0000-7000-8000-00000000e601";
const SESSION = "fe0000e2-0000-7000-8000-00000000e602";
const ECONOMY = "E2E Hall Economy";

const here = dirname(fileURLToPath(import.meta.url));
const appDir = resolve(here, "..", "..", "..");
const repoRoot = resolve(appDir, "..", "..");

function resetHall(): void {
  const given = process.env.ARENA_E2E_SEED_BIN;
  const built = join(appDir, ".live", process.platform === "win32" ? "arena-e2e-seed.exe" : "arena-e2e-seed");
  const bin = given !== undefined && given !== "" ? given : built;
  if (!existsSync(bin)) throw new Error(`the seed binary ${bin} is missing: run through the live harness`);
  const r = spawnSync(bin, ["--seats-op", "reset"], { cwd: repoRoot, env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed --seats-op reset failed: ${r.stderr}`);
}

const seatNode = (page: Page, key: string) => page.locator(`[data-k="${key}"]`);
const card = (page: Page) => page.locator(".arena-seat-card");

test.beforeAll(() => resetHall());
test.afterAll(() => resetHall());

test.describe("the seat card (live, desktop mouse)", () => {
  test("owner: hover shows the seat, its category and state, and paints nothing", async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 900 });
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}/seats`);
    await expect(page.getByTestId("seat-map")).toBeVisible();

    // A sold seat: place, category, price and the state "Sold".
    await seatNode(page, "P|1|1").hover();
    await expect(card(page)).toBeVisible();
    await expect(card(page)).toHaveAttribute("role", "tooltip");
    await expect(card(page)).toContainText("Row 1");
    await expect(card(page)).toContainText("Seat 1");
    await expect(card(page)).toContainText("Sold");

    // A free seat: no state line. Hover changed nothing in the tray.
    await seatNode(page, "P|6|1").hover();
    await expect(card(page)).toContainText("Row 6");
    await expect(card(page)).not.toContainText("Sold");
    await expect(page.getByTestId("seat-tray-count")).toContainText("No changes.");

    // A paint that is not applied yet shows on the card.
    await page.getByTestId("seat-tab-brush").click();
    await page.getByTestId("seat-mode-categories").click();
    await page.locator(`[data-testid="seat-brush"][data-tier-name="${ECONOMY}"]`).click();
    await page.getByTestId("seat-tool-seat").click();
    await seatNode(page, "P|6|1").click();
    await expect(page.getByTestId("seat-tray-count")).toContainText("1 seats changed");
    await seatNode(page, "P|6|1").hover();
    await expect(card(page)).toContainText(`Not applied yet: ${ECONOMY}`);

    // Away from every seat the card goes.
    await page.mouse.move(2, 2);
    await expect(card(page)).toBeHidden();
  });
});
