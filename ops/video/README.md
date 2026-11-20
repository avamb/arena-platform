# ops/video: customer videos built from code

This folder builds the customer videos of the platform (one selling video and a
library of short how-to videos) from a scenario file. Nothing is recorded by
hand: when the interface changes, capture the screens again and build again.

The folder is a separate npm project. It does not import code from the rest of
the repository and the rest of the repository does not import it.

## Chain

1. **Scenario** `scenarios/<id>/scenario.yaml`: scenes, picture source, narrator
   text for each language, text on the screen.
2. **Capture** (`npm run capture -- <id>`): Playwright does the steps of each
   scene on the staging stand and saves a screenshot at double resolution and a
   short screen recording. Not necessary for a scene that uses a ready picture.
3. **Voice**: ElevenLabs, or a silent stub when there is no key.
4. **Subtitles**: `.srt` and `.vtt` from the narrator text.
5. **Edit** in Remotion: scene length from the voice length, zoom, highlight,
   drawn cursor, titles, burned-in subtitles.
6. **Render**: three files from one scenario, 16:9 (1920x1080), 9:16
   (1080x1920) and 4:5 (1080x1350).

## Commands

Run all commands in `ops/video`.

```powershell
npm install
npm run setup
npm run build:video -- demo ru
```

- Run `npm install` inside `ops/video`. Do not use `npm --prefix ops/video install`
  from the repository root: npm 10 then adds the repository itself to
  `package.json` as a dependency (`"arena-new": "file:../.."`). Delete that line
  if it appears.
- `npm run setup` downloads the browser that Remotion renders with. It needs
  the network one time. After that, a build of the `demo` scenario needs no
  network and no keys.
- `npm run build:video -- <scenario> <language>` makes the voice, the
  subtitles and the three videos. Result: `out/<scenario>/<language>/`.
- `npm run capture -- <scenario>` records the scenes that have `capture` steps.
  Result: `scenarios/<scenario>/captures/` (not committed).
- `npm run typecheck` checks the TypeScript code.

Files of one build:

```text
out/demo/ru/demo_ru_16x9.mp4
out/demo/ru/demo_ru_9x16.mp4
out/demo/ru/demo_ru_4x5.mp4
out/demo/ru/demo_ru.srt
out/demo/ru/demo_ru.vtt
out/demo/ru/manifest.json
```

`manifest.json` shows the timing of each scene and which voice provider made
the sound. Read it to make sure that a build did not use the silent stub by
mistake.

## Scenario format

One YAML file. All coordinates are fractions of the picture, from 0 to 1, with
the origin at the top left corner. Thus one scenario is correct for all three
frame sizes.

```yaml
id: demo                      # same as the folder name
fps: 30
languages: [ru, en]
title:                        # shown in the file metadata, not in the frame
  ru: Демонстрационный ролик
  en: Demo video

voice:
  provider: auto              # auto | elevenlabs | silent
  elevenlabs:
    voice_id_env: ELEVENLABS_VOICE_ID   # name of the variable, not the id
    model_id: eleven_multilingual_v2

capture:                      # necessary only if a scene has capture steps
  base_url_env: VIDEO_BASE_URL
  viewport: { width: 1440, height: 900 }   # or 390 x 844 for the phone frame
  setup:                      # done one time before the scenes, not recorded
    - goto: /login
    - fill: { selector: "input[name=email]", value_env: VIDEO_LOGIN_EMAIL }
    - fill: { selector: "input[name=password]", value_env: VIDEO_LOGIN_PASSWORD }
    - click: "button[type=submit]"
    - wait_for: "text=ABH TEAM"

scenes:
  - id: events-list
    source:
      image: assets/01-events.png        # ready picture, path from the scenario folder
    narration:
      ru: Все ваши мероприятия на одном экране.
      en: All your events are on one screen.
    title:                    # text on the screen, optional
      ru: Мероприятия
      en: Events
    zoom: { from: 1.0, to: 1.25, x: 0.3, y: 0.4 }   # optional
    highlight: { x: 0.1, y: 0.3, w: 0.4, h: 0.12 }  # optional frame around an element
    cursor:                   # optional drawn cursor, time in fractions of the scene
      - { at: 0.2, x: 0.8, y: 0.8 }
      - { at: 0.6, x: 0.3, y: 0.36, click: true }
    pause_after: 0.4          # seconds of silence after the narrator text

  - id: new-event
    source:
      capture:                # Playwright steps, result captures/new-event.png and .webm
        - goto: /o/{{env.VIDEO_ORG_ID}}/events
        - click: "text=Новое мероприятие"
        - wait_for: "h1"
        - wait: 500
      use: image              # image (default) or clip
    narration:
      ru: Новое мероприятие создаётся за минуту.
```

