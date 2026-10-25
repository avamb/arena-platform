# Arena event center (web)

The organizers' web event center: the same functions as the Telegram
event-center bot, in a browser. Spec: `08_architecture/37_web_event_center_ru.md`.

This app is for organizers only. The platform superadmin console is
`apps/admin-web`; never import its screens here.

## Run locally

1. Copy `.env.example` to `.env.local` and set `VITE_API_BASE_URL`.
2. Add the dev origin (`http://localhost:5176`) to `CORS_ALLOWED_ORIGINS` of arena-api.
3. Start the dev server:

```bash
npm --prefix apps/event-center run dev
```

## Tests

- `npm --prefix apps/event-center test` runs vitest. It includes
  `src/parity/parity.test.ts`, which checks every web call against
  `apps/backend/openapi/openapi.yaml` and the parity table of the spec.
  After you change `src/parity/matrix.ts`, rewrite the table with
  `UPDATE_PARITY=1 npm --prefix apps/event-center test`.
- `npm --prefix apps/event-center run test:smoke` runs the Playwright smoke
  (375x812) against a mocked API (`tests/smoke/mockApi.ts`).

## Add an API call

1. Add the route to `src/lib/api/routes.ts` (the path template exactly as in openapi.yaml).
2. Add a function to `src/lib/api/endpoints.ts`.
3. Add the route to its function in `src/parity/matrix.ts` and rewrite the spec table.

## Deploy

Dokploy application, like `tickets-page`: Dockerfile `apps/event-center/Dockerfile`,
build context the repository root, build argument `VITE_API_BASE_URL`, port 8080,
health check `GET /health`. Add the app's origin to `CORS_ALLOWED_ORIGINS` of arena-api.
Production: `https://events.arenasoldout.com`; the step-by-step checklist is
`docs/ops/event_center_rollout_ru.md`, and `scripts/check-live.ps1` checks the live site.

Build flag `VITE_FEATURE_WIZARD`: `1` shows "Create event" (the events list, the menu)
and the event wizard that creates a DRAFT general-admission event (task 6-1c).
The release image builds with `1` (the Dockerfile default). Rollback: rebuild with the
build argument `VITE_FEATURE_WIZARD=0`; a bare `vite` run without the variable keeps it
off. The flag is inlined at build time, like `VITE_API_BASE_URL`. The screen also needs
the `import.bil24_session` permission (owner and managers), which the import route
itself demands.

This release saves one general-admission session on an existing venue. The new
event and session stay in draft, off sale. Multiple dates and a new venue can
be explored in the form but cannot be saved in this release; the Create button
explains the restriction. Assigned seating and editing an existing event's
main details are not part of this release.

The live acceptance scenario is `tests/smoke/live/create-event.spec.ts` (run
`npm --prefix apps/event-center run test:e2e:live` with a local scratch database
as described in `tests/README.md`). It covers persistence, exact prices and
capacity, an answer lost after the write, retry with the same key, double
clicks, and authorization across roles and organizations. Unit tests also
reject a start in the venue's skipped daylight-saving hour.

Password letters of organizers link here (`/reset-password`, `/accept-invite`) once
arena-api and arena-worker carry `EVENT_CENTER_PUBLIC_URL`; platform users keep
the admin-web link (`APP_PUBLIC_URL`).

nginx sends a strict Content-Security-Policy and the other security headers from
`security-headers.conf.template`, included in EVERY location (a location with its own
`add_header` drops the server-level ones). `scripts/check-headers.sh` asserts them on a
running container; CI also runs the smoke against the built image, so the CSP is tested.

## Event posters (task 7-1)

An organizer uploads a poster as the first step of the creation wizard (or continues
without one on purpose) and can upload or replace it on the event card. The code is `src/lib/posters.ts` (rules, preview),
`src/lib/posterFlow.ts` (upload and attach), `src/lib/posterImage.ts` (showing
the stored poster) and the components `PosterPicker`, `WizardPosterSection` and
`EventPoster`.

