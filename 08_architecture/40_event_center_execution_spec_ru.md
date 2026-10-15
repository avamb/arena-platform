# 40. Исполнительная спецификация общего плана ивент-центра (14.10.2026)

Статус: **готова к прогону**. Источник правды по содержанию — `docs/plans/event_center_unified_plan_2026-10-14_ru.md` (влит в master PR #109, решения владельца 1–18). Этот документ не повторяет план: он превращает его в каталог задач с классом сложности, рекомендованной моделью агента, зависимостями, приёмкой и стоп-условиями, и задаёт протокол ОДНОГО автономного прогона, который ведёт Claude-сессия-оркестратор. Владельца зовут только при неразрешимом противоречии (раздел 4).

Почему не AutoForge: он подключён к Codex с малыми лимитами (решение владельца 14.10). Оркестрация — Claude, модели раскладываются по сложности (раздел 2), чтобы токены тратились там, где решается архитектура, деньги и права, а не на механику.

## 1. Как читать

- **Линии** — те же, что в плане: этапы 0–8, S (места), G (генератор), П (страница продаж), Д (домен), О (платёжка), М (проходки МАКС), V (VIP). Биллинг (этап 9, волна кредитов) в прогон не входит; из него действует только правило usage (план 8.5 п. 2).
- **Задача** — одна карточка раздела 5: одна сессия, один worktree, одна ветка, один PR (или звено стопки). Карточка даёт цель, границы, файлы, приёмку и стоп-условия; подробности поведения — по ссылке на раздел плана или спеки.
- **Детализация по мере подхода.** Карточки ближних линий (этапы 0, 3, 1, 2, П, 4, 5, О) содержат файлы и маршруты по коду master на 14.10. Дальние линии (S, G, Д, М, V, этапы 6–8) описаны на уровне плана: оркестратор перед стартом линии заказывает разведку (haiku) и дописывает файлы в карточки — так спека не устаревает за три недели и не тратит токены на то, что изменится.
- **Нумерация миграций** (решение В-12, 15.10): номер = **следующий свободный на `origin/master` в момент вливания**. Резерв по линиям отменён — он создал ловушку М1 (0164 при пустых 0154–0163: goose без `WithAllowMissing` отказывает каждой более поздней миграции с меньшим номером). Исполнитель берёт следующий номер при старте, а перед вливанием сверяет с головой master и перенумеровывает, если номер занят (файл, пин `migrations_head_test.go`, `lock_timeout_test.go`, комментарии, openapi + клиенты); две миграции одного PR идут подряд. Номера в карточках ниже — ориентир по порядку, не обязательство. ST-5 — только если на master обнаружилась дыра в нумерации.

## 2. Классы сложности и модели

| Класс | Признаки | Исполнитель | Проверка |
|---|---|---|---|
| **R разведка** | только чтение: найти файлы, маршруты, тесты, собрать факты | haiku (Explore) | — |
| **S** | ≤ 1 день; ясные файлы; без новых таблиц; без денег и прав; без публичных маршрутов | sonnet | самопроверка по приёмке + CI |
| **M** | 1–3 дня; новый маршрут или миграция; тесты-близнецы; тексты в 3–7 локалях | sonnet | sonnet-ревью по карточке (чужая сессия) |
| **L** | 3+ дней; конкуренция и замки; деньги (суммы, получатели, возвраты); права; публичная ЗАПИСЬ без входа; вход и сессии; новые секреты | opus (или fable, если на кону живые организаторы) | независимое opus-ревью ДО слияния (правило плана §4 п. 1) |
| **Ф фронт** | экраны веба и виджета по готовому API; класс Ф-L — большой экран (мастер, компонент мест, возвраты) | sonnet (frontend-design по вкусу); Ф-L — тоже sonnet, но с opus-ревью и разбиением на секции | близнец-тест Playwright в `apps/event-center/tests/smoke` против живого API (харнесс 0-9) |

Уточнения к классам: публичное ЧТЕНИЕ (геопомощники) — M; предзаполнение данных покупателя у провайдера без изменения сумм — M с opus-ревью; перенос секрета между контейнерами — M с opus-ревью.

Правила экономии: разведку никогда не делает opus; sonnet не решает архитектурные развилки — при развилке он пишет «Развилка» в журнал и останавливается (раздел 4), оркестратор решает; одна сессия — одна задача; контекст сессии не раздувается чтением AGENTS.md целиком — оркестратор даёт выдержку (раздел 3) и ссылки.

## 3. Общие правила прогона (выдержка, полный текст в AGENTS.md)

1. Код пишется в worktree от свежего `origin/master`; ветка `feat/<линия>-<номер>-<слаг>`; PR в master; стопка — нижний PR не удалять до перенацеливания верхнего.
2. Ворота перед PR: `go build ./...`, `go vet`, тесты затронутых пакетов, `gofmt -l` по изменённым файлам (и с тегом integration), интеграционные тесты на СВЕЖЕЙ базе `arena_ci` (рецепт в AGENTS.md), `openapi.yaml` → `gen-openapi` + `gen-ts-client` после КАЖДОГО касания спеки, `golangci-lint` по рецепту, фронтовые `vitest`/`type-check`/Playwright. Полный `go test ./...` — перед сливом стопки, без пайпов (`> log; echo EXIT:$?`).
3. Миграции только добавляют; новое право — грант `platform_superadmin` в той же миграции; enum в openapi — без переименования констант; `$ref` с `description`; `httpserver` файл ≤ 400 строк; snake_case JSON; RFC 3339 (`// allow:timeformat` только для человеческих дат в чате); новый `bil24.*`/`bot.*` ключ — во все локали; тексты по `docs/text_style_standard_ru.md` (строгий глоссарий: hold = «бронь», разные слова отмены).
4. Транзакции: замок `sessions` первым statement'ом перед `inventory_ledger`; «best effort» внутри денежной транзакции — под SAVEPOINT; внутри открытой транзакции не звать `*gen.Queries`, привязанный к пулу; outbox только через `PGEventsWriter`; вебхуки — одна машина состояний `processPaymentWebhook`.
5. Письма — только через `buildMIMEMessage` с записью в `email_letters`; новый вид письма — CHECK + Go-константа одной миграцией с тестом на пару.
6. Ничего не коммитить по `git add -A`; секреты и токены в репозиторий не попадают; `gh` доступен в сессиях desktop, недоступен в песочнице AutoForge.
7. Журнал прогона — `docs/plans/exec_ledger_2026-10_ru.md`: по задаче — ветка, PR, коды выхода ворот, что НЕ проверено, «Решения прогона» (развилки, которые оркестратор закрыл сам, с обоснованием) и «Вопросы владельцу» (раздел 4). Журнал — единственное место, откуда владелец узнаёт ход прогона.

## 4. Стоп-условия: когда зовут владельца

Оркестратор решает сам всё, что решается кодом, спекой или решениями 1–18 плана, и записывает решение в журнал. Владельца зовут ТОЛЬКО при одном из условий ниже; до его ответа линия стоит, остальные линии идут.

| Код | Условие |
|---|---|
| **ST-1** | Противоречие между планом (решениями 1–18) и кодом, которое меняет поведение для ЖИВЫХ организаторов или покупателей (не тестовых), и его нельзя закрыть флагом по умолчанию выключенным |
| **ST-2** | Деньги: любая задача, которая меняет, КОМУ и СКОЛЬКО уходит денег (комиссия, удержание, `application_fee`, возврат не на исходный способ), сверх того, что написано в плане 8.5 и 8.11 |
| **ST-3** | Право и закон: текст условий, согласий, рассылок; хранение личных данных дольше или шире, чем записано (спека 38 о доказательной базе чарджбэков — сегодня на ветке `docs/chargeback-evidence-spec`, влить до П2; план 8.9); любая страна, по которой нет решения |
| **ST-4** | Новый секрет или доступ наружу, не названный планом (план называет `TELEGRAM_MINIAPP_SECRET_KEY`, `STRIPE_PLATFORM_SECRET_KEY`, `STRIPE_CONNECT_WEBHOOK_SECRET`, `POSTER_LLM_API_KEY` на api, Cloudflare for SaaS) |
| **ST-5** | Резерв миграций исчерпан или занят чужой веткой; конфликт стопок, который нельзя решить перенацеливанием |
| **ST-6** | Внешняя зависимость отвечает не так, как написано в документации (Stripe OAuth недоступен платформе; Cloudflare for SaaS не держит Full strict; Flitt не принимает поля) — и обходной путь меняет пользовательский сценарий |
| **ST-7** | Нужен ручной шаг владельца, не решение: живая организация или платёж (ABH TEAM, Stripe test), показ макета, аккаунт или дашборд, куда у агентов нет доступа (платформенный аккаунт Stripe, Cloudflare for SaaS), выкат на прод |

Что НЕ стоп-условие: выбор имени таблицы или маршрута; порядок PR внутри линии; оценка в днях разошлась; тест-фикстура сломалась от чужих данных; текст подсказки; выбор между двумя эквивалентными реализациями (записать «Решение прогона»).

## 5. Каталог задач

Колонки: **ID** · название · **класс/модель** · зависит от · ссылка на план · приёмка (кратко) · стоп. Файлы и маршруты — в карточках после таблицы линии (ближние линии) или «по разведке» (дальние).

### 5.0 Этап 0 «Основание» (серверная линия + веб без экранов)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| 0-1 | WEB-LIST: пакетные цифры продаж списка мероприятий одним запросом | M sonnet | — | этап 0 (WEB-LIST) | **Решение прогона:** маршрут `GET …/events/sales-summary?ids=a,b,c` (до 50 id), ответ — те же числа, что `summary` по каждому; веб делает 1 запрос на экран; тест сравнивает с per-event summary | — |
| 0-2 | WEB-ORDER-EVENT: имя мероприятия и время сеанса в `GET …/orders/{id}` | S sonnet | — | этап 0 (WEB-ORDER-EVENT) | поля `event_name`, `session_start_at`, `venue_timezone` в ответе; openapi + кодген; бот и веб перестают брать их из строки списка | — |
| 0-3 | A6: публичная ссылка мероприятия в ответе API | S sonnet | — | этап 0 (A6) | `public_url` в карточке события = та же функция «адрес страницы организации», что у бота `salesLink` (заготовка под Д2: одна функция) | — |
| 0-4 | A5: геопомощники — дописать в существующий `hgeo` | M sonnet | — | этап 0 (A5) | `hgeo` и маршруты `GET /v1/geo/countries`, `/v1/geo/cities` УЖЕ есть (`mount_iam.go`): добавить `…/countries/{iso2}/timezones`, `…/countries/{iso2}/currency` и поиск `cities?q=` поверх `geotz`; без миграции; лимит как у публичных чтений; тесты на ES/PT (несколько зон) | — |
| 0-5 | A2: приглашение в команду с каналом `web` или `telegram` | M sonnet | — | этап 0 (A2) | `POST …/bot-invitations` принимает `channel`; `web` шлёт письмо со ссылкой `/accept-invite` (тот же `password_reset_tokens` путь); роль существующего участника не меняется (AGENTS.md) | — |
| 0-6 | Матрица паритета: строка «Данные организации», правило «готово = близнец-тест» | S sonnet | — | этап 0 | `matrix.ts` + тест дерева маршрутов зелёный | — |
| 0-7 | Общие компоненты веба: подтверждение словом, «сухой прогон и одно нажатие», календарь с подтверждением даты, разбор кодов ошибок API в тексты | M sonnet (Ф) | — | этап 0, принцип 4 | компоненты рядом с `src/components/ui.tsx` и `StepUpDialog.tsx` (существующий слой компонентов), ошибки — в `src/lib/errors`; слова подтверждения из тех же i18n-текстов, что бот (по языкам); юнит-тесты; ни один экран ещё не подключён | — |
| 0-8 | Проверки писем V1–V6 и автотест на `X-Mailin-custom` + пиксель | S sonnet | — | план 3а | автотест в `internal/adapters/email` (`buildMIMEMessage`) и `emailstatus`; V1–V5 выполняются вручную на ABH TEAM — ST-7 для ручной части | ST-7 |
| 0-9 | **Харнесс близнец-тестов веба**: Playwright в `apps/event-center/tests/smoke` против ЖИВОГО api (как `apps/widget` `test:e2e:real`): скрипт поднимает api + свежую базу `arena_ci` + `arena-seed`, заводит организацию с тремя ролями и выдаёт JWT; CI-job в `event-center-build` | M sonnet | — | 8.2 веб-линия, правило этапов 4–5 | один e2e («список мероприятий у трёх ролей») зелёный в CI; документ `apps/event-center/tests/README.md` | — |

**Карточки 0-1…0-9 (файлы по master 14.10).** `horders/session_summary.go` и `queries/session_summary.sql` (0-1: агрегат по списку событий одним SQL, не циклом); `horders` карточка заказа + `orders.sql` JOIN `events`/`sessions`/`venues` (0-2); `hcatalog/events.go` + общая функция в новом пакете `internal/platform/publicurl` (0-3, её же берёт `eventbot.salesLink` и позже Д2); `httpserver/hgeo/{geo.go,handler.go}`, `geo_shims.go`, `mount_iam.go` (0-4); `hbot/invitations.go` + `authemail` (0-5: `Purpose=org_invitation` уже есть); `apps/event-center/src/parity/matrix.ts` (0-6); `apps/event-center/src/components`, `src/lib/errors` (0-7); `internal/adapters/email/sender.go`, `internal/platform/emailstatus` (0-8); `apps/event-center/tests/smoke`, `.github/workflows/ci.yml` job `event-center-build` (0-9).

### 5.3 Этап 3 «Сухой прогон пакета, афиша, геопомощники» (серверная линия, после этапа 0)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| 3-1 | `POST …/imports/event-bundle?dry_run=true` | **L opus** | — | 8.2 | тот же код проверки; транзакция откатывается; ответ: нормализованный пакет, значения по умолчанию, `warnings[]`, `errors[]` — ВСЕ ошибки с путём поля (JSON pointer); тест на отсутствие побочных эффектов: side-load афиши, медиа, `EnqueueInTx`, `sessionchange`, аудит, публикации, вебхуки — ничего не вызвано; бот не меняется | ST-1, если сбор всех ошибок требует менять поведение НЕ-dry-run пути |
| 3-2 | `GET …/events/{id}/event-bundle` — мероприятие обратно пакетом | M sonnet | 3-1 (формат) | 8.2 | обратное отображение `wizard_load.go` на сервере; `arenaEventId/SessionId/TierId` заполнены; круговой тест: bundle → import → GET → равен | — |
| 3-3 | Чтение афиши на api: `POST …/media/{id}/poster-facts`, лимит, перенос ключа; бот зовёт api | M sonnet, ревью opus (секрет) | — | 8.2 | `posterread` вызывается из api; лимит на организацию в сутки через `ratelimit`; бот: `readPosterHints` (`wizard_bot.go`) и `PosterReader` (`bot.go`) идут через `ArenaClient`; e2e мастера зелёный; `deploy/DOKPLOY.md`: `POSTER_LLM_API_KEY` у api, у бота убран; выкат в порядке плана | ST-4 только если нужен ДРУГОЙ секрет |

**Карточки.** 3-1: `himports/bil24_session.go` (`handleImport` — точка входа; завести `importMode{DryRun bool}`), `import_exec.go`, `import_arena.go`, `import_categories.go`, `import_chain.go`, `import_sale_times.go`, `event_slug.go`, `session_change.go` (журнал — запрещён в dry_run), `import_publication.go` (публикации и вебхуки — запрещены), `types.go` (`errors[]` с `field`), openapi `…/imports/event-bundle` (параметр и схема ответа), `tests/staticanalysis/session_change_guard_test.go` (не нарушать). 3-2: новый `himports/event_bundle_read.go` + `hcatalog` чтение; openapi. 3-3: новый `hmedia/poster_facts.go` + `mount_media.go`; `eventbot/arena_client.go` новый метод; `internal/platform/posterread` без изменений.

### 5.1 Этап 1 «Модель материалов HP-1» (серверная линия, после этапа 3)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| 1-1 | Миграция 0153: галерея, видео, «где ещё купить», точка на карте, фотоотчёт; владельцы медиа + CHECK в паре; таблица `event_page_stats` (для 2-4) | M sonnet | — | этап 1, 8.3 этап 2 | `mediastore.AllowedOwnerTypes` и CHECK расширены вместе, тест на пару зелёный; лимиты (фото ≤ 6, видео ≤ 4, партнёры ≤ 3) в CHECK/коде | — |
| 1-2a | API: галерея (`…/events/{id}/media-items`) и видео (`…/videos`: только ссылка и разобранный id, проверка YouTube/Vimeo/Rutube/https) под `event.update` | M sonnet | 1-1 | этап 1 | лимиты фото ≤ 6, видео ≤ 4; тесты | — |
| 1-2b | API: партнёры «где ещё купить» (`…/partner-links`, ≤ 3, продавец по ссылке), точка на карте (`…/map-point`), фотоотчёт (`…/photo-report`, скрыт до даты на уровне запроса) | M sonnet | 1-1 | этап 1 | тесты на лимиты и скрытие | — |
| 1-2c | Публичные ответы страницы и виджета расширены материалами; копирование материалов вместе с ивентом | S sonnet | 1-2a, 1-2b | этап 1 | существующие поля ответа не меняются (тест снимка) | — |

**Карточки.** 1-1: `internal/migrations/sql/0153_event_materials.sql`, `mediastore/mediastore.go` (`AllowedOwnerTypes`), `hmedia/media.go` (enum openapi). 1-2a/b: новый `httpserver/hmaterials/` + `mount_materials.go`; `queries/event_materials.sql`. 1-2c: `hfeed/public_page.go` (расширить ответ, не менять существующие поля), копирование — где живёт копия ивента у бота (`eventbot` → event-bundle) и в `hcatalog`.

### 5.2 Этап 2 «Показ HP-2 и счётчики HP-2a» (линия страницы; порядок на линии: П1 → П2 → этап 2 → П3)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| 2-1 | Страница: галерея, видео (`youtube-nocookie`), карта (ссылка + статичная схема), партнёры с продавцом по ссылке, фотоотчёт; CSP `frame-src` | M sonnet (Ф) | 1-2c, **П2** (тот же `render.ts`) | этап 2, решение 7 | `render.ts` одним PR; без сторонних скриптов и cookie; vitest + контейнерный smoke | — |
| 2-2 | Виджет: то же в `ArenaTickets.svelte`/компонентах | M sonnet (Ф) | 1-2c, **П2** (тот же `CartSheet.svelte`) | этап 2 | vitest + mock E2E; размер бандла в лимите CI | — |
| 2-3 | HP-2a: `POST /v1/public/pages/{slug}/{event_slug}/track`, счётчики по дням, показ в карточке (веб, бот), CSV | M sonnet, **opus-ревью** (публичная запись) | 1-1 | 8.3 этап 2, 8.8 п. 2 | allowlist видов, без IP/страны/часа, лимиты `enforceRateLimit`, бот-фильтр по UA в `suspect`; метрика с ограниченной кардинальностью; тест на накрутку сверх лимита → 429 | — |

### 5.П Линия П «Страница продаж организатора» (линия страницы + сервер)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| П1a | Блок продавца включён по умолчанию при полном профиле; телефон в `sellerinfo.Profile` | S sonnet | — | 8.9 | `show_seller` по умолчанию `true`, когда `sellerinfo.Missing` пуст; выключить может только суперадмин; статус полноты в боте и вебе | — |
| П1b | Поля покупателя: тристатус `collect_name`/`collect_phone` = `off|optional|required`, по умолчанию `optional`; одно поле «Имя и фамилия» | M sonnet (ревью sonnet: публичный checkout) | — | 8.9 | `BuildBuyerFields` отвязан от `required=enabled`; виджет показывает «(optional)»; 422 только при `required`; флаги живут в `sales_channels.settings` (jsonb) — миграции нет, нынешние `true` читаются как `required` (поведение не меняется); если флаги окажутся колонками — ST-5 | ST-5 |
| П1c | Предзаполнение Stripe: Customer (e-mail, имя, телефон) в аккаунте организатора перед Checkout Session (и для `connect`-конфига через `Stripe-Account`); `stripe_customer_id` у `customer_org_links` — колонка в 0163 вместе с О1 | M sonnet, ревью opus | П1b, **О1** | 8.9 | хостед-страница предзаполнена; повторная покупка переиспользует Customer; стаб Stripe в тестах расширен; ничего не меняется в суммах | ST-6 если Customer недоступен для connect-конфига |
| П1d | Flitt и AllPay: передать имя и телефон, если API принимает | S sonnet | П1b | 8.9 | по документации Flitt; AllPay уже получает | ST-6 |
| П2 | Согласие при покупке: `checkout_consents` (0160) в транзакции холда, галочка в виджете с двумя ссылками, 422 без принятия, флаг канала с проверкой полного профиля | **L opus** | П1a; спека 38 влита в master (сегодня на ветке `docs/chargeback-evidence-spec`, PR не влит — влить первым) | 8.9, 8.8 п. 5 | состав записи согласован со спекой 38 (версия рендера, язык, время, хеш IP, id сессии оформления); замок/порядок транзакции по правилам; виджет и страница — один PR на `render.ts`; e2e покупки с согласием | ST-3 (состав и тексты), ST-1 если флаг нельзя оставить выключенным для живых |
| П3a | Условия: модель и рендер — `legal_documents`/`legal_renderings` (0161), плейсхолдеры из `sellerinfo.Profile` + промоутер + три режима возврата, версия рендера, `GET /v1/public/pages/{slug}/terms` | **L opus** | П2 | 8.9, план 13.10 §4 | без текстов юриста — английский черновик с пометкой «ждёт юриста», линию не блокировать; организатор текст не редактирует; П2 ссылается на версию | ST-3 для текстов (не для кода) |
| П3b | Экран суперадмина: версии типового текста по языкам, предпросмотр с подстановкой, режим «свой текст» под проверкой оператора | Ф sonnet | П3a | план 13.10 §4 | `apps/admin-web/src/routes/legal.tsx`; право `legal.manage` только суперадмин | — |
| П3c | Ссылка на условия в письме с билетом и строка «Продавец» в PDF; страница условий на странице продаж | M sonnet | П3a, этап 2 (2-1) | 8.9 | тексты писем не меняются, только ссылка; 7 локалей | — |

**Карточки.** П1a: `sellerinfo/sellerinfo.go` (`Profile`, `Missing`), `hfeed/public_page.go` + `public_page_seller.go`, настройки канала (`hosted_page.show_seller`), `eventbot/org_data.go`. П1b: `hfeed/public_feed.go` (`BuildBuyerFields`), `public_feed_checkout.go` (422-ветки), `apps/widget/src/components/BuyerForm.svelte`, `lib/checkout.ts`, openapi `PublicBuyerInfo`, миграция данных канала (в 0160 или без миграции, если настройки в jsonb). П1c: `hfeed/payment_start.go` (`CustomerName/Phone` уже есть), `adapters/stripe/adapter.go` (Customer API, `customer` в сессии), `customers` (`customer_org_links.stripe_customer_id` — в 0163). П2: `hcheckout`/`hfeed` транзакция холда (`public_feed_checkout.go`), `0160_checkout_consents.sql`, виджет `CartSheet.svelte`/`BuyerForm.svelte`, `tickets-page/src/lib/render.ts`. П3a: новый `httpserver/hlegal/`, `0161_legal_documents.sql`; П3b: `apps/admin-web/src/routes/legal.tsx`; П3c: `delivery/templates` (ссылка), `delivery/pdf` (строка «Продавец»), `tickets-page/src/lib/render.ts`.

Что такое «близнец» для веба: Go-e2e бота (`bot_e2e_*_integration_test.go`, `eventbot/*_test.go` со стабом Telegram: `resend`, `promo`, `evstatus`, `categories`, `org_data`, `promoters`, `refunds_queue`, `wizard_edit`, `wizard_sale_times`, `refund`, `bot_e2e_sessions`, `bot_e2e_refund`) задаёт СЦЕНАРИЙ; близнец веба — Playwright-тест в `apps/event-center/tests/smoke` (харнесс 0-9), который проходит тот же сценарий через живой api и проверяет те же строки базы.

### 5.4 Этап 4 «Веб R3: повседневное» (веб-линия; API есть у бота)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| 4-1 | Повторная отправка билетов | Ф sonnet | 0-7, 0-9 | этап 4 | экран + близнец сценария `resend`; права по `GET …/me`; три роли | — |
| 4-2 | Отмена неоплаченного заказа (слово) | Ф sonnet | 0-7 | этап 4 | слово отмены заказа ≠ слово отмены сеанса (тексты из i18n бота) | — |
| 4-3 | Промокоды: список, создание, пауза, сеансы, удаление | Ф sonnet | 0-7 | этап 4 | близнецы `promo_*` e2e бота | — |
| 4-4 | Пригласительные: выдача, список, аннулирование + статус письма через билет приглашения | Ф sonnet + S сервер | 0-7 | этап 4, 3а пробел 1 | сервер: `letters[]` у приглашения; веб: экран | — |
| 4-5 | Команда: приглашение (каналы `web`/`telegram`), удаление с ролью в теле | Ф sonnet | 0-5 | этап 4 | `DELETE …/members/{id}` с `{role}` (AGENTS.md) | — |
| 4-6 | Промоутеры: список, правка, архив | Ф sonnet | 0-7 | этап 4 | близнец `promoters` | — |
| 4-7 | Данные организации (владелец) + «оплата не подключена» + статус полноты продавца (П1a) | Ф sonnet | П1a | этап 4, 8.9 | близнец `org_data` | — |

Правило этапа: на каждый экран тест на трёх ролях (владелец, менеджер, агент); заказы сайтов как продавцов блокируются тем же кодом, что в боте (`order.seller_site_order`).

### 5.5 Этап 5 «Веб R4: сеансы, категории, статус» (веб-линия + сервер для SCN-11)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| 5-1 | Статус мероприятия: опубликовать, снять, архив, удалить (слово, `delete-impact`) | Ф sonnet | 0-7, 0-9, 4-1…4-7 | этап 5 | близнец `evstatus` | — |
| 5-2 | Перенос и отмена сеанса: сухой прогон `change-impact`, слово, контакт организатора при `contact_missing` | Ф sonnet, ревью opus (пишет письма покупателям) | 0-7, 0-9 | этап 5 | близнец `bot_e2e_sessions`; блок сайтов `session.change_site_unsupported` показан честно | — |
| 5-3 | Конец продаж, «вход с» | Ф sonnet | 0-7, 0-9 | этап 5 | близнец `wizard_sale_times` | — |
| 5-4 | Категории: открыть, закрыть, цена, окно, количество | Ф sonnet | 0-7 | этап 5 | близнец `categories` | — |
| 5-5 | SCN-11: сводка охвата писем по сеансу после переноса/отмены («отправлено N, доставлено M, не дошло K») — сервер + веб + бот | M sonnet | 5-2 | этап 5, 3а пробел 2 | `GET …/sessions/{id}/change-letters-summary` из `email_letters`; показ в вебе и боте | — |

### 5.О Линия О «Подключение платёжки одной кнопкой» (сервер после этапа 1; веб после этапа 4)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| О0 | **Шаг владельца** (ST-7, урезан по В-7): в платформенном аккаунте Stripe включить Connect ТОЛЬКО ради `client_id` и OAuth для Standard-аккаунтов (Connect settings → Integration → OAuth), взять sandbox-ключ платформы (`sk_test_…`); Connect-вебхук НЕ нужен. Агент (haiku) готовит чек-лист и таблицу переменных | ST-7 владелец + R haiku | — | 8.11 | факты в журнал; если OAuth недоступен платформе — стоп В-7 к владельцу, Account Links не запасной путь | ST-4, ST-6, ST-7 |
| О1a | Схема и адаптер: `connection=keys|connect` + поля (миграция следующего свободного номера, вместе с `customer_org_links.stripe_customer_id` для П1c), `ResolveProviderConfig` различает оба, адаптер шлёт заголовок `Stripe-Account: acct_…` с платформенным секретным ключом для `connect` (поля `access_token`/`stripe_publishable_key` OAuth-ответа Stripe объявил устаревшими — не хранить); `config.go` + `Validate` (в production `STRIPE_PLATFORM_SECRET_KEY` и `STRIPE_CONNECT_CLIENT_ID` вместе, только у api). Списания прямые на аккаунт организатора, `application_fee` не включается (ST-2) | **L opus** | О0 | 8.11 | `keys`-конфиги не меняют поведения (тест на оба пути); стаб Stripe принимает `Stripe-Account` | ST-4 |
| О1b | OAuth Standard: `POST …/payment-configs/stripe/connect/start` (ОДНА кнопка для «есть аккаунт» и «нет аккаунта» — Stripe создаёт аккаунт в том же потоке; `state` одноразовый, привязан к организации и пользователю), callback меняет код на `stripe_user_id` и дописывает конфиг (`stripe_connect.go` подключается через `mount_commerce.go`, `Options.StripeConnect` из файла-соседа `wire.go`), затем api САМ регистрирует вебхук на аккаунте организатора: `POST /v1/webhook_endpoints` с `Stripe-Account`, `url` = существующий `…/payment-intents/webhook/{config_id}`, события из `hpayments/payment_config_verify.go` (checkout.session.*, payment_intent.*, refund.*), возвращённый `secret` → `secrets.webhook_secret` конфига; `POST …/disconnect` (deauthorize + удаление endpoint, best effort). Account Links не нужны | **L opus** | О1a | 8.11 | `state` одноразовый; чужая организация в callback → 403; endpoint создан ровно один раз (повтор callback не плодит); стаб расширен на `/oauth/token`, `/v1/webhook_endpoints`, `/v1/accounts/{id}` | ST-6 |
| О1c | **Отменена (В-7, 15.10).** Платформенного Connect-вебхука нет: события приходят на существующий маршрут `…/webhook/{config_id}` с секретом, который О1b сохранила в конфиг — машина вебхуков не меняется | — | — | 8.11 | — | — |
| О2 | Веб «Платежи» (владелец): кнопка Stripe («Подключить Stripe» — одна, для обоих случаев), статус «подключено» / «выплаты не включены», «Отключить»; Flitt/AllPay — направляемые формы ключей с проверкой и готовым адресом вебхука, «вебхук получен»; бот: ссылка вместо `bot.pay.not_connected` | Ф sonnet | О1b, 4-7, 0-9 | 8.11 | близнец e2e; права `payment_config.*` | — |
| О3 | `charges_enabled` как сигнал в `onboarding_checks` оператора: читается `GET /v1/accounts/{acct}` с `Stripe-Account` при подключении и по кнопке «Проверить»; KYB не автоматизируется | S sonnet | О1b | 8.11 | проверка `stripe_charges_enabled` в `checks.go`, тест | — |

**Карточки.** О1a: `adapters/stripe/adapter.go` (`PlatformAccountID` → заголовок `Stripe-Account`; Customer/Account/AccountLink вызовы), `hcheckout/provider_config.go` (`ResolveProviderConfig` + `connection`), `0163_payment_connect.sql`, `config.go` (`STRIPE_PLATFORM_SECRET_KEY`, `STRIPE_CONNECT_CLIENT_ID`, `STRIPE_CONNECT_WEBHOOK_SECRET`). О1b: `hbilling/stripe_connect.go`, `mount_commerce.go` (`mountStripeConnectRoutes`), новый `wire_stripe_connect.go` рядом с `wire.go`, `apps/widget/scripts/stripe-stub.cjs`. О1b также: `hpayments/payment_config_verify.go` (список событий — один источник для проверки и для регистрации endpoint). О2: `apps/event-center/src/screens/PaymentsScreen.tsx` + `router.tsx` (экраны живут в `src/screens/*Screen.tsx`), `eventbot/payments.go`.

### 5.S Линия S «Места по схеме» (четвёртая линия; S1 на сервере после этапа 3) — дальняя, файлы по разведке

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| S0 | Разведка: `hseating`, `domain/seating`, `session_seats` запросы, `public_schema.go`/`layout_svg.go`/`sbt10_svg.go`, виджет `seatmap-render.ts` | R haiku | — | 8.3 | карточки S1a–S1f дописаны файлами | — |
| S1a | Нестыковка 1.1: три рендера покупателю берут категорию из `session_seats.tier_id` | M sonnet, ревью opus | S0 | спека 26 §1.1 | виджет, `/compat/bil24/image`, `GET_SEAT_LIST` показывают одну цену после `PATCH …/seats/category`; интеграционный тест | ST-1 если меняет цены у живых сеансов Akropolis/Escape |
| S1b | Seated-ветка пакета события `source=arena`: `arenaSeatingPlanVersionId` + стартовая раскладка | **L opus** | S0, 3-1 | 8.3, спека 19 | сеанс со всеми местами; `dry_run` проверяет seated-пакет; `WarnSeatingNotImported` остаётся для `seatList` без svg | — |
| S1c | `seatlayout.ApplyBatch` + `session_seat_batches` с откатом (0156); перевод `…/seats/category` и `…/seats/admin` | **L opus** (замки) | S0 | 8.3, спека 26 §3.3 | замок `sessions` первым; проданные/в брони пропускаются; один bump версии на пакет; гонка с бронью — тест; откат | — |
| S1d | Раскладки `seat_layouts`, `ticket_tiers.color`, `seating_plans.annotations` (0157) | M sonnet | S1c | 8.3 | CRUD + применение раскладки через ApplyBatch | — |
| S1e | Аллокатор `domain/seating/layoutgen` + `POST …/sessions/{id}/seat-layout/suggest` (предпросмотр) | M opus (алгоритм) | S1d | спека 26 §4.1, решение владельца 15.10 | чистая функция, тест на Palác Akropolis, < 100 мс на 3 000 мест, ничего не применяет; **число категорий 1–15** (как `MaxCategories` геометрии, решение В-3 15.10, не 2–6) — тест на 15; ответ `suggest` называет алгоритм раскладки (`algorithm`), чтобы потом видеть, какие востребованы; на сеансе с продажами проданные места не трогает и считает выручку как «проданные по факту + остаток по новым ценам» | — |
| S1f | Право `session.seats.manage` менеджеру + `platform_superadmin` (0158) | S sonnet | — | 8.3 | парити-тест 532 зелёный | — |
| S2a | Мобильный макет автораскладки (дополнение артефакта 24.09) — показать владельцу | Ф sonnet + ST-7 | — | 8.8 п. 6, решение 9 | артефакт дополнен, владелец ответил | ST-7 |
| S2b | Компонент `<arena-seat-manager>` (Svelte, общее ядро с виджетом): режимы «Категории»/«Продажа», лоток, история | Ф-L sonnet + ревью opus | S1c, S1d, S2a | 8.3, макет артефакт 24.09 | встраивается в event-center и admin-web; e2e против живого API | — |
| S2c | Автораскладка в компоненте (предпросмотр `suggest`, ползунки — число категорий до 15 по В-3, «перенести как изменения»), в том числе перераскладка после начала продаж; выбранный алгоритм пишется в журнал выбора (аудит `metadata.algorithm`), владелец хочет видеть, какие алгоритмы востребованы | Ф sonnet | S2b, S1e | спека 26 §4.1, решение владельца 15.10 | e2e: сеанс с частичными продажами — проданные заморожены, выручка «по факту + остаток» | — |
| S3a | Веб-мастер: шаг «площадка со схемой → версия → раскладка» | Ф sonnet | S1b, S2b, 6-1c | 8.3 | включается тем же флагом сборки, что мастер | — |
| S3b | Mini App: `POST /v1/auth/telegram-miniapp` (initData, производный ключ `TELEGRAM_MINIAPP_SECRET_KEY`, свежесть ≤ 5 мин, суперадмину отказ), маршрут `/tg` в event-center, CSP `frame-ancestors`, кнопка «Назад» | **L opus**, независимое ревью входа | S2b | 8.3, 8.8 п. 1 | e2e: поддельная подпись → 401; просроченная → 401; корректная → сессия веба; ключ назван планом — не стоп | — |
| S3c | Бот: один вопрос мастера (версия схемы, раскладка), кнопка «Схема» `web_app`; до S3b — обычная ссылка; `wizard_load.go` правит seated-сеанс кроме мест | M sonnet | S1b | 8.3 | e2e мастера; `ErrNotEditable` только без сеансов | — |

**Карточки (разведка S0, 14.10, по master `d8695cb`).** Что есть: `hseating/` (authz, bind, category, handler, layout_svg, plans, public_schema, sbt10_svg, seats, types, versions; монтаж `mount_seating.go`, шимы `seating_shims.go`), `domain/seating/` (geometry.go: `Geometry{Canvas, Categories, Sections, Tables, DecorSVG}`, `Seat{Key, Number, X, Y, Radius, CategoryIndex, …}`; GA-зона = `Category` с `Kind == KindGeneralAdmission` и `Polygon`; `ImportSVG` svg_import.go:70, `ImportSBTSVG` sbt_import.go:121, transform.go), SQL `queries/session_seats.sql` + ручные обёртки `gen/session_seats.sql.go` (`LockSessionSeatsForHold`, `ListSessionSeatsByKeys`, `BulkSetSessionSeatTier` — только `kind='seat'` и available/unavailable, `ListSessionSeatsAdmin`, `IncrementSessionSeatStatusVersion`, `InsertSessionSeats`, `AllocateGAUnitsForHold`, `ReleaseSold*`); таблицу также читают `ga_quota.sql`, `tier_chain.sql`, `reservation_seats.sql`, `session_summary.sql`, `event_sales_state.sql`, `tickets.sql`. Миграции 0057 (планы и версии, права `seating_plan.*`, `event_session.assign_seating_plan` — `platform_superadmin` там НЕ выдан), 0058 (`session_seats`, `admission_mode` CHECK `general_admission|assigned_seats|hybrid`), 0084 (`kind`), 0101, 0109; `ticket_tiers.color` и `seating_plans.annotations` отсутствуют (образец ALTER — 0083). Telegram Mini App: кода нет (grep `initData`/`web_app` пуст); ссылка в браузер — `hbot/web_login.go` (`/login/link#code=`), `eventbot/web_login.go` URL-кнопка.
- **S1a.** Источник расхождения: `hseating/types.go:265 resolveCategoryTiers` ставит категории tier ПЕРВОГО места (anchor); так считают `public_schema.go`, `layout_svg.go` (~:302, места `sbt:cat` из геометрии), `sbt10_svg.go` (+ `hbil24/image.go` `imageCategoryIDs`); только `hbil24/cmd_seat_list.go` (`buildSeatList` ~:510) читает `tier_id` каждого места. Виджет: `apps/widget/src/lib/seatmap-render.ts` (`buildSeatMapSVG`, `buildPriceLabel(category_index, categoryPrices)`, `seatFillColor`), `SeatMapView.svelte:109–114` (`schema.category_prices`). Тесты: `hseating/public_schema_test.go`, `layout_svg_test.go`, `sbt10_svg_test.go`, `hbil24/bil24_501_image_test.go`, `tests/compat/bil24/image_sbt10_500_test.go`, `seat_list_499_test.go`, `gen/session_seats_category_integration_test.go`; теста «PATCH category → цена во всех трёх рендерах» нет — его и писать. Golden `tests/compat/bil24/testdata/wp/svg/palac_akropolis.sbt.svg`. Без миграции.
- **S1b.** `himports/import_arena.go` (`executeArenaImport`, `syncImportedCategoryQuotas(…, true, true, …)` — всё GA), `bil24_session.go:367 noteArenaIgnoredFields` (вызов :178), `seating.go:55 importSeating` (вызов только из Bil24-пути `import_exec.go:128`; `buildImportGeometry` переиспользовать), `types.go` `WarnSeatingNotImported`; спека 19 строки 21, 26, 75, 91, 175–177, 212–215. Тесты: `event_bundle_524_test.go` (`TestNoteArenaIgnoredFields`), `seating_518_test.go`, `event_bundle_525_integration_test.go`, `event_bundle_arena_ids_integration_test.go`.
- **S1c.** Пакета `seatlayout` и таблицы `session_seat_batches` нет — с нуля (0156). Переводимые обработчики: `hseating/category.go` (`HandlePatchSessionSeatsCategory`, tx :327–390; `HandleListSessionSeatsAdmin`), `seats.go` (`HandlePatchSessionSeats`, `applySeatBlock/Unblock`), `bind.go` (`InsertSessionSeats` :588, wipe :506); замок — `queries/sessions.sql` `GetSessionSeatingBindingForUpdate` + `IncrementSessionSeatStatusVersion`. Гонка с бронью — образец `hcheckout/hold_mutation_concurrency_integration_test.go`, `gen/ga_units_burst_integration_test.go`. Гейты сегодня `event_session.assign_seating_plan` (`mount_seating.go:96–104`; `org_isolation_flat_guard_test.go:283`, `perm_org_rows_integration_test.go:180`).
- **S1d.** 0157: `ticket_tiers.color`, `seating_plans.annotations`, `seat_layouts`. `queries/ticket_tiers.sql` + `gen/ticket_tiers.sql.go`; `queries/seating_plans.sql` (`SetSeatingPlanCategoryNameOverrides` — образец) + `gen/seating_plans.sql.go`; `hseating/plans.go` (`HandleUpdateSeatingPlan`, `normalizeCategoryNameOverrides` :687), `types.go` `SeatingPlanFromRow`; тесты `plans_test.go`, `versions_test.go`.
- **S1e.** `domain/seating/layoutgen/` — создать; вход `Geometry`/`Seat`/`Category` (`geometry.go`, `Canonicalize`); тестовые планы `domain/seating/testdata/Palac_Akropolis.svg`, `Palac_Akropolis_GA.svg`; ручка рядом с `seats/admin` в `mount_seating.go`, обработчик по образцу `category.go`; НЕ трогать `gaquota.Recompute`/`AdoptPlanCategory` (`bind.go:644`). Без миграции.
- **S1f.** Права `session.*` — 0016:75–78, `seating_plan.*` — 0057:187–245, менеджер (`organizer`) — 0115:48, 0122; `session.seats.manage` нигде нет; 0158 выдаёт его менеджеру и `platform_superadmin` в одном файле (гарды `superadmin_permission_parity_532_test.go` + `_integration_test.go`), затем гейты `mount_seating.go:96–104` переводятся с `event_session.assign_seating_plan`.
- **S2b.** Компонента нет; ядро виджета — `SeatMapView.svelte`, `lib/seatmap-render.ts`, `pan-zoom.ts` (`fitTransform`, `wheelZoom`, `pan`, `pinchZoom`), `selection.ts`, `poller.ts` (`SeatStatusPoller`); данные — `GET /event-sessions/{id}/seats/admin`, `PATCH …/seats/category`, `/schema`, `/seat-status`; тесты `seatmap-render.test.ts`, `pan-zoom.test.ts`, `selection.test.ts`. Внимание: `seatmap-render.ts:168` ссылается на `geometry.standing_zones`, которого в Go-модели нет.
- **S3b.** С нуля: вход рядом с `httpserver/mount_auth.go`, образец выдачи веб-сессии — `hauth/web_link.go` (`newWebSession`), ссылка бота — `hbot/web_login.go:156`; CSP веба — `apps/event-center/security-headers.conf.template`, `nginx.conf.template`.
- **S3c.** `eventbot/wizard_load.go:29–60` (`ErrNotEditable`; условие :54 сравнивает `AdmissionMode` со строкой `"seated"`, которой нет в CHECK 0058 — фактически защищает только `SeatingPlanVersionId`), `wizard.go`, `wizard_bot.go`, `wizard_keys.go`, `wizard_screens.go`, `wizard_io.go`, `web_login.go` (пока только URL-кнопки); тесты `wizard_load_test.go`, `wizard_edit_test.go`, `wizard_render_test.go`.

### 5.G Линия G «Генератор схем как сервис» (G1 сразу; G2 после S1, S2)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| G1 | Переезд `Venue_SVG_Generator` в `apps/venue-svg-generator` (`git subtree add`), Dockerfile, CI-job pytest, compose-сервис `venue_gen` без публикации порта; ROADMAP M3 = авторский SVG под `ImportSVG` | M sonnet | — | 8.4 | CI зелёный; образ собирается; история коммитов сохранена | — |
| G2a | HTTP генератора: `POST /generate`, `GET /jobs/{id}`, без состояния, выход — SVG + `seating-json-v1`-отчёт | M sonnet (Python) | G1 | 8.4 | профиль подставляет временную категорию, холст, радиус; карантин в отчёте | — |
| G2b | Шлюз арены: `venue_plan_generations` (0159) + владелец медиа `venue_plan_source` (`AllowedOwnerTypes` и CHECK в паре, тест на пару), `POST …/venues/{id}/plan-generations`, воркер `venue.plan_generate`, черновая версия схемы `needs_review`, уведомление, `usage_records` метрика `venue_plan_generation`; лимиты загрузки | **L opus** | G2a, S1b | 8.4 | публикация только человеком с `seating_plan.verify`; тест: сбой генератора → `failed`, ничего не опубликовано | — |
| G2c | Режим «проверка геометрии» в `<arena-seat-manager>` (карантин, низкая уверенность) + загрузка файла в мастере веба и боте | Ф sonnet | G2b, S2b | 8.4 | e2e | — |

`tools`-организации и внешний продукт — волна биллинга, в прогон не входят.

### 5.6 Этап 6 «Веб R2: мастер и правка» (веб-линия, после 3-1, 3-2, 3-3, 0-4; флаг сборки)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| 6-1a | Мастер, секции «Мероприятие» и «Площадка» (название, описание, возраст, промоутер, площадка с новой площадкой и зоной через 0-4): форма с проверкой на лету через `dry_run` | Ф-L sonnet + ревью opus | 3-1, 0-4, 0-7, 0-9 | этап 6 | ошибки по полям из `errors[].field` | — |
| 6-1b | Секции «Даты» и «Билеты» (сеансы, категории, цепочки, ступени, окна, конец продаж, «вход с») | Ф-L sonnet + ревью opus | 6-1a | этап 6 | пакет один в один с ботом (`BuildBundle`) — тест сравнивает пакеты веба и бота для одного набора ответов | — |
| 6-1c | Сохранение через `event-bundle`, итоговый экран, публикация в каналы | Ф sonnet | 6-1b | этап 6 | близнец сценария создания | — |
| 6-2 | Правка по частям и копия ивента через `GET …/event-bundle` | Ф sonnet | 6-1c, 3-2 | этап 6 | близнец `wizard_edit` | — |
| 6-3 | Афиша: загрузка, проверка 4:5, чтение через `poster-facts` | Ф sonnet | 3-3 | этап 6 | подсказки как кнопки, как в боте | — |
| 6-4 | Образец билета в мастере; флаг сборки `VITE_FEATURE_WIZARD` | S sonnet | 6-1c | этап 6 | на проде выключен, на стенде включён | — |

### 5.7 Этап 7 «Материалы и письма на всех клиентах»

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| 7-1 | EC-31: свой текст письма ивента (блок в `ticket` и `change`, плейсхолдеры, письмо-тест себе без записи в доказательную таблицу или с видом `test`) | M sonnet | 1-2 | этап 7, 3а п. 3, 5 | серверный фильтр ссылок и текста, без HTML; `template_version` отражает блок | — |
| 7-2 | EC-32: просьба об отзыве — вид `review_request` (0154), подавления `email_suppressions` (0155) из `unsubscribed`/`spam`, отписка, один раз каждому, возвращённые пропускаются, флаг сервера выкл | **L opus** | 7-1 | этап 7, решение 6 | транзакционные письма отписка не блокирует; рассылочные уважают; тест на пару CHECK/константа | ST-3 (включение на проде — только по слову владельца и юриста) |
| 7-3 | EC-30 бот: фото, видео, карта, «где ещё купить», фотоотчёт | M sonnet | 1-2 | этап 7 | e2e бота | — |
| 7-4 | Веб: те же редакторы материалов и письма | Ф sonnet | 1-2, 7-1 | этап 7 | близнецы | — |

### 5.8 Этап 8 «Веб R5: возвраты» (после стека возвратов; повторный пароль есть)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| 8-1 | Возврат по заказу и билету, часть суммы, с повторным паролем | **L opus** (деньги; экран может собрать sonnet под его ревью) | 4-1…4-7, 0-9 | этап 8 | близнецы `refund`, `bot_e2e_refund`; тестовый прогон на ABH TEAM (Stripe test) до выката | ST-7 |
| 8-2 | Очередь «Возвраты» и повтор | Ф sonnet | 8-1 | этап 8 | близнец `refunds_queue` | — |

### 5.Д Линия Д «Домен организатора» (после П3) — дальняя

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| Д0 | **Шаг владельца** (ST-7, в волне A): включить Cloudflare for SaaS в зоне (платёжные данные), дать токен с правами на custom hostnames; затем проба на тестовом поддомене (fallback origin, CNAME-цель `pages.arenasoldout.com`, Full strict + SNI) | ST-7 владелец + R haiku (чек-лист) + ops | — | 8.10 | факты в журнал | ST-4, ST-6, ST-7 |
| Д1 | `org_domains` (миграция следующего свободного номера), маршруты владельца, TXT-проверка (`_arena-verify`) и CNAME на `pages.arenasoldout.com`, три проверки с отдельным статусом — DNS, сертификат (Cloudflare for SaaS), «работает» (запрос страницы по хосту), автоперепроверка каждые 10 минут пока не «работает» и суточная после, уведомление владельцу в Telegram через продажного бота при смене состояния; Host-режим публичных маршрутов только внутри организации, динамические CORS и `ReturnURLPolicy`, выключение при снятом CNAME. Эскиз В-2 (15.10): https://claude.ai/artifact/W8pTjb336FZYH4K5yWbP91 раздел 2 | **L opus** | Д0, П3c | 8.10 | чужой слаг на чужом домене → 404; `X-Forwarded-Host` только от доверенного прокси; каждая из трёх проверок падает и поднимается отдельно; тесты | — |
| Д2 | Canonical, функция «адрес страницы организации» (из 0-3) везде: письма, бот, веб, QR; `/admin` → `events.arenasoldout.com`; веб-экран «Домен» по эскизу В-2: поддомен → две DNS-записи с «Копировать» → «Проверить» с тремя шагами → состояние «работает» с «Отключить» | M sonnet | Д1, 0-3 | 8.10, решение 18 | ссылки в письмах и у бота ведут на домен, когда он проверен; близнец e2e экрана | — |

### 5.М Линия М «Проходки МАКС в арену» (сервер после О1; показ с этапом 4)

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| М0 | Разведка: `scan_events` (0055), `/v1/scanner/scan-events`, `macs_webhook_subscribers`, спека 17 | R haiku | — | 8.12 | карточки дописаны | — |
| М1 | `POST …/macs/scan-events` (HMAC секретом подписчика, идемпотентность по `(subscriber, external_scan_id)`, пакеты задним числом), колонки `source`/`scanner_label`/`external_scan_id`/`subscriber_id` в `scan_events` (0164; `gate` и `device_id` там уже есть), `tickets.used_at` как «первый проход» (колонка 0055 уже есть — `first_admitted_at` НЕ добавлять, Решение прогона Р-6), событие `v1.ticket.scanned` (уже эмитится собственным сканером — внести в каталог спеки 15 и публиковать в одной транзакции со вставкой), счётчики входа по источнику в сводке сеанса | **L opus** | М0 | 8.12 | 202 с принятыми/отклонёнными; повтор пакета — no-op; спека 17 дополнена; решено явно, считается ли проход МАКС основанием блокировки возврата (`refunds/scanned.go`) | — |
| М2 | Показ проходок в карточке заказа и билета (бот, веб), фильтр «не прошли», колонки CSV | M sonnet | М1 | 8.12 | близнецы | — |

**Карточки (разведка М0, 14.10, по master `d8695cb`).** Расхождения с планом §8.12, учтённые выше: `tickets.used_at` (0055, `MarkTicketUsedAtIfUnset`) уже хранит первый допущенный скан и сводка уже считает «вошли N из M» (`horders/summary_core.go:100–105, 266`, `ListSessionSummaryTickets`); `v1.ticket.scanned` уже публикуется (`hscanner/scanner_callback.go:78`, отдельной транзакцией в `scanner_events.go:195–223`); таблицы `macs_webhook_subscribers` нет — подписчики МАКС это `webhook_subscribers` с `kind='macs'` (0089), секрет `signing_secret` (0040), запросы `gen/macs_webhook_subscribers.sql.go` (`GetMACSSubscriberByOrg`, `ListActiveMACSSubscribers`; lookup по id добавить); `scan_events` (0055): `gate`, `device_id` NOT NULL DEFAULT '', `result` CHECK `admitted|denied`, UNIQUE `(credential_code, scanned_at)`, колонки `source` нет; `PATCH …/tickets/{id}` не существует; `holderStatus` — поле `macs.Ticket` (`platform/macs/export.go:89`), не `orderexport`; callback `POST /v1/scanner/scan-events` (`hscanner/scanner_callback.go:134`, `mount_scanning.go:57`, Bearer agent_feed_token, до 500 сканов, частичный успех) резолвит билет только по static-QR (`ResolveScanCredentialByTicketQR`) — EAN-13 (`ticket_credentials.type='ean13'`) и human code там не резолвятся; спека 15 (`15_webhook_event_catalog.md`) не содержит `v1.order.paid`, `v1.ticket.cancelled`, `v1.ticket.scanned`, `v1.refund.*` — константы разбросаны (`macs/dispatcher.go:35`, `salesnotify/salesnotify.go:33`, `bil24wire/dispatcher.go:45`, `hscanner/scanner_events.go:54`, `htickets/tickets.go:447`, `refunds/scanned.go:34`).
- **М1.** Миграция 0154 (`scan_events`: `source`, `scanner_label`, `external_scan_id`, `subscriber_id`, UNIQUE `(subscriber_id, external_scan_id)`); `queries/scan_events.sql` + ручной `gen/scan_events.sql.go` + `gen/querier.go`; билет по МАКС — `GetTicketBySystemTicketID` (`gen/macs_system_ids.sql.go:98`) и `ticket_credentials` ean13; подпись по образцу `macs/dispatcher.go:418–424` (`X-MACS-Signature: sha256=<hex HMAC>`) и `macs/stub/stub.go:539`; обработчик — новый файл, маршрут в `mount_scanning.go` или `mount_catalog.go` (рядом `…/macs-webhook` :206–218); логика скана по образцу `scanner_callback.go:224–341`; outbox — `outbox/writer.go:162` `PGEventsWriter.Append` в той же tx; риск — `refunds/scanned.go:60–80` читает `scan_events` `result='admitted'` и `used_at`: проход МАКС станет основанием блокировки возврата — решить явно; документы: спека 15 (каталог), спека 17 §Internal Scanner Endpoints (:322). Тесты: `httpserver/scanner_callback_293_test.go`, `hscanner/scanner_callback_status_test.go`, `httpserver/openapi_scan_events_278_test.go`, `refund_scanned_round3_integration_test.go`, `admin_ticket_scans_295_test.go`, `macs/stub/stub_test.go`, `cmd/arena-worker/outbox_legs_test.go`.
- **М2.** Бот `eventbot/order_card.go` (`ticketLine` :217–238 — «вошёл» уже печатается из `UsedAt`, ключ `bot.ord.entered`; добавить вход/сканер), веб `apps/event-center/src/screens/OrderScreen.tsx:181` (`tk.used_at`), API `horders/detail.go:82` + `OrderTicketSummary` в openapi; списка билетов сеанса нет (только `GET /v1/checkout/{id}/tickets`) — фильтр «не прошли» требует нового чтения; CSV `hexport/sales.go:18–24, 134–168` (`ColEntered`), `queries/export.sql:34`, `gen/export.sql.go`, `csvexport/labels.go`; админ `htickets/admin_ticket_scans.go` `ScanEventToMap:116`, `apps/admin-web/src/routes/tickets.tsx:1380–1500`. Тестов `hexport` нет.

### 5.V Линия V «VIP-флаг»

| ID | Задача | Класс | Зависит | План | Приёмка | Стоп |
|---|---|---|---|---|---|---|
| V1 | `tickets.vip` + `vip_set_by/at` (0165), `PATCH …/tickets/{id}/vip`, аудит, кнопка в карточке заказа и в выдаче приглашения (бот, веб), метка в списках | M sonnet | — | 8.12 | право `ticket.update`; пригласительный — тот же столбец; PDF и письмо не меняются (тест) | — |
| V2 | Уведомление при проходке VIP: нога `salesnotify` на `v1.ticket.scanned`, подписка `on_vip_scan`, одно на билет, текст с именем гостя, входом, сканером | M sonnet | V1, М1 | 8.12 | повторный скан не повторяет; срабатывает с обоих источников | — |
| V3 | `vip` в выгрузке МАКС, снимке и ответе сканера | S sonnet | V1 | 8.12 | поле необязательное, старые версии игнорируют; реализация экрана — волна приложений, не здесь | — |

**Карточки (разведка М0, 14.10).** V1: 0165 (`tickets.vip boolean NOT NULL DEFAULT false`, `vip_set_by`, `vip_set_at`); маршрут `PATCH /v1/organizations/{org_id}/tickets/{id}/vip` по образцу `refund_org_routes.go:49–50`, право `ticket.update` (0054; у менеджера с 0129), org-guard через `GetTicketOrgID` (`queries/tickets.sql:48`), запрос по образцу `ClearTicketReviewHold` (:129), аудит по образцу `htickets/admin_ticket_scans.go:148–179`; кнопки — `eventbot/order_card.go:243 orderCardKeyboard`, `OrderScreen.tsx:181`, список приглашений `htickets/complimentary_list.go`, выдача — `complimentary.go:61–71` (`createComplimentaryIssuanceRequest`); PDF (`delivery/pdf/`) и письма не меняются — тест на неизменность. V2: 0165 также `sales_notification_subscriptions.on_vip_scan` (образец 0124); `salesnotify/salesnotify.go` `Subscription:60`, `Route:86`, whitelist `Dispatch:165` (+ `v1.ticket.scanned`), `compose:232`, ключ дедупа `vipscan:<ticket>`; `store.go` `Subscriptions:22`, имя гостя по образцу `RefundByTicket:90`; проводка `cmd/arena-worker/main.go:739 buildSalesNotifier`, `:206 outboxLegs`; тесты `salesnotify_test.go`, `store_integration_test.go`, `outbox_legs_test.go`. V3: `macs/export.go` `Ticket:73–92` (+ `vip` omitempty), `encodeTicket:184`, проекция `orderexport/orderexport.go:56–81`, `query.go:71`, `build.go:95`; снимок `hscanner/scanner_snapshot.go` `SnapshotBarcodeResponse:52–58` + `queries/barcodes.sql:108`, validate `ValidateBarcodeResponse:64–72`; спека 17 §JSON Export (:51); тесты `macs/export_golden_test.go` (список ключей ~:97, :184), `export_unit_test.go`, `testdata/sample_tickets.json`, `dispatcher_test.go:209`, `hscanner/scanner_validate_status_test.go`, `httpserver/scanner_snapshot_144_test.go`.

## 6. Порядок прогона (одна итерация оркестратора)

1. **Старт:** оркестратор создаёт журнал, проверяет голову миграций на `origin/master` против резерва (ST-5), читает решения 1–18.
2. **Волна A (параллельно, три линии):** серверная 0-1…0-5, 0-8 → 3-1, 3-2, 3-3; веб 0-6, 0-7, 0-9; страница П1a, П1b, П1d; плюс G1, разведки М0, S0 и **шаги владельца О0 и Д0** (их просят в первый день, чтобы к волне B и E входы были; без них О и Д просто ждут, остальное идёт). Влить PR спеки 38 (`docs/chargeback-evidence-spec`) — нужен П2.
3. **Волна B:** серверная 1-1, 1-2a/b/c → О1a → О1b, О1c → П1c → М1; веб 4-1…4-7; страница П2 → 2-1, 2-2, 2-3; компонент S1a (после S0), S2a (макет владельцу).
4. **Волна C:** серверная S1c → S1d → S1e, S1b, S1f → 7-1, 7-2; веб 5-1…5-5 → О2, О3, V1, М2; страница П3a → П3b, П3c; компонент S2b → S2c (внутри волны строго по очереди).
5. **Волна D:** веб 6-1a → 6-1b → 6-1c → 6-2…6-4 (флаг), затем S3a, S3c; сервер S3b, G2a, G2b; компонент G2c; V2.
6. **Волна E:** веб 7-3, 7-4, 8-1, 8-2; сервер Д1 → Д2; V3.
7. После каждой волны: полный `go test ./...` + фронтовые прогоны на master, выпуск по протоколу плана §4 (стенд → ручной обход → выкат по слову владельца — это ST-7, единственный обязательный вызов владельца на волну).
8. Оркестратор заканчивает прогон, когда каталог исчерпан или все оставшиеся задачи стоят на стоп-условиях; итог — в журнале и в памяти.

## 7. Что проверяет оркестратор у каждой задачи перед «готово»

1. PR ссылается на ID задачи и раздел плана; CI зелёный; ворота раздела 3 с кодами выхода в журнале.
2. Для M: ревью чужой сессией по карточке (sonnet). Для L: независимое ревью opus, раунд замечаний закрыт.
3. Для Ф: близнец-тест e2e зелёный против живого API; строка матрицы паритета получила фазу «готово».
4. Карточка не была расширена сверх границ (если расширена — «Решение прогона» с обоснованием).
5. AGENTS.md дополнен, если нашлась новая ловушка; `deploy/DOKPLOY.md` — если появилась переменная.
