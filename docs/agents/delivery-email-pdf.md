# Ticket delivery, e-mail letters and PDF

Delivery jobs, the e-ticket PDF layout, barcodes, buyer letters, tracking, locale. Verbatim items of the former single AGENTS.md. Add a new trap here as a `- **Title.** ...` item and put one title line for it into the index in AGENTS.md.

- **Platform EAN-13 ticket barcodes are random, not sequential, and must
  stay unique across EVERY barcode authority, not just `platform`.**
  `ean13.Random()` draws the 10-digit body uniformly from `crypto/rand`
  (owner-approved "variant A" — the old `"21" + zeroPad10(system_ticket_id)
  + check` formula made neighbouring tickets' codes guessable and full of
  zeros). `ean13.PlatformCode` is now LEGACY-ONLY: a pure-function read-time
  fallback (`orderexport`, `hiam` export helpers) for a ticket that predates
  this change and has no stored credential — nothing mints through it
  anymore. Because the owner will later import already-sold tickets from
  two live clients into the `legacy_bil24` authority, and
  `GetBarcodeByExternalRefAny` (SCAN_TICKET) searches ALL authorities in
  one round-trip, a code must never collide across authorities either — the
  `barcodes` table's own UNIQUE constraint is scoped to `(authority_id,
  external_ref)` and cannot catch that alone. Both issuance
  (`htickets.IssueTicketsForCheckout`) and the backfill job
  (`barcodes/backfill`) mint through the shared
  `internal/platform/barcodes/mint.EAN13` helper: draw a candidate, try to
  atomically claim it via `gen.Queries.InsertBarcodeIfUnique` (`INSERT ...
  WHERE NOT EXISTS (SELECT 1 FROM barcodes WHERE external_ref = $2) ON
  CONFLICT (authority_id, external_ref) DO NOTHING RETURNING ...`), and
  redraw on a loss — up to `mint.MaxAttempts` (8) times. A losing attempt
  is zero rows / no error, NEVER a raw 23505, so it is safe to call inside
  the ticket-issuance transaction without a SAVEPOINT (AGENTS.md "best
  effort writes inside a money transaction" — there is nothing to roll
  back). The `ticket_credentials` row is only written AFTER the barcodes
  row is won. Any NEW code path that mints a platform EAN-13 must go
  through `mint.EAN13`, never call `ean13.Random`/`ean13.Encode` and
  `InsertBarcode` directly.

- **The buyer's language lives on `checkout_sessions.buyer_locale`, and that
  column is the ONLY thing that decides a ticket email's language.**
  The widget sends an optional `locale` on
  `POST /v1/public/feeds/{token}/checkout/start`;
  `hfeed.normalizeBuyerLocale` folds case and drops the region subtag
  (`cs-CZ` -> `cs`) and returns `""` for anything outside
  `templates.SupportedLocales` — an unknown tag is NEVER an error, because
  a cosmetic mismatch must not cost a sale, and `localePtr("")` stores
  NULL. Migration 0105 added the nullable column; since every `gen` query
  feeding `scanCheckoutSessionRow` must select it, a new checkout-session
  query has to append `buyer_locale` to `selectCheckoutSessionColumns` or
  the scan fails on a column count — `ListAllCheckoutSessions`
  (`superadmin.sql.go`) was already silently short one column before this.
  `htickets.EnqueueDeliveryJobs` reads it back through a per-call
  `buyerLocaleCache` (one query per distinct checkout session, not per
  ticket) and `EnqueueComplimentaryDeliveryJobs` /
  `HandleAdminResendTicketDelivery` through `BuyerLocaleForTicket` —
  a resend must arrive in the SAME language as the original. Bil24-gateway
  orders pass `nil` (`hbil24`'s `InsertCheckoutSessionWithToken`): the
  selling site owns that buyer's language, arena never learns it.
  `customers.ResolveInput.Locale` is applied on CREATE only —
  `customers.locale` is shared across every org that buyer ever bought
  from, so one purchase must not retitle their existing preference.
  Note the PDF still prints English labels regardless (see the gotcha
  above about `delivery/pdf`).

- **The ticket-delivery PDF now embeds a UTF-8 TrueType font — do not add a
  `pdf.SetFont("Helvetica", ...)` call back into
  `internal/platform/delivery/pdf/{layout,pdf}.go`.** Until 2026-09-19 every
  layout used gofpdf's built-in Core 14 Helvetica, which is WinAnsi/Latin-1
  only: Cyrillic, Czech diacritics, Greek and most Hebrew came out as
  mojibake (a real blocker for the Czech launch — Russian event names,
  "Příliš žluťoučký kůň"-style venue/address data). Fixed by embedding
  DejaVu Sans Condensed (Regular/Bold/Oblique) via `//go:embed` +
  `AddUTF8FontFromBytes` (`internal/platform/delivery/pdf/fonts.go`,
  constant `fontFamily`); the three TTFs plus `LICENSE-DejaVu.txt` are
  vendored verbatim from `github.com/jung-kurt/gofpdf@v1.16.2`'s own
  `font/` directory (Bitstream Vera license — permissive, redistribution
  allowed — so no network fetch needed, no new go.mod dependency). DejaVu
  covers Latin Extended, Cyrillic, Greek and the Hebrew base consonants
  (Aleph..Tav all present, verified by parsing the ttf's own `cmap` in
  `TestDejaVuSansCondensed_HasHebrewGlyphs`) — but gofpdf's `RTL()`/`LTR()`
  only reverses character order before left-to-right layout, it is NOT a
  bidi/shaping engine, so Hebrew renders correct glyphs in a naive
  right-to-left character order, not proper contextual shaping. The
  human-entry code (`drawHumanCode`) deliberately stays on the core
  Courier font — `humancode.Format` output is always Crockford-Base32
  ASCII, and Courier's fixed advance is what makes the manual per-glyph
  letter-spacing exact. **Once a layout calls `pdf.SetFont(fontFamily,
  ...)`, gofpdf switches that text's content-stream encoding from literal
  Latin-1 bytes to UTF-16BE (2 bytes/rune, no BOM) — a test asserting on
  raw PDF bytes must build its expected token through `pdfText()`
  (`pdf_testutil_test.go`), not a plain `[]byte("literal string")`**, or it
  silently stops matching (this is not a bug, the text just isn't
  single-byte-encoded anymore). Regression coverage lives in
  `render_i18n_test.go` — including the actual mojibake signature check
  (the raw UTF-8 bytes of a Cyrillic string must NOT appear literally in
  the uncompressed content stream). `Ticket.Locale` (`en`/`ru`/`cs`,
  default `en`) separately controls the PRINTED FIELD LABELS only
  (Session/Venue/Sector/Row/Seat/Holder/Ticket ID — see `labels.go`);
  content values are never translated. `delivery.Handler` threads
  `Payload.Locale` into `pdf.Ticket.Locale` in `renderTicketPDF`
  (`handler.go`) — this is independent of `delivery/templates`' own
  locale/fallback set (en/de/es/he) for the EMAIL BODY: the two locale
  mechanisms are not unified (different supported-locale lists, different
  fallback code — `labelsFor` here vs `Renderer.ResolveLocale` there), so
  don't assume a locale value valid for one is valid for the other.

