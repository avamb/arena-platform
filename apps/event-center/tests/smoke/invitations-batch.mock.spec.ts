/**
 * Smoke of the CSV upload of invitations (spec 46, INV-06b) against a MOCKED
 * arena-api that parses the multipart form and validates the rows for real.
 */
import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { readFileSync } from "node:fs";
import { EVENT, ORG_A, SESSION, mockApi } from "./mockApi";
import { mockInvitationsSeats } from "./invitationsSeatsMock";
import { mockInvitationsBatch } from "./invitationsBatchMock";

async function signIn(page: Page) {
  await page.goto("/login");
  await page.getByLabel("E-mail").fill("manager@example.test");
  await page.getByLabel("Password").fill("right-password");
  await page.getByTestId("login-submit").click();
  await expect(page.getByRole("heading", { name: "Choose an organization" })).toBeVisible();
}

const url = `/o/${ORG_A}/events/${EVENT}/sessions/${SESSION}/invitations`;

async function start(page: Page, permissions?: string[], expectUpload = true) {
  await page.setViewportSize({ width: 1280, height: 900 });
  await mockApi(page);
  const seats = await mockInvitationsSeats(page, permissions);
  const batch = await mockInvitationsBatch(page, seats);
  await signIn(page);
  await page.goto(url);
  if (expectUpload) await expect(page.getByTestId("csv-upload")).toBeVisible();
  return { seats, batch };
}

const csv = (text: string, name = "guests.csv") => ({ name, mimeType: "text/csv", buffer: Buffer.from(text, "utf8") });
const pickFile = (page: Page, file: ReturnType<typeof csv>) => page.getByTestId("csv-input").setInputFiles(file);

// Line 2 has no name, line 3 a bad e-mail, line 4 a sold seat (A|1|3).
const BAD = [
  "guest,name,email,sector,row,seat",
  "1,,ana@example.com,Parter,2,2",
  "2,Carlos Ruiz,carlos-at-example,Parter,2,4",
  "3,Marta Gil,marta@example.com,Parter,1,3",
].join("\n");
const GOOD = [
  "guest,name,email,sector,row,seat,category,qty",
  "1,Ana López,ana@example.com,Parter,2,2,,",
  "1,Ana López,,Parter,2,4,,",
  "2,Carlos Ruiz,,,,,General admission,3",
].join("\n");