Capture steps: `goto`, `click`, `hover`, `fill` (`value` or `value_env`),
`press`, `wait_for`, `wait` (milliseconds). A secret is given only by the name
of an environment variable (`value_env`). The capture script stops if the
scenario contains a `value` for a password field.

The length of a scene is the length of its voice file plus `pause_after`. Do
not write a length in the scenario.

### Shots, phone frame, graphics, formats

A scene can show more than one picture while the narrator says one text. Use
`shots` instead of `source`. The time of the scene is divided between the shots
in proportion to `weight` (default 1). Each shot has its own `title`, `zoom`,
`highlight` and `cursor`; the cursor time is a fraction of the shot.
A shot can set `per_language: { en: { zoom: ... } }`: for that language the
given `zoom`, `highlight`, `cursor` or `scroll` replace the shot's own field
as a whole. A picture of one language lives in a `<lang>/` folder next to the
common one: `captures/en/x.png` replaces `captures/x.png` when building `en`.

```yaml
formats: [16x9, 9x16, 4x5]    # optional, default all three; a short video can ask for [9x16] only
background: "#4f46e5"         # optional colour behind phone frames and graphics

scenes:
  - id: buyer
    narration:
      ru: Покупатель открывает ссылку, выбирает билеты и платит картой.
    shots:
      - image: captures/buyer/page.png
        frame: phone            # phone | browser | none (default none)
        title: { ru: Выбор билетов }
        weight: 2
        scroll: { from: 0, to: 0.4 }   # for a picture taller than the frame: part of the hidden height
      - image: captures/buyer/pay.png
        frame: phone
        title: { ru: Оплата картой }
      - graphic: flow           # drawn by the edit, no picture
        steps: { ru: [Покупатель, Stripe, Ваш счёт] }
        caption: { ru: Деньги идут напрямую вам }
      - graphic: cta
        heading: { ru: Оставьте заявку }
        lines: ["arenasoldout.com", "@EventsCentrBot"]
        qr: "https://t.me/EventsCentrBot?start=apply"
        logo: assets/logo.png   # optional
      - graphic: placeholder    # a scene that is not filmed yet; the frame says so
        caption: { ru: "Сканер билетов: съёмка на телефоне" }
```

- `frame: phone` draws the picture in a phone body (round corners, dark edge)
  on the `background` colour. In 16:9 the phone is in the centre and the shot
  title is at its side; in 9:16 and 4:5 the phone fills the height that is left
  between the title and the subtitles. Zoom, highlight and cursor stay in the
  coordinates of the picture and are cut by the phone screen.
- `frame: browser` draws a simple browser window (bar with three dots) around
  a desktop picture. Without `zoom` the page is shown whole.
- Close-ups: with `zoom`, x and y are the point of the picture that the frame
  looks at. In 16:9 the phone has the full height of the frame and the title and
  subtitles are in a column at its right; the browser window fills the frame.
  In 9:16 and 4:5 the phone screen has almost the full width and its lower part
  can be under the subtitles, thus keep the important element in the upper half
  of the phone screen: for a button at the bottom of a screenshot, add empty
  space under the picture and use `scroll` (see `sell-tickets-from-poster`, pictures `*_close.png`).
  A chat column is narrower than the screen, thus a zoom larger than 1.2 cuts
  its text at the sides.
- A picture shot can look different in one format. `per_format` has the keys
  `16x9`, `9x16`, `4x5`; each holds any of `image`, `frame`, `zoom`,
  `highlight`, `cursor`, `scroll`, `address`. For that format the given field
  replaces the shot's own field as a whole, and it also wins over `per_language`.
  A picture of `per_format` follows the same `<lang>/` folder rule.
  `address` is the text in the address bar of a `frame: browser` shot: a string,
  or a mapping language to string.

  ```yaml
  - image: captures/site/page.png
    frame: browser
    address: arenasoldout.com/apply
    per_format:
      9x16:
        frame: phone
        image: captures/site/page_mobile.png
        zoom: { from: 1, to: 1.3, y: 0.2 }
  ```