- **The EAN-13 barcode on the e-ticket PDF is hand-drawn, not rasterized —
  `internal/platform/delivery/pdf/ean13_symbol.go`** (`ean13Pattern`/
  `ean13Bars` pure functions, `drawEAN13Symbol` draws them with
  `gofpdf.Rect(...,"F")`). Bars are pure black filled rectangles on white,
  never given a grey fill or turned into a raster image — a grey/AA'd bar
  is not reliably scannable by the venue's laser/handheld scanners. The
  quiet zones (`eanQuietLeftModules`/`eanQuietRightModules`, 11/7 modules)
  must stay completely untouched: no bar, no text, nothing drawn there —
  the human-readable leading digit is deliberately positioned to the LEFT
  of the quiet zone, not inside it. `drawEAN13Symbol` shrinks
  `eanBarH`/`eanGuardExtra` (never below `eanMinBarHeightPt`) or skips the
  symbol entirely — no empty placeholder box — when a page's other content
  leaves too little room before the footer; never "fix" a tight layout by
  drawing a barcode below that floor.

- **`drawDetails`'s label column width is computed per-render, not a fixed
  `layoutSpec.labelW`.** `layoutSpec.labelW` is only the format's baseline/
  minimum; `computeLabelWidth` (`layout.go`) measures the active locale's
  actual widest label (both detail-row and seat-row font sizes) and grows
  the column to fit, capped at `labelColumnMaxFraction` of the content
  width. Before this, a fixed `labelW` tuned for English overlapped the
  value on cs ("Místo konání:Divadlo…", first letter of the value lost
  under the wider Czech label) — any new label added to `ticketLabels`
  needs no manual width tuning, `computeLabelWidth` picks it up
  automatically.

