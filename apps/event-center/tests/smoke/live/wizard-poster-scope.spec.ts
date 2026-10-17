import { expect, test, type Page } from "@playwright/test";
import { liveEnv, person, signInAs } from "./liveEnv";

const env = liveEnv();
const SECOND_ORG = "fe0000e2-0000-7000-8000-0000000000b2";

// Exercise a client-side route change, not page.goto (a full reload would
// hide retained React state). Only a second organization's access/read
// responses are simulated here; no write is allowed or sent to that tenant.
async function navigateWizard(page: Page, orgId: string) {
  await page.evaluate(async (id) => {
    const modulePath = "/src/router.tsx";
    const { router } = await import(modulePath);
    await router.navigate({ to: "/o/$orgId/events/new", params: { orgId: id } });
  }, orgId);
  await expect(page).toHaveURL(new RegExp(`/o/${orgId}/events/new$`));
  await expect(page.getByTestId("wizard-name")).toBeVisible();
}

test("cached organization navigation isolates wizard text and a selected poster", async ({ page, request }) => {
  const owner = person(env, "org_admin");
  const response = await request.get(`${env.api_base_url}/v1/organizations/${env.org_id}/me`, {
    headers: { Authorization: `Bearer ${owner.access_token}` },
  });
  expect(response.status()).toBe(200);
  const allowed = await response.json();
  await page.route(`${env.api_base_url}/v1/organizations/${SECOND_ORG}/**`, async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/me")) {
      await route.fulfill({ json: { ...allowed, org_id: SECOND_ORG, organization_id: SECOND_ORG } });
    } else if (path.endsWith("/venues")) {
      await route.fulfill({ json: { venues: [] } });
    } else if (path.endsWith("/promoters")) {
      await route.fulfill({ json: { promoters: [] } });
    } else if (path.endsWith("/imports/event-bundle") && new URL(route.request().url()).searchParams.get("dry_run") === "true") {
      await route.fulfill({ json: { errors: [], warnings: [] } });
    } else {
      throw new Error(`Unexpected second-org request: ${route.request().method()} ${path}`);
    }
  });

  let writes = 0;
  page.on("request", (r) => {
    if (["POST", "PATCH"].includes(r.method()) && !r.url().includes("dry_run=true") && !r.url().includes("/auth/")) writes += 1;
  });
  await signInAs(page, owner);
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events$`));
  await navigateWizard(page, env.org_id);
  await page.getByTestId("wizard-name").fill("Draft belonging to first organization");

  await navigateWizard(page, SECOND_ORG);
  await page.getByTestId("wizard-name").fill("Draft belonging to second organization");
  const encoded = await page.evaluate(() => {
    const canvas = document.createElement("canvas");
    canvas.width = 8;
    canvas.height = 12;
    return canvas.toDataURL("image/png").split(",")[1];
  });
  await page.getByTestId("wizard-poster-picker-input").setInputFiles({ name: "second.png", mimeType: "image/png", buffer: Buffer.from(encoded, "base64") });
  await expect(page.getByTestId("wizard-poster-picker-preview")).toBeVisible();

  // Both org access queries are cached now: the parent loading screen cannot
  // incidentally unmount the wizard and conceal a missing scope boundary.
  await navigateWizard(page, env.org_id);
  await expect(page.getByTestId("wizard-name")).toHaveValue("Draft belonging to first organization");
  await expect(page.getByTestId("wizard-poster-picker-preview")).toHaveCount(0);
  await navigateWizard(page, SECOND_ORG);
  await expect(page.getByTestId("wizard-name")).toHaveValue("Draft belonging to second organization");
  await expect(page.getByTestId("wizard-poster-picker-preview")).toHaveCount(0);
  expect(writes).toBe(0);
});
