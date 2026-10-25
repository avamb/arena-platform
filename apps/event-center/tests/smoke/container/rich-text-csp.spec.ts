/**
 * The editor under the REAL nginx Content-Security-Policy (spec 44, RT-11, RT-13). The page is served by the
 * production image (`style-src 'self'`, `script-src 'self'`, no `unsafe-inline`) and talks to a real arena-api.
 * It proves what a dev server and jsdom cannot:
 *  - the editor loads (its own chunk and its own stylesheet are same-origin files) and works: typing, the
 *    toolbar, the link dialog, the counter;
 *  - not one Content-Security-Policy violation is reported in the console or by `securitypolicyviolation`,
 *    on the wizard's description, on the text of the letter and on the message of a session change;
 *  - HTML pasted the way Word and Google Docs put it on the clipboard (style, class, span, div, table, img,
 *    script, comments) is cut to the white list of the profile BEFORE the browser parses it, so even the
 *    `style` attributes of the paste raise no violation, and what is saved is exactly the white list.
 *  - the same on the message of a session change (the fixture of `arena-e2e-seed --session-change-op reset`).
 *
 * Run it as the poster CSP spec is run (tests/README.md, "Posters under the real CSP").
 */
import { expect, test, type Page } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { liveEnv, person, richText, signInAs } from "../live/liveEnv";

test.describe.configure({ mode: "serial" });

const env = liveEnv();
const base = `${env.api_base_url}/v1/organizations/${env.org_id}`;
const auth = { Authorization: `Bearer ${person(env, "org_admin").access_token}` };

/** Records every CSP violation the page reports: by the console and by the event. */
async function watchCsp(page: Page) {
  const violations: string[] = [];
  await page.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (e) => {
      (window as unknown as { __csp: string[] }).__csp ??= [];
      (window as unknown as { __csp: string[] }).__csp.push(`${e.violatedDirective} ${e.blockedURI}`);
    });
  });
  page.on("console", (m) => {
    if (/content security policy|refused to (apply|load|execute)/i.test(m.text())) violations.push(`console: ${m.text().slice(0, 400)}`);
  });
  return {
    all: async () => [...violations, ...(await page.evaluate(() => (window as unknown as { __csp?: string[] }).__csp ?? []))],
  };
}

/** A paste the way a browser makes it: the data is read from clipboardData (a real DataTransfer). */
async function pasteHtml(page: Page, testId: string, html: string, text: string) {
  await page.evaluate(
    ({ testId, html, text }) => {
      const box = document.querySelector(`[data-testid="${testId}"] [role="textbox"][aria-multiline="true"]`) as HTMLElement;
      box.focus();
      const data = new DataTransfer();
      data.setData("text/html", html);
      data.setData("text/plain", text);
      box.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }));
    },
    { testId, html, text },
  );
}

const WORD =
  "<html xmlns:o=\"urn:schemas-microsoft-com:office:office\"><head><meta http-equiv=Content-Type content=\"text/html; charset=utf-8\">" +
  "<style><!-- p.MsoNormal { margin:0cm; font-size:11.0pt; font-family:\"Calibri\",sans-serif; } --></style></head>" +
  "<body lang=EN-US style='tab-interval:36.0pt'><!--StartFragment-->" +
  "<p class=MsoNormal><b style='mso-bidi-font-weight:normal'><span style='font-size:14.0pt;font-family:\"Calibri\",sans-serif'>Summer concert</span></b><o:p></o:p></p>" +
  "<p class=MsoNormal>Doors at <i>7 pm</i>, tickets at <a href=\"https://example.com/tickets\" style=\"color:blue\" onclick=\"alert(1)\">our site</a>.<o:p>&nbsp;</o:p></p>" +
  "<div class=\"x\" style=\"background:url(https://evil.example/x.png)\"><img src=\"https://evil.example/p.png\" onerror=\"alert(2)\"><script>alert(3)</script>" +
  "<table style=\"width:100%\"><tr><td style=\"color:red\">cell text</td></tr></table></div><!--EndFragment--></body></html>";