- **`delivery_jobs` holds at most ONE row per ticket, so "enqueue another
  delivery" is `RequeueDeliveryJob`, never `InsertDeliveryJob`.**
  `InsertDeliveryJob`'s `ON CONFLICT (ticket_id) DO UPDATE SET ticket_id =
  EXCLUDED.ticket_id` is a deliberate no-op so a replayed payment webhook
  cannot mail the same ticket twice; `RequeueDeliveryJob` is the opposite
  intent and resets `status='pending'`, `attempts=0`, `last_error`,
  `sent_at`, `processing_at`, `queued_at`. `HandleAdminResendTicketDelivery`
  used the former, and since the worker's `ClaimDeliveryJobForProcessing`
  only ever transitions `pending` → `processing`, the admin "resend" button
  answered 202, wrote a `worker_jobs` row, and that job silently skipped the
  send for EVERY ticket it would ever be pressed on — they all already carry
  a terminal (`sent`/`failed`/`skipped`/`disabled`) or stuck `processing`
  row (found on prod 2026-09-20, first production events). A row stuck in
  `processing` is unclaimable forever, so any future "retry this delivery"
  path must requeue, not insert. Guarded by
  `TestAdminResendTicketDeliveryIntegration_ResetsTerminalJobToPending`.

- **`delivery.Payload`'s presentation fields are HINTS, resolved at render
  time — never re-add them to an enqueuer.** `EventName`, `SessionStart`,
  `SessionTZ`, `VenueName`, `VenueCity`, `TierName`, `HolderName` and
  `TicketNumber` were declared in feature #141 as "baked in at enqueue
  time"; no enqueuer ever set them
  (`htickets/delivery_enqueue.go` sends `{TicketID, Locale}` plus the seat
  fields, the complimentary path adds `Template`, the admin resend sends
  `{TicketID, Locale}`), so every live e-ticket printed the
  `defaultStr(p.EventName, "Arena Event")` placeholder with blank Venue /
  Category / Holder rows and a UTC session time — found on the first
  production client 2026-09-20. The worker now resolves them from the
  ticket's own rows in step 8c
  (`internal/platform/delivery/presentation.go` ->
  `gen.GetTicketPresentationByID`), beside the existing render-time
  resolutions of the recipient address (step 5) and the EAN-13 credential
  (step 8b). A hint that IS set still wins, so tests and any future caller
  keep control; resolution is best effort (a failed lookup logs and the
  ticket still ships). Holder name is `orders.buyer_name` falling back to
  `customers.display_name`; venue city is the `i18n_text` English name
  falling back to `cities.slug`, the same projection `orderexport` uses.
  Add new presentation data to that ONE query, not to three enqueuers.
  The same day, the four remaining fields the ported layout draws joined
  it: `OrderNumber` (`orders.system_id`, the footer's "Order <n>"),
  `VenueAddress` (`venues.address_line1` falling back to the legacy
  free-form `venues.address`), `PriceMinor`/`Currency` and
  `PosterMediaID` (`COALESCE(sessions.poster_media_id,
  events.poster_media_id)`, migration 0082's order). **Price is
  `order_items.total`, never `ticket_tiers.price_amount`** — what the
  buyer paid for THAT unit after its share of the discount and of the
  service charge; the tier column is only today's list price. A resolved
  0 is deliberately left UNSET, which is the invitation case
  (`complimentaryBreakdown` discounts the whole subtotal, so every item
  totals 0) and makes `pdf.priceValue` drop the cell instead of printing
  "0 EUR" on a gift. The poster's BYTES come from `resolvePoster`, which
  goes through the same `MediaResolver` as the org logo, bounds the fetch
  (5s, 8 MiB) and drops the artwork — never the e-mail — when it is
  missing, slow, oversized or not a format `pdf.SupportsImage` accepts.

- **The buyer never sees a ticket UUID.** The printed "Ticket ID" line, the
  PDF document title, the e-mail body and the attachment filename all carry
  `pdf.DisplayNumber(...)` — `tickets.system_ticket_id` (migration 0088),
  falling back to the first 8 hex digits of the UUID, uppercased, for a
  pre-0088 row. `pdf.Ticket.TicketID` is still required but is only an
  in-document image-resource map key, which gofpdf never writes to the
  output. A test asserting "no UUID in the PDF" must check BOTH the plain
  bytes and the UTF-16BE form (`utf16beEscaped`), and note that gofpdf's
  `SetTitle(..., true)` adds a `\xFE\xFF` BOM that page text does not have.

- **`arena-worker` is the ONLY process that constructs
  `delivery.NewHandler` — `httpserver` never does, despite owning the
  enqueuers.** Anything `delivery.HandlerOptions` needs must be wired in
  `cmd/arena-worker/main.go` or it degrades silently on every live ticket;
  there is no API-side reference wiring to copy. This is how
  `HandlerOptions.Media` stayed nil from feature #290 until 2026-09-20, so no
  organizer logo ever reached a real e-ticket. The media store is now built
  once in `run()` (`buildMediaRepo`, `SigningSecret: cfg.MediaSigningKey()` —
  it MUST match arena-api's key or the API rejects the URLs this worker
  signs) and shared by media-gc, customer.import and delivery;
  `internal/platform/delivery/mediaresolver` is the only implementation of
  `delivery.MediaResolver` and absolutizes the host-relative signed
  `/v1/media-files/{id}` path onto `cfg.APIPublicBaseURL()` exactly as
  `httpserver.Server.signedMediaURL` does (a relative URL is useless in an
  e-mail `<img src>`). Options go through `buildDeliveryHandlerOptions`, a
  seam whose unit test (`cmd/arena-worker/delivery_wiring_test.go`) is the
  regression guard — keep the registration going through it.

- **The e-ticket layout is a PORT — its spec lives outside this repo.**
  `internal/platform/delivery/pdf` reproduces the design the owner already
  ships from the WordPress sites: `bil24-ticket-mailer/templates/ticket.php`
  (the CSS and the millimetre geometry, with comments explaining WHY) and
  `includes/class-btm-renderer.php` (the string table, the localized
  month/weekday tables, the page-size rationale). Read those before changing
  a constant in `layout.go`. The three constraints they encode: the page is
  105x297 mm (half of A4 lengthwise, NOT A5 — one code block per phone
  screen, and it prints on A4 at 100%), the code block is absolutely
  anchored at 140 mm so nothing above can push it, and long values SHRINK
  their font (title 13.5→11.5pt past 55 runes, info values 10.5→9pt past 28)
  rather than reflow. Two deliberate arena departures: the QR carries the
  EAN-13 digits — the same value as the barcode, never the ticket UUID —
  and the accent colour is a `Ticket` field (`DefaultAccentColor`, the Arena
  Sold Out indigo) so Lampyris' yellow can be restored per organization.

- **gofpdf gotchas that cost time in that package.** (1) Byte-determinism
  breaks when a document holds two images of the SAME pixel width:
  `putimages` sorts image objects by width under `SetCatalogSort` and breaks
  the tie with Go's randomized map iteration. A fixture with a logo and a
  poster must give them different widths or the determinism tests are flaky
  — and it passes in isolation, so it looks like test pollution. (2) Cells
  carry a 1 mm margin by default; `SetCellMargin(0)` is required before any
  geometry ported from CSS lines up. (3) There is no character-spacing
  operator, so letter-spaced text (the uppercase info labels) is drawn one
  glyph per show operator and never appears as a single run in the content
  stream — assert on the glyphs, or on a value drawn with `MultiCell`. (4) A
  value that wraps is several runs, so a raw-bytes assertion on it must
  match a PREFIX (`pdfTextPrefix`), not the whole string — or, as
  `presentation_integration_test.go` now does, extract ALL of the page's
  text first (`pdfDrawnText` scans every `Td (<utf-16be>)Tj` run, joins
  them and collapses whitespace) and assert against that. A fixture value
  carrying a random nonce is exactly long enough to wrap, so the older
  whole-string byte assertions in that file were failing against a fresh
  database before this. (5) `ImageInfoType.i`
  — the `/I<id> Do` name — is a SHA-1 hex digest, not an index.

- **`cmd/arena-worker` builds the `ticket.deliver` handler with NO
  `MediaResolver`, so no organizer artwork reaches a production e-ticket
  yet.** `delivery.HandlerOptions.Media` is left nil there (main.go, next
  to `TicketQueries`/`Sender`), and `resolveBranding` / `resolvePoster`
  both degrade silently: the e-mail header falls back to
  `templates.PlatformLogoURL` and the PDF prints neither the org logo nor
  the event poster, whatever `organizations.logo_media_id` /
  `events.poster_media_id` say. The worker DOES build a `mediastore.Repo`
  already, but only inside the `MEDIA_BACKEND`-gated `registerMediaGC`
  helper and WITHOUT `SigningSecret`/`DownloadURLBase` — so wiring it into
  delivery also means giving the worker those two config values, or the
  e-mail's `<img src>` would come back empty. Until that lands, poster and
  logo support is proven by tests only.

- **A session has a sample e-ticket (migration 0119):
  `GET /v1/organizations/{org_id}/sessions/{session_id}/sample-ticket`**
  (`session.read`, `httpserver/hsample`) renders the buyer's real layout
  through `pdf.Render` with the new `Ticket.Watermark` field (a diagonal
  low-alpha stamp that never touches the anchored code block) and a REAL
  EAN-13 minted through `mint.EAN13` in the `sample` barcode authority
  (`barcode_authorities.type` CHECK widened; the REST enums for creating or
  scanning authorities deliberately do not list it) and linked once per
  session in `session_sample_barcodes`. SCAN_TICKET answers such a code
  `-2 bil24.sample_ticket` and never marks it scanned — the check is on an
  optional `barcodeAuthorityQuerier` extension of `ScanQuerier`, so the
  pre-#472 fakes skip it. The bot sends the PDF after "Done!" and on the
  event card's "Sample ticket" button (`sendSampleTicket`, Telegram
  `sendDocument`); the e2e stub records it as `[document <name>] <caption>`.

- **One ticket e-mail per ORDER, in the site design (2026-10-07).** `ticket.deliver` is still one worker job per ticket, but the handler claims the pending `delivery_jobs` of the same order AND recipient in ONE statement (`ClaimPendingDeliveryJobsForOrder`, `delivery/order_letter.go`): the first worker job to start owns the letter and attaches every ticket's PDF, the sibling jobs find nothing pending and skip with nil. A failed send hands ALL claimed jobs back to `pending` (`fail()` / `releaseLetterMembers`), never leave one in `processing`. Invitations are not grouped; an admin resend requeues ONE ticket and stays a single-ticket letter. The "Payment" block (`templates.PaymentData`, built from `checkout_sessions` money by `buildPaymentData`) is printed only for a paid, non-complimentary order when the letter carries ALL `TicketCount` tickets of the checkout, so a one-ticket resend never prints the whole order's total; the breakdown rows appear only when subtotal - discount + fee = total. `templates.PlatformLogoURL` is EMPTY on purpose: an organization without a logo gets its name in the header, never an `<img>` pointing at a placeholder that 404s. The accent colour is `Data.Accent` (default `DefaultAccentColor` #4f46e5). The 28 `{ticket,change,cancel,invitation}.<locale>.tmpl` files were generated from the bil24-ticket-mailer `email.php` design by a one-off script that READS the existing templates, so it is not idempotent: edit the `.tmpl` files by hand now, and keep all seven locales (he is RTL) in step. Tests: `templates_order_test.go`, and the live-DB `order_letter_integration_test.go` (needs `ordinal` on extra tickets of one checkout: `tickets_checkout_ordinal_uq`).

- **Every buyer letter is recorded in `email_letters`, and its status comes from Brevo's transactional webhook, never from "last event wins" (migration 0147, `internal/platform/emailstatus`, runbook `docs/ops/brevo_delivery_status_webhook_ru.md`).** arena sends over the Brevo SMTP relay, not the API. A new buyer-letter send must go through `delivery.sendRecorded` (`delivery/letter_status.go`): it writes the letter BEFORE the relay sees it (`emailstatus.Begin`, status `sending`, its id goes into `X-Mailin-custom: arena-letter:<id>`, which Brevo echoes on every event), then `Accepted` stores the relay's `250 … queued as <id>` (`email.SMTPSender.SendReceipt` reads the final DATA reply on the textproto connection because `net/smtp`'s `Data()` discards it) and links every `delivery_jobs` row of the letter (`email_letter_id`, `provider_message_id`), and a refused send deletes it (`Abandon`). Recording is best effort and never blocks a send. A failed QUIT after the 250 is no longer an error (it used to make the worker mail the letter twice). Status rules (`emailstatus.Derive`, a pure function of the SET of `email_letter_events` rows, one per letter and event type with first/last time): opened if any open; failed if a failure's last time is after the last sign of delivery (delivered, proxy_open, open) or there is none; delivered if delivered, proxy_open or a click; delayed if Brevo reported `deferred` (stored as `deferred_at`, a non-terminal step); else sent. `proxy_open` (Apple Mail Privacy Protection pre-loads images) is delivery, NEVER reading: it is `proxy_opened_at`, status delivered. The webhook (`httpserver/brevo_webhook.go`) has two routes: `POST /v1/webhooks/brevo` with `Authorization: Bearer <token>` (preferred: a webhook created through Brevo's `POST /v3/webhooks` with an `auth` object, the token never sits in a URL that Traefik or Cloudflare log) and `POST /v1/webhooks/brevo/{token}` for the web form. Both are mounted only with `BREVO_WEBHOOK_TOKEN`, accept `BREVO_WEBHOOK_TOKEN_PREVIOUS` too during a rotation (production refuses one under 32 characters), compares the token in constant time over SHA-256 hashes, and the router redacts the segment in logs and spans (`adapters/http.redactPath`; add any future secret-in-path route to `secretPathPrefixes`). The Brevo account is shared with the WordPress sites: an event for an unknown message, and an ignored type (unsubscribed, spam, list addition), answers 200 and stores nothing; only a database error answers 500 (Brevo retries, applying twice is a no-op). The body is capped by SIZE (1 MiB), never by the number of events, so a big batch is not refused and retried for ever. Event rows keep no address: the reason goes through `emailstatus.ScrubReason` (e-mail addresses, IPv4/IPv6 addresses and host names removed), the payload keeps only the event name and `ts_epoch`. Metrics `arena_email_webhook_events_total{event_type,outcome}` (event_type through `observability.EmailWebhookEventLabel`) and `arena_email_webhook_rejected_total{reason}`. The order card carries `letters[]` (`emailstatus.OrderLetters`: ticket letters grouped by how they went out — one per sent letter, one `queued` for pending/processing jobs, one `failed`/`send_failed` for jobs the relay refused, one legacy `sent` for jobs sent before 0147 — then change/cancel/refund letters) and `letter_failed`; the orders list carries `letter_failed` (`emailstatus.FailedLetterOrders`). The `queued as` id matching Brevo's webhook `message-id` is NOT proven against the live relay yet; the tracking header is the fallback. Test cleanup: `DELETE FROM email_letters WHERE org_id = …` before the organization goes (the FK cascades, but `delivery_jobs.email_letter_id` must not dangle). **The letters are dispute (chargeback) evidence and are kept at least 24 months (`emailstatus.MinRetention`)**: `tests/staticanalysis/email_letters_retention_test.go` fails on any DELETE/TRUNCATE of `email_letters`, `email_letter_events` or `email_letter_interactions` outside a migration's Down section, except the clean-up of letters still `sending` (never accepted by the relay); a future purge must keep 24 months and be added there with the runbook (§5). Each letter keeps its passport (`subject`, `ticket_numbers`, `body_sha256` = SHA-256 of HTML, NUL, text; `attachments` = filename, bytes, sha256 per PDF; `template_version` = `<kind>.<locale>@<12 hex>` from `templates.Renderer.Version`), filled by `sendRecorded` from the message itself. Every open, proxy open and click is one `email_letter_interactions` row with its own time (max 500 per letter); a click keeps only scheme, host and path with long token-like segments as `{token}` (`emailstatus.CleanLink`) and counts as delivery, never reading. The reader's USER AGENT is stored (U-7, migration 0156, owner decision 2026-10-15): `email_letter_interactions.user_agent` from Brevo's `user_agent` field of opened/proxy_open/click events (`emailstatus.CleanUserAgent`: invalid UTF-8 and control characters, NUL included, removed, 512 runes), read only through `GET /v1/organizations/{org_id}/orders/{order_id}/letter-interactions` (`order.read`, `horders.HandleLetterInteractions`, order loaded in the path org = 404 otherwise, an organization API key 403 `orders.letter_interactions_api_key_forbidden`), never logged, never in `email_letter_events.payload`, audit or Telegram, retained like the letters (no purge). Brevo's webhook sends NO reader IP (its only IP field is `sending_ip`, the relay's), so there is deliberately no `client_ip` column; the reader's address would need an arena-served pixel or redirect, an undecided owner question (runbook §6). **Review rules (2026-10-13):** `Accepted` never fails on a message id the relay repeated: the id is set only when no other letter holds it (`NOT EXISTS`, the unique index would otherwise roll the whole UPDATE back and leave the letter `sending` with unlinked jobs), and marking sent, storing the id and linking the jobs are three statements whose errors are joined, not one. `SweepStale` never deletes a letter that went out: a `sending` row older than a day first becomes `sent` with `receipt_missing` when its delivery job is `sent`, or (change/cancel) its `session_change_notices` row is `sent`, or (refund) the batch's refunds carry `buyer_notified_at` after it; only the rest is deleted. A letter `sent` or `delayed` for more than 6 h with no delivery is `stuck` (`emailstatus.StuckAfter`) when the webhook has stored any event since it was sent (silence of a switched-off webhook is not a stuck letter); `stuck` sets `letter_failed` on the card and the list. Invitation letters have no order and are listed nowhere yet. Retention is at least 24 and at most 36 months COUNTED FROM THE EVENT DATE (owner decision 2026-10-14): `emailstatus.RetentionAnchorSQL` is max(sent_at, end of the latest session the letter is tied to through its order, its change notice or the ticket numbers of its passport), or sent_at for a letter tied to none; a future purge must use it, never sent_at alone (GDPR Art. 6(1)(f), refusal of erasure under Art. 17(3)(e) while a dispute is possible; runbook §5): no purge exists yet, the first rows reach 36 months in 2029-10 at the earliest. Tests: `delivery/letter_status_integration_test.go` (duplicate id, stale sweep), `httpserver/brevo_webhook_integration_test.go` (bearer and rotation, batch of 250, deferred and stuck, org isolation of letters). Migration 0147 follows 0146 (organization legal form): deploy them in that order, goose refuses an out-of-order 0146 on a database that already has 0147. **Follow-up rules (2026-10-14, migration 0149):** every letter is tied to its OWN source at `Begin`, and only that source may confirm it: `delivery_jobs.email_letter_id` (ticket), `session_change_notices.email_letter_id` (`NewLetter.ChangeID` + `OrderID`, change/cancel) and `refunds.buyer_notice_letter_id` (`NewLetter.RefundIDs`, refund). A retry relinks the source to the newer letter. Never confirm a letter by "some notice or refund of the same order": a second move or refund operation would turn a letter that never left into false evidence. A stale `sending` letter whose ticket job is still `processing` is kept, not deleted (the worker may have died after the 250). `email_letters.from_address` / `reply_to` record what the buyer saw (`sendRecorded` takes the platform default through the sender's `DefaultFrom()`). Brevo's zoneless `date` field is never trusted (only RFC 3339 with a zone). The card's `letter_failed` forgives a change/cancel/refund letter followed by a newer letter of the same kind, exactly like the list. The bot reads `letters[]` (`eventbot/order_letters.go`: delivered, opened, delayed, bounced, stuck; `delivery[]` is only the fallback for a card without letters) and marks a list row on `letter_failed`. The retention guard (`tests/staticanalysis/email_letters_retention_test.go`) allows only a DELETE whose WHERE starts with `status = 'sending' AND` and has no OR, plus Abandon's by-id delete.

- **The e-mail open-tracking pixel is Brevo's, not arena's, and `buildMIMEMessage` keeps a text part ONLY when the letter has attachments (0-8, 2026-10-14).** No template or sender code writes a tracking `<img>`: Brevo injects it into the HTML body of a relayed message (this needs open tracking switched on in the Brevo account, a manual check, V1 of the unified plan section 3a). Arena guarantees only what that injection needs: exactly one `text/html` part with one `</body>`, exactly one `X-Mailin-custom: arena-letter:<id>` header (never in a body), and one MAIL/RCPT/DATA per send; `adapters/email/sender_tracking_test.go` pins it on the raw bytes and through a counting fake SMTP server. A letter WITHOUT attachments (refund, invitation) is a single `text/html` part even when `Message.TextBody` is set, so it carries no plain-text alternative; a letter with PDFs is `multipart/mixed` over `multipart/alternative` (text, html) plus the PDFs. The status rows (sent, delayed, delivered, opened, failed; a proxy open is delivery) are pinned in `emailstatus/status_regulation_test.go` on top of `TestDerive`.

- **The "Unsubscribe" a buyer sees on a ticket letter is Brevo's, not arena's (2026-10-15).** Nothing in `delivery/templates` or `adapters/email` writes a `List-Unsubscribe` header or an unsubscribe link; the Brevo SMTP relay adds the header itself to transactional mail, and Gmail shows it as a button. A press blocks the address for ALL later transactional mail of the account (tickets, refund letters). Brevo documents no setting or SMTP header to switch it off on ordinary plans: only Enterprise can replace it with `List-Help`, on request to support. Arena guards its own side with `TestTransactionalTemplates_*NoUnsubscribe` and `TestBuildMIMEMessage_NoListUnsubscribeHeader`, so a new template or header must not add one, and the owner's steps are in section 10 of `docs/ops/brevo_delivery_status_webhook_ru.md`. Do not add our own `List-Unsubscribe`: nothing proves Brevo honours a sender's header, and a link would invite the block it is meant to prevent.

- **The "Unsubscribe" a buyer sees on a ticket letter is Brevo's, not arena's (2026-10-15).** Nothing in `delivery/templates` or `adapters/email` writes a `List-Unsubscribe` header or an unsubscribe link; the Brevo SMTP relay adds the header itself to transactional mail, and Gmail shows it as a button. A press blocks the address for ALL later transactional mail of the account (tickets, refund letters). Brevo documents no setting or SMTP header to switch it off on ordinary plans: only Enterprise can replace it with `List-Help`, on request to support. Arena guards its own side with `TestTransactionalTemplates_*NoUnsubscribe` and `TestBuildMIMEMessage_NoListUnsubscribeHeader`, so a new template or header must not add one, and the owner's steps are in section 10 of `docs/ops/brevo_delivery_status_webhook_ru.md`. Do not add our own `List-Unsubscribe`: nothing proves Brevo honours a sender's header, and a link would invite the block it is meant to prevent. **Since U-2 (owner decision 2026-10-15) every buyer letter (ticket, invitation, change, cancel, refund; seven locales) ends with a plain-text note after the platform line, explaining that pressing "Unsubscribe" stops the ticket and order letters and that arena sends no marketing mail without consent; the wording lives in `unsubscribeNotes` of `templates_unsubscribe_note_test.go`, the guard forbids any unsubscribe link, header or placeholder but accepts exactly those sentences, and a new buyer template or locale must carry the note.**

- **The VIP mark of a ticket is the organizer's PRIVATE flag (V1, migration 0164, `tickets.vip` + `vip_set_by`/`vip_set_at`).** `PATCH /v1/organizations/{org_id}/tickets/{ticket_id}/vip` `{vip: bool}` (`ticket.update`: owner and manager, never an agent) sets or clears it in `httpserver/ticket_vip.go`: the ticket's organization comes from `GetTicketOrgID` (a foreign ticket is 404 `ticket.not_found`), the call is idempotent (`SetTicketVIP` returns the row only when the value really changed, so a repeat writes neither a row nor the `v1.ticket.vip_set` audit event) and `tickets.updated_at` is left alone on purpose. An invitation is the same ticket, so `POST .../complimentary` takes `vip` and marks every ticket of the issuance in the issuing transaction (`SetTicketsVIPByIssuance`). The flag is read by the order card (`OrderTicketSummary.vip`), the invitations list and the issue answer (`ListTicketVIPByIDs`, one extra query) and by NOTHING that a buyer sees: `GetTicketPresentationByID`, `pdf.Ticket`, `delivery.Payload` and the `*.tmpl` letter templates must never gain a `vip` field or word (`TestTicketPresentation_NeverSelectsTheVIPMark`, `TestTicket_HasNoVIPField`, `TestPayload_HasNoVIPField`, `TestTemplates_NeverMentionTheVIPMark`). The bot's press carries the WANTED value (`or:vip:1:<ticket>` / `or:vip:0:<ticket>`, `iv:vip:1|0` for the invitation tick), never a toggle, and both are in `inPlaceToggles`, so a stale button repeats a state and never flips it back. Trap found on the way: `inviteOutcome(st)` returns `(issued, left)`; destructuring it as `_, issued :=` silently takes the OTHER list and the VIP button vanished. A VIP tick that changes the request needs a NEW batch id (the API answers a repeated batch with the first result). Tests: `httpserver/ticket_vip_integration_test.go`, `bot_e2e_vip_orders_integration_test.go`, `bot_e2e_vip_integration_test.go`, `eventbot/order_vip_test.go`, web twin `apps/event-center/tests/smoke/live/vip.spec.ts` (fixture `cmd/arena-e2e-seed/vip_fixture.go`). Not built (V2/V3): the notification and the MACS/scanner export of the mark.

- **The organizer's own event text (EC-31, task 7-1, migration 0165) is a table of its own, `event_email_notes`, filtered by `eventnote.Normalize`, and printed only by the `ticket` and `change` letters.** A table rather than an `events` column because `scanEventRow` is fed by ~15 queries (same trade as `event_promoters`). It is plain text of at most 2000 characters: HTML tags are removed, NUL/control/bidi characters, `javascript:`/`data:`/`file:` and plain `http://` are refused, at most 5 links, every link `https://` with a host; empty after the filter means "no note" and deletes the row. The limit is kept equal to the CHECK of 0165 by `TestLimitMatchesMigration`. Writers: PATCH `email_note` (absent keeps, null or "" clears; checked BEFORE `UpdateEvent` so a refused text changes nothing, 400 `event.invalid_email_note` with `details.reason`) and event-bundle `action.emailNote` (source=arena only, same tri-state, 422 `import.invalid_email_note` found by the dry run too; the bundle READ answers "" when there is none). Readers: `hydrateEmailNotes` (`EventItem.email_note`) and `GetTicketPresentationByID.email_note` -> `Payload.EventEmailNote` -> `Data.EventNote` (an `eventnote.Block`: paragraphs of text/link segments for the HTML part, `Plain` for the text part). The template block is `{{with .EventNote.Paragraphs}}` captioned "About the event" in 7 locales; cancel, invitation and refund letters ignore it. **`email_letters.template_version` ends in `+note` (`templates.NoteVersionSuffix`, `Renderer.VersionFor`) when the letter carried the block**, because the note changes `body_sha256` (plan 3a rule 5): build the version through `templateVersionFor`, never `templateVersion`, for a kind that can carry a note. The sample letter (`POST .../events/{event_id}/email-note/test`, `event.update`, worker job `event.email_note_test`, `delivery.NewEventNoteTestHandler`) is sent straight through the sender and is NEVER written to `email_letters` (that table is the buyer-evidence trail); recipient = the caller's own address or an active member's, 5 an hour per organization from a process-memory `ratelimit` held on the Server (`emailNoteTestQuota`, built lazily because `catalogHandler()` is per request). Not copied by the "copy event materials" route. The `ticket` kind prints the note in the test letter too, so the sample says "your ticket is attached" with nothing attached.
