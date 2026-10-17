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

const SECOND_MEDIA = "0199aaaa-0000-7000-8000-0000000000c2";

test("cached organization navigation isolates wizard text, a selected poster, its upload and a read in flight", async ({ page, request }) => {
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
    } else if (path.endsWith(`/media/${SECOND_MEDIA}/poster-facts`)) {
      // SIMULATED read of the second organization's poster: slow, so the screen can be left while it runs.
      secondReads += 1;
      await new Promise((r) => setTimeout(r, 2500));
      await route.fulfill({
        json: { facts: { name: "Hint of the second organization", date: "", time: "", venue_name: "", city: "", age: "", description: "", categories: [] } },
      });
    } else if (path.endsWith("/imports/event-bundle") && new URL(route.request().url()).searchParams.get("dry_run") === "true") {
      await route.fulfill({ json: { errors: [], warnings: [] } });
    } else {
      throw new Error(`Unexpected second-org request: ${route.request().method()} ${path}`);
    }
  });

  // The upload of the second organization's poster is SIMULATED too (the fake organization cannot own real media):
  // every write of this test is answered here, none reaches the real server.
  let secondReads = 0;
  await page.route(/\/v1\/media$/, async (route) => {
    if (route.request().method() !== "POST") return route.continue();
    await route.fulfill({ status: 201, json: { media_object: { id: SECOND_MEDIA } } });
  });
  const writes: string[] = [];
  page.on("request", (r) => {
    if (["POST", "PATCH"].includes(r.method()) && !r.url().includes("dry_run=true") && !r.url().includes("/auth/")) writes.push(`${r.method()} ${new URL(r.url()).pathname}`);
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
  // Poster first: the picture is uploaded at once (simulated here) and is the second organization's decision only.
  await expect(page.getByTestId("wizard-poster-ok")).toBeVisible();
  await expect(page.getByTestId("wizard-poster")).toHaveAttribute("data-decision", "poster");
  // A read of it starts, and the person leaves for the first organization while it runs.
  await page.getByTestId("wizard-poster-read-button").click();
  await expect(page.getByTestId("wizard-poster-reading")).toBeVisible();

  // Both org access queries are cached now: the parent loading screen cannot
  // incidentally unmount the wizard and conceal a missing scope boundary.
  await navigateWizard(page, env.org_id);
  await expect(page.getByTestId("wizard-name")).toHaveValue("Draft belonging to first organization");
  await expect(page.getByTestId("wizard-poster-picker-preview")).toHaveCount(0);
  // Nothing of the second organization's poster decision, upload or read reaches the first.
  await expect(page.getByTestId("wizard-poster")).toHaveAttribute("data-decision", "");
  await expect(page.getByTestId("wizard-poster-ok")).toHaveCount(0);
  await expect(page.getByTestId("wizard-poster-skip")).toBeVisible();
  // The slow answer arrives while the first organization is open: it changes nothing there.
  await page.waitForTimeout(3200);
  await expect(page.getByTestId("poster-review")).toHaveCount(0);
  await expect(page.getByTestId("wizard-name")).toHaveValue("Draft belonging to first organization");
  await navigateWizard(page, SECOND_ORG);
  await expect(page.getByTestId("wizard-name")).toHaveValue("Draft belonging to second organization");
  await expect(page.getByTestId("wizard-poster-picker-preview")).toHaveCount(0);
  // The second organization keeps its stored picture id, and its stale read was dropped: no review, no auto read.
  await expect(page.getByTestId("wizard-poster")).toHaveAttribute("data-decision", "poster");
  await expect(page.getByTestId("wizard-poster-read-button")).toBeVisible();
  await expect(page.getByTestId("poster-review")).toHaveCount(0);
  await expect(page.getByTestId("wizard-name")).toHaveValue("Draft belonging to second organization");
  await page.waitForTimeout(500);
  expect(secondReads).toBe(1);
  expect(writes).toEqual(["POST /v1/media", `POST /v1/organizations/${SECOND_ORG}/media/${SECOND_MEDIA}/poster-facts`]);
});
