/**
 * Twin test "Billing → Balance and packs / Invoices" (tasks B3-3 and B4), on
 * the three roles of one organization, against a REAL arena-api.
 *
 * The harness has NO platform Stripe key, so card payment is unavailable
 * there. The twin therefore proves the bank transfer end to end and the
 * honest refusal of the card:
 *
 *  - the owner sees a balance of 0 and the EUR packs of the standard price
 *    list, with no card fee on offer;
 *  - the commission helper shows the same number as the API;
 *  - the owner orders the smallest pack by bank transfer: a double click sends
 *    ONE request, the issued invoice shows its number, amount and an
 *    ARN-XXXX-XXXX reference, and it is in the invoices list (details, state
 *    filter);
 *  - the same Idempotency-Key sent again answers the same invoice (200) and
 *    creates no second one;
 *  - the card option says it is not available, and the API answers 503 for it;
 *  - the owner can save the payer details;
 *  - the manager and the agent have no balance or invoices page content, send
 *    no billing request from it, and the API refuses every one of those
 *    routes with 403.
 */
import { expect, test, type Page } from "@playwright/test";
import { liveEnv, noA11yViolations, person, signInAs as signInRaw, type Role } from "./liveEnv";

const env = liveEnv();
const balanceUrl = `/o/${env.org_id}/billing/balance`;
const invoicesUrl = `/o/${env.org_id}/billing/invoices`;
const apiBase = `${env.api_base_url}/v1/organizations/${env.org_id}/billing`;
const REFERENCE = /^ARN-[A-Z0-9]{4}-[A-Z0-9]{4}$/;

async function signInAs(page: Page, role: Role) {
  await signInRaw(page, person(env, role));
  await expect(page).toHaveURL(new RegExp(`/o/${env.org_id}/events`));
}

const authHeaders = (role: Role) => ({ Authorization: `Bearer ${person(env, role).access_token}` });

const euro = (minor: number) => new Intl.NumberFormat("en-GB", { style: "currency", currency: "EUR" }).format(minor / 100);

