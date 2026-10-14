# Журнал автономного прогона ивент-центра (октябрь 2026)

Ведёт оркестратор (Claude) по `08_architecture/40_event_center_execution_spec_ru.md` (влита PR #110). Содержание — по `docs/plans/event_center_unified_plan_2026-10-14_ru.md` (решения владельца 1–18). Это единственное место, откуда владелец узнаёт ход прогона: что сделано, какие ворота пройдены, что НЕ проверено, какие развилки оркестратор закрыл сам и какие вопросы ждут владельца.

Правила записи: по каждой задаче — ветка, PR, коды выхода ворот (раздел 3 спеки), что не проверено; «Решения прогона» — развилки, закрытые оркестратором, с обоснованием; «Вопросы владельцу» — только стоп-условия ST-1…ST-7 (раздел 4 спеки). Время — Мадрид.

## 0. Старт прогона — 14.10.2026

- `origin/master` на старте: `d8695cb` (PR #110, спека 40).
- Голова миграций: **0152** (`0152_event_watch_site_sync.sql`). Резерв спеки 0153…0166 свободен, чужих веток с номерами из резерва нет — ST-5 не сработало. В волне A миграций нет (этап 0, этап 3, П1a/П1b/П1d, G1 — без миграций; П1b: флаги канала живут в `sales_channels.settings` jsonb).
- Решения владельца 1–18 прочитаны; ревизия 14.10 (разделы 8.1–8.12 плана) учтена.
- Рабочая копия `C:\Projects\arena_new` стоит на старой ветке; весь прогон идёт в worktree от `origin/master`. Каждая задача — своя сессия, свой worktree, своя ветка `feat/<линия>-<номер>-<слаг>`, один PR в master.
- Открытые PR на старте: #89 (спека 38, `docs/chargeback-evidence-spec`, не влит — нужен П2 волны B), #73 (черновик, не из плана), #11 (docs, не из плана).

## 1. Вопросы владельцу (стоп-условия; линии О и Д стоят, остальные идут)

| № | Код | Что нужно от владельца | Зачем | Когда нужно |
|---|---|---|---|---|
| В-1 | **О0** (ST-7) | В платформенном аккаунте Stripe: включить Connect (Standard), взять `client_id` (Connect settings → Integration), создать sandbox-ключи платформы (`sk_test_…`) и Connect-вебхук «Listen to events on Connected accounts» (`whsec_…`); проверить, доступен ли платформе OAuth (`connect.stripe.com/oauth/authorize`) — Stripe пишет, что для новых платформ он «не рекомендуется»; передать значения в стенд (`STRIPE_PLATFORM_SECRET_KEY`, `STRIPE_CONNECT_CLIENT_ID`, `STRIPE_CONNECT_WEBHOOK_SECRET` — переменные ТОЛЬКО у api). Чек-лист и таблица переменных — в разделе 5 этого журнала | без этого линия О (О1a/О1b/О1c, волна B) не может начать: адаптер и вебхук проверяются только на sandbox-ключах платформы | к волне B |
| В-2 | **Д0** (ST-7) | В зоне `arenasoldout.com` в Cloudflare включить Cloudflare for SaaS (нужна платёжная карта в аккаунте; 100 hostnames включены), выпустить API-токен с правом «SSL and Certificates: Edit» на зону (custom hostnames) и положить его в `arena-site-secrets` (не в репозиторий); после этого проба на тестовом поддомене: fallback origin = нынешний хост страницы, CNAME-цель `pages.arenasoldout.com`, Full strict + SNI. Чек-лист — в разделе 5 | без этого линия Д (Д1/Д2, волна E) не может начать; проба на поддомене должна показать, что Full strict держится — иначе ST-6 | к волне E |
| В-3 | **S2a** (ST-7) | Посмотреть дополненный макет «Схема зала: управление местами» (артефакт 24.09, https://claude.ai/artifact/LVD8uqJxjdycw1yi8CCQTH) — агент дополняет его МОБИЛЬНЫМ экраном автораскладки (решение 9: на телефоне главный режим — автораскладка, кисть второстепенна) и ответить, годится ли. Ссылку на дополненный макет оркестратор добавит сюда, когда S2a будет сделан (волна B) | компонент `<arena-seat-manager>` (S2b, волна C) строится по этому макету | к волне C |
| В-4 | спека 38 (ST-3) | Влить PR #89 (`docs/chargeback-evidence-spec`, «Ответ на чарджбэк: доказательная база») — или сказать, что в ней поправить. Оркестратор её не вливает сам: это состав хранимых личных данных и тексты согласий (ST-3) | П2 волны B (`checkout_consents`, миграция 0160) согласует состав записи со спекой 38; без неё П2 стоит | к волне B |

| В-5 | **0-8 / V1–V5** (ST-7) | Ручные проверки писем на ABH TEAM (после выката 52dd47f, уже на проде): (1) купить билет на свой адрес или нажать повторную отправку в боте — письмо одно, с PDF, оформленное; через 1–2 минуты карточка заказа показывает «доставлено», после открытия — «открыто»; (2) в базе: `SELECT id, kind, status, provider_message_id, sent_at FROM email_letters ORDER BY sent_at DESC LIMIT 5;` и события письма из `email_letter_events` — ожидаются ~4 события, `provider_message_id` заполнен; если «открыто» не приходит — проверить в Brevo, включено ли отслеживание открытий для транзакционных писем; (3) V2: в Brevo (Transactional → Settings → Webhook) статус «enabled», метрика `arena_email_webhook_events_total` растёт, `…_rejected_total{reason="bad_token"}` не растёт; (4) V3: по одному письму каждого вида `ticket`, `invitation`, `change`, `cancel`, `refund` доходит до `delivered` (invitation видно только в базе); (5) V4: «Отправить билеты повторно» — новая строка `email_letters`, старая остаётся, карточка показывает последнее; (6) V5: статус одного письма совпадает в боте и в вебе; (7) V6: открытие из Apple Mail даёт «доставлено», не «открыто» — это норма | автотест 0-8 (PR #114) закрепил заголовок `X-Mailin-custom` и HTML-часть; живую цепочку Brevo из кода проверить нельзя | в любой день до выпуска волны A |

| В-6 | **П1d** (ST-7) | Разрешить одну пробу на публичном sandbox Flitt (мерчант 1549901, ключ `test` — документированный тестовый мерчант из спеки 29, денег не двигает): два вызова `checkout/url` через наш адаптер — с `reservation_data` (латинское имя + телефон) и без него. Исполнитель и оркестратор пробу не выполнили: правила агентов запрещают передавать ключ внешнему сервису без слова владельца в чате, а автоматический классификатор запретил и файл пробы. Достаточно ответа «разрешаю пробу Flitt» — остальное сделает исполнитель и допишет результат в PR #116 и спеку 29 §4.2 | без пробы неизвестно, принимает ли sandbox `reservation_data` в `checkout/url` (ST-6 при отказе); PR #116 до пробы не вливается — он меняет живой путь оплаты Flitt | до выпуска волны A |

Ответы владельца: (пусто)

## 2. Решения прогона (развилки, закрытые оркестратором)

| № | Задача | Развилка | Решение | Обоснование |
|---|---|---|---|---|
| Р-1 | 0-1 | маршрут пакетных цифр | `GET /v1/organizations/{org_id}/events/sales-summary?ids=a,b,c` (до 50 id), как записано в карточке спеки; ответ — массив по id с теми же числами, что `GET …/events/{id}/summary` | карточка спеки уже содержит это решение; второе чтение («агрегат внутри `GET …/events`») отвергнуто: список событий уже тяжёлый (`ListEventSalesFacts`), а деньги нужны не на каждом экране |
| Р-2 | П1d | зависимость от П1b | П1d запускается параллельно с П1b: адаптеры Flitt/AllPay передают имя и телефон, когда они есть в запросе провайдеру (`CustomerName`/`CustomerPhone` уже кладёт `hfeed/payment_start.go`), а П1b только меняет, когда они собираются | файлы не пересекаются (`adapters/flitt`, `adapters/allpay` против `hfeed`/виджета); зависимость в спеке смысловая, не кодовая |
| Р-3 | все | общие файлы `openapi.yaml`, `types_gen.go`, TS-клиенты | каждая сессия правит спеку в своей ветке; перед финальным push — rebase на свежий master и повторный кодген; PR, трогающие openapi, вливаются по одному, следующий rebase'ится после слива предыдущего | правило раздела 5 плана; конфликты в сгенерированных файлах решаются только регенерацией, никогда руками |
| Р-4 | все | интеграционные тесты на общей базе | каждая сессия создаёт СВОЮ базу `arena_ci_<задача>` на стенде (порт 55432) по рецепту AGENTS.md и удаляет её после; общую `arena` не трогать | параллельные сессии на одной базе ломают друг другу фикстуры (урок AGENTS.md) |
| Р-5 | П1b | флаги `collect_name`/`collect_phone` оказались КОЛОНКАМИ `sales_channels` (миграция 0061), карточка грозила ST-5 | не стоп-условие: план §5.П сам допускает «миграция данных канала — в 0160 или без миграции». Колонки остаются единственным источником правды; новое чтение `true` = `required`, `false` = `optional` (поле показывается необязательным — решение владельца 14); состояние `off` НЕ реализуется в волне A и приходит миграцией 0160 вместе с П2 (волна B) — карточка П2 дополняется | резерв миграций не нарушен, двойного источника правды в jsonb нет, поведение для живых организаторов меняется ровно так, как велит решение 14 (ST-1 не возникает) |
| Р-6 | М1 | разведка М0 расходится с планом §8.12 | `tickets.first_admitted_at` НЕ добавлять — есть `tickets.used_at` (0055) с той же семантикой, сводка сеанса уже считает «вошли N из M»; событие `v1.ticket.scanned` УЖЕ эмитится собственным сканером (`hscanner/scanner_callback.go`) — М1 только вносит его в каталог спеки 15 и переводит публикацию в одну транзакцию со вставкой; таблицы `macs_webhook_subscribers` нет — подписчики МАКС это `webhook_subscribers` с `kind='macs'`, секрет `signing_secret` (0040/0089); `gate` и `device_id` в `scan_events` уже есть — 0164 добавляет только `source`, `scanner_label`, `external_scan_id`, `subscriber_id` + UNIQUE | факты по коду master; карточка М1 в спеке 40 поправлена PR разведки |
| Р-7 | все | запуск исполнителей | задачи волны A идут фоновыми агентами с изолированными worktree (по одному на задачу, модель по классу), а не сессиями приложения: приложение отказывает в старте сессии на ходу, начатом сообщением другой сессии | тот же результат — свой worktree, своя ветка, свой PR; отчёт исполнителя приходит оркестратору напрямую |

## 3. Волна A — состояние задач

Классы и модели — по разделу 2 спеки. «Ворота» — коды выхода: build / vet / tests / gofmt / lint / integration / openapi-check / фронт. Статусы: `ожидает` → `в работе` → `PR` → `ревью` → `влито` / `стоп (ST-x)`.

### 3.1 Серверная линия

| ID | Задача | Класс | Сессия | Ветка | PR | Статус | Ворота | Не проверено |
|---|---|---|---|---|---|---|---|---|
| 0-1 | WEB-LIST: пакетные цифры продаж списка | M sonnet | | `feat/s-0-1-events-sales-summary` | | ожидает | | |
| 0-2 | WEB-ORDER-EVENT: имя мероприятия и время сеанса в заказе | S sonnet | агент | `feat/s-0-2-order-event-fields` | #121 | PR, ждёт CI | build 0, vet 0 (± integration), gofmt пусто, lint 0 (весь backend), unit 0 (eventbot, horders, gen, adapters/http, staticanalysis, httpserver), integration (`arena_ci_s02`: EventCenterOrders, BotE2E_Orders, BotE2E_Refund, ManagerPermissions0129) 0, кодген без drift, event-center type-check 0, vitest 0 (114) | Playwright smoke веба не гонялся (обновлён только mock); латентные тесты `TestInfoP99Latency_*` флаки при параллельном прогоне (прошли отдельно); стенд не пересобран |
| 0-3 | A6: публичная ссылка мероприятия, пакет `publicurl` | S sonnet | | `feat/s-0-3-public-url` | | ожидает | | |
| 0-4 | A5: геопомощники в `hgeo` | M sonnet | агент | `feat/s-0-4-geo-helpers` | #119 | PR, ревью (sonnet) | build 0, vet 0, gofmt пусто, lint 0 (geotz, hgeo, httpserver), test 0 (geotz, hgeo, httpserver, eventbot, adapters/http, staticanalysis), кодген без drift | интеграционных тестов нет (ни одного нового запроса к БД); маршрут валюты не гонялся на живой базе (у страны без валюты в БД default USD по миграции 0081) |
| 0-5 | A2: приглашение в команду с каналом `web` | M sonnet | | `feat/s-0-5-invite-channel` | | ожидает | | |
| 0-8 | Автотест писем (`X-Mailin-custom` + HTML-часть); V1–V5 вручную — ST-7 | S sonnet | агент | `feat/s-0-8-letter-tracking-test` | #114 | **влито** 14.10 | build 0, vet 0, test 0, gofmt пусто, lint 0 (только два пакета) | V1–V5 — владелец (В-5); интеграционные тесты `delivery` не гонялись (пакет не менялся) |
| 3-1 | `event-bundle?dry_run=true` | **L opus** + opus-ревью | | `feat/s-3-1-bundle-dry-run` | | ожидает (после этапа 0) | | |
| 3-2 | `GET …/events/{id}/event-bundle` | M sonnet | | `feat/s-3-2-event-bundle-read` | | ожидает (после 3-1) | | |
| 3-3 | Чтение афиши на api, бот зовёт api | M sonnet + opus-ревью | | `feat/s-3-3-poster-facts-api` | | ожидает (после этапа 0) | | |

### 3.2 Веб-линия

| ID | Задача | Класс | Сессия | Ветка | PR | Статус | Ворота | Не проверено |
|---|---|---|---|---|---|---|---|---|
| 0-6 | Матрица паритета: «Данные организации», правило «готово = близнец» | S sonnet | агент | `feat/w-0-6-parity-org-data` | #113 | **влито** 14.10 | type-check 0, vitest 0 (114), build 0 | Playwright smoke не гонялся (менялись только матрица, тест и спека 37) |
| 0-7 | Общие компоненты веба | M sonnet (Ф) | агент | `feat/w-0-7-shared-components` | #117 | PR, ревью (sonnet) | npm ci 0, type-check 0, vitest 0 (19 файлов, 264 теста), build 0, main-чанк +8.0 КБ gz (тексты каталогов) | в браузере не прогонялось (vitest без DOM; клики и фокус-ловушка покрыты чистыми автоматами), Playwright-смоука нет — экраны ещё не подключают |
| 0-9 | Харнесс близнец-тестов против живого api | M sonnet | | `feat/w-0-9-live-e2e-harness` | | ожидает | | |

### 3.3 Линия страницы

| ID | Задача | Класс | Сессия | Ветка | PR | Статус | Ворота | Не проверено |
|---|---|---|---|---|---|---|---|---|
| П1a | Блок продавца по умолчанию при полном профиле; телефон в `Profile` | S sonnet | | `feat/p-1a-seller-default-on` | | ожидает | | |
| П1b | Поля покупателя `optional/required` (`off` → 0160 с П2, Р-5) | M sonnet + sonnet-ревью | агент | `feat/p-1b-buyer-fields-tristate` | #120 | PR, ревью (sonnet) | build 0, vet 0, gofmt чисто, lint 0, integration (`arena_ci_p1b`: свои + PublicFeed/HostedCheckout/WID0d/Quote) 0, виджет vitest 672 passed, type-check 0, build 72.85 КБ gz (лимит 150), mock e2e 79 passed | подпись «Имя и фамилия» глазами в браузере не смотрели (в mock-фикстурах нет required-имени); `TestInfoP99Latency_HighConcurrency` флаки под нагрузкой (прошёл отдельно) |
| П1d | Flitt и AllPay: имя и телефон | S sonnet | агент | `feat/p-1d-provider-buyer-fields` | #116 | PR, **стоп ST-7** (проба sandbox — В-6) | build 0, vet 0, gofmt пусто, lint 0 (adapters + domain/payments), test 0, integration `AllPay|Flitt|HostedCheckout` 0 на `arena_ci_p1d` | живой sandbox Flitt (принимает ли `reservation_data`); эхо `reservation_data` в callback |

### 3.4 Прочее волны A

| ID | Задача | Класс | Сессия | Ветка | PR | Статус | Ворота | Не проверено |
|---|---|---|---|---|---|---|---|---|
| G1 | Переезд генератора в `apps/venue-svg-generator` (`git subtree add`) | M sonnet | агент | `feat/g-1-venue-svg-generator` | #115 | ревью «ВЛИВАТЬ», ждёт CI после merge master | subtree 0, docker build 0, docker run 0, pytest 0 (16 passed, 10 skipped — контейнер и venv), compose config 0, go build 0, staticanalysis 0, CI зелёный (новый job «Venue SVG generator» 31 с) | 9 тестов на приватных XLSX/результатах и `benchmark_v1` (импортирует `seating_poc`, которого нет в git) не гонялись; `arena_importsvg_proof` в репозиторий не попал (в upstream `.gitignore`) — код экспортёра перенести до G2a; работа конвейера на Hunter/Aribau не воспроизводилась |
| S0 | Разведка линии S (haiku) → карточки спеки 40 | R haiku | оркестратор | `docs/spec40-recon-s0-m0` | #112 | **влито** 14.10 | — | — |
| М0 | Разведка линии М (haiku) → карточки спеки 40 | R haiku | оркестратор | `docs/spec40-recon-s0-m0` | #112 | **влито** 14.10 | — | Р-6: три расхождения с планом учтены в карточке М1 |
| О0 | Чек-лист и таблица переменных (haiku) → раздел 5 | R haiku | оркестратор | `docs/exec-ledger` | #111 | готово, ждёт владельца | — | шаг владельца В-1 |
| Д0 | Чек-лист (haiku) → раздел 5 | R haiku | оркестратор | `docs/exec-ledger` | #111 | готово, ждёт владельца | — | шаг владельца В-2 |

## 4. Записи по задачам

**0-6 (PR #113).** `matrix.ts`: строка «Данные организации» (бот ✔ `org_data.go`, веб — R3/этап 4), поле `ready: {twin, legacy?, smoke?}` и `readyViolations`; `parity.test.ts` проверяет существование файла близнеца, `twin: null` допустим только как `legacy` и только для строк R1 (их mock-smoke не близнец по спеке 40 — близнец идёт на живом api, харнесс 0-9); таблица спеки 37 перегенерирована (столбец «Готово»); ловушка в AGENTS.md. Решения исполнителя: «готово» = наличие `ready`; столбец «сайт» не добавлялся (карточка не расширена).

**0-8 (PR #114).** Тесты `email/sender_tracking_test.go` (заголовок `X-Mailin-custom` ровно один раз, одна `text/html` часть с одним `</body>`, `text/plain`, два PDF, один DATA через счётчик-сервер SMTP, `queued as` разобран) и `emailstatus/status_regulation_test.go` (недостающие строки таблицы регламента: deferred → delivered → opened; failure после delivered; `proxy_open` = delivered; blocked без доставки = failed). Решение исполнителя: пиксель вставляет Brevo, арена проверяет только HTML-часть. **П1d (PR #116).** По документации Flitt (`docs.flitt.com/api/order-parameters`, `/api/reservation_data`) у `checkout/url` одно поле покупателя `sender_email`; имя и телефон принимаются только внутри `reservation_data` (BASE64 от JSON с `customer_name` ≤ 1024 и `phonemobile` ≤ 16, «латиница, цифры, разделители»). Сделано: `adapters/flitt/buyer_fields.go` строит `reservation_data` и кладёт его в тело как обычный подписываемый параметр (алгоритм подписи, суммы, валюты не тронуты); нелатинское имя (кириллица, иврит, `ø`/`ł`/`ß`) не отправляется вовсе — отказ по одному полю сорвал бы весь платёж; латинские диакритики сворачиваются (`García` → `Garcia`); телефон приводится к `+цифры` (`00` → `+`, 7–16 знаков), иначе не отправляется; своя нормализация в адаптере, т.к. `NormalizeE164` живёт в `eventbot` и строже нужного. AllPay: код не менялся, добавлен тест на отсутствие `client_name`/`client_phone` при пустых значениях. Спека 29 §4.2, AGENTS.md дополнены. Среда: `arena_postgres` на этом хосте опубликован на порту **45432**, не 55432 (Hyper-V-ловушка AGENTS.md) — учтено в брифах следующих исполнителей.

**0-7 (PR #117).** `lib/confirmWords.ts` (слова бота для cancel_order / cancel_session / delete_event / refund / annul_invitation; тест читает `locales/{en,ru,es}.toml` и `typed_words.go` с диска), `lib/dates.ts` (порт `DateCandidates`/`DateWords`/`dateBounds`, таблица тестов 1:1 из `TestDateCandidates`), `lib/dateField.ts` и `lib/dryRun.ts` (чистые автоматы состояния), компоненты `ConfirmWordDialog`, `DryRunConfirm`, `DateField`, `ErrorNotice`, хук `useDialogBehaviour` (Esc, ловушка Tab, возврат фокуса — как в `StepUpDialog`), `lib/errors.ts` расширен (`describeApiError`, `API_ERROR_STEMS`, фолбэки по `.not_found` и статусу, 504/524 = «возможно, дошло»), тексты `apiError.*`, `confirmWord.*`, `dryRun.*`, `dateField.*` в en/ru/es. Решения исполнителя: таблица ошибок в `errors.ts` (файл уже был); кнопка подтверждения всегда активна и показывает заметку при неверном слове, как бот; `DateField` не `<form>`; `DryRunConfirm` не модальный, результат описан типом `DryRunResult`; свой хук вместо рефакторинга `StepUpDialog`. Ловушка в AGENTS.md (vitest без DOM; `01.10` читается двумя способами; новый код ошибки требует стем и ключи в трёх каталогах).

**G1 (PR #115).** `git subtree add` без squash: история исходного репозитория (18 коммитов, авторство цело) — во втором родителе merge-коммита (`git log <merge>^2`), `git log -- apps/venue-svg-generator` показывает один коммит (subtree переписывает путь) — записано в AGENTS.md; ветку сливать с master через `merge`, не `rebase`. Dockerfile (python:3.12-slim, не root, копирует только `xlsx_seating_adapter`, `seating_svg_renderer`, `m1_product`, команда по умолчанию `python -m m1_product --help`); зависимости собраны из импортов (`requirements.txt`: openpyxl; `requirements-dev.txt`: numpy, pillow, pytest; `pdfplumber`/`pypdfium2` только для `pdf_diagnostics` — не включены); `pytest.ini` исключает `benchmark_v1`, `conftest.py` пропускает 9 тестов на приватных данных (`ChatExport_*`, `results/`, `seating_poc`), когда папок нет; CI job `venue-svg-generator` с фильтром путей внутри job (фильтр на уровне workflow отключил бы остальные джобы); compose `venue_gen` под `profiles: [venue-gen]`, без портов, `restart: "no"`; ROADMAP M3 переписан по решению 12 (группа `PriceCategory`, `inkscape:label="#<Сектор>"`, `<title>` = номер, холст до 2000×2000, без `sbt:*`, сверено с `domain/seating/doc.go`). **Решение прогона Р-8:** код экспортёра `arena_authoring_exporter.py` (`arena_importsvg_proof`, не в git upstream) переносится в репозиторий задачей G2a, до неё — не блокирует.

**0-4 (PR #119).** `GET /v1/geo/countries/{iso2}/timezones` → `{timezones:[{id,label}], single}` (ES: Madrid/Canary/Ceuta; PT: Lisbon/Madeira/Azores; CZ одна зона, `single:true`; неизвестная страна 404 `geo.country_not_found`, не две буквы — 400 `geo.invalid_iso2`); `GET …/currency` → `{currency}` из `countries.currency` (`GetCountryByISO2`); `GET /v1/geo/cities` получил `country` и `q` (нормализованный префикс имени или слага, ≤ 64 символов, 20 результатов): платформенные города в `cities` (теперь с `timezone` и `source:"platform"`), подсказки GeoNames в отдельном `suggestions` (`{name,country_iso2,timezone,source:"geonames"}`, без id, только при `q` и `country`, без уже существующих в платформе); новые `geotz.SearchCities`, `SingleZone` (`search.go`), `cityZone` теперь отдаёт основное имя — кириллический префикс «мадр» находит Madrid. Две строки `flatPublic` в guard-тесте, AGENTS.md. **Решения прогона Р-9 (приняты по отчёту исполнителя):** карточка ошибочно считала, что таблица «страна → валюта» живёт у бота — её нет (мастер читает `currency` из списка стран, `cmd/arena-bil24-import` держит свой импорт-дефолт), поэтому валюта берётся из БД и бот не меняется; `suggestions` отдельно от `cities`, потому что `GeoCityItem.id` обязателен и его декодирует `eventbot/arena_client_refs.go` — смешанный список сломал бы контракт; лимита запросов нет, как у соседних geo-маршрутов (`q` ≤ 64, поиск в памяти ≈ 100k сравнений). Ревью: см. ниже.

**0-7 — ревью (sonnet, чужая сессия): ВЛИВАТЬ после двух правок.** Слова подтверждения сверены вручную с toml бота (сеанс/заказ/возврат/удаление/аннулирование совпадают, перекрёстный тест «заказ ≠ сеанс» по языкам), `normalizePhrase` построчно как в Go; порт `dateCandidates` прогнан на 38 входах против Go — единственное расхождение NBSP как разделитель (TS терпимее, безвредно); ru-тексты без запрещённых слов глоссария; бандл экранов не растёт. Поправить (передано автору): фокус после «Прочитать дату» уходит на body, варианты не озвучиваются; `apiError.unauthorized.title` говорит «сеанс работы» — двусмысленно с глоссарием. Заметки на потом (не в этой задаче): `bot.promo.delete_word` отсутствует в `CONFIRM_KINDS` (понадобится экрану промокодов, этап 4); `ConfirmWordDialog` при `busy` теряет фокусируемые элементы; `DateField` берёт месяц из `value` один раз; быстрые кнопки «сегодня/завтра/неделя/месяц» — небольшое расширение карточки, оставлено (Решение прогона: безвредно, укладывается в смысл «календарь»).

**0-4 — ревью (sonnet): ВЛИВАТЬ после зелёного CI и одной правки.** Приёмка покрыта (ES Madrid/Canary, `las palmas` → Atlantic/Canary; PT три зоны; CZ single; валюты; кириллица «мадр»); `country` по `^[A-Za-z]{2}$`, `q` ≤ 64 рун, SQL по `q` нет (фильтр в Go), GeoNames-поиск в памяти с лимитом 20, порядок детерминирован; `types_gen.go` только добавления (+69). Поправить (передано автору): `HandleListCities` при `country`/`q` читает ВСЕ платформенные города с JOIN `i18n_text` на каждое нажатие — фильтровать по стране в SQL; запись AGENTS.md про «in-memory scan» верна только для GeoNames; рассинхрон 404 между `/timezones` (страна вне GeoNames) и `/currency`. Заметки на потом: в односоставных странах (CZ, IL) поиск только по латинским top-городам (иврит «תל» пусто); `/currency` отдаст пустую строку при пустом `countries.currency`; 503 у `/timezones` документирован, но не отдаётся.

**П1b (PR #120).** Решение Р-5 применено. `BuildBuyerFields`: имя и телефон всегда `enabled: true`, `required` = колонка (`true` → required, `false` → optional; флаги не прочитались → оба optional); `checkout/start`: 422 только при required, пробелы = пусто, введённое значение сохраняется в любом режиме; виджет: одно поле «Имя и фамилия» (en «First and last name», ru, cs «Jméno a příjmení», he), метка optional локализована (`optional_label`); openapi — описания `BuyerFieldItem`/`PublicBuyerInfo` с пометкой «`off` появится позже». Тесты: юнит на 4 сочетания, `public_feed_buyer_fields_integration_test.go` (бесплатная корзина, 10 случаев `checkout/start`), vitest `widget_buyer_fields.test.ts`. Решения исполнителя: `PATCH …/channels/{id}` флаги никогда не принимал — включить `required` сегодня можно только SQL; добавление в PATCH — отдельная мелкая задача (в бэклог этапа 4, экран «Данные организации»/каналы). Среда: исходный worktree исполнителя исчез посреди работы (почему — не выяснено; взят новый `p1b-buyer-fields`), постгрес на 45432.

**0-2 (PR #121).** Новый запрос `GetOrderEventFacts` (`queries/orders.sql` + `gen/orders_event_facts.sql.go`, inner JOIN events/sessions/venues без фильтра `deleted_at`, в рамках организации) — отдельный запрос вместо расширения `OrderRow` (у `GetOrderByID` ≈ 9 вызывающих и общий сканер); `horders/handler.go` `HandleGet` заполняет `event_name`, `session_start_at`, `venue_timezone` (required в openapi); бот: `orderMetaOf(detail)` вместо поиска по списку, старый `orderMeta()` удалён; веб: `OrderScreen.tsx` снял `useQuery` списка и читает карточку; smoke-mock дополнен. Тесты: `TestEventCenterOrders_CardCarriesEventFacts` (оплаченный и истёкший заказ, затем мягкое удаление сеанса), `horders/handler_test.go`. Ловушка в AGENTS.md: `horders.fakeDB` матчит SQL по подстроке — новый запрос требует `-- name:` case ДО общего `FROM orders`.

**G1 — слияние master.** После слияния #113/#114 ветка G1 дважды конфликтовала в AGENTS.md (все задачи волны дописывают ловушки в конец файла); второй merge сделал оркестратор (обе записи сохранены). **Решение прогона Р-10:** конфликт AGENTS.md — техническая правка, оркестратор решает её сам (обе стороны сохраняются, своя запись ниже), не возвращая задачу автору.

**Наблюдение (не дефект карточки, в бэклог):** `buildMIMEMessage` без вложений шлёт только `text/html`, даже если задан `TextBody` — альтернатива `text/plain` есть только у писем с PDF (письма возврата, приглашения); записано в AGENTS.md, исправление — отдельной задачей (Решение прогона: не расширять 0-8).

## 5. Чек-листы шагов владельца (О0, Д0)

Подготовлены разведкой (haiku) 14.10 по официальной документации; факты, которые документация не подтвердила, отмечены отдельно. Секреты в чат и в репозиторий не присылать — только в секретный стор (`arena-site-secrets`).

### 5.1 О0 — чек-лист владельца: платформенный аккаунт Stripe

Проверка репозитория: в `config.go` и `deploy/DOKPLOY.md` нет `STRIPE_PLATFORM_SECRET_KEY`, `STRIPE_CONNECT_CLIENT_ID`, `STRIPE_CONNECT_WEBHOOK_SECRET` (есть только `STRIPE_WEBHOOK_SECRET` и test-only `STRIPE_API_BASE_URL`). Маршрут `GET /v1/stripe/connect/callback` в коде уже есть (`hbilling/stripe_connect.go`, `mount_commerce.go`), но не подключён.

1. Если в Dashboard не включён Connect, откройте раздел Connect и примите условия. Переключатель слева вверху должен показывать «Test mode».
2. Откройте настройки брендинга Connect: https://dashboard.stripe.com/account/applications/settings — название, цвет, иконка (Stripe показывает их организатору в онбординге).
3. Откройте https://dashboard.stripe.com/settings/connect/onboarding-options/oauth и скопируйте тестовый `client_id` вида `ca_…` в секретный стор.
4. На той же странице добавьте оба redirect URI через запятую: `https://api-staging.arenasoldout.com/v1/stripe/connect/callback` и `https://api.arenasoldout.com/v1/stripe/connect/callback` (байт в байт, без слэша в конце).
5. Откройте https://dashboard.stripe.com/test/apikeys и скопируйте secret key `sk_test_…` платформы в секретный стор. `sk_live_…` в этом чек-листе не создавайте.
6. Откройте https://dashboard.stripe.com/test/webhooks → «Add endpoint», в «Events from» выберите «Connected accounts». URL пока оставьте заглушкой: маршрут `/v1/payment-intents/webhook/connect` появится в волне B (О1c); агент сообщит адрес.
7. Выберите события `account.updated`, `account.application.deauthorized` (подтверждены документацией https://docs.stripe.com/connect/webhooks), а также `checkout.session.completed`, `payment_intent.succeeded`, `payment_intent.payment_failed`, `refund.created`, `refund.updated`, `refund.failed` — их точные имена проверьте в списке на экране.
8. Скопируйте signing secret `whsec_…` endpoint'а в секретный стор. Для sandbox-аккаунта Stripe требует отдельный endpoint.
9. Проверка доступности OAuth (не нажимайте «Allow»):

```powershell
Start-Process 'https://connect.stripe.com/oauth/authorize?response_type=code&client_id=<CA_CLIENT_ID>&scope=read_write&redirect_uri=https://api-staging.arenasoldout.com/v1/stripe/connect/callback'
```

Если показана форма подключения — OAuth доступен. Если `invalid_redirect_uri` — вернитесь к шагу 4.

10. Факт документации: фразы «OAuth не рекомендуется для новых платформ» на открытых страницах Stripe нет; Stripe говорит, что legacy-типы Standard «applies only to platforms that already use legacy connected account types», а новым платформам советует Accounts v2 (https://docs.stripe.com/connect/standard-accounts, https://docs.stripe.com/connect/interactive-platform-guide). OAuth остаётся документированным путём для СУЩЕСТВУЮЩИХ аккаунтов; `read_write` не подключает Standard-аккаунт другой платформы (https://docs.stripe.com/connect/oauth-changes-for-standard-platforms). Решение по модели (OAuth против Accounts v2) агент О1a сверит с этим перед production — это и есть риск (2) плана §8.11.
11. Передайте агентам (только в стенд, только контейнер api):

| Переменная | Значение | Где |
|---|---|---|
| `STRIPE_PLATFORM_SECRET_KEY` | `sk_test_…` платформы | только api |
| `STRIPE_CONNECT_CLIENT_ID` | `ca_…` | только api |
| `STRIPE_CONNECT_WEBHOOK_SECRET` | `whsec_…` Connect-endpoint | только api |

`STRIPE_WEBHOOK_SECRET` остаётся отдельным. Строки в `deploy/DOKPLOY.md` §2.6 и в `roleSecrets` впишет агент О1a.

**Что передать агентам:** `ca_…`, результат шага 9, id endpoint'а и список событий (тест), подтверждение, что три значения лежат в секретном сторе стенда.

### 5.2 Д0 — чек-лист владельца: Cloudflare for SaaS

Подтверждено документацией: 100 custom hostnames включены на Free/Pro/Business, далее 0,10 $ за hostname в месяц (https://developers.cloudflare.com/cloudflare-for-platforms/cloudflare-for-saas/plans/); Enterprise нужен только для своих сертификатов, wildcard и Apex Proxying. Требование платёжного метода документация не называет — проверьте в панели.

1. Если план зоны `arenasoldout.com` — Free, Pro или Business, переходите к шагу 2 (Enterprise не нужен).
2. Если в панели зоны есть пункт «Custom Hostnames», Cloudflare for SaaS уже включён; иначе включите его по https://developers.cloudflare.com/cloudflare-for-platforms/cloudflare-for-saas/start/getting-started/ (панель может попросить платёжный метод).
3. Создайте в DNS зоны proxied-запись (оранжевое облако) на нынешний хост витрины `tickets.arenasoldout.com`, например `fallback.arenasoldout.com` — это fallback origin.
4. В Custom Hostnames → Fallback Origin укажите это имя; дождитесь `Active`.
5. Создайте proxied CNAME-таргет для организаторов, например `pages.arenasoldout.com` (имя — наша конвенция из плана §8.10, не Cloudflare). Организатор ставит у себя `tickets.organizer.com CNAME pages.arenasoldout.com`. Голый домен (apex) без Enterprise невозможен — только поддомены, как и записано в плане.
6. My Profile → API Tokens → Create Custom Token: права «SSL and Certificates: Edit» + «Zone: Read», ресурс — только зона `arenasoldout.com`, срок действия задайте. Сохраните в `arena-site-secrets`.
7. Проверка токена (значение подставьте из стора, файлом не сохраняйте):

```powershell
$env:CF_API_TOKEN = '<ИЗ_СЕКРЕТНОГО_СТОРА>'
Invoke-RestMethod -Uri 'https://api.cloudflare.com/client/v4/user/tokens/verify' -Headers @{ Authorization = "Bearer $env:CF_API_TOKEN" }
```

Ожидается `success: true`, `status: active`.

8. На тестовом поддомене, которым вы владеете (например `trial.<ваш-домен>`), создайте CNAME на таргет из шага 5 и дождитесь распространения DNS (`nslookup trial.<домен>`).
9. Создайте custom hostname (платный объект — после пробы удалите, шаг 12):

```powershell
$body = @{ hostname = 'trial.<ВАШ-ДОМЕН>'; ssl = @{ method = 'http'; type = 'dv' } } | ConvertTo-Json
Invoke-RestMethod -Method Post -Uri 'https://api.cloudflare.com/client/v4/zones/<ZONE_ID>/custom_hostnames' -Headers @{ Authorization = "Bearer $env:CF_API_TOKEN" } -ContentType 'application/json' -Body $body
```

Из ответа возьмите `result.id`.

10. Проверяйте статус каждые 5 минут, не дольше часа:

```powershell
Invoke-RestMethod -Uri 'https://api.cloudflare.com/client/v4/zones/<ZONE_ID>/custom_hostnames/<HOSTNAME_ID>' -Headers @{ Authorization = "Bearer $env:CF_API_TOKEN" } | Select-Object -ExpandProperty result | Select-Object status, @{n='ssl';e={$_.ssl.status}}
```

Нужно `status: active` и `ssl: active` (`pending`, `initializing`, `pending_validation` — промежуточные).

11. Откройте `https://trial.<ВАШ-ДОМЕН>` — должна отдаться витрина. **Не подтверждено документацией:** поведение Full (strict) для fallback origin (открытые страницы Cloudflare for SaaS не описывают требований к сертификату origin). Режим SSL зоны в день пробы не менять (ловушка 526 из заметок проекта). Результат пробы решает ST-6 для линии Д.
12. Удалите тестовый hostname:

```powershell
Invoke-RestMethod -Method Delete -Uri 'https://api.cloudflare.com/client/v4/zones/<ZONE_ID>/custom_hostnames/<HOSTNAME_ID>' -Headers @{ Authorization = "Bearer $env:CF_API_TOKEN" }
```

**Что передать агентам:** `ZONE_ID` (не секрет), имена fallback origin и CNAME-таргета, имя записи токена в секретном сторе (не значение), результат шагов 10–11 (в т.ч. отдаётся ли витрина по https и какой режим SSL стоял на зоне).

## 6. Хроника

- 14.10 — старт прогона, журнал создан (PR #111), голова миграций проверена, вопросы владельцу В-1…В-4 записаны.
- 14.10 — разведки S0, М0 (PR #112 в спеку 40), чек-листы О0, Д0 (раздел 5); запущены 13 исполнителей волны A (Р-7); П1b остановилась на развилке колонок — закрыта решением Р-5.