- `VIDEO_FORMATS=16x9` (a comma list) makes `build:video` render only these
  formats of the scenario. A format that is not in the scenario is an error.
  Not set: all formats of the scenario.
- A QR code is made at build time from the `qr` text. No network.
- `manifest.json` records for each scene the number of characters sent to the
  voice provider and if the sound came from the cache; the build prints the
  total of new characters.

### Telegram bot capture

`src/telegram.ts` drives ONE bot chat in Telegram Web (version K) in a phone
size window. It uses a browser profile where a person signed in before
(`.cache/tg-profile`, never committed) through `launchPersistentContext`,
because Telegram Web keeps its session in IndexedDB and `storageState` does not
carry it. Rules that the module enforces:

- It opens only `https://web.telegram.org/k/#@<bot>`, where the bot name comes
  from `VIDEO_TG_BOT`, and stops if the title of the open chat is not that bot
  or if the page asks to sign in. It never signs in.
- Before each screenshot it puts the public name (`VIDEO_TG_PUBLIC_TITLE`) in
  the chat header, hides unread counters and can replace listed strings in the
  messages (a staging host name with the public host name).
- It takes screenshots at double resolution; it never screenshots the chat list.

A film that needs the bot has its own capture script (`scripts/capture-<id>.ts`)
because the steps change data on the stand (they create an event).

### Film sell-tickets-from-poster

`scenarios/sell-tickets-from-poster` is the 70-second selling film and `scenarios/sell-tickets-from-poster-short`
is its 30-second vertical version; the text and the storyboard are in
`docs/video/01_first_video_script_ru.md`. The pictures in `captures/` are not
committed. They were made on the staging stand on 2026-11-18 with scratch
scripts (bot chat, buyer page up to the payment page, web event center), which
are not in the repository because they contain test accounts. A committed
`scripts/capture-sell-tickets-from-poster.ts` on top of `src/telegram.ts` is not written yet.

Before a screenshot the scripts replace text in the page: the test bot name
with the public name, the staging host with the public host, the test
organization name with the example name, and the data of the test buyer with
the invented buyer of the script. `reconstructions/` holds pages drawn by hand
for frames that could not be filmed; a shot made from them is marked in the
scenario.

The English and Spanish films have their own pictures in `captures/en/` and
`captures/es/` (made on 2026-11-19): the bot was set to that language and made
the events "Jazz Night" and "Noche de jazz" from the posters in `assets/en/`
and `assets/es/`; the buyer page was opened with `?lang=en` and `?lang=es`, and
the web event center with a browser of that language. The sale message is
English in each film, as in production. The scanner and the payouts list are
English in each film.

### Film apply-on-website

`scenarios/apply-on-website` shows a new organizer who applies on the website and signs
in to the web event center (ru, en, es; about 55 seconds). The pictures were
made on 2026-11-19 with two committed scripts:

- `scripts/capture-apply-on-website.ts <ru|en|es> <form|approve|login|signin>` does the
  steps in a phone-size browser of the language of the film. Its header lists
  the environment variables. The steps change data on the stand: each language
  makes one application, one organization and one owner account.
- `scripts/render-letter.ts` draws a letter as a phone mail screen. The body is
  the text of the letter that the stand sent (read from the test mailbox); the
  bar with the sender and the subject is drawn by the script.

The stand has no copy of the website. The form was filmed on a local run of
the site repository (`npx astro dev`) with two changes in its working copy
that are not committed: the API address is the stand, and the captcha key is
the test key of the captcha provider that always passes (the stand does not
check the captcha). The stand does not list a local address in its CORS
settings, thus the script sends the API calls itself (`context.route`) and no
setting of the stand changes. The one-time tokens of the letters are read from
the mail jobs of the stand (`VIDEO_STAND_PSQL`).

Before each screenshot the script replaces the test mailbox with
`carmen@example.com`, the stand name of the organization with `Aurora Events`
and the local and stand hosts with the public hosts, and it hides the captcha
box and the fixed bars. The picture `f_04_confirmed.png` of this build was a
cut of the form block put on an empty phone-size background; the script now
takes the top of the page instead.

