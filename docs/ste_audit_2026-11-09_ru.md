# Проверка английских текстов на соответствие ASD-STE100 Issue 9

Дата: 2026-11-09. Код: `origin/master` 0eb18b66 (08.11.2026). Стандарт: ASD-STE100 Issue 9 (2025-01-15), PDF из `github.com/avamb/ASD-STE100_Issue9`.

Проверены четыре части: Telegram-бот, веб-ивент-центр, админка (SuperAdmin), генератор площадок (venue-svg-generator). Только английский. Другие языки адаптируем после.

## 1. Итог

**Ни одна часть не соответствует STE полностью.** Грамматика уже близко к STE: это результат стандарта текстов от 13.10. Словарь далеко: примерно 6 строк из 10 содержат слово, которого нет ни в словаре STE, ни в проектных терминах.

| Часть | Строк (EN) | Без замечаний | Слова в норме | Грамматика и знаки в норме | Действительный залог нарушен |
|---|---:|---:|---:|---:|---:|
| Telegram-бот (`bot.*` в `locales/en.toml`) | 1 969 | 36 % | 40 % | 87 % | 107 строк |
| Ивент-центр (`event-center/src/lib/i18n/en.ts`) | 3 070 | 38 % | 44 % | 87 % | 249 строк |
| Админка (`admin-web`) | 2 401 | 31 % | 36 % | 90 % | 68 строк |
| Генератор площадок | 43 | 12 % | 12 % | 88 % | 1 строка |

«Слова в норме» = все слова есть в словаре STE в разрешённой форме, или это технический термин по категориям STE, или термин из проектного глоссария (раздел 4). «Грамматика» = длина фразы, времена, модальные глаголы, фразовые глаголы, точка с запятой, тире, сокращения.

Три главных вывода:

1. **Главная работа — словарь, а не грамматика.** Самые частые нарушения: `yet` (247), `already` (205), `choose` (184), `another` (147), `now` (147), `nothing` (223), `so` (186), `pick` (105), `please` (53).
2. **Один предмет называют разными словами** (правило 1.11). Это нужно решить до переписывания, иначе перепишем дважды. Список решений — раздел 5.
3. **Админку сейчас нельзя перевести.** Из 2 401 английской строки только 136 лежат в каталоге (`lib/i18n/catalog.ts`). Остальные 2 265 написаны прямо в коде экранов (`routes/events.tsx` 256, `routes/organizations.tsx` 232 и т. д.). Сначала их надо вынести в каталог.

## 2. Что проверено и как

| Часть | Источник текстов |
|---|---|
| Бот | все ключи `bot.*` (формы `other` и `one`) из `apps/backend/internal/platform/i18n/locales/en.toml` |
| Ивент-центр | каталог `apps/event-center/src/lib/i18n/en.ts` (3 602 ключа, 3 070 разных текстов). Вне каталога в экранах английского текста нет, это хорошо |
| Админка | английская часть `catalog.ts` и `analyticsStrings.ts`, плюс текст в JSX и строковые свойства (`label`, `title`, `placeholder`, `aria-label`, `hint` и др.) во всех `.tsx`/`.ts` без тестов |
| Генератор | строки сообщений в Python-коде без тестов и экспериментов (`service/`, `arena_export/`, `seating_svg_renderer/`, `xlsx_seating_adapter/`, `m1_product/`) |

Проверка автоматическая:

- Словарь STE (части 2 стандарта) разобран из PDF: 800 разрешённых слов с формами и около 1 400 запрещённых с заменами.
- Слово вне словаря считается допустимым, если это технический термин: примеры из 22 категорий технических существительных и 4 категорий технических глаголов STE (правила 1.5, 1.12) или проектный глоссарий (раздел 4).
- Правила фразы: 5.1/6.3 (20 слов для инструкции, 25 для описания), 3.2/3.4 (времена, `would/could/should/may`), 3.5 (`-ing`), 3.6 (пассив), 4.2 (сокращения), 8.1 (`;`, тире-связка, правило P15 проекта), 9.3 (фразовые глаголы), 1.14 (британское написание), 1.11 (синонимы).

Ограничения, их надо помнить при чтении цифр:

1. Нет разбора по частям речи. Например, `order` (существительное, заказ) — наш термин, а `order` (глагол, «приказать») запрещён. Проверка их не различает.
2. Пассив разделён на два вида. `was sent`, `will be sent`, `cannot be undone` — настоящий пассив, нарушение 3.6. `is paid`, `are sold` — причастие как прилагательное, правило 3.3 его разрешает. В таблице выше только первый вид.
3. Короткие подписи (до 6 слов без точки: кнопки, заголовки) проверены только по словам, не по грамматике.
4. Цифры — это кандидаты на правку. Каждую правку делает человек или агент по контексту.

Не проверялись: письма покупателю (`delivery/templates`), виджет, страница билетов, страница магазина, тексты ошибок API в `openapi.yaml`. Их можно проверить тем же инструментом следующим шагом.

## 3. Нарушения по правилам

Число строк с нарушением в каждой части:

| Правило STE | Что нарушено | Бот | Ивент-центр | Админка | Генератор |
|---|---|---:|---:|---:|---:|
| 1.1 | слово не из словаря и не термин | 1 164 | 1 680 | 1 485 | 37 |
| 1.11 | синоним вместо термина глоссария | 135 | 208 | 169 | 0 |
| 3.6 | пассив действия (`was sent`, `be undone`) | 107 | 249 | 68 | 1 |
| 3.5 | форма на `-ing` (`waiting`, `loading`, `working`) | 81 | 111 | 145 | 1 |
| 3.2/3.4 | сложные глагольные формы и модальные (`may`, `could`, `will be`) | 75 | 80 | 30 | 1 |
| 9.3 | фразовые глаголы (`come back`, `wait for`, `fill in`, `go to`) | 64 | 93 | 25 | 0 |
| 3.2 | перфект (`has not joined`, `has already passed`) | 34 | 47 | 18 | 0 |
| 8.1 | точка с запятой или тире, склеивающее две мысли | 17 | 4 | 136 | 4 |
| 1.14 | британское написание (`colour`, `programme`, `cancelled`) | 16 | 24 | 13 | 0 |
| 1.4/3.1 | неразрешённая форма глагола | 15 | 12 | 24 | 0 |
| 5.1/6.3 | фраза длиннее 20/25 слов | 3 | 6 | 12 | 0 |
| 4.2 | сокращения (`don't`) | 1 | 0 | 1 | 0 |

### 3.1 Частые запрещённые слова и замены по STE

| Слово | Сколько | Замена по STE | Пример правки |
|---|---:|---|---|
| yet | 247 | убрать или `at this time` | «No buyers yet» → «There are no buyers» |
| already | 205 | обычно просто убрать | «That date has already passed» → «This date is before today» |
| choose / pick | 184 / 105 | `select` | «Pick today or later» → «Select today or a date after today» |
| another | 147 | `one more`, `a different` | «Add another date» → «Add one more date» |
| now | 147 | убрать или `at this time` | |
| nothing / everything / anything | 223 / 23 / 14 | `no` + существительное, `all` | «Nothing was deleted» → «We did not delete the event» |
| so (в значении «поэтому») | 186 | две фразы или `thus` | «…does not match, so nothing was deleted» → «…is not the same. We did not delete…» |
| please | 53 | слова нет в STE | см. решение 5.10 |
| create | 108 | `make` | «Create a session» → «Make a session» |
| reason | 108 | `cause` | |
| every / whole | 104 / 74 | `all`, `each`, `full` | |
| ask | 81 | `tell`, `speak` | |
| leave | 65 | `keep`, `stay`, `go` | |
| failed | 65 | `if … not` | «Upload failed» → «The upload did not complete» |
| once | 61 | `one time`, `when` | |
| requires / need | 57 / 69 | `necessary` | «Requires a name» → «A name is necessary» |
| later | 50 | `then`, `after`, лучше точное время | |
| undone (`cannot be undone`) | 34 | | «This cannot be undone» → «You cannot go back after this step» |
| confirm | 36 | `make sure` | |
| per | 53 | `for each` | |
| still | 40 | `continue`, `stay` | |
| what, why, own, like, itself, someone, nobody | 56, 20, 78, 52, 37, 20, 14 | слов нет в STE | перестроить фразу |

Полный список по каждой строке — в файлах раздела 7.

