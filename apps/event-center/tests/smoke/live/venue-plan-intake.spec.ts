/**
 * Twin test "Seating plan: upload and review" (spec 64 sections 2.1 to 2.5) on the
 * three roles of one organization, against a REAL arena-api.
 *
 * Since wave 2 the create route leaves a PDF, a picture or a spreadsheet in state
 * `received` and the worker job venue_plan.generate moves it on. The harness
 * starts arena-api only, no worker, so such an upload stays `received` (the page
 * says "We make the seating plan"). A source SVG that imports cleanly becomes
 * draft 1 inside the create route and waits for the organizer's check (state
 * `review`). The cases below use exactly those two paths.
 *
 * The spec makes its own venue (a fresh name per run) with the owner's token, so
 * every run starts with no intake and no plan on it, and repeats cleanly.
 *
 * What the organization's API cannot set up is `fixme` at the end, with its reason:
 * the harness has no platform administrator and no second organization.
 */
import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";
import { pickWizardVenue, skipPoster } from "./wizardHelpers";

// The cases upload, read a draft, approve and open the wizard: with a cold Vite dev server that is
// more than the 30 s default. (The real cause of the failure in a full run was a second sign-in
// on a signed-in page, see the wizard step of the second case.)
test.describe.configure({ mode: "serial", timeout: 120_000 });

const env = liveEnv();
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const nonce = Date.now().toString(36);
const HALL = `Plan Intake Hall ${nonce}`;
const CITY = "Madrid";

const ROLES: { role: Role; label: string }[] = [
  { role: "org_admin", label: "owner" },
  { role: "organizer", label: "manager" },
  { role: "agent", label: "agent" },
];