const GOOGLE_DOCS =
  "<meta charset='utf-8'><b style=\"font-weight:normal;\" id=\"docs-internal-guid-1a2b3c4d\">" +
  "<p dir=\"ltr\" style=\"line-height:1.38;margin-top:0pt;\"><span style=\"font-size:11pt;font-weight:700;\">Google bold</span>" +
  "<span style=\"font-size:11pt;font-weight:400;\"> and plain</span></p>" +
  "<ul style=\"margin-top:0;\"><li dir=\"ltr\" style=\"list-style-type:disc;\"><p dir=\"ltr\" role=\"presentation\"><span>One</span></p></li>" +
  "<li dir=\"ltr\" style=\"list-style-type:disc;\"><p dir=\"ltr\" role=\"presentation\"><span>Two</span></p></li></ul></b>";

test("the container sends a policy without unsafe-inline for scripts and styles", async ({ request }) => {
  for (const path of ["/", "/o/any/events/new", "/index.html"]) {
    const csp = (await request.get(path)).headers()["content-security-policy"] ?? "";
    expect(csp, path).toContain("style-src 'self'");
    expect(csp, path).toContain("script-src 'self'");
    expect(csp, path).not.toMatch(/unsafe-inline|unsafe-eval/);
  }
});

test("control: the watcher does see a violation (a style attribute made by the page), so an empty list means something", async ({ page }) => {
  const csp = await watchCsp(page);
  await page.goto("/login");
  await page.evaluate(() => {
    const d = document.createElement("div");
    d.innerHTML = '<i style="color:red">x</i>';
    document.body.appendChild(d);
  });
  await expect.poll(async () => (await csp.all()).length).toBeGreaterThan(0);
  expect((await csp.all()).join(" ")).toMatch(/style-src/);
});

test("the wizard's description: the editor loads and works under the policy, a paste is cut to the white list, nothing violates the CSP", async ({ page }) => {
  const csp = await watchCsp(page);
  await signInAs(page, person(env, "org_admin"));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`), { timeout: 20_000 });
  await page.getByTestId("events-create").click();
  await expect(page.getByTestId("wizard-description")).toBeVisible();
  const box = richText(page, "wizard-description");
  await expect(box).toBeVisible({ timeout: 20_000 });

  // The editor's stylesheet is a file of this origin; the page has no inline style of the editor.
  const sheets = await page.evaluate(() => Array.from(document.styleSheets).map((s) => s.href ?? "inline"));
  expect(sheets.every((h) => h !== "inline" && new URL(h).origin === new URL(page.url()).origin)).toBe(true);
  expect(await page.locator("style").count()).toBe(0);

  // Typing, the toolbar and the counter work.
  await box.click();
  await page.keyboard.type("A twin test show");
  await page.keyboard.press("Control+A");
  await page.getByTestId("wizard-description").getByRole("button", { name: "Bold" }).click();
  await expect(box.locator("strong")).toHaveText("A twin test show");
  await expect(page.getByTestId("wizard-description")).toContainText("of 4000");

  // Word, then Google Docs: only the white list stays, and the browser reports nothing.
  await box.press("Control+A");
  await box.press("Delete");
  await pasteHtml(page, "wizard-description", WORD, "Summer concert Doors at 7 pm, tickets at our site. cell text");
  const html = await box.innerHTML();
  expect(html).toContain("<strong>Summer concert</strong>");
  expect(html).toContain("<em>7 pm</em>");
  expect(html).toContain('href="https://example.com/tickets"');
  expect(html).toContain("cell text");
  for (const bad of ["style=", "class=", "onclick", "onerror", "<img", "<script", "<table", "<span", "<div", "MsoNormal", "evil.example", "alert("]) {
    expect(html, bad).not.toContain(bad);
  }
  expect(await page.getByTestId("wizard-description").locator("[style]").count()).toBe(0);

  await box.press("Control+A");
  await box.press("Delete");
  await pasteHtml(page, "wizard-description", GOOGLE_DOCS, "Google bold and plain One Two");
  await expect(box.locator("strong")).toHaveText("Google bold");
  await expect(box.locator("li")).toHaveCount(2);
  expect(await box.innerHTML()).not.toContain("style=");
  // The wrapper <b style="font-weight:normal"> of Google Docs does not make everything bold.
  expect(await box.innerHTML()).not.toContain("<strong> and plain");

  // The draft keeps ART (the page's own storage), so the text survives a reload exactly as the white list.
  await page.getByTestId("wizard-name").fill("Rich text CSP");
  await page.reload();
  await expect(richText(page, "wizard-description").locator("li")).toHaveCount(2, { timeout: 20_000 });
  expect(await richText(page, "wizard-description").innerHTML()).not.toContain("style=");

  expect(await csp.all()).toEqual([]);
});

test("the text in the letter: the editor, the paste and the save under the policy", async ({ page, request }) => {
  const csp = await watchCsp(page);
  await signInAs(page, person(env, "org_admin"));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`), { timeout: 20_000 });
  await page.goto(`/o/${env.org_id}/events/${env.event_id}`);
  const box = richText(page, "email-note-field");
  await expect(box).toBeVisible({ timeout: 20_000 });
  await box.click();
  await pasteHtml(page, "email-note-field", GOOGLE_DOCS, "Google bold and plain One Two");
  await expect(box.locator("li")).toHaveCount(2);
  await page.getByTestId("email-note-save").click();
  await expect(page.getByTestId("email-note-status")).toContainText("Saved");
  const stored = (await (await request.get(`${base}/events`, { headers: auth })).json()) as { events: { id: string; email_note: string | null; email_note_format: string | null }[] };
  const mine = stored.events.find((e) => e.id === env.event_id);
  expect(mine?.email_note_format).toBe("art");
  expect(mine?.email_note).toContain("<strong>Google bold</strong>");
  expect(mine?.email_note).not.toMatch(/style=|<span|<b |<div|class=/);
  expect(await csp.all()).toEqual([]);

  // Cleanup: the fixture event goes back to having no text.
  await request.patch(`${base}/events/${env.event_id}`, { headers: auth, data: { email_note: null } });
});