### 3.2 Примеры «было / стало»

Это черновики для показа подхода, не утверждённый текст.

**Бот**

1. `bot.sl.err_apply`
   - Было: The seats could not be changed. Nothing was applied. Please check the categories and try again.
   - Стало: We did not change the seats. Make sure that the categories are correct. Then try again.
   - Правила: 3.4 (`could`), 3.6 (пассив), 1.1 (`nothing`, `please`, `check` как глагол).
2. `bot.ses.no_buyers`
   - Было: This session has no buyers yet, so no emails will be sent.
   - Стало: This session has no buyers. We do not send e-mails.
   - Правила: 1.1 (`yet`, `so`), 1.11 (`emails` → `e-mail`), 3.6.
3. `bot.evs.delete_wrong`
   - Было: The text does not match, so nothing was deleted. To delete, type exactly: {{.Word}}
   - Стало: The text is not the same as the word. We did not delete the event. To delete the event, type this word: {{.Word}}
4. `bot.team.revoke_confirm`
   - Было: Revoke the invitation for {{.Email}}? The link in the e-mail stops working, and if they have not joined yet, I remove them from the team.
   - Стало: Cancel the invitation for {{.Email}}? Then the link in the e-mail does not operate. If this person did not open the link, the bot removes this person from the team.
   - Правила: 3.2 (перфект), 3.5 (`working`), 1.1 (`revoke`, `yet`), бот говорит «I» (см. решение 5.11).
5. `bot.dp.card_lowvalue`
   - Было: Advice: this amount is under your small-amount limit of {{.Amount}}. Contesting costs about €20, returned only if you win. Accepting may cost less.
   - Стало: Note: this sum is less than your limit of {{.Amount}}. A dispute costs approximately €20. You get this money back only if the bank decides for you. If you accept the dispute, it can cost less.

**Ивент-центр**

6. `dateField.error.past`
   - Было: That date has already passed. Pick today or later.
   - Стало: This date is before today. Select today or a date after today.
7. `password.otherSession`
   - Было: This browser is already signed in to the event center. When you save the password, we sign out here, so that you sign in with the new password.
   - Стало: You are signed in to the event center in this browser. When you save the password, the event center signs you out. Then sign in with the new password.
8. `poster.fail.attach`
   - Было: The picture was uploaded, but it is not the event's poster yet. The current poster has not changed.
   - Стало: We uploaded the image, but we did not attach it to the event. The event keeps its poster.
9. `orgData.status.incomplete`
   - Было: The seller data is not complete, so it is not shown on the sales page yet. Please fill in:
   - Стало: The seller data is not complete. The sales page does not show it. Fill these fields:
10. `categories.qty.belowUsed`
    - Было: Not saved. {floor} places are sold or on hold. Type {floor} or more.
    - Стало: Not saved. {floor} seats are sold or in a hold. Type {floor} or more.
    - Зависит от решения 5.1 (`place` или `seat`).

**Админка**

11. Было: No sessions yet -- create a session before adding ticket tiers.
    - Стало: There are no sessions. Make a session before you add categories.
12. Было: ✓ Registered. Save the signing secret — it will not be shown again.
    - Стало: ✓ Done. Save the signing key. This page shows it only one time.
13. Было: The venue no longer exists. Refresh the list.
    - Стало: We cannot find this venue. Reload the list.

**Генератор**

14. Было: Generated from seating-json-v1; coordinates are worksheet grid units.
    - Стало: The generator made this drawing from seating-json-v1. The coordinates are in grid units of the worksheet.

### 3.3 Особенности каждой части

