# Telegram buttons in the Arena UX journey

Use this only for the already authorized user-session and only in the direct dialog with `@EventsCentrBot`.

## Why the normal adapter is insufficient

The `hermes-telegram-user` adapter is appropriate for read-only history and confirmed text sends, but a bot's inline buttons carry callback payloads. A label shown on a button is not necessarily valid plain-text input. Do not replace a button press by sending its caption as a message.

## Procedure

1. Send a command or text through the confirmed adapter and wait 2–3 seconds.
2. Read the newest bot message through the existing Telethon user-session; inspect `message.buttons` and record the exact labels.
3. Click only an observed, expected callback label with `message.click(text=<exact label>)`.
4. Wait 2–3 seconds, then read the newest bot message and its buttons again before the next action.
5. If the expected button is absent, stop that branch and record the mismatch. Do not guess callback data, send a button label as text, or inspect other dialogs.

## Minimal safe pattern

Use the existing session configuration internally; do not print the Telegram API values, session path, cookies, or other credentials.

```python
message = await client.get_messages(bot, ids=message_id)
labels = [button.text for row in (message.buttons or []) for button in row]
if expected_label not in labels:
    raise RuntimeError(f"Expected callback is absent: {expected_label}")
await message.click(text=expected_label)
await asyncio.sleep(3)
```

This preserves the user-visible button path and prevents a false negative caused by treating an inline callback as free text.

## Batch runner for long wizards

For a wizard with many steps (application Z2, event master Z7/B2), do not do one send/read per
call: run one Python script that drives the whole sequence and prints each step's text + buttons.
Read the newest bot message fresh before every action (buttons expire after each action; the
list screen can change).

```python
async def latest(c):
    msgs = await c.get_messages('@EventsCentrBot', limit=2)
    x = [v for v in msgs if not v.out][0]
    return x, (x.raw_text or '')[:400], [b.text for r in (x.buttons or []) for b in r]

async def click(c, label):
    x, _, labels = await latest(c)
    if label not in labels:
        raise RuntimeError({'step': (x.raw_text or '')[:300], 'missing': label, 'saw': labels})
    await x.click(text=label); await asyncio.sleep(3)
    _, t, b = await latest(c); return t, b

async def send(c, text):
    await c.send_message('@EventsCentrBot', text); await asyncio.sleep(3)
    _, t, b = await latest(c); return t, b
```

Run the whole sequence in `main()` with `print` per step. The helpers raise with the exact
missing label and the current screen, so a flow deviation stops the script instead of silently
clicking the wrong thing.

**Entry-point normalization:** Before starting, check the current screen. If the latest buttons
are the events list (`✔ Running · N`, `Archive · M`, event entries, `Menu`) but the menu button
`+ Event` is not there, click `Menu` first. If the latest message has no buttons at all (a media
message like a sample ticket PDF), send `/events` or `/start` to return to a known state before
proceeding. A helper like this avoids the `UnboundLocalError: cannot access local variable 'b'`
that occurs when `click()` is called in a conditional branch where `b` was never assigned:

```python
x, t, b = await latest(c)          # always assign b first
if 'Menu' in b:
    t, b = await click(c, 'Menu')
t, b = await click(c, '+ Event')   # now b is always defined
```

## Pitfalls that break the script

- Index the coroutine AFTER awaiting it: `(await client.get_messages(...))[0]`, never
  `await client.get_messages(...)[0]` — the latter raises `TypeError: 'coroutine' object is not
  subscriptable` after the click already went through, so the next `read` then shows an unknown
  state. The same slip recurs when copying the pattern into longer sequences; write the helper once
  and reuse it.
- **Диалог устарел — нажми Menu.** После паузы ≥ 30 минут попытка нажать кнопку или отправить
  команду возвращает «I did not understand that. Use the buttons below.» — старый диалог истёк,
  и бот не может найти контекст. Восстановление: нажми кнопку `Menu` (или отправь `/start`),
  затем начни заново. Не отправляй /new или другую команду, не вернувшись в меню.

## Sending files (posters)

The `hermes-telegram-user send` adapter only sends text. To deliver a poster or any media, call
Telethon directly: `await client.send_file(bot, path, force_document=True)`. Send as a document
(not as a photo) so Telegram does not recompress; the bot's acceptance message reports the exact
received dimensions.

- The file must arrive while the wizard is at the "Send the poster" step. Sending a file from the
  main menu (or after the wizard finished) answers "I did not understand that. Use the buttons
  below." — click `📷 Fill in from a poster` first, then send.
- OCR takes up to ~25–45 s after the file lands: sleep, then read the newest message. Do not send
  the file twice while the first read is still running (rate limit + duplicate poster).

## Contact-sharing reply keyboard

The Z2 phone step is not an inline callback: it is a Telegram reply-keyboard button of type `KeyboardButtonRequestPhone`. Inspect the button type before acting. Only at that authorized Z2 step, after confirming the exact visible label, use `message.click(text=<exact label>, share_phone=True)`; this sends the phone number of the authenticated Telegram account to the bot. Do not substitute a typed number, and never use `share_phone=True` at any other step.

```python
expected_label = "📱 Share my number"
labels = [button.text for row in (message.buttons or []) for button in row]
if expected_label not in labels:
    raise RuntimeError(f"Expected contact button is absent: {labels}")
await message.click(text=expected_label, share_phone=True)
await asyncio.sleep(3)
```

Treat a contact-share control as a distinct action from a callback: clicking it without `share_phone=True` does not reproduce the user-visible flow.