The 16:9 film shows desktop pictures in a browser window with a close-up
(`per_format` in the scenario); 9:16 and 4:5 show the phone pictures. The
desktop pictures have the names `d_*.png`. They come from a second run of the
same scripts with `ONB_VIEW=desktop` (window 1280 x 720, new test addresses
and organization names) and from `render-letter.ts ... desktop`.

To build without the paid voice: set `VIDEO_VOICE=silent`.

### Film apply-in-telegram-bot

`scenarios/apply-in-telegram-bot` shows how a new organizer applies in the event-center bot
and gets an organization. The pictures in `captures/<language>/` are not
committed. They were made on the staging stand on 2026-11-19 with a separate
test Telegram account and scratch scripts, one full pass for each language:
`/start`, the language button, "Apply to become an organizer", all questions,
the three agreements, "Send the application". A test operator of the stand
approved the application through the operator API, and the approval message
and the menu were filmed after that.

The stand needs `ONBOARDING_ENABLED` on arena-api and
`BOT_SELF_ONBOARDING_ENABLED` on arena-bot. The bot speaks the language of the
button pressed on the welcome message; the link `?start=apply` uses the
language of the Telegram account instead. The six-digit e-mail code was read
from the job queue of the stand. Before the next pass the Telegram link of the
account has to be deleted, the test organization renamed and the Telegram id
taken off the approved application (limit: three applications a day for one
Telegram account); the product has no screen for that.

Before a screenshot the scripts replace the test bot name with the public
name, the contact card (name, picture letters, phone number), the e-mail
address and the Telegram name of the test account with the invented applicant
of the film. Each picture gets 900 px of chat background under it, so that
`scroll` can lift the lower part of the chat above the subtitles.

The staging build of 2026-11-19 has the bot in English, Russian and Spanish.
A fourth language needs its texts in the scenario and its pictures; see the
comment at the top of the scenario.

### Films bot-create-event-from-poster and bot-sales-link-and-qr

`scenarios/bot-create-event-from-poster` (an event from a poster) and `scenarios/bot-sales-link-and-qr` (sales
link and QR code) are instruction films about the event-center bot, in the
vertical formats only. The pictures in `captures/<language>/` are not
committed. They were made on the staging stand on 2026-11-19 with the test
Telegram account, which is the owner of the test organization "Luna Events",
and scratch scripts. For each language the bot was set to that language
(Settings, Language) and one full pass was made:

- bot-create-event-from-poster: Menu, "New event", "Fill in from a poster", the poster sent as a
  file, each question answered with the first fitting button, "Publish". The
  venue "Saal Aurora" was added in the first pass; the bot refuses a second
  venue with the same name, thus the film chooses the venue from the list.
- bot-sales-link-and-qr: Menu, "My events", the event, "Link for buyers", "More links
  and QR codes", the placement "Poster", a name for the location, then
  "Statistics" after one opening of the short link with a phone browser.
  A headless browser is counted as a robot and adds the line "Robots
  excluded" to the statistics.

The buyer's page is the page of the event on the tickets site, opened with
`?lang=<language>` in a phone-size browser. Payments are not connected in the
test organization, thus the films do not show a purchase.

Before a screenshot the scripts replace the test bot name, the staging hosts
(tickets and short links) and the number at the end of the brand address with
the public names. They also replace the QR pictures in the chat with a code
of the public site, because the real codes open the staging stand.

### Bot instruction films

Six more instruction films about the event-center bot, in the vertical formats
only: `bot-orders-resend-tickets`, `bot-promo-codes`, `bot-invite-manager`,
`bot-change-price`, `bot-invitations`, `bot-move-or-cancel-session`. The
pictures were made on the staging stand on 2026-11-20 in the same test
organization "Luna Events", one pass for each language and film.

- The organization has two events for each language. The first has a free
  order of the invented buyer (Carmen Ortega, an `example.com` address), made
  on the buyer's page with a 100 % promo code. The films about orders and
  about the move and the cancellation of a session use it. The second event
  is for the price and the invitations.
- The test organization has no paid orders, because payments are not
  connected. The bot answers that a free order has nothing to refund, thus
  no film shows a refund.
- A promo code name stays taken after the code is deleted, thus each language
  has its own code (LUNA10, NIGHT10, NOCHE10).
- The invited manager and the guests have `example.com` addresses. The
  manager was removed from the team after each pass.
- A session cancellation cannot be undone: after the pass the first event of
  each language has a cancelled session. A new pass needs a new event and a
  new order.