const headers = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function orgMe(request: APIRequestContext, role: Role) {
  const res = await request.get(`${base}/me`, { headers: headers(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return (await res.json()) as { role: string; permissions: string[] };
}

/** A venue with a country and a city, made once per run with the owner's token. */
let ensured: Promise<string> | null = null;
function ensureHall(request: APIRequestContext): Promise<string> {
  ensured ??= (async () => {
    const owner = headers("org_admin");
    const countries = await request.get(`${env.api_base_url}/v1/geo/countries?lang=en`);
    expect(countries.status(), "GET countries").toBe(200);
    const spain = ((await countries.json()) as { countries: { id: string; iso2: string }[] }).countries.find((c) => c.iso2 === "ES");
    expect(spain, "the reference has Spain").toBeDefined();
    const city = await request.post(`${base}/cities`, { headers: owner, data: { country_id: spain?.id, name: CITY, locale: "en" } });
    expect(city.ok(), `POST cities: ${await city.text()}`).toBe(true);
    const cityId = ((await city.json()) as { city: { id: string } }).city.id;
    const made = await request.post(`${base}/venues`, {
      headers: owner,
      data: { name: HALL, city_id: cityId, country: "ES", timezone: "Europe/Madrid", address_line1: "Calle Mayor 2", capacity_default: 120, status: "active" },
    });
    expect(made.ok(), `POST venues: ${await made.text()}`).toBe(true);
    return ((await made.json()) as { venue: { id: string } }).venue.id;
  })();
  return ensured;
}

/** The smallest PDF the server reads as a PDF: the signature decides, the server counts pages when it can. */
const TINY_PDF = Buffer.from("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%EOF\n", "latin1");

/** A seat SVG that passes ImportSVG (the Go importer's own rules: canvas, legend, price category, rows with titles, seats with numbers). */
function planSvg(rows: number, seatsPerRow: number): string {
  const parts = [
    `<?xml version="1.0" encoding="UTF-8"?>`,
    `<svg xmlns="http://www.w3.org/2000/svg" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape" viewBox="0 0 400 300">`,
    `<g id="Legend"/>`,
    `<g id="PriceCategory"><circle inkscape:label="#First" cx="5" cy="5" r="3" style="fill:#ff0000"/></g>`,
  ];
  for (let r = 1; r <= rows; r += 1) {
    parts.push(`<g inkscape:label="#Parter"><title>${r}</title>`);
    for (let n = 1; n <= seatsPerRow; n += 1) {
      parts.push(`<circle cx="${20 + n * 20}" cy="${20 + r * 25}" r="6" style="fill:#ff0000"><title>${n}</title></circle>`);
    }
    parts.push(`</g>`);
  }
  parts.push(`</svg>`);
  return parts.join("");
}

interface IntakeCard {
  id: string;
  state: string;
  escalation_reason: string | null;
  plan_id: string | null;
  draft: { summary: { seats: number; rows: number; sections: number } } | null;
}

async function intakesOf(request: APIRequestContext, venueId: string): Promise<{ id: string; state: string }[]> {
  const res = await request.get(`${base}/plan-intakes?venue_id=${venueId}`, { headers: headers("org_admin") });
  expect(res.status(), "GET plan-intakes").toBe(200);
  return ((await res.json()) as { intakes: { id: string; state: string }[] }).intakes;
}

async function intakeCard(request: APIRequestContext, id: string): Promise<IntakeCard> {
  const res = await request.get(`${base}/plan-intakes/${id}`, { headers: headers("org_admin") });
  expect(res.status(), "GET plan-intake").toBe(200);
  return ((await res.json()) as { intake: IntakeCard }).intake;
}

/** Opens the venue card as the owner. */
async function openCard(page: Page, venueId: string) {
  await page.goto(`/o/${env.org_id}/venues/${venueId}`);
  await expect(page.getByTestId("venue-card")).toBeVisible();
}

/** Sends files from the card: the button, the files, "Send the files". Ends on the page of the new intake. */
async function uploadFromCard(page: Page, files: { name: string; mimeType: string; buffer: Buffer }[]) {
  await page.getByTestId("venue-plan-upload-button").click();
  await page.getByTestId("plan-upload-input").setInputFiles(files);
  await expect(page.getByTestId("plan-upload-list")).toBeVisible();
  await page.getByTestId("plan-upload-send").click();
  await page.waitForURL(/\/venues\/[^/]+\/plan-intakes\/[^/?]+/);
}

test.describe("seating plan upload and review (live)", () => {
  test("the owner uploads a PDF, sees that the plan is being built (no worker in the harness), and cancels", async ({ page, request }) => {
    const hallId = await ensureHall(request);
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await openCard(page, hallId);
    await expect(page.getByTestId("venue-plan-slot-none")).toBeVisible();

    await uploadFromCard(page, [{ name: "hall.pdf", mimeType: "application/pdf", buffer: TINY_PDF }]);
    await expect(page.getByTestId("intake-waiting-generator")).toContainText("We make the seating plan");
    await expect(page.getByTestId("intake-waiting-generator")).toContainText("up to 5 minutes");
    const id = /plan-intakes\/([^/?]+)/.exec(page.url())?.[1] ?? "";
    expect(id).not.toBe("");

    // The platform's own view of it: the create route leaves the intake `received`; only the worker
    // job venue_plan.generate moves it on, and the harness starts no worker.
    const card = await intakeCard(request, id);
    expect(card.state).toBe("received");
    expect(card.escalation_reason).toBeNull();

    // The venue card says the same and links to the page.
    await openCard(page, hallId);
    await expect(page.getByTestId("venue-plan-slot-running")).toContainText("The upload has the status: File received.");
    await page.getByTestId("venue-plan-slot-open").click();
    await expect(page.getByTestId("intake-waiting-generator")).toBeVisible();

    // Cancel with one confirm press.
    await page.getByTestId("intake-cancel").click();
    await page.getByTestId("intake-cancel-dialog-submit").click();
    await expect(page.getByTestId("intake-cancelled")).toContainText("The upload is canceled");
    expect((await intakeCard(request, id)).state).toBe("cancelled");
    // A canceled upload offers a new one.
    await expect(page.getByTestId("intake-send-another")).toBeVisible();
  });

  test("the owner uploads a seat SVG, checks the draft, approves it and the plan appears on the card and in the wizard", async ({ page, request }) => {
    const hallId = await ensureHall(request);
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await openCard(page, hallId);

    await uploadFromCard(page, [{ name: "hall.svg", mimeType: "image/svg+xml", buffer: Buffer.from(planSvg(2, 5), "utf8") }]);
    const review = page.getByTestId("intake-review");
    await expect(review).toBeVisible();
    await expect(page.getByTestId("intake-stat-seats")).toHaveText("10");
    await expect(page.getByTestId("intake-stat-rows")).toHaveText("2");
    await expect(page.getByTestId("intake-stat-sections")).toHaveText("1");
    await expect(page.getByTestId("intake-ready")).toContainText(`We made the seating plan of «${HALL}»: 10 seats, 2 rows.`);
    await expect(page.getByTestId("intake-warnings")).toHaveCount(0);

    // The draft is shown large, from a blob URL, and the file that was sent beside it.
    const draft = page.getByTestId("intake-draft-img");
    await expect(draft).toBeVisible();
    await expect.poll(() => draft.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth > 0)).toBe(true);
    expect(await draft.getAttribute("src")).toMatch(/^blob:/);
    await expect(page.locator('[data-testid^="intake-source-img-"]').first()).toBeVisible();
    await expect(page.getByTestId("intake-sources")).toContainText("hall.svg");
    await expect(page.getByTestId("intake-sources")).toContainText("Download");

    // Pan and zoom: the buttons change the scale and "Show all" resets it.
    const content = page.locator(".plan-zoom-content");
    await expect(content).toHaveAttribute("data-scale", "1.00");
    await page.getByTestId("intake-draft-in").click();
    await expect(content).toHaveAttribute("data-scale", "1.50");
    await page.getByTestId("intake-draft-reset").click();
    await expect(content).toHaveAttribute("data-scale", "1.00");

    // Approve with one confirm press.
    await page.getByTestId("intake-approve").click();
    await expect(page.getByTestId("intake-approve-dialog")).toContainText("10 seats");
    await page.getByTestId("intake-approve-dialog-submit").click();
    await expect(page.getByTestId("intake-saved")).toContainText("We saved the seating plan: 10 seats");
    // Opened without a date: prices are split on the date of the event, after the event is created.
    await expect(page.getByTestId("intake-saved-no-event")).toContainText("Add an event at this venue first");
    await expect(page.getByTestId("intake-create-event")).toBeVisible();
    await expect(page.getByTestId("intake-split")).toHaveCount(0);

    const id = /plan-intakes\/([^/?]+)/.exec(page.url())?.[1] ?? "";
    const card = await intakeCard(request, id);
    expect(card.state).toBe("approved");
    expect(card.plan_id).not.toBeNull();
    const planId = card.plan_id as string;

    // The same page, opened from a date, goes back to that date: "Split seats by price".
    await page.goto(`/o/${env.org_id}/venues/${hallId}/plan-intakes/${id}?event=${env.event_id}&session=${env.session_id}`);
    const split = page.getByTestId("intake-split");
    await expect(split).toBeVisible();
    await expect(split).toHaveAttribute("href", `/o/${env.org_id}/events/${env.event_id}/sessions/${env.session_id}/seats?tab=layout`);
    await split.click();
    await expect(page).toHaveURL(new RegExp(`/events/${env.event_id}/sessions/${env.session_id}/seats\\?tab=layout`));

    // The plan is listed on the venue card with its preview.
    await openCard(page, hallId);
    await expect(page.getByTestId(`venue-card-plan-${planId}`)).toContainText("10");
    await expect(page.getByTestId(`venue-plan-preview-${planId}`)).toBeVisible();
    await expect(page.getByTestId("venue-plan-slot-none")).toBeVisible();

    // And in the wizard's seating section, to choose.
    // The page is signed in already: openWizard() would sign in AGAIN, and the login form of a signed-in
    // person detaches under the fill (the app sends them on to the events), so the wizard is opened directly.
    await page.goto(`/o/${env.org_id}/events`);
    await page.getByTestId("events-create").click();
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events/new`));
    await expect(page.getByRole("heading", { level: 1, name: "New event" })).toBeVisible();
    await skipPoster(page);
    await pickWizardVenue(page, HALL);
    await expect(page.getByTestId("wizard-seating")).toBeVisible();
    const choice = page.locator('[data-testid^="wizard-seating-plan-"]:not([data-testid="wizard-seating-plan-none"])');
    await expect(choice).toHaveCount(1);
    await expect(page.getByTestId("wizard-seating")).toContainText("10 seats");
    await expect(page.getByTestId("wizard-seating-none")).toHaveCount(0);
  });

  test("the owner writes a correction on a second SVG and the plan waits for an administrator", async ({ page, request }) => {
    const hallId = await ensureHall(request);
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await openCard(page, hallId);

    await uploadFromCard(page, [{ name: "hall-2.svg", mimeType: "image/svg+xml", buffer: Buffer.from(planSvg(1, 6), "utf8") }]);
    await expect(page.getByTestId("intake-review")).toBeVisible();
    await expect(page.getByTestId("intake-stat-seats")).toHaveText("6");
    const id = /plan-intakes\/([^/?]+)/.exec(page.url())?.[1] ?? "";

    // An empty text is refused on the screen.
    await page.getByTestId("intake-fix").click();
    await expect(page.getByTestId("intake-fix-text-error")).toContainText("Write the correction");
    expect((await intakeCard(request, id)).state).toBe("review");

    await page.getByTestId("intake-fix-text").fill("Row 1 has 8 seats, not 6.");
    await page.getByTestId("intake-fix").click();
    await expect(page.getByTestId("intake-waiting-operator")).toContainText("An administrator got your message");
    const card = await intakeCard(request, id);
    expect(card.state).toBe("operator");
    expect(card.escalation_reason).toBe("organizer_text");
    await noA11yViolations(page);
  });

  test("a file that is too large and a file that is not accepted are refused on the screen, with no request", async ({ page, request }) => {
    const hallId = await ensureHall(request);
    const before = (await intakesOf(request, hallId)).length;
    const sent: string[] = [];
    page.on("request", (r) => {
      if (r.method() === "POST" && r.url().includes("/plan-intakes")) sent.push(r.url());
    });
    await signInAs(page, person(env, "org_admin"));
    await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
    await openCard(page, hallId);
    await page.getByTestId("venue-plan-upload-button").click();

    const input = page.getByTestId("plan-upload-input");
    const big = Buffer.alloc(21 * 1024 * 1024);
    big.write("%PDF-1.4\n", 0, "latin1");
    await input.setInputFiles([{ name: "huge.pdf", mimeType: "application/pdf", buffer: big }]);
    await expect(page.getByTestId("plan-upload-problems")).toContainText("huge.pdf");
    await expect(page.getByTestId("plan-upload-problems")).toContainText("The file is more than 20 MB");

    await input.setInputFiles([{ name: "notes.txt", mimeType: "text/plain", buffer: Buffer.from("these are not seats\n") }]);
    await expect(page.getByTestId("plan-upload-problems")).toContainText("We cannot read this file");

    // A name does not make a file: a PDF without its signature is refused too.
    await input.setInputFiles([{ name: "fake.pdf", mimeType: "application/pdf", buffer: Buffer.from("not a pdf at all") }]);
    await expect(page.getByTestId("plan-upload-problems")).toContainText("We cannot read this file");

    await expect(page.getByTestId("plan-upload-list")).toHaveCount(0);
    await expect(page.getByTestId("plan-upload-send")).toBeDisabled();
    await noA11yViolations(page);
    expect(sent, "no upload request was sent").toEqual([]);
    expect((await intakesOf(request, hallId)).length).toBe(before);
  });

  for (const { role, label } of ROLES) {
    test(`${label} (${role}): the server's /me decides who sees the upload button`, async ({ page, request }) => {
      const hallId = await ensureHall(request);
      const me = await orgMe(request, role);
      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      await page.goto(`/o/${env.org_id}/venues/${hallId}`);

      if (!me.permissions.includes("venue.read")) {
        // No right to see venues at all: the screen says so and shows nothing of the plan.
        await expect(page.getByTestId("venues-no-access")).toBeVisible();
        await expect(page.getByTestId("venue-plan-upload-button")).toHaveCount(0);
        return;
      }
      await expect(page.getByTestId("venue-card")).toBeVisible();
      if (me.permissions.includes("seating_plan.create")) {
        await expect(page.getByTestId("venue-plan-upload-button")).toBeVisible();
      } else {
        // The role may look at the plans, never start an upload.
        await expect(page.getByTestId("venue-plan-upload-button")).toHaveCount(0);
      }
      if (!me.permissions.includes("seating_plan.read") && !me.permissions.includes("seating_plan.create")) {
        await expect(page.getByTestId("venue-plan-slot")).toHaveCount(0);
      }

      // A deep link to an intake without the right to read it shows the refusal, and no button.
      const intakes = await intakesOf(request, hallId);
      expect(intakes.length).toBeGreaterThan(0);
      await page.goto(`/o/${env.org_id}/venues/${hallId}/plan-intakes/${intakes[0].id}`);
      if (me.permissions.includes("seating_plan.read")) {
        await expect(page.getByTestId("venue-plan-intake")).toBeVisible();
        if (!me.permissions.includes("seating_plan.create")) {
          for (const id of ["intake-approve", "intake-fix", "intake-escalate", "intake-cancel", "intake-send-another"]) {
            await expect(page.getByTestId(id), id).toHaveCount(0);
          }
        }
      } else {
        await expect(page.getByTestId("intake-no-access")).toBeVisible();
      }
    });
  }

  test("an administrator's draft comes back to the organizer for review", async () => {
    test.fixme(true, "needs a platform administrator session: the harness has the three members of the organization only, and the draft route is /v1/admin/plan-intakes/*");
  });

  test("a rejected upload shows the administrator's reason and 'Send another file'", async () => {
    test.fixme(true, "needs a platform administrator to reject the intake (POST /v1/admin/plan-intakes/{id}/reject), which the harness cannot sign in as");
  });

  test("a verified plan of the venue is offered before an upload and its copy is selected", async () => {
    test.fixme(true, "needs a verified template of ANOTHER organization (POST /v1/admin/seating-plans/{id}/verify by a superadmin and a second organization): the organization API cannot make one. The wizard's offer, decline and copy rules are covered by WizardSeatingSection.test.tsx and wizardFlow.test.ts");
  });

  test("the generator builds a draft in up to 5 minutes", async () => {
    test.fixme(true, "wave 2 of the server: the generator and the states received and generating have no writer yet (docs/agents/venue-plan-intake.md)");
  });
});