- **Бот.** Лучше всего по грамматике. Основное: `yet/already/now/so/nothing`, бот говорит о себе «I» (83 строки), а слова `I` в словаре STE нет. `we` разрешено (значит «компания»). `please` в 37 строках.
- **Ивент-центр.** Каталог чистый по структуре, весь текст в одном файле. Больше всего пассива (`Nothing was changed`, `The letter was sent`) и синонимов (`place` 92 раза, `picture` 43, `hall` 17, `mail` 14, `goods` 16).
- **Админка.** Много текста для разработчика: `endpoint`, `UUID`, `config`, `cross-tenant`, `backend`, `idempotent`, `dry-run`, пути API прямо в подсказках («No /v1/admin/orders/{id}/tickets endpoint yet»). 136 строк с `;` или тире. По STE такие тексты либо переписываются для оператора, либо получают свой список технических терминов (раздел 4, группа «Админка»). Слова `tier`, `customer`, `reservation`, `org` расходятся с глоссарием.
- **Генератор.** Сейчас его сообщения видит только разработчик: в `master` нет кода arena-api или ивент-центра, который показывает их пользователю. Покупателю и организатору доходят только подписи внутри SVG («Venue plan (authoring export)», описание файла). Когда генератор подключат к ивент-центру, ошибки надо показывать текстом каталога ивент-центра по коду ошибки (`queue.full`, `job.quarantined`), а не сырым `message`.

## 4. Проектный глоссарий технических терминов (на утверждение)

STE разрешает слово вне словаря, если это технический термин вашей области (правила 1.5, 1.6, 1.8, 1.12). Он должен быть один на одно понятие (1.11), коротким и понятным (1.9), без жаргона (1.10). Ниже — предложение. Номер категории STE указан в скобках: 9 числа и время, 11 роли и организации, 15 документы, 19 компьютеры и связь, 20 услуги и продукт, 21 право и торговля.

Многие слова STE сам приводит как примеры технических терминов: `e-mail`, `file`, `field`, `menu`, `screen`, `phone`, `chatbot`, `customer`, `operator`, `page`, `letter`, `day`, `hour`, `month`, и технические глаголы `click`, `tap`, `press`, `type`, `enter`, `save`, `delete`, `copy`, `open`, `close`, `upload`, `download`, `filter`, `sort`.

### 4.1 Существительные

| Группа | Термины |
|---|---|
| Продажа билетов (20) | event, session, ticket, order, category, venue, seat, seating plan, row, zone, hold, invitation, guest list, sample ticket, poster, sales end, doors |
| Деньги (21) | price, payment, payment provider, refund, discount, promo code, cashback, credit, fee, invoice, receipt, currency, tax, VAT, dispute, chargeback, amount, sum |
| Роли (11) | buyer, organizer, promoter, owner, manager, operator, guest, seller, partner, applicant, administrator, superadmin, team, organization, platform, support |
| Названия (11) | Arena, Telegram, Stripe, Flitt, WordPress, Bil24, MACS, Google, Meta |
| Связь и интерфейс (19) | bot, e-mail, site, link, password, account, button, message, chat, menu, screen, field, file, image, browser, QR code, barcode, scanner, storefront, channel, sales link, queue, draft, preview, archive, version, layout, map, tab, report, setting, notification, template, widget |
| Документы (15) | page, text, description, caption, terms, privacy policy, statement, application, form, list |
| Магазин (5, 20) | shop, product, stock, delivery, shipping, variant |
| Время и единицы (9) | day, hour, minute, week, month, year, today, tomorrow, percent, character, digit |
| Только админка (19) | webhook, endpoint, token, key, API, ID, slug, feed, import, export, audit, permission, role, scope, log, network |

### 4.2 Глаголы

| Группа | Термины |
|---|---|
| Интерфейс (STE, категория 2) | click, tap, press, type, enter, select, copy, paste, save, delete, edit, open, close, filter, sort, scroll, print, upload, download, import, export, share, reload, scan |
| Вход | sign in, sign out (как технические глаголы, чтобы не нарушать 9.3) |
| Продажа билетов | sell, buy, pay, refund, publish, unpublish, invite, issue, reissue, resend, charge, archive |

Правило 1.7: технический термин-существительное нельзя использовать как глагол. `refund` и `archive` есть в обоих списках сознательно: это устойчивые глаголы отрасли. Если владелец против, глагол `refund` заменяется на `give the money back`.

## 5. Решения владельца

**Утверждено 2026-11-09:** все предложения таблицы ниже, кроме 5.10 и 5.11. По ним владелец решил: тёплый тон важнее строгого STE, `please` и «I» в боте остаются. Глоссарий раздела 4 и эти решения внесены в `docs/text_style_standard_ru.md` (разделы 4, 4.0, 11).

Правило 1.11: один предмет — один термин. На момент проверки в текстах было так (сумма по четырём частям):