- The mask that replaces a phone number also replaces a 13-digit ticket
  barcode; the order pictures were made with that mask off.

## Environment variables

Keys, addresses and logins are never in the repository. The scripts read them
from the environment, or from `ops/video/.env` (in `.gitignore`).

| Variable | Used by | Meaning |
|---|---|---|
| `ELEVENLABS_API_KEY` | voice | Key of ElevenLabs. Without it the build uses the silent stub. |
| `ELEVENLABS_VOICE_ID` | voice | Voice that reads the text. |
| `VIDEO_BASE_URL` | capture | Address of the staging stand. |
| `VIDEO_LOGIN_EMAIL`, `VIDEO_LOGIN_PASSWORD` | capture | Test user of the test organization. |
| `VIDEO_ORG_ID` | capture | Identifier of the test organization. |

Capture rules:

- The capture script works only with a staging host, `localhost` or a local
  file. It refuses a host that has no `staging` label. There is no flag that
  switches this check off.
- Only test data must be in the frame: the test organization ABH TEAM.
- The one host that is not a staging host and is allowed is `web.telegram.org`,
  and only through `src/telegram.ts` (see "Telegram bot capture").

## Voice

Two providers with one interface (`src/voice/`):

- `elevenlabs`: calls the API and keeps each answer in `.cache/voice/` under a
  hash of the text, voice and model. A build that changes one scene pays for
  one scene.
- `silent`: no network. Makes a silent WAV file. Its length comes from the
  number of words (2.5 words per second, not less than 1.5 seconds). The video
  timing is thus near the timing with a real voice.

`provider: auto` uses ElevenLabs when `ELEVENLABS_API_KEY` is set and the
silent stub when it is not.

## Subtitles

The narrator text of each scene is divided into cues of not more than two
lines of 42 characters. The time of a scene is divided between its cues in
proportion to their length. The same cues are written to `.srt` and `.vtt` and
burned into the video, thus the three always agree.

With the silent stub and with the plain ElevenLabs call this timing is an
estimate. See "Not done yet" for exact timing.

## External services: what was checked

Checked on 2026-11-18 on the official pages. Prices change: check again before
you buy.

### ElevenLabs

- Call: `POST https://api.elevenlabs.io/v1/text-to-speech/{voice_id}`, header
  `xi-api-key`, JSON body with `text`, `model_id` (default
  `eleven_multilingual_v2`), optional `voice_settings` (`stability`,
  `similarity_boost`, `style`, `speed`), `seed`, `previous_text` and
  `next_text`. Query parameter `output_format`, default `mp3_44100_128`. The
  answer is the audio file. Some output formats need a higher plan (44.1 kHz
  PCM is listed for Pro).
  Source: <https://elevenlabs.io/docs/api-reference/text-to-speech/convert>
- A second call, `.../{voice_id}/with-timestamps`, returns `audio_base64` and
  the start and end time of each character. This gives exact subtitle timing.
  Source: <https://elevenlabs.io/docs/api-reference/text-to-speech/convert-with-timestamps>
- Models that read Russian and English: `eleven_v4`, `eleven_v3`,
  `eleven_multilingual_v2` (described as the most stable on long text),
  `eleven_flash_v2_5` (lower price). The limit of one request is 5,000 to
  40,000 characters, which is much more than one scene. `language_code` is not
  supported by `eleven_multilingual_v2`.
  Source: <https://elevenlabs.io/docs/models>
- Price of the API: 0.08 USD for 1,000 characters for v4, v3 and Multilingual
  v2; 0.04 USD for the Flash and Turbo models. Included each month for the
  0.08 USD models: Free 10,000 characters, Starter (6 USD) 75,000, Creator
  (22 USD) 275,000, Pro (99 USD) 1,238,000.
  Source: <https://elevenlabs.io/pricing/api>
- The plan table shows a commercial licence from the Starter plan. It does not
  show one for the Free plan. Thus videos for customers need Starter or higher.
  Source: <https://elevenlabs.io/pricing>
- Estimate: a 70-second video is about 1,000 characters of Russian text, that
  is about 0.08 USD for one full voicing. A library of 40 two-minute videos in
  two languages is about 140,000 characters for one full pass. The Starter plan
  is sufficient for the first videos; the full library with repeated takes
  needs Creator for one or two months. This is an estimate from the prices
  above, not a measured number.