test.describe("billing balance, packs and invoices", () => {
  test("owner: balance 0, the EUR packs, no card fee, the commission helper equals the API", async ({ page, request }) => {
    await signInAs(page, "org_admin");
    await page.getByTestId("nav-billing").click();
    await page.getByTestId("billing-nav-balance").click();
    await expect(page).toHaveURL(new RegExp(`${balanceUrl}$`));

    const balance = page.getByTestId("billing-balance-EUR");
    await expect(balance.getByTestId("billing-balance-total")).toHaveText("€0.00");
    await expect(balance.getByTestId("billing-balance-purchased")).toContainText("€0.00");
    await expect(balance.getByTestId("billing-balance-bonus")).toContainText("€0.00");
    await expect(balance.getByTestId("billing-balance-rate-none")).toBeVisible();

    // The standard price list: seven tiers, cheapest first, in EUR only.
    const packs = page.getByTestId("billing-packs-EUR").getByTestId("billing-pack");
    await expect(packs.first().getByTestId("billing-pack-face")).toHaveText("€16.50");
    await expect(packs.last().getByTestId("billing-pack-face")).toHaveText("€1,900.00");
    expect(await packs.count()).toBeGreaterThanOrEqual(7);
    // No platform Stripe key here: no pack offers a card fee.
    await expect(page.getByTestId("billing-pack-card").first()).toContainText("Card payment is not set up yet");
    await expect(page.getByText("payment system fee of")).toHaveCount(0);
    await noA11yViolations(page);

    // The helper shows the commission per ticket, the API's own number.
    for (const seated of [false, true]) {
      const apiRes = await request.get(`${apiBase}/service-fee-quote?currency=EUR&price_minor=2000&seated=${seated}`, {
        headers: authHeaders("org_admin"),
      });
      expect(apiRes.status()).toBe(200);
      const quote = (await apiRes.json()) as { service_fee_minor: number };
      const box = page.getByTestId("billing-fee");
      await box.getByTestId("billing-fee-price").fill("20");
      if (seated) await box.getByTestId("billing-fee-seated").check();
      await box.getByTestId("billing-fee-submit").click();
      await expect(box.getByTestId("billing-fee-amount")).toHaveText(`Commission per ticket: ${euro(quote.service_fee_minor)}`);
    }

    // A price that is not a number is explained, and no request is sent.
    const feeCalls: string[] = [];
    page.on("request", (req) => {
      if (req.url().includes("/service-fee-quote")) feeCalls.push(req.url());
    });
    await page.getByTestId("billing-fee-price").fill("twenty");
    await page.getByTestId("billing-fee-submit").click();
    await expect(page.getByTestId("billing-fee-price-error")).toContainText("Enter the price as a number");
    expect(feeCalls).toEqual([]);
  });

  test("owner: orders a pack by bank transfer, one request on a double click, the same key makes no second invoice", async ({
    page,
    request,
  }) => {
    const listBefore = await request.get(`${apiBase}/account-invoices?limit=100`, { headers: authHeaders("org_admin") });
    expect(listBefore.status()).toBe(200);
    const before = ((await listBefore.json()) as { invoices: { id: string }[] }).invoices.length;

    await signInAs(page, "org_admin");
    await page.goto(balanceUrl);
    await expect(page.getByTestId("billing-balance-EUR")).toBeVisible();

    const orderRequests: { key: string | undefined; body: string | null }[] = [];
    page.on("request", (req) => {
      if (req.method() === "POST" && /\/billing\/packs\/[^/]+\/order$/.test(req.url())) {
        orderRequests.push({ key: req.headers()["idempotency-key"], body: req.postData() });
      }
    });

    await page.getByTestId("billing-pack-buy").first().click();
    const panel = page.getByTestId("billing-order");
    await expect(panel).toBeVisible();
    await expect(panel.getByTestId("billing-method-bank")).toBeChecked();
    // The card says it is not available here, and cannot be chosen.
    await expect(panel.getByTestId("billing-method-card")).toBeDisabled();
    await expect(panel.getByTestId("billing-method-card-hint")).toContainText("Card payment is not available now");
    await expect(panel.getByTestId("billing-order-total")).toContainText("€16.50");
    await expect(panel).toContainText("Credits appear only after the payment is confirmed");

    const answer = page.waitForResponse((r) => r.request().method() === "POST" && /\/billing\/packs\/[^/]+\/order$/.test(r.url()));
    await panel.getByTestId("billing-order-submit").dblclick();
    const response = await answer;
    expect(response.status()).toBe(201);
    const invoice = (await response.json()) as {
      id: string;
      number: string;
      payment_reference: string;
      total_minor: number;
      state: string;
      payment_method: string;
    };
    expect(orderRequests, "a double click is ONE order").toHaveLength(1);
    expect(orderRequests[0].key).toMatch(/^web-pack-/);
    expect(JSON.parse(orderRequests[0].body ?? "{}")).toEqual({ payment_method: "bank_transfer" });
    expect(invoice.state).toBe("issued");
    expect(invoice.payment_method).toBe("bank_transfer");
    expect(invoice.payment_reference).toMatch(REFERENCE);

    // What the owner sees: the invoice, the amount and the reference to write in the transfer.
    const issued = page.getByTestId("billing-issued");
    await expect(issued).toContainText(`Invoice ${invoice.number} is ready`);
    await expect(issued.getByTestId("billing-issued-amount")).toContainText(euro(invoice.total_minor));
    await expect(issued.getByTestId("payment-reference")).toHaveText(invoice.payment_reference);
    await expect(issued.getByTestId("billing-issued-instruction")).toContainText("Write this reference in the details of your transfer");
    await expect(issued).toContainText("Credits appear only after the payment is confirmed");
    // No credits yet: the balance is still 0 until the payment is confirmed.
    await expect(page.getByTestId("billing-balance-EUR").getByTestId("billing-balance-total")).toHaveText("€0.00");

    // The same key again: the first invoice, no second one.
    const replay = await request.post(`${apiBase}/packs/${await firstPackId(request)}/order`, {
      headers: { ...authHeaders("org_admin"), "Idempotency-Key": orderRequests[0].key as string },
      data: { payment_method: "bank_transfer" },
    });
    expect(replay.status()).toBe(200);
    expect(((await replay.json()) as { id: string }).id).toBe(invoice.id);
    const listAfter = await request.get(`${apiBase}/account-invoices?limit=100`, { headers: authHeaders("org_admin") });
    const after = ((await listAfter.json()) as { invoices: { id: string }[] }).invoices;
    expect(after.length).toBe(before + 1);
    expect(after.filter((i) => i.id === invoice.id)).toHaveLength(1);

    // The invoice is in the list, waiting for payment, with its lines and the reference.
    await page.getByTestId("billing-issued-invoices").click();
    await expect(page).toHaveURL(new RegExp(`${invoicesUrl}$`));
    const card = page.locator(`[data-testid="billing-invoice"][data-invoice-id="${invoice.id}"]`);
    await expect(card).toBeVisible();
    await expect(card).toContainText(`Invoice ${invoice.number}`);
    await expect(card).toContainText("Waiting for payment");
    await expect(card.getByTestId("payment-reference")).toHaveText(invoice.payment_reference);
    await expect(card.getByTestId("billing-invoice-pay")).toHaveCount(0);
    await card.getByTestId("billing-invoice-toggle").click();
    await expect(card.getByTestId("billing-invoice-lines")).toContainText("Credit pack");
    await expect(card.getByTestId("billing-invoice-detail")).toContainText(euro(invoice.total_minor));

    // The state filter: waiting shows it, paid does not.
    await page.getByTestId("billing-invoices-filter").selectOption("issued");
    await expect(card).toBeVisible();
    await page.getByTestId("billing-invoices-filter").selectOption("paid");
    await expect(card).toHaveCount(0);
    await page.getByTestId("billing-invoices-filter").selectOption("");
    await expect(card).toBeVisible();

    // Nothing was paid, so the journal has nothing to say.
    await expect(page.getByTestId("billing-ledger")).toContainText("No credit changes yet");
    await noA11yViolations(page);
  });

  test("owner: card payment is refused honestly, and a missing key is refused", async ({ request }) => {
    const packId = await firstPackId(request);
    const card = await request.post(`${apiBase}/packs/${packId}/order`, {
      headers: { ...authHeaders("org_admin"), "Idempotency-Key": `twin-card-${Date.now()}` },
      data: { payment_method: "stripe_card" },
    });
    expect(card.status()).toBe(503);
    expect(((await card.json()) as { error: { code: string } }).error.code).toBe("billing.card_payment_unavailable");

    const noKey = await request.post(`${apiBase}/packs/${packId}/order`, {
      headers: authHeaders("org_admin"),
      data: { payment_method: "bank_transfer" },
    });
    expect(noKey.status()).toBe(400);
    expect(((await noKey.json()) as { error: { code: string } }).error.code).toBe("billing.idempotency_key_required");

    const unknownPack = await request.post(`${apiBase}/packs/00000000-0000-4000-8000-00000000dead/order`, {
      headers: { ...authHeaders("org_admin"), "Idempotency-Key": `twin-gone-${Date.now()}` },
      data: { payment_method: "bank_transfer" },
    });
    expect(unknownPack.status()).toBe(404);
  });

  test("owner: the payer details are checked, then saved", async ({ page }) => {
    await signInAs(page, "org_admin");
    await page.goto(balanceUrl);
    const form = page.getByTestId("billing-payer");
    await expect(form).toBeVisible();

    await form.getByTestId("billing-payer-name").fill("");
    await form.getByTestId("billing-payer-save").click();
    await expect(form.getByTestId("billing-payer-name-error")).toContainText("Enter the legal name of the payer");

    await form.getByTestId("billing-payer-name").fill("Twin Test Payer S.L.");
    await form.getByTestId("billing-payer-country").fill("1");
    await form.getByTestId("billing-payer-save").click();
    await expect(form.getByTestId("billing-payer-country-error")).toBeVisible();

    await form.getByTestId("billing-payer-country").fill("es");
    await form.getByTestId("billing-payer-tax").fill("B12345678");
    await form.getByTestId("billing-payer-email").fill("billing@twin.test");
    const saved = page.waitForResponse((r) => r.request().method() === "PUT" && r.url().endsWith("/billing/payer"));
    await form.getByTestId("billing-payer-save").click();
    const res = await saved;
    expect(res.status()).toBe(200);
    expect(JSON.parse(res.request().postData() ?? "{}")).toMatchObject({ legal_name: "Twin Test Payer S.L.", country: "ES", tax_id: "B12345678" });
    await expect(form.getByTestId("billing-payer-saved")).toBeVisible();
  });

  test("owner: the Stripe return address says confirming until the invoice itself says paid", async ({ page, request }) => {
    // An issued invoice from this run's order: a return with paid=1 must NOT claim it is paid.
    const list = await request.get(`${apiBase}/account-invoices?state=issued&limit=1`, { headers: authHeaders("org_admin") });
    const first = ((await list.json()) as { invoices: { id: string }[] }).invoices[0];
    test.skip(first === undefined, "needs an issued invoice, which the order test creates");
    await signInAs(page, "org_admin");
    await page.goto(`${balanceUrl}?invoice=${first.id}&paid=1`);
    const note = page.getByTestId("billing-return");
    await expect(note).toBeVisible();
    await expect(note).toHaveAttribute("data-status", "confirming");
    await expect(note.getByTestId("billing-return-text")).toContainText("We are confirming your payment");
    await expect(note.getByTestId("billing-return-text")).not.toContainText("is paid");

    await page.goto(`${balanceUrl}?invoice=${first.id}&cancelled=1`);
    const canceled = page.getByTestId("billing-return");
    await expect(canceled).toHaveAttribute("data-status", "cancelled");
    await expect(canceled.getByTestId("billing-return-text")).toContainText("nothing was charged");

    // An address that is not a return shows no note.
    await page.goto(`${balanceUrl}?invoice=not-an-id&paid=1`);
    await expect(page.getByTestId("billing-balance-EUR")).toBeVisible();
    await expect(page.getByTestId("billing-return")).toHaveCount(0);
  });

  for (const role of ["organizer", "agent"] as const) {
    test(`${role}: no billing pages, an explanation, no billing request, and the API says 403`, async ({ page, request }) => {
      await signInAs(page, role);
      await expect(page.getByTestId("nav-billing")).toHaveCount(0);

      const billingCalls: string[] = [];
      page.on("request", (req) => {
        // The pages themselves are /o/<org>/billing/..., so look at API calls only.
        if (req.url().startsWith(env.api_base_url) && req.url().includes("/billing/")) billingCalls.push(`${req.method()} ${req.url()}`);
      });
      for (const url of [balanceUrl, invoicesUrl]) {
        await page.goto(url);
        await expect(page.getByTestId("billing-no-access")).toBeVisible();
      }
      await expect(page.getByTestId("billing-packs")).toHaveCount(0);
      await expect(page.getByTestId("billing-pack-buy")).toHaveCount(0);
      await expect(page.getByTestId("billing-payer")).toHaveCount(0);
      expect(billingCalls, "a person without billing.read must not send a billing request").toEqual([]);

      const packId = "00000000-0000-4000-8000-0000000000aa";
      const invoiceId = "00000000-0000-4000-8000-0000000000bb";
      const headers = authHeaders(role);
      const reads = ["balance", "packs", "account-invoices", `account-invoices/${invoiceId}`, "ledger", "service-fee-quote?currency=EUR&price_minor=2000&seated=false"];
      for (const path of reads) {
        const res = await request.get(`${apiBase}/${path}`, { headers });
        expect(res.status(), `GET ${path} as ${role}`).toBe(403);
      }
      const order = await request.post(`${apiBase}/packs/${packId}/order`, {
        headers: { ...headers, "Idempotency-Key": "twin-denied" },
        data: { payment_method: "bank_transfer" },
      });
      expect(order.status(), `order as ${role}`).toBe(403);
      const card = await request.post(`${apiBase}/account-invoices/${invoiceId}/pay-by-card`, { headers });
      expect(card.status(), `pay-by-card as ${role}`).toBe(403);
      const payer = await request.put(`${apiBase}/payer`, { headers, data: { legal_name: "Nope" } });
      expect(payer.status(), `payer as ${role}`).toBe(403);
    });
  }
});

/** The smallest pack of the owner's list, read through the API. */
async function firstPackId(request: import("@playwright/test").APIRequestContext): Promise<string> {
  const res = await request.get(`${apiBase}/packs`, { headers: authHeaders("org_admin") });
  expect(res.status()).toBe(200);
  const packs = ((await res.json()) as { packs: { id: string; face_minor: number }[] }).packs;
  return [...packs].sort((a, b) => a.face_minor - b.face_minor)[0].id;
}