**Contract used (no new endpoint, no backend change):**

| Step | Call | Permission |
|------|------|------------|
| Upload | `POST /v1/media` multipart: `file`, `owner_type=event_poster`, `org_id`, `content_type`; answer is `{ media_object: { id, ... } }` (not flat) | `media.write` |
| Attach | `PATCH /v1/organizations/{org}/events/{id}` with `{ "poster_media_id": "<id>" }` only. `clear_session_overrides` is NEVER sent, so a date with its own poster keeps it | `event.update` |
| Read the id | `poster_media_id` of the event list item the card already has | `event.read` |
| Show | `GET /v1/media/{id}` gives a signed address (about 7 minutes); the bytes are fetched into a blob at once and the blob address is revoked on leaving | `media.read` |

The buttons need `media.write` AND `event.update`; without them the card shows the
poster read-only. The agent holds `media.read` only. An organization cannot attach
another organization's media (422 `event.invalid_poster_media_id`).

**Rules.** JPEG and PNG only (WebP is NOT accepted: the server stores whatever
`content_type` the client names and verifies nothing, so the web proves the type
itself). The type is read from the first bytes, never from the name; the file must
be structurally complete, decodable by the browser, at most 10000 px a side,
25 megapixels in total (checked before decoding to bound bitmap memory), and at
most **5 MiB** (`POSTER_MAX_BYTES`, the same cap as the poster reader; the server
allows 32 MiB). SVG, PDF, empty, cut-off and damaged files are refused before
anything is sent.

**Publishing a wizard draft.** The event and its prepared draft dates become
`published` and `scheduled` together, in the same server transaction as the audit
row. Cancelled, completed and deleted dates are never revived. A repeated publish
is a no-op; an event already published before this fix needs an explicit take-off-sale
and publish cycle to schedule its prepared drafts. There is no automatic data repair
or new channel assignment. Existing sold tickets remain valid while off sale.

**Upload and attachment are separate.** The wizard uploads once, creates the draft
with the draft's own stable key (`publish: false`, off sale), then attaches. If the
attachment fails the event exists: the draft keeps the media id and the event id
(never the file, a blob address or a signed link), the screen says so and offers
"set the poster again" (the PATCH only) or "open the event card". A lost answer is
checked by reading the event back before anything is called failed or saved.
Replacing a poster keeps the old one until the new id is confirmed on the event.

**Known limits.** (1) A picture that was uploaded but never attached, and a poster
that was replaced, stay in storage: this release has no cleanup and never deletes
media (it may be shared; the server's `media-gc` only removes soft-deleted rows).
(2) The signed address is used only when it points at the API origin the app is
built for (`VITE_API_BASE_URL`); a storage that signs addresses on its own origin
(S3) is refused with a "try again" message instead of adding an origin to the
policy. Supporting it needs that origin in `connect-src` of
`security-headers.conf.template`, decided per deployment. (3) The chosen file
lives in memory; after a reload only an already uploaded id survives.

### Poster first: upload, review the hints, apply on purpose

The wizard's first block is **Poster** (never "optional"). It says "Upload a poster —
we will offer to fill in the event details" and offers two explicit ways on: upload a
poster, or **Continue without a poster** (a conscious choice; a reminder says the poster
can be uploaded later in the event card). "Create event" stays disabled until one is
chosen. There is no hard poster requirement for publishing or selling: skipping is an
accepted decision. A person who may not upload posters (`media.write` + `event.update`)
is never asked.

- **Draft v4** (`src/lib/wizard/draft.ts`) stores `posterDecision` (`""`, `"poster"`,
  `"skipped"`) next to the two stable ids. A v3 draft with a media/event id is read as
  `"poster"` (its picture is not uploaded again); a v2 draft and a v3 draft without a
  picture keep every typed field and are asked once. `"poster"` with no id left asks
  again. Removing or changing the picture goes back to "not decided", never to "skipped".
- **One upload, reused.** The picture is uploaded as soon as it passes the local checks
  (`src/lib/posterIntake.ts`, `useWizardPoster`). The returned media id is saved in the
  draft, used for reading, and attached by "Create event" (`createWithPoster` skips the
  upload when the draft holds the id). Upload failure is its own message with "Upload
  again"; the file stays in the picker and the form is kept.
- **Reading is explicit and paid** (`POST /v1/organizations/{org}/media/{media}/poster-facts`,
  `event.update`; the model key lives on arena-api only). The person is told that the
  picture (nothing else from the form) goes to an automatic text recognition service
  *before* the button. The call has its own 60 s timeout (`POSTER_READ_TIMEOUT_MS`;
  the server allows 45 s + 10 s), one request per press (a double click is one request),
  and is **never repeated by itself**: not on render, reload, failure or timeout. "Read
  again" is a separate explicit press, offered only where a retry can help (service
  failure, no answer); not configured, daily limit, no permission, missing or unsupported
  picture say so plainly and leave "Continue by hand". The uploaded picture is never lost.