### Remotion licence

- Remotion is not under an open-source licence. The free licence is for
  individuals and for organizations of not more than 3 people. A larger company
  must buy the company licence.
  Source: <https://www.remotion.dev/docs/license/pricing>
- People are counted for the whole organization, not only those who use
  Remotion. Contractors who work with the Remotion project are counted. A
  client who only gets the finished video is not counted.
  Source: <https://www.remotion.dev/docs/license/faq>
- Videos made with the free licence can be used for business. No account and
  no key is necessary. Rendering on your own computer sends no telemetry
  unless you switch it on.
  Source: <https://www.remotion.dev/docs/license/faq>
- Company licence: 25 USD for each seat each month for videos made by people;
  0.01 USD for each render with a minimum of 100 USD each month for automatic
  video products. Enterprise from 500 USD each month.
  Source: <https://www.remotion.dev/docs/license/pricing>
- Decision for the owner: if the organization has not more than 3 people
  (contractors who work on these videos included), the free licence is
  sufficient. If it has more, buy one seat (25 USD each month) before these
  videos are published. This repository is public and AGPL; the Remotion
  licence applies to who renders, not to who reads the code.

## Not done yet

- Capture of the Telegram bot (Telegram Web in the phone frame size).
- Exact subtitle timing from the ElevenLabs `with-timestamps` call.
- The OpenAI TTS provider as a spare.
- Background music and a logo sting.
- Upload to YouTube or Cloudflare Stream.

## How-to films of the web event center (web-*)

Fourteen films show the organizer's web event center in a browser window. They have one format, 16x9.
Two more films of the same script are for the BUYER (`buyer-*`): a phone frame, formats 9x16 and 4x5.

| Film | Subject | Stage of the capture script |
|---|---|---|
| `web-create-event` | add an event by hand, upload the poster, publish, the buyers' page | `event` |
| `web-payments-status` | the section "Payments from buyers" and its two notes | `pay` |
| `web-organization-details` | organization data, contact e-mail, the seller block on the buyers' page | `org` |
| `web-sales-link-and-qr` | a tracked sales link, its QR code and its scan count | `links` |
| `web-promo-codes` | a new promo code and the cart of a buyer with the code | `promo` |
| `web-orders-and-refund` | the orders list, the order card, sending tickets again, the refund screens | `order`, `refund` |
| `web-invitations` | a free ticket for a named guest | `invite` |
| `web-invite-manager` | an invitation of a manager to the team | `team` |
| `web-change-price` | a new price of a category and the buyer page | `price` |
| `web-move-or-cancel-session` | moving a date to a new day, cancelling it | `move`, `cancel` |
| `web-brands` | a brand (the name under which tickets are sold) and its public page | `brand` |
| `web-sales-statistics` | totals of an event, page statistics, the sales funnel, the summary for a period | `stats` |
| `web-buyer-messages-and-notifications` | the unpaid-order reminder, the news subscription, sales notifications by the Telegram bot | `messages` |
| `web-entrance-and-guest-list` | where the ticket scanner is (a mobile app), the guest list as Excel, the tickets of a date | `entrance` |
| `buyer-where-is-my-ticket` | the page after the order, the ticket letter, the PDF, the form "Write to the organizer" | `ticket` |
| `buyer-how-to-read-the-date` | the date of the event on the page, in the letter and on the ticket, the line "Doors open" | pictures of `ticket` |

1. Draw the poster of the invented event: `npx tsx scripts/make-web-posters.ts`.
2. Take the pictures for each language, in this order: `npx tsx scripts/capture-web.ts <ru|en|es> event`,
   then `org`, `links`, `pay`, `promo`, `order`, `refund`, `invite`, `team`, `price`, `move`, and `cancel` last. The script signs in as the test organizer of film apply-on-website
   (`VIDEO_ONB_EMAIL_<LANG>`, `VIDEO_ONB_ORG_<LANG>`, `VIDEO_ONB_PASSWORD`, `VIDEO_BASE_URL` in `.env`).
3. Build: `npm run build:video -- web-create-event ru` and so on.

Rules of these pictures:

- The script opens only staging hosts. It replaces the stand data in the page before each picture
  (mailbox, organization name, `-staging` in hosts) and puts the real text back after it.
