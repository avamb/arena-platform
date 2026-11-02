/** The summary and the reward row of "Refer a friend", rendered on the server in the three languages. */
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ReferralSummaryBlock, RewardRow } from "@/components/ReferralResults";
import type { ReferralRewardItem, ReferralSummary } from "@/lib/api/types";
import { I18nProvider, LOCALES } from "@/lib/i18n";

const render = (node: React.ReactNode, locale: (typeof LOCALES)[number] = "en") =>
  renderToStaticMarkup(<I18nProvider locale={locale}>{node}</I18nProvider>);

const zeroTotals = { paid_minor: 0, sent_minor: 0, due_minor: 0, not_possible_minor: 0, capped_minor: 0, failed_minor: 0, cancelled_minor: 0 };
const summary = (over: Partial<ReferralSummary> = {}): ReferralSummary => ({
  event_id: "e1",
  codes_issued: 12,
  friends: 5,
  friend_reversals: 0,
  discount_given: [],
  cashback: [],
  ...over,
});

const reward = (over: Partial<ReferralRewardItem> = {}): ReferralRewardItem => ({
  id: "r1",
  code: "ANNA-7K2Q",
  friend_order_number: 1000000501,
  friend_amount_minor: 4500,
  friend_currency: "EUR",
  referrer_order_number: 1000000400,
  cashback_minor: 900,
  currency: "EUR",
  cashback_pct: 20,
  state: "paid",
  reason: null,
  friend_reversal: null,
  created_at: "2026-11-02T10:00:00Z",
  updated_at: "2026-11-02T10:05:00Z",
  ...over,
});

describe("ReferralSummaryBlock", () => {
  it("prints each currency in its own block and never one sum of two", () => {
    const html = render(
      <ReferralSummaryBlock
        summary={summary({
          discount_given: [
            { currency: "EUR", amount_minor: 500 },
            { currency: "CZK", amount_minor: 20000 },
          ],
          cashback: [{ currency: "EUR", ...zeroTotals, paid_minor: 900, due_minor: 100 }],
        })}
      />,
    );
    expect(html.match(/data-testid="referral-currency"/g)).toHaveLength(2);
    expect(html).toContain('data-currency="EUR"');
    expect(html).toContain('data-currency="CZK"');
    expect(html).toContain("Cashback paid");
    expect(html).toContain("Personal codes issued: 12");
    expect(html).not.toContain("referral-reversals");
  });

  it("says plainly when there is no money yet and shows the reversals when there are some", () => {
    expect(render(<ReferralSummaryBlock summary={summary()} />)).toContain("referral-summary-empty");
    expect(render(<ReferralSummaryBlock summary={summary({ friend_reversals: 2 })} />)).toContain('data-testid="referral-reversals"');
  });

  it("has every text in all three languages", () => {
    for (const locale of LOCALES) {
      const html = render(<ReferralSummaryBlock summary={summary({ cashback: [{ currency: "EUR", ...zeroTotals, capped_minor: 5 }] })} />, locale);
      expect(html, locale).not.toMatch(/referral\.summary/);
    }
  });
});

describe("RewardRow", () => {
  it("shows the code, the friend's order, the state in words and no buyer contact", () => {
    const html = render(<ul><RewardRow r={reward()} zone="Europe/Madrid" /></ul>);
    expect(html).toContain("ANNA-7K2Q");
    expect(html).toContain("№ 1000000501");
    expect(html).toContain("Paid");
    expect(html).not.toContain("@");
    expect(html).not.toContain("referral-reward-reason");
    expect(html).not.toContain("referral-reward-reversal");
  });

  it("explains a reward that ended without money", () => {
    const html = render(<ul><RewardRow r={reward({ state: "not_possible", reason: "card_closed" })} zone={undefined} /></ul>);
    expect(html).toContain("Not possible");
    expect(html).toContain("could not go back to the buyer");
    expect(render(<ul><RewardRow r={reward({ state: "failed", reason: "some_provider_code" })} zone={undefined} /></ul>)).toContain("ended without a payout");
  });

  it("marks a friend who took the money back and says the cashback stays", () => {
    const html = render(<ul><RewardRow r={reward({ friend_reversal: "disputed" })} zone={undefined} /></ul>);
    expect(html).toContain("The friend opened a dispute");
    expect(html).toContain('data-testid="referral-reward-reversal-note"');
  });

  it("copes with an unknown friend order", () => {
    const html = render(<ul><RewardRow r={reward({ friend_order_number: null, friend_amount_minor: null, friend_currency: null })} zone={undefined} /></ul>);
    expect(html).toContain("Not known");
    expect(html).not.toContain("№ null");
  });

  it("speaks Russian and Spanish", () => {
    const ru = render(<ul><RewardRow r={reward({ state: "cancelled", reason: "self_referral" })} zone={undefined} /></ul>, "ru");
    expect(ru).toContain("Отменён");
    expect(ru).toContain("собственный код");
    const es = render(<ul><RewardRow r={reward()} zone={undefined} /></ul>, "es");
    expect(es).toContain("Pagado");
  });
});
