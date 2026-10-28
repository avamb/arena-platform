# Event-bundle import and export

The event-bundle import (source=arena) and its dry run, read-back, venue and channel pick. Verbatim items of the former single AGENTS.md. Add a new trap here as a `- **Title.** ...` item and put one title line for it into the index in AGENTS.md.

- **The event-bundle (source=arena) addresses existing rows by arena UUID
  as well as by compat id: `action.arenaEventId`,
  `actionEvent.arenaSessionId`, `categoryList[].arenaTierId`** (same
  pattern as `venue.arenaVenueId`; an unknown or foreign UUID is 422
  `import.invalid_event` / `invalid_session` / `invalid_category`, never
  404). With `arenaSessionId` the top-level `externalRef` MAY be omitted —
  `resolveExternalRef` (`himports/bil24_session.go`) only demands it for a
  bundle that addresses no session — and the session keeps the key it was
  created under; a DIFFERENT key for it is 409 `import.external_ref_conflict`,
  a session of another event than `arenaEventId` is 409
  `import.action_mismatch`. This is what the Telegram bot's "Edit" uses
  (`eventbot/wizard_load.go` → `BuildBundle`): the bot never learns compat
  ids or external refs. A category NOT addressed by `arenaTierId` still
  matches by name (`lower(btrim(name))`), so a rename without the id mints a
  new category and closes the old one. Guarded by
  `himports/event_bundle_arena_ids_integration_test.go`.

- **An event needs BOTH a slug and a hosted-page channel before it has a
  public page, and until 2026-09-29 an import gave it neither.** The
  hosted-page queries (`public_page.sql`) skip `events.slug IS NULL`, and
  the whole organization page 404s unless one of its channels has
  `settings.hosted_page.enabled = true`. Every import path now mints the
  slug (`himports.assignEventSlug`: `geoslug.Slugify(name)`, `-2`, `-3`…
  against `EventSlugTaken`, set once via `SetEventSlugIfEmpty` — a re-save
  keeps it). The channel flag has NO admin toggle (only the free-text
  "Additional settings JSON" textarea), so the import switches it on itself
  for a channel a human NAMED in `channelIds` that carries no gateway
  credential (`gen.EnableChannelHostedPage`, advisory warning
  `import.hosted_page_enabled`, shown by the bot); a WordPress site's
  channel (`settings.gateway.token_hash` / legacy `gateway_token_hash`) and
  the API key's own bound channel are never touched. An organization whose
  channel predates this gets the flag on its next publish through the bot.

- **A JWT caller of the event-bundle import binds the event to NO sales
  channel unless it names `channelIds`.** `ensureChannelPublication` only
  ever knew `api_keys.channel_id`; a human operator (the bot) got a
  published event that appeared in no storefront and fired no site webhook.
  `channelIds` (checked against the organization BEFORE the transaction,
  422 `import.invalid_channel`) is the fix; the response's `publications[]`
  lists every binding and `publication` stays the first one. An API key
  bound to a channel publishes into its own channel plus the named ones.

- **An event center picks a venue by `venue.arenaVenueId`** (event-bundle,
  source=arena): the org's venue UUID from `GET /v1/organizations/{org_id}/venues`.
  It wins over `venueId`/`venueName`, never edits the venue, and answers 422
  `import.invalid_venue` for a UUID that is not this org's venue. The bundle
  decoder ignores unknown fields, so an older arena silently falls back to the
  `venueName` match — ship the backend before relying on the pick.