- **Hints, not values** (`src/lib/wizard/posterHints.ts`). The answer is untrusted: only a
  real day, an `HH:MM` time, a known age rating, a whole-number price and plain text are
  kept (shown as text, never HTML). The review lists what the poster says against the
  current form: empty fields are ticked, typed text is **not** (ticking it is the
  confirmation to replace it, and typing after the review appeared cancels the tick).
  "Fill in the selected" is the only thing that changes the form.
  - Venue: never created or chosen from a name. Matching venues of the organization are
    offered as radio buttons (none preselected); only when none matches, the name is a
    hint for a *new* venue form and the city is shown as a hint until a country is chosen.
  - Date and time: the reader returns one date; it goes to the **first** date, other
    dates stay as typed. The time is the venue's local time (the wizard converts it).
  - Price: only into an untouched single-category setup (a typed price is an explicit
    replacement; several categories, steps or other modes are not touched), only with a
    known currency (the form's, or the poster's when the form has none and the person
    chose the price). A different currency, or no currency anywhere, is explained and not
    applied. Capacity, sale times and open/closed state are never set from a poster.
- **Isolation.** The intake is created per organization *and* draft: leaving, removing,
  skipping or choosing another picture starts a new generation and aborts the request, so
  a late answer cannot touch the new state. Keyed `Wizard` (#192) keeps organizations apart.
- **Existing events.** Replacing the poster on the event card does not read it and does not
  touch event data (`live/poster-first.spec.ts`).

**Tests and honest labels.** Unit: `posterHints`, `posterIntake`, `draft.posterDecision`,
`posterFlow`, `http.multipart` (transport, timeout, refresh). Live: `live/poster-first.spec.ts`
against a real arena-api built with the **test-only** tag `e2eposter`
(`apps/backend/cmd/arena-api/poster_reader_e2e.go`): only the vision model is scripted
(`E2E_POSTER_SCRIPT`, `E2E_POSTER_CALLS`, see `scripts/live-harness.mjs`); the poster-facts
route, upload, creation and attach are real. A normal build injects nothing, and no model
key exists in the harness. **No real poster has been read by a real model in these tests.**

**Content-Security-Policy.** The one change is `blob:` in `img-src` (previews and
the shown poster are blob addresses made in the page). `script-src`, `frame-*`,
`object-src` and `connect-src` are unchanged. `tests/smoke/container/poster-csp.spec.ts`
proves it against the real nginx image (see `tests/README.md`).

## Telegram mini app: `/tg/edit` (spec 44, RT-12)

The bot's «Править в редакторе» button (behind the bot flag
`BOT_MINIAPP_EDITOR_ENABLED`, off by default) is an ordinary callback button until it is pressed:
the press draws the code and turns that button into a Telegram `web_app` button that opens
`<EVENT_CENTER_PUBLIC_URL>/tg/edit#code=<code>&kind=description|email_note&event=<event id>`.
The page exchanges the one-time code (purpose `miniapp`, 5 minutes) at `POST /v1/auth/web/link`
for a 30-minute session of channel `event_center_tg`, takes the three parameters out of the
address (`src/lib/tgEdit.ts`, called first by `main.tsx`), looks the event up and opens the editor of the
text (RT-12b): the description (`TgDescriptionEditor`, ONE PATCH of the event with `description` and
`description_format: "art"`) or the letter text (the same `EmailNoteSection` as the event card). The event of the
link is untrusted: the page looks it up in the events list of every organization of the person
(`src/lib/tgEditEvent.ts`), and a foreign event, a refused list or a person without `event.update` all get the
same calm line «Нет доступа к этому мероприятию». After a save the page calls `Telegram.WebApp.close()`; in
a plain browser (the script says `platform: "unknown"`) it says «Сохранено» and links to the event. The
Telegram theme and safe areas reach the page only through custom properties (`.tg-page` in `styles.css`), never
a style attribute. Tests: `src/screens/TgEditor.test.tsx` (jsdom), `tests/smoke/tg-edit.mock.spec.ts` (360 x 740,
also run against the nginx image in CI, with the CSP check), `tests/smoke/live/tg-edit.spec.ts` (real API; the
harness gives the API a `BOT_SERVICE_TOKEN` and links the three people to Telegram accounts). Not reachable by
real users while the bot flag is off.

