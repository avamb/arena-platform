# Sales page materials: implementation and verification report

Base: `820393174209cba99da6c76e541435c25f07f662` (origin/master, PR #195), branch `codex/sales-page-materials`.
Nothing committed, pushed, merged or deployed. Changes are in the working tree (some staged by `git apply --3way`).

## What changed and where it came from

| Area | Change | Origin |
|---|---|---|
| tickets-page | `lib/materials.ts`, api types, i18n (5 locales), render hook, CSS, CSP `frame-src` (youtube-nocookie, player.vimeo.com, rutube.ru), tests | PR #164 patch applied (`git diff base...origin/pr-164`), then extended |
| tickets-page | new "Where the event takes place" section (venues with own address, dates per venue, point shown separately when several venues), `materials="hidden"` on BOTH widget mounts (event page and promoter/catalog cards) | new |
| widget | `Materials.svelte`, `lib/materials.ts`, `materialsI18n.ts` (7 locales), `materials` attribute, types, e2e mock, embed docs (strict CSP) | PR #181 patch applied, then extended with venues |
| backend | public `venues[]` (name, country, city, address, postal_code, timezone, session_starts) on hosted page event and single-event feed event: `gen/venues_public.sql.go`, `hfeed/public_venues.go`. Proven gap: public API carried only `venue_names`. Visible sessions only (scheduled/completed), nothing guessed, no contacts. | new |
| OpenAPI | `PublicEventVenue`, `HostedPageEvent.venues`, feed event `venues`; `types_gen.go` and TS client regenerated (go.mod unchanged) | new |
| Event Center | `components/materials/*` (section, gallery/report, videos, partners, where+map), `lib/materials.ts`, `lib/api/materialsApi.ts`, 23 routes in `routes.ts`, 86 i18n keys en/ru/es, parity matrix row + spec table, CSS, mounted in `EventScreen` | new (PR #171 not used) |
| e2e seed | `materials_fixture.go` (hosted channel, feed token, slug, publication for the fixture event) | new |
| tests | Go unit + integration, Vitest, live Playwright twin `materials.spec.ts` | new |

## Where each type is edited, and what the buyer sees

All in Event Center, event card, section "Sales page materials".
- Gallery (max 6): upload (media API, owner `event_gallery`, JPEG/PNG 5 MB, same client check as posters), caption, order, delete. Buyer: grid, lightbox (Escape, keys, focus return).
- Videos (max 4): add by URL, view, reorder, delete, "replace" (add new, delete old, restore position; at the limit the screen asks to remove first). Buyer: click-to-load embed only for the 3 allowlisted hosts, other https = plain link.
- Where else to buy (max 3): seller name + https URL, edit, order, delete. Buyer: explicit "other seller" link, `noopener nofollow`.
- Where it takes place: venue address (street, line 2, postal code, country) saved on the venue (`venue.update`), list per venue of the event's dates; map point (lat/lng/label, 0,0 valid, one coordinate rejected). Buyer: venue addresses, OSM/Google links, no auto-loaded map.
- Photo report (max 30): same editor, note shows when it becomes public (last session end, from server rule `MAX(end_at)`; none = hidden). Server hides before that.
- Public link shown only when event is published and `public_url` is non-empty.

## Tests (commit base 8203931 + working tree)

| Check | Command | Result |
|---|---|---|
| Go unit (httpserver tree, adapters, migrations) | `go test ./apps/backend/internal/platform/httpserver/... ./apps/backend/internal/adapters/... ./apps/backend/internal/migrations/...` | first run 1 FAIL (key-set pin `TestHostedPageResponse_ExistingFieldsUnchanged`, expected: new `venues`), pin updated, hfeed re-run ok. Final: hfeed, e2e-seed, adapters, migrations, staticanalysis re-run exit 0. The full httpserver tree was not re-run after the pin fix (only hfeed). |
| Go integration (scratch Postgres 17 container `sp_materials_pg`, own port, removed) | `go test -tags integration -p 1 ./apps/backend/internal/platform/httpserver/... -run "Material\|Gallery\|Video\|Partner\|MapPoint\|PhotoReport\|Venue\|PublicPage\|PublicFeed"` | ok, 184 PASS, 0 FAIL, 0 SKIP (includes new `TestPublicVenues_AddressPerVenueNothingInvented`, existing photo-report-hidden-until-last-session tests, key-set pins) |
| Codegen | oapi-codegen v2.4.1 from module cache + `gen-ts-client.mjs` | regenerated, only expected diffs |
| tickets-page | `npx vitest run`, `tsc --noEmit`, `npm run build` | 158 passed, 0 failed; tsc clean; build produced no error |
| widget | `npx vitest run`, `tsc --noEmit`, `npm run build` | 716 passed, 0 failed; tsc clean; build no error |
| event-center | `npx vitest run`, `tsc -b`, `npm run build` | 836 passed, 0 failed; tsc clean; build no error |
| Live E2E (real arena-api built from this tree, real DB, local media, vite dev UI, Chromium phone viewport) | `playwright test --config tests/smoke/playwright.live.config.ts materials.spec.ts` | 10 passed, 0 failed, 0 skipped. Covers: add all types via screen, reload persistence, public feed and hosted page JSON equal to saved data (venue address from venue, point 0,0, XSS text kept as text), edit/reorder/replace/delete, bad URLs and coordinates (screen and server 400), foreign media id and foreign org refused, 4/3/6 limits (screen hides the form, server 409), wrong file refused, three roles by server permissions with direct write refused 403 for read-only, axe a11y, no horizontal overflow desktop and phone |

Test count notes: Vitest counts are the totals of each app suite, not only new tests.

## Screenshots
`docs/tasks/sales_page_materials_screens/`: Event Center mobile (materials, limits) and desktop. No secrets or personal data (fixture data).

## Incomplete acceptance gates (honest list)
1. Public tickets-page and widget were NOT driven in a real browser against the live backend. Their rendering is proven by jsdom/Vitest/svelte-server tests only; the live E2E proves the public JSON they read. No tickets-page/widget screenshots, no lightbox keyboard test in a real browser.
2. Container CSP smoke (nginx image with `security-headers.conf.template`) not run. CSP template and unit test that lists match are in place.
3. Real external video players (YouTube/Vimeo/Rutube) were NOT exercised; only the allowlist/URL logic is tested. No mock-iframe claim is made.
4. Photo report visibility after the last session ended (multi-session, multi time zone) is covered by the pre-existing Go integration tests, not by the new live spec (past session times cannot be set through the UI). Not re-verified through the public page.
5. Widget `npx svelte-check` has pre-existing errors unrelated to this change; not used as a gate. The widget mock Playwright e2e (`tests/materials.e2e.ts` from #181) was not run.
6. Widget/tickets-page `node_modules` were copied from the root checkout (read-only copy, versions may differ slightly from the lockfile; `npm ci --offline` failed with ENOTCACHED for those two apps). Event Center used `npm ci --offline` successfully.
7. Venue editing needs `venue.update` and the owning org; city is not editable here (comes from the venue record). Video URL replace at the 4-video limit requires remove-then-add (no PATCH API).
8. Role live test adapts to whatever the server grants each role; it does not assert that a specific role is read-only in the fixture.
9. Not done: full `go test ./...`, golangci-lint, mock-smoke Playwright suite of Event Center, Go `-tags integration` for the whole repo.
10. Independent Codex verification, commit/PR/GitHub sync pending by the parent; no release-readiness claim.

## Cost / duration
The parent runner recorded a paid Sonnet 5.5 SDK call: $4.474148, 1,406,802 ms (23 min 27 sec), 145 turns. No paid retries. These figures come from the runner's result.json, not a model estimate.

## Independent verification after this report

The incomplete-gate list above describes the initial handoff. Subsequent fixes and actual rerun results are documented in `sales_page_materials_release_2026-10-18_ru.md`.