test.describe("invitations from a CSV file (mock)", () => {
  test.describe.configure({ timeout: 60_000 });

  test("a bad file shows its problems by line, a fixed file is checked, issued and the result file downloads", async ({ page }) => {
    const { batch } = await start(page);

    // Nothing to check before a file is chosen.
    await expect(page.getByTestId("csv-check")).toBeDisabled();

    await pickFile(page, csv(BAD));
    await expect(page.getByTestId("csv-file-name")).toContainText("guests.csv");
    await page.getByTestId("csv-check").click();

    const report = page.getByTestId("csv-report");
    await expect(report).toHaveAttribute("data-valid", "0");
    await expect(report.getByTestId("csv-problem")).toHaveCount(3);
    await expect(report.locator('[data-testid="csv-problem"][data-line="2"]')).toContainText("Write the guest's name");
    await expect(report.locator('[data-testid="csv-problem"][data-line="3"]')).toContainText("e-mail address does not look right");
    await expect(report.locator('[data-testid="csv-problem"][data-line="4"]')).toContainText("sold or in a hold");
    // Nothing can be issued from it.
    await expect(page.getByTestId("csv-issue")).toHaveCount(0);
    expect(batch.requests).toHaveLength(1);
    expect(batch.requests[0]).toMatchObject({ dryRun: true, requestId: null, delivery: "pdf_only" });

    // The fixed file: a new pick, a new check.
    await pickFile(page, csv(GOOD, "guests-fixed.csv"));
    await expect(page.getByTestId("csv-report")).toHaveCount(0);
    await page.getByTestId("csv-check").click();
    await expect(page.getByTestId("csv-report")).toHaveAttribute("data-valid", "1");
    await expect(page.getByTestId("csv-report-summary")).toContainText("2 guests, 5 places");
    await expect(page.getByTestId("csv-preview-guest")).toHaveCount(2);
    await expect(page.getByTestId("csv-preview-guest").first()).toContainText("Ana López");
    // The charge is built from the numbers: 5 places at 0.50 EUR.
    await expect(page.getByTestId("csv-charge")).toContainText("€2.50");
    await expect(page.getByTestId("csv-charge")).toContainText("5 places × €0.50");

    await expect(page.getByTestId("csv-issue")).toContainText("Issue invitations: 2");
    await page.getByTestId("csv-issue").click();

    const result = page.getByTestId("csv-result");
    await expect(result).toContainText("The file is issued: 2 invitations.");
    await expect(result).toContainText("Places in total: 5.");
    await expect(page.getByTestId("csv-result-order")).toHaveCount(2);
    expect(batch.requests).toHaveLength(3);
    const real = batch.requests[2];
    expect(real.dryRun).toBe(false);
    expect(real.requestId).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(real.fileName).toBe("guests-fixed.csv");
    expect(real.fileText).toBe(GOOD);

    // The result file is fetched with the session and saved.
    const [download] = await Promise.all([page.waitForEvent("download"), page.getByTestId("csv-result-download").click()]);
    expect(download.suggestedFilename()).toMatch(/^invitation-batch-[0-9a-f]{8}\.csv$/);
    const path = await download.path();
    expect(readFileSync(path, "utf8")).toContain("1000000900");

    // The list follows: the orders of the file are marked as coming from it.
    const marks = page.getByTestId("inv-batch");
    await expect(marks).toHaveCount(2, { timeout: 15_000 });
    await expect(marks.first()).toContainText("From a CSV file");

    // Another file starts from the beginning.
    await page.getByTestId("csv-another").click();
    await expect(page.getByTestId("csv-file-name")).toContainText("No file chosen yet.");
    await expect(page.getByTestId("csv-check")).toBeDisabled();
  });

  test("a file over the limits is refused with the way out, before and after the server", async ({ page }) => {
    const { batch } = await start(page);

    // Over 512 KB: refused by the page, nothing is sent.
    await pickFile(page, csv("name,seat_key\n" + "x".repeat(520 * 1024)));
    await expect(page.getByTestId("csv-file-problem")).toContainText("larger than 512 KB");
    await expect(page.getByTestId("csv-check")).toBeDisabled();
    expect(batch.requests).toHaveLength(0);

    // Over 250 places: the server says so and the page tells the person to split the file.
    await pickFile(page, csv("name,category,qty\nAna,General admission,300\n"));
    await expect(page.getByTestId("csv-file-problem")).toHaveCount(0);
    await page.getByTestId("csv-check").click();
    const error = page.getByTestId("csv-error");
    await expect(error).toContainText("too big for one upload");
    await expect(error).toContainText("Split it into several files");
    await expect(page.getByTestId("csv-issue")).toHaveCount(0);

    // A spreadsheet is not a CSV.
    await pickFile(page, { name: "guests.xlsx", mimeType: "application/vnd.ms-excel", buffer: Buffer.from("x") });
    await expect(page.getByTestId("csv-file-problem")).toContainText("Save as");
  });

  test("a lost answer locks the form, Retry sends the SAME request id and file, the server answers the first batch", async ({ page }) => {
    const { batch } = await start(page);
    await pickFile(page, csv(GOOD));
    await page.getByTestId("csv-delivery-my_email").check();
    await page.getByTestId("csv-check").click();
    await expect(page.getByTestId("csv-report")).toHaveAttribute("data-valid", "1");
    expect(batch.requests[0].delivery).toBe("my_email");

    // The server commits the batch, the answer is lost.
    batch.dropNextAnswer = true;
    await page.getByTestId("csv-issue").click();
    await expect(page.getByTestId("csv-stalled")).toBeVisible();
    await expect(page.getByTestId("csv-pick")).toBeDisabled();
    await expect(page.getByTestId("csv-issue")).toHaveCount(0);

    await page.getByTestId("csv-retry").click();
    await expect(page.getByTestId("csv-result")).toBeVisible();
    await expect(page.getByTestId("csv-replayed")).toBeVisible();
    const real = batch.requests.filter((r) => !r.dryRun);
    expect(real).toHaveLength(2);
    expect(real[0].requestId).not.toBeNull();
    expect(real[1].requestId).toBe(real[0].requestId);
    expect(real[1].fileText).toBe(real[0].fileText);
    expect(real[1].delivery).toBe("my_email");
    expect(batch.batches).toBe(1);
  });

  test("Discard gives up an open attempt and unlocks the form", async ({ page }) => {
    const { batch } = await start(page);
    await pickFile(page, csv(GOOD));
    await page.getByTestId("csv-check").click();
    await expect(page.getByTestId("csv-report")).toHaveAttribute("data-valid", "1");
    batch.dropNextAnswer = true;
    await page.getByTestId("csv-issue").click();
    await expect(page.getByTestId("csv-stalled")).toBeVisible();
    await page.getByTestId("csv-discard").click();
    await expect(page.getByTestId("csv-pick")).toBeEnabled();
    await expect(page.getByTestId("csv-discarded")).toBeVisible();
    // The batch did go through on the server: the list shows it.
    await expect(page.getByTestId("inv-batch")).toHaveCount(2, { timeout: 15_000 });
  });

  test("a guest without an e-mail is a problem when delivery asks for one, and the check starts over when the delivery changes", async ({ page }) => {
    const { batch } = await start(page);
    await pickFile(page, csv(GOOD));
    await page.getByTestId("csv-delivery-guest_email").check();
    await page.getByTestId("csv-check").click();
    await expect(page.getByTestId("csv-report")).toHaveAttribute("data-valid", "0");
    await expect(page.locator('[data-testid="csv-problem"][data-code="email_required"]')).toHaveCount(1);

    await page.getByTestId("csv-delivery-pdf_only").check();
    await expect(page.getByTestId("csv-report")).toHaveCount(0);
    await page.getByTestId("csv-check").click();
    await expect(page.getByTestId("csv-report")).toHaveAttribute("data-valid", "1");
    expect(batch.requests.map((r) => r.delivery)).toEqual(["guest_email", "pdf_only"]);
  });

  test("a person who may read but not issue sees no upload", async ({ page }) => {
    await start(page, ["event.read", "session.read", "complimentary.read"], false);
    await expect(page.getByTestId("inv-list")).toBeVisible();
    await expect(page.getByTestId("csv-upload")).toHaveCount(0);
  });

  test("phone: nothing scrolls sideways and axe finds nothing, with a check table on screen", async ({ page }) => {
    await mockApi(page);
    const seats = await mockInvitationsSeats(page);
    await mockInvitationsBatch(page, seats);
    await signIn(page);
    await page.goto(url);
    await expect(page.getByTestId("csv-upload")).toBeVisible();
    await pickFile(page, csv(BAD));
    await page.getByTestId("csv-check").click();
    await expect(page.getByTestId("csv-report")).toBeVisible();
    await page.getByTestId("csv-help").locator("summary").click();
    const width = page.viewportSize()?.width ?? 375;
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
    const result = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"]).analyze();
    expect(result.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`)).toEqual([]);
  });
});