// The message to buyers of a session change (spec 44, RT-11): the same editor in a third screen. The session
// and its paid order come from the fixture of `arena-e2e-seed --session-change-op reset` (the binary the
// live harness built into .live); nothing is confirmed, so nothing is written but the organizer's contact.
const SESSION_EVENT = "fe0000e2-0000-7000-8000-000000000501";
const SESSION_CANCEL = "fe0000e2-0000-7000-8000-000000000512";

function seedSessions(): void {
  const here = dirname(fileURLToPath(import.meta.url));
  const given = process.env.ARENA_E2E_SEED_BIN;
  const bin = given !== undefined && given !== "" ? given : join(resolve(here, "..", "..", ".."), ".live", process.platform === "win32" ? "arena-e2e-seed.exe" : "arena-e2e-seed");
  if (!existsSync(bin)) throw new Error(`the seed binary ${bin} is missing: run through the live harness`);
  const r = spawnSync(bin, ["--session-change-op", "reset"], { cwd: resolve(here, "..", "..", "..", "..", ".."), env: process.env, encoding: "utf8" });
  if (r.status !== 0) throw new Error(`arena-e2e-seed --session-change-op reset failed: ${r.stderr}`);
}

test("the message of a session change: the editor and the paste under the policy", async ({ page, request }) => {
  seedSessions();
  const put = await request.put(`${base}/events/${SESSION_EVENT}/contact`, { headers: auth, data: { email: "csp-contact@example.test" } });
  expect(put.status()).toBe(200);
  const csp = await watchCsp(page);
  await signInAs(page, person(env, "org_admin"));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`), { timeout: 20_000 });
  await page.goto(`/o/${env.org_id}/events/${SESSION_EVENT}/sessions/${SESSION_CANCEL}`);
  await page.getByTestId("session-cancel-open").click();
  const box = richText(page, "session-change-message");
  await expect(box).toBeVisible({ timeout: 20_000 });
  expect(await page.locator("style").count()).toBe(0);

  await box.click();
  await box.press("Control+A");
  await box.press("Delete");
  await pasteHtml(page, "session-change-message", GOOGLE_DOCS, "Google bold and plain One Two");
  await expect(box.locator("strong")).toHaveText("Google bold");
  await expect(box.locator("li")).toHaveCount(2);
  const html = await box.innerHTML();
  expect(html).not.toMatch(/style=|<span|<b |<div|class=/);
  expect(await page.getByTestId("session-change-message").locator("[style]").count()).toBe(0);
  // The editor of this profile has no heading or quote button.
  await expect(page.getByTestId("session-change-message").locator('button[data-action="h3"]')).toHaveCount(0);
  expect(await csp.all()).toEqual([]);

  seedSessions(); // the fixture goes back to how it was
});
