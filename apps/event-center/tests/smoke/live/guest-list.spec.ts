/**
 * Twin test "Guest list" on the three roles of one organization, against a
 * REAL arena-api (owner decision 2026-11-02, migration 0187). It walks the
 * scenario of the bot's e2e (httpserver/bot_e2e_guest_list_integration_test.go):
 * the date's guest list downloads as an Excel workbook of its active tickets
 * (and as a CSV for import, the secondary link), and the
 * person turns "send me the guest list before every session" on (on the date)
 * and off (on the Notifications page, where the bot keeps the same toggle).
 *
 * The truth about each role comes from the API (GET /v1/organizations/{org_id}/me).
 * Owner and manager hold order.read; the agent does not, so the date shows
 * neither the button nor the setting and the API itself refuses both.
 *
 * Data: the door-scans fixture (apps/backend/cmd/arena-e2e-seed/scans_fixture.go),
 * four active tickets of one date, one of them an invitation.
 */
import { readFile } from "node:fs/promises";
import { expect, test, type APIRequestContext } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";
import { xlsxPart } from "./xlsxHelpers";

const env = liveEnv();
const EVENT = "fe0000a2-0000-7000-8000-000000000002";
const SESSION = "fe0000a2-0000-7000-8000-000000000003";

const ROLES: { role: Role; label: string }[] = [
  { role: "org_admin", label: "owner" },
  { role: "organizer", label: "manager" },
  { role: "agent", label: "agent" },
];

const orgUrl = (path: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${path}`;
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

async function permissionsOf(request: APIRequestContext, role: Role): Promise<string[]> {
  const res = await request.get(orgUrl("/me"), { headers: auth(role) });
  expect(res.status(), `GET /me as ${role}`).toBe(200);
  return ((await res.json()) as { permissions: string[] }).permissions;
}

async function subscriptionOf(request: APIRequestContext, role: Role) {
  const res = await request.get(orgUrl("/guest-list-subscription"), { headers: auth(role) });
  expect(res.status(), `GET guest-list-subscription as ${role}`).toBe(200);
  return (await res.json()) as { enabled: boolean; channel: string };
}

test.describe("guest list on the three roles (live)", () => {
  for (const { role, label } of ROLES) {
    test(`${label} (${role}) gets the guest list only with order.read`, async ({ page, request }) => {
      const may = (await permissionsOf(request, role)).includes("order.read");

      if (!may) {
        const csv = await request.get(orgUrl(`/sessions/${SESSION}/guest-list.csv`), { headers: auth(role) });
        expect(csv.status(), "the API refuses the file without order.read").toBe(403);
        const xlsx = await request.get(orgUrl(`/sessions/${SESSION}/guest-list.xlsx`), { headers: auth(role) });
        expect(xlsx.status(), "the API refuses the workbook without order.read").toBe(403);
        const sub = await request.get(orgUrl("/guest-list-subscription"), { headers: auth(role) });
        expect(sub.status(), "the API refuses the setting without order.read").toBe(403);
        await signInAs(page, person(env, role));
        await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
        await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);
        await expect(page.getByTestId("csv-session-guest-list")).toHaveCount(0);
        await expect(page.getByTestId("xlsx-session-guest-list")).toHaveCount(0);
        await expect(page.getByTestId("guest-list-subscription")).toHaveCount(0);
        await page.goto(`/o/${env.org_id}/notifications`);
        await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
        await expect(page.getByTestId("guest-list-subscription")).toHaveCount(0);
        await noA11yViolations(page);
        return;
      }

      // Start from "off", whatever an earlier run left.
      const reset = await request.put(orgUrl("/guest-list-subscription"), {
        headers: { ...auth(role), "Content-Type": "application/json" },
        data: { enabled: false },
      });
      expect(reset.status()).toBe(200);

      await signInAs(page, person(env, role));
      await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
      await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);

      // ── the workbook (the main button) ─────────────────────────────────
      const [book] = await Promise.all([
        page.waitForEvent("download"),
        page.getByTestId("xlsx-session-guest-list").click(),
      ]);
      // The event's name, never an id, and the date of the session.
      expect(book.suggestedFilename()).toMatch(/^guests_[a-z0-9-]+_\d{4}-\d{2}-\d{2}\.xlsx$/);
      expect(book.suggestedFilename()).not.toContain(EVENT);
      const sheet = xlsxPart(await readFile(await book.path()), "xl/worksheets/sheet1.xml");
      expect(sheet).toContain('<row r="5">'); // the header and the four active tickets
      expect(sheet).not.toContain('<row r="6">');
      // Columns (2026-11-08): last and first name, phone, e-mail, category,
      // row, seat, status, reason, barcode (J), order number, sector,
      // ticket (M), VIP, entry time, invitation. Barcode and ticket cells are
      // strings in the text format, and the columns themselves are text, so
      // Excel never shows 4.6E+12.
      for (const row of [2, 3, 4, 5]) {
        expect(sheet).toMatch(new RegExp(`<c r="J${row}" s="2" t="inlineStr"><is><t>[0-9]{13}</t>`));
        expect(sheet).toMatch(new RegExp(`<c r="M${row}" s="2" t="inlineStr">`));
      }
      expect(sheet).toContain('<col min="10" max="10" width="');
      expect(sheet).toMatch(/<col min="10" max="10" width="\d+" customWidth="1" style="2"\/>/);
      expect(sheet).toContain('state="frozen"');
      expect(sheet).toContain('<autoFilter ref="A1:P5"/>');

      // ── the CSV for import (the secondary link) ────────────────────────
      const [download] = await Promise.all([
        page.waitForEvent("download"),
        page.getByTestId("csv-session-guest-list").click(),
      ]);
      expect(download.suggestedFilename()).toMatch(/^guests_.*\.csv$/i);
      const path = await download.path();
      const text = (await readFile(path, "utf8")).replace(/^﻿/, "");
      const lines = text.trimEnd().split("\r\n");
      expect(lines[0]).toBe(
        '"Last name";"First name";"Phone";"E-mail";"Category";"Row";"Seat";"Ticket status";"Status reason";"Barcode";"Order number";"Sector";"Ticket";"VIP";"Entry time";"Invitation"',
      );
      expect(lines).toHaveLength(5); // the header and the four tickets
      expect(lines.filter((l) => l.endsWith('"yes"'))).toHaveLength(1); // one invitation

      // ── the setting ────────────────────────────────────────────────────
      const box = page.getByTestId("guest-list-subscription");
      const toggle = box.getByTestId("guest-list-toggle");
      await expect(toggle).not.toBeChecked();
      await toggle.check();
      await expect(box.getByTestId("guest-list-channel")).toBeVisible();
      await expect.poll(async () => (await subscriptionOf(request, role)).enabled).toBe(true);

      // The same setting on the Notifications page (the twin of the bot's
      // Notifications screen): it is on there too, and turns off from there.
      await page.goto(`/o/${env.org_id}/notifications`);
      const notif = page.getByTestId("guest-list-subscription");
      await expect(notif.getByTestId("guest-list-toggle")).toBeChecked();
      await notif.getByTestId("guest-list-toggle").uncheck();
      await expect(notif.getByTestId("guest-list-channel")).toHaveCount(0);
      await expect.poll(async () => (await subscriptionOf(request, role)).enabled).toBe(false);

      const width = page.viewportSize()?.width ?? 375;
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
      await noA11yViolations(page);
    });
  }
});