nginx serves `/tg/` with its own headers: `location ^~ /tg/` and the named location
`@tg_index` (`nginx.conf.template`), `security-headers-tg.conf.template`. Everything is the main
policy except two things: no `X-Frame-Options` and `frame-ancestors` naming the Telegram web
clients (the file says which and why). `scripts/check-headers-tg.sh` is the container smoke (CI
job `event-center-build`); `src/lib/tgHeaders.test.ts` checks the files before an image exists.

### Telegram web app script

`public/vendor/telegram-web-app.124bdff7.js` is a copy of `https://telegram.org/js/telegram-web-app.js`
(the file has no version number: it is the script of Bot API 9.x, `Last-Modified: Tue, 06 Oct 2026 09:27:17 GMT`,
119731 bytes, downloaded 2026-10-25). `script-src 'self'` is unchanged: the page loads the copy.

    sha256  124bdff7ba3fe86c8ba5143467afec21e020405e74abdb92cfdda6c0ee3b5908

The first 8 hex digits are in the file name, so the one-year cache of nginx cannot serve an old copy.
To update: download the new file, compute `sha256sum`, rename it `telegram-web-app.<8 digits>.js`,
change `TELEGRAM_SCRIPT_SRC` in `src/lib/tgEdit.ts` and the hash above. `src/lib/tgEdit.test.ts`
fails if the file, its name and this note disagree. The page works without the file (the close
button then goes back to the app), so a missing script never blocks the sign-in.

Known limits. The fragment check below must be done by hand on Android, iOS, Telegram Desktop and
Telegram Web BEFORE `BOT_MINIAPP_EDITOR_ENABLED` is turned on anywhere (`src/lib/tgEdit.test.ts`
covers the parser for a second `#` and for data appended after `&`, not the clients themselves):

- Telegram may add its own parameters to the fragment or replace it. The page keeps everything
  except `code`, `kind` and `event`, and it stops at a second `#`. If a client drops our fragment, the spec's fall-back is the
  code in the path; the nginx access log writes paths, so it needs a log rule first.
- Inside the Telegram Web iframe the script tries to apply the client's theme through a `<style>`
  element, which `style-src 'self'` refuses (one console message). The page does not depend on it.
- The page signs in with the app's normal token store (the same storage keys as the web sign-in): a mini app session replaces a 72-hour web
  session kept in the same browser profile or Telegram Desktop web view of this origin, for
  example when the link is forwarded and opened in an ordinary browser. This is accepted: the
  person signs in again. The session has no sign-in slot in the limit of concurrent sessions, so
  it never pushes the main web or admin session out.