- **`POST .../imports/event-bundle?dry_run=true` checks a bundle with the import's own code and writes nothing (task 3-1, `himports/dry_run.go`).** The mode is `importMode{DryRun}`, parsed only on the event-bundle route (the legacy bil24-session route ignores it) and threaded as `importPlan.Mode`. Stage 1 is `prepareImport` (the pre-transaction ladder, unchanged order) plus `checkBundleFields`: an `issueSink` keeps the FIRST error for an import and every `*importError` for a dry run, so the import's 4xx is byte-for-byte what it was. Stage 2 looks every reference up read-only (`checkArenaReferences` / `checkBil24References`, built from the executors' own helpers `matchArenaSession`, `findArenaEvent`, `planArenaVenue`, `resolveArenaPromoter`, `arenaTierRef`, `findBil24*`). Stage 3, only when 1-2 found nothing, runs the executor in a transaction that is always rolled back; its write-dependent errors (chain cycle via stored links, sellLimit without a link, refused session move) therefore come one at a time. In dry-run mode the executors must skip every effect that outlives the transaction or leaves the process: no `sideLoadPoster` (only `posterPrecheck` warnings), `applySessionChange` asks `sessionchange.Preview` instead of `Apply` (no journal, no `worker.EnqueueInTx`), `applyPublish` stops at the gate, `ensureChannelPublication` binds nothing, no compat id is minted (`ensureCompatIDs` / untouched-tier `EnsureMany` skipped: the sequence would move), no audit, no `publishCatalogEvent`. A NEW import step with an outside effect must check `plan.Mode.DryRun` the same way, and `TestEventBundleDryRun_*` (`event_bundle_dry_run_integration_test.go`) counts the org's rows plus the poster/audit/webhook fakes before and after. Errors carry a JSON pointer: create them with `failImportAt(status, code, "/field/path", msg)` so the dry run can report and de-duplicate them by field (`issueSink.list` keeps one error per field). Rolled-back inserts may still advance sequences (session_seats system ids): harmless gaps.

- **An event is read back as importable bundles by `GET /v1/organizations/{org_id}/events/{event_id}/event-bundle` (task 3-2, `himports/event_bundle_read.go`, `event.read`).** It is the server twin of the bot's `wizard_load.go` and the call the web event center loads an event for edit or copy with. The answer is `EventBundleReadResponse`: `{event_id, status, poster_media_id, bundles[], skipped[], warnings[]}` with ONE bundle per live session (the import takes one date per call), oldest first, each a `source=arena` `ImportEventBundleRequest` addressed by `arenaEventId`/`arenaSessionId`/`arenaVenueId`/`arenaTierId`, with no `externalRef` (the session keeps its key) and no compat ids (a read mints nothing). The guarantee is a round trip: a bundle posted back is accepted by `?dry_run=true` with no error, leaves the rows as they are, and a second read answers the same bytes (`TestEventBundleRead_RoundTrip*`; the snapshot there ignores `updated_at`, the optimistic counters `sessions.seat_status_version` / `inventory_ledger.version` and the ids of `ticket_tier_prices` rows, because an import always bumps the first two and replaces a category's schedule as a whole). It holds because every field is written the way the importer reads it: venue wall clock plus RFC 3339 instants at SECOND precision (a value the importer would read back differently is a `event_bundle.time_not_representable` warning, never rounded silently); `availability` is the quantity a category owns and, in a chain, the head carries the WHOLE chain's places and every successor 0; `nextCategoryIndex` is explicit on every category (-1 = none), `sellLimit` only on a linked one, `priceSchedule` always a list (empty clears); `bigPosterUrl` is left empty because an import without one keeps the poster (the id is `poster_media_id`, GET `/v1/media/{id}` signs a URL); `promoterId` is "" when the organization promotes the event and absent when the promoter is archived (the import refuses an archived one); `publish` is `status == published` and `channelIds` are the organization's channels reached through a usable feed token (`ListEventPublicationChannelIDs`), kept even for a draft. NOT in a bundle, by design: deleted sessions (absent), cancelled sessions, sessions with a seating plan or a pay-what-you-want category, a deleted venue (the last four are listed in `skipped[]` with a stable `event_bundle.session_*` code), the open/closed flag of a category, and the event's other columns (slug, artists, genre). The read runs in one REPEATABLE READ read-only transaction; a foreign, unknown or deleted event is 404 `event.not_found`. A new field the importer starts reading must be added to `bundleCategories`/`buildEventBundles` AND to the snapshot of the round-trip test, or the guarantee silently stops covering it.
