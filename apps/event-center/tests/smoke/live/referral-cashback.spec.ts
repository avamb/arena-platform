/**
 * Twin test "Refer a friend cashback in the sales reports" (spec 52, owner
 * decision 2026-11-03) on the three roles of one organization, against a REAL
 * arena-api. It walks the bot's case (TestReferralCashback_InSalesReports and
 * eventbot's TestMoneyBlock_ShowsReferralCashback / TestReferralLines_OrderCard):
 * the cashback paid to a referrer is its own line on the session and event
 * figures and comes off the net, the refunds to buyers stay without it, the
 * referrer's order card names the friend's order the cashback came from with
 * its date, and the friend's order card names the referrer's code.
 *
 * Data: apps/backend/cmd/arena-e2e-seed/referral_cashback_fixture.go (fixed ids,
 * repeated below; read-only here, so no reset). The stand's case: 454,50 +
 * 252,50 paid, 207,05 refunded to the friend, 45,45 cashback to the referrer,
 * 454,50 kept.
 *
 * Owner and manager hold order.read; the agent holds none, so no figures are
 * drawn for them and the API refuses the summary.
 */
import { expect, test } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs, type Role } from "./liveEnv";

const env = liveEnv();
const EVENT = "fe0000e2-0000-7000-8000-00000000c501";
const SESSION = "fe0000e2-0000-7000-8000-00000000c502";
const REFERRER_ORDER = "fe0000e2-0000-7000-8000-00000000c511";
const FRIEND_ORDER = "fe0000e2-0000-7000-8000-00000000c521";
const CODE = "E2E-ANNA-7K2Q";

const orgUrl = (path: string) => `${env.api_base_url}/v1/organizations/${env.org_id}${path}`;
const auth = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

interface Money {
  currency: string;
  paid: number;
  refunded: number;
  cashback: number;
  cashback_refunds: number;
  net: number;
}

for (const { role, label } of [
  { role: "org_admin" as Role, label: "owner" },
  { role: "organizer" as Role, label: "manager" },
]) {
  test(`${label} (${role}) sees the cashback on its own line and in the net`, async ({ page, request }) => {
    for (const path of [`/sessions/${SESSION}/summary`, `/events/${EVENT}/summary`]) {
      const res = await request.get(orgUrl(path), { headers: auth(role) });
      expect(res.status(), `GET ${path} as ${role}`).toBe(200);
      const body = (await res.json()) as { money: Money[]; refunds: { amount: number }[] };
      const eur = body.money.find((m) => m.currency === "EUR");
      expect(eur, `${path}: EUR money`).toMatchObject({ paid: 70700, refunded: 20705, cashback: 4545, cashback_refunds: 1, net: 45450 });
      expect(body.refunds.reduce((s, r) => s + r.amount, 0), `${path}: refunds to buyers only`).toBe(20705);
    }

    await signInAs(page, person(env, role));
    await expect(page).not.toHaveURL(/\/login/);
    await page.goto(`/o/${env.org_id}/events/${EVENT}/sessions/${SESSION}`);
    const figures = page.getByTestId("sales-figures").first();
    await expect(figures).toContainText(/Refer a friend cashback: 1 for \D*45\.45/);
    await expect(figures).toContainText(/Net: \D*454\.50/);

    // The referrer's card: the cashback with its date and the friend's order.
    await page.goto(`/o/${env.org_id}/orders/${REFERRER_ORDER}`);
    const cashback = page.getByTestId("order-referral-cashback");
    await expect(cashback).toHaveCount(1);
    await expect(cashback).toContainText(/Cashback \D*45\.45 paid/);
    await noA11yViolations(page);
    await cashback.getByRole("link").click();

    // The friend's card: bought with the referrer's code.
    await expect(page).toHaveURL(new RegExp(`/orders/${FRIEND_ORDER}$`));
    await expect(page.getByTestId("order-referral-code")).toContainText(CODE);
    await expect(page.getByTestId("order-referral-cashback")).toHaveCount(0);
  });
}

test("agent (agent) is shown no figures and is refused by the API", async ({ request }) => {
  const res = await request.get(orgUrl(`/sessions/${SESSION}/summary`), { headers: auth("agent") });
  expect(res.status()).toBe(403);
});
