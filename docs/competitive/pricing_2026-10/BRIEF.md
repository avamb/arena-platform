# Ticketing platform pricing research — collector brief (2026-10-09)

You are one of several collectors. Each collector covers one group of platforms.
The coordinator merges all groups into one comparison table, computes the
scenarios and writes the report. Your job is FACTS with SOURCES, not opinions.

## Why

Arena (arenasoldout.com) is a ticketing platform for small and mid-size
organizers (theatres, master classes, festivals, wine evenings, diaspora tours,
seated halls of 15–2 000 places) in Europe, Israel and beyond. Organizers
connect their OWN payment account (Stripe, Flitt, AllPay), so card processing
is paid by the organizer to their provider; Arena charges a platform fee.
We are about to set our own prices and need the full picture of what the
market charges — including everything hidden behind asterisks.

## What to capture for EVERY platform (dig deep)

Read the pricing page AND the fee FAQ / help-centre article AND, where they
exist, the terms-of-service fee section and any "fees explained" blog post.
The headline price is the least interesting part. Capture:

1. **Model**: flat per ticket / % per ticket / % + fixed / monthly or annual
   subscription / prepaid credits / per-event fee / free (donation or
   buyer-tip model) / revenue share negotiated / mixed.
2. **Every plan or tier**: name, price per month and per year, what it
   unlocks, per-ticket fee inside that plan.
3. **Per-ticket fee**: percent, fixed part, minimum, maximum cap, whether it
   differs by ticket price band, and the currency.
4. **Prepaid vs pay-as-you-go**: credit bundle sizes and their unit prices,
   expiry of credits, refundability of unused credits.
5. **Payment processing**: included in the fee or extra? Which processors;
   their rate; can the organizer bring their own processor (Stripe, PayPal,
   Square…) and does the fee change then; platform "payment processing fee"
   on top of the processor's.
6. **Who pays by default**: buyer (booking fee added on top) or organizer
   (absorbed); can it be switched; can it be split.