- Stage `event` creates a real event on the stand. `reset` deletes that event (off sale, "Delete", the typed word).
  `card` repeats the pictures of the event card: it takes the event off sale first and publishes it again. `WEB_STOP=ready` stops before the event is created.
  `WEB_ONLY=<regexp>` takes only the pictures with such names.
- Stage `links` creates a real link. `WEB_LINK_EXISTS=1` opens the first link of the list (the link of an earlier run) and does not create a second one.
  The QR code in the pictures is drawn for the short address on the public host, not for the address of the stand.
  The buyer's visit uses an ordinary browser name: the stand counts a headless browser as a bot, not as a scan.
- Stage `pay`: the web event center has no screen where an organizer selects a provider or types its keys.
  `WEB_PAY_CONNECTED=1` replaces the stand's answer "payment configs" with one accepted config, and the screen
  draws its own note "connected" (picture `p_05_connected`). Without the variable this picture is not taken.
- Stage `order` makes a real FREE order on the stand: a promo code of 100 % (`GUEST100`), the buyer Carmen Ortega,
  the mailbox of the test organizer. No card is typed and no payment page is opened. For a free order the
  screen "Refund" answers that there is nothing to refund; the refund of a paid order is not filmed.
- Stage `team` sends a real invitation letter of the stand to `marco@example.com`.
- Stages `move` and `cancel` send real letters of the stand to the holders of tickets. Run them only for the
  event of stage `event`, whose holders are the test addresses of these films. `WEB_STOP=move` and
  `WEB_STOP=cancel` stop before the change is saved.
- A half-hidden address on a screen (`a***@a***.com`) is replaced too: it keeps the first letters of the real mailbox.
- A film is named by what the function does, not by the name of its screen: the screen `/promoters` is "Brands",
  `/growth` is "Messages to buyers", and the web event center has no ticket scanner (the screen "Entrance" sends
  the organizer to the scanner app), so no film shows a scan.
- Stage `brand` creates the brand "Aurora Jazz Club"; stage `messages` switches on the reminder and the subscription.
- Stage `ticket` needs an event with the line "Doors open": create it with `WEB_DOORS=19:30` at stage `event`.
  It makes a free order in a phone window (390 x 844) and saves the PDF ticket to `.cache/ticket-<lang>.pdf` when
  the page shows its link. The masks of the pictures work inside the ticket widget too (shadow DOM).
- The ticket letter of the buyer films is drawn again from the TEXT of the real letter: copy its lines to
  `scenarios/buyer-where-is-my-ticket/captures/letters/<lang>.json` with the invented addresses, then
  `npx tsx scripts/make-ticket-letter.ts <fields.json> <letter.json>` and `npx tsx scripts/render-letter.ts`.
  The PDF ticket becomes a picture with PyMuPDF; the organization name and the contact address in it are replaced.
  `buyer-how-to-read-the-date` uses copies of these pictures in its own `captures/`.
- `look` prints the text of a screen of the event center and takes no picture; use it to read a screen before a film is planned.
- The web event center has no logo of the organization, so `web-organization-details` does not show one.
- Stage `org` writes the mailbox of the test organizer as the contact e-mail and as the e-mail of the sales page.
  The stand sends buyers' letters and messages of the page form to it; the pictures show `carmen@example.com`.

## Covers

Covers are still images for the videos, in the Arena Sold Out Instagram style.
Run `npm run covers` to make them.

- `covers/cover.html` is the one template. `covers/covers.yaml` holds the texts and the subject images.
- The script writes each cover next to its film: `out/<film>/<lang>/<film>_<lang>_<format>_cover.png` (folders are created).
- Formats of a film: the `formats` list in `scenarios/<film>/scenario.yaml` (16x9, 9x16, 4x5 when the key is missing), plus 16x9 always for YouTube.
  A film can name its cover formats itself with `formats:` in `covers/covers.yaml`: the films of the web event center are 16x9 only, and their covers are made in all three formats. A film with no scenario in this repo (onb-02) gets all three.
- Films: sell-tickets-from-poster, apply-in-telegram-bot, apply-on-website, the bot instruction films (names start with `bot-`), the films of the web event center (`web-`) and the films for the buyer (`buyer-`). Languages: ru, en, es.
- Long lines get a smaller font by themselves, so they never leave the frame.
- Subject images live in ignored folders. If a file is missing, the cover is made without it.
- Fonts are system fonts (Impact, Inter, Segoe UI). Nothing is loaded from the network.