| № | Предмет | Сейчас в текстах | Предложение |
|---|---|---|---|
| 5.1 | место на площадке | `seat` и `place` (168 строк с `place`) | `seat` для мест на схеме. Для входных билетов без мест писать количество в `tickets`. `place` по STE запрещено (значит «положение») |
| 5.2 | площадка | `venue` и `hall` (35) | `venue` для площадки, `seating plan` для схемы зала. `hall` убрать |
| 5.3 | картинка | `picture` (54), `image` (8), `photo` | `image`. Для афиши — всегда `poster` |
| 5.4 | электронное письмо | `e-mail`, `email` (40), `mail` (19), `letter` | `e-mail` (так в STE). `letter` — калька с «письмо», заменить на `e-mail` |
| 5.5 | сайт | `site` и `website` (27) | `site` |
| 5.6 | категория билетов | `category` и `tier` (38, в основном админка) | `category` |
| 5.7 | бронь | `hold`, `reservation` (13), `reserve` | `hold` |
| 5.8 | сбор | `fee`, `commission` (12), `service charge` (15) | Это, похоже, два разных понятия: сбор с покупателя и комиссия платформы. Нужно два термина: `service fee` и `platform fee` |
| 5.9 | покупатель | `buyer` и `customer` (32, админка) | `buyer` (по глоссарию 13.10) |
| 5.10 | вежливость | `please` в 53 строках | В STE слова нет. Принцип P18 стандарта проекта требует теплого тона. Предложение: убрать `please` из инструкций, оставить только в просьбах к покупателю |
| 5.11 | голос бота | бот говорит «I» (83 строки) | `I` нет в словаре STE. Предложение: `we` (разрешено, значит «компания») или «the bot» |
| 5.12 | товар | `product`, `goods` (21), `item` (10) | `product` |
| 5.13 | обновить | `reload` и `refresh` (9) | `reload` |
| 5.14 | организация | `organization`, `org` (16), `company` (4) | `organization` |

## 6. Что предлагаю делать дальше

1. **Утвердить глоссарий и решения раздела 5.** Внести английские термины в `docs/text_style_standard_ru.md` (раздел 4).
2. **Расширить автоматическую проверку.** Сейчас `text_style_test.go` проверяет только `bot.*`. Добавить такую же проверку для `event-center/src/lib/i18n/en.ts` (vitest) и каталога админки: синонимы глоссария (красный тест), `;`, тире, длина фразы, пассив (предупреждения). Словарь STE в репозиторий не кладём: стандарт защищён авторским правом ASD, и это уже решение 13.10. Полная проверка по словарю STE остаётся локальным инструментом (раздел 7).
3. **Переписать английский текст волнами:** бот (≈1 260 строк с замечаниями) → ивент-центр (≈1 900) → письма покупателю → админка (≈1 650, после выноса текста из кода в каталог). Каждую волну перепроверить инструментом.
4. **Генератор:** привести к STE подписи в SVG сейчас. Сообщения об ошибках — когда его подключат к ивент-центру, через коды ошибок.
5. **Перевод.** После утверждения английского обновить ru, es, cs, he и остальные языки от нового английского исходника.

## 7. Подробные файлы

Лежат в `C:\Projects\arena_new\.claude\ste-audit\` (локально у владельца). В git не добавлять: в файлах есть замены из словаря STE, а его текст защищён авторским правом ASD.

| Файл | Что внутри |
|---|---|
| `telegram-bot_findings.csv` | каждая строка бота: ключ, текст, правило, слово, подсказка |
| `event-center_findings.csv` | то же для ивент-центра |
| `admin-web_findings.csv` | то же для админки, с файлом и номером строки кода |
| `venue-svg-generator_findings.csv` | то же для генератора |
| `scripts/` | инструмент проверки: `parse_dict.py` (словарь из PDF), `extract_ts.cjs`, `extract_py.py` (сбор текстов), `glossary.py` (проектные термины), `check.py` (проверка) |

Повторный запуск: склонировать репозиторий стандарта, разобрать PDF `parse_dict.py` в `dict.json`, собрать тексты экстракторами, затем `check.py dict.json <папка> <текст стандарта> <файлы .jsonl>`. Файлы открываются в Excel (UTF-8 с BOM).