7. **Free tickets / free events**: free, capped (e.g. "free up to N per
   year"), or charged.
8. **Seating**: surcharge for reserved seating / seating charts / seat maps,
   setup fee for a chart, per-seat price.
9. **Payouts**: when (before event / after event / weekly), payout fees,
   reserve or rolling hold, minimum payout, currency conversion fee.
10. **Refunds and chargebacks**: is the platform fee refunded when a ticket
    is refunded; refund handling fee; chargeback fee.
11. **Tax**: are prices shown with or without VAT/GST/sales tax; is VAT
    charged on the platform fee.
12. **Fine print — the asterisks**: every `*`, `†`, footnote, "from",
    "starting at", "up to", minimum spend, setup/onboarding fee, contract
    length or lock-in, volume discounts, charity/non-profit discount,
    discount for cheap tickets, fees for SMS, printed tickets, box office /
    POS, scanner app, hardware rental, white-label, custom domain, API
    access, extra users, email marketing, add-ons, "premium" features,
    currency conversion, dormant account fee, cancellation fee.
13. **Regional differences**: different price per country/currency (record
    each one you find), countries where the product is not offered.
14. **"Contact sales" / enterprise**: say so explicitly when the price is not
    public; do NOT invent a number. If a credible third-party source (G2,
    Capterra, a comparison article, a public tender, a news article) quotes a
    number, record it with that source and mark it `third_party`.
15. **Money flow — the most important classification.** For every
    platform and market record `money_flow`:
    - `direct_merchant` — buyer's money goes straight into the ORGANIZER's
      own payment account (organizer connects their Stripe/PayPal/Square/
      local acquirer; the platform takes its fee by invoice, prepaid credits,
      subscription or an application fee split). Arena works this way.
    - `collecting` — the PLATFORM collects the money as merchant of record
      (or into its own account) and pays the organizer out later.
    - `both` — the organizer can choose; say which is the default and
      whether the price differs between the two.
    Also record how the platform charges its own fee in the direct-merchant
    case (invoice, card on file, credits, application fee on each payment)
    and, for collecting platforms, payout timing and any reserve held.
16. **Target segment** in one line (who they are for) and anything else
    notable about pricing strategy (e.g. "free for organizers, buyer pays",
    "fee capped per order", "price beats X guarantee").

## Rules

- Every number needs a `source_url` and the date you read it (2026-10-09).
  If a number comes only from a search snippet and you could not open the
  page, mark `confidence: "snippet"`.
- Keep the original currency; do not convert. The coordinator converts.
- Prefer the platform's own pages. Use the local-language page for local
  platforms (Czech, Polish, Spanish, Hebrew, Ukrainian, Kazakh/Russian,
  Portuguese, etc.) and translate the facts into English in your output.
- If a site has a country selector, check the country variants that matter
  for your group and record each.
- Do not log in, do not create accounts, do not fill forms, do not request
  quotes, do not contact anyone. Public pages only.
- Do not copy long passages; paraphrase. Short quotes (< 15 words) only
  where the exact wording of a fine-print condition matters.

## Tools

Load the Bright Data tools first with ToolSearch:
`select:mcp__bright-data__search_engine,mcp__bright-data__scrape_as_markdown,mcp__bright-data__search_engine_batch,mcp__bright-data__scrape_batch`

- `scrape_as_markdown` opens pages behind bot protection; it is the main tool.
- `search_engine` quirks seen today: a query in double quotes, or with
  `geo_location`, sometimes fails with "non-JSON response"; a multi-word
  query was once read as a single word. Use plain short queries without
  quotes (e.g. `tickettailor pricing`, `ticketportal poplatky poradatel`),
  and check the results are on topic. Retry with a rephrased query on error.
- Fallback when Bright Data fails: WebFetch / WebSearch (load with
  ToolSearch `select:WebFetch,WebSearch`).
- Budget: about 80 Bright Data calls for your whole group. Prefer
  `scrape_batch` / `search_engine_batch` for several URLs/queries at once.

## Output

Write ONE JSON file: `docs/competitive/pricing_2026-10/raw/<group_id>.json`
(path given in your task), UTF-8, an array of objects of this shape — use
`null` for unknown, never guess:

```json
{
  "platform": "Ticket Tailor",
  "website": "https://www.tickettailor.com",
  "hq_country": "GB",
  "segment": "self-serve, small/mid organizers, flat fee per ticket",
  "markets": [
    {
      "market": "UK",
      "currency": "GBP",
      "vat_note": "prices exclude VAT",
      "model": "flat_per_ticket | percent | percent_plus_fixed | subscription | prepaid_credits | per_event | free_buyer_pays | negotiated | mixed",
      "plans": [
        {"name": "Pay as you sell", "monthly_fee": null, "annual_fee": null,
         "per_ticket_percent": null, "per_ticket_fixed": 0.60,
         "min_per_ticket": null, "cap_per_ticket": null,
         "includes": "...", "notes": "..."}
      ],
      "prepaid": [{"bundle": "500 credits", "unit_price": 0.41, "expiry": "...", "refundable": null}],
      "processing": {"included": false, "processors": ["Stripe","PayPal","Square"],
                     "own_processor_allowed": true, "rate_note": "Stripe UK 1.5% + £0.20",
                     "platform_extra_processing_fee": null},
      "who_pays_default": "organizer | buyer | configurable",
      "free_tickets": "free up to 5000 free tickets/year, then …",
      "seating": "seated ticket uses +1 credit",
      "payouts": "...",
      "refunds_chargebacks": "...",
      "fine_print": ["50% discount for charities", "..."],
      "add_ons": [{"name": "box office app", "price": "..."}],
      "enterprise": "contact sales above X tickets/year",
      "sources": [{"url": "...", "read": "2026-10-09", "what": "pricing page"}],
      "confidence": "page | snippet | third_party"
    }
  ],
  "notes": "anything that does not fit above"
}
```

When done, reply to the coordinator with at most 250 words: how many
platforms you covered, which you could not get and why, and the 3–5 most
surprising pricing facts (with platform names). Do not paste the JSON.
