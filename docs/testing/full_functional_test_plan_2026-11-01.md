# Полный функциональный тест-план платформы Arena

Дата: 2026-11-01. Автор: Claude Code (QA architect), по заданию владельца.

## 0. Основа плана и правила чтения

**Что изучено.** Код `origin/master` на коммите `23b7d49e` (Merge PR #342, session-sales-state — это же состояние прода по журналу владельца), отдельный репозиторий `C:\Projects\Venue_SVG_Generator` (ветка с `663fb12`), CI-конфигурации, сиды тестовых данных, существующие тесты (Go, Vitest, Playwright, pytest), навыки и отчёты Hermes, репозиторий маркетингового сайта `C:\Projects\arena-site` (только CI).

**Важно про рабочую копию.** Ветка `feat/ec-01-bot-dialogs` в `C:\Projects\arena_new` отстаёт от `origin/master` на 1884 коммита: в ней нет `apps/event-center`, `apps/shop-page`, `apps/venue-svg-generator` и многого другого. Все пути в документе даны **от корня репозитория на `origin/master`**. Ссылка из этого файла откроется, только когда рабочая копия будет на master.

**Чего этот план НЕ утверждает.** Я ничего не запускал: ни тестов, ни сборок, ни запросов к стендам. Отметка «✅ есть» значит, что **тест существует и по коду проверяет названное**. Она не значит, что тест сейчас зелёный. Статус прогона CI на `23b7d49e` я не проверял (`gh` в этой среде недоступен). Первый шаг любого релиза: зафиксировать зелёный прогон всех джобов на коммите релиза (гейт G-01).

**Отметки покрытия:** ✅ есть конкретный тест · ◐ частично (есть тест, но не на всё, что требует кейс, или только unit/mock) · ✗ теста нет · ⊘ функции нет в коде (кейс описывает ожидаемое поведение или проверку отсутствия).

**Сокращения путей:**

| Сокращение | Путь от корня репозитория |
|---|---|
| `B/` | `apps/backend/` |
| `P/` | `apps/backend/internal/platform/` |
| `HS/` | `apps/backend/internal/platform/httpserver/` |
| `EB/` | `apps/backend/internal/platform/eventbot/` |
| `CB/` | `apps/backend/tests/compat/bil24/` |
| `EC/` | `apps/event-center/` |
| `AW/` | `apps/admin-web/` |
| `W/` | `apps/widget/` |
| `TP/` | `apps/tickets-page/` |
| `SP/` | `apps/shop-page/` |
| `VG/` | `apps/venue-svg-generator/` (копия генератора в монорепо, авторитетна для сервиса и CI) |
| `VSG/` | `C:\Projects\Venue_SVG_Generator` (исходный репозиторий: PDF/растровые эксперименты, оркестрация) |

**Поля тест-кейса** (в таблицах раздела 3): ID · P (P0/P1/P2) · Роль · Сервисы и точки коммуникации · Предусловия и данные · Шаги · Ожидаемый результат · Evidence · Уровень · Авто (да/частично/нет) · Исполнитель (CI, Codex, Claude Code, Hermes, Человек) · Блок (блокирует ли провал production-релиз) · Покрытие сейчас.

**Безопасность самого документа.** Репозиторий `avamb/arena-platform` переведён в приватный 01.11.2026. Документ можно коммитить. В разделе D перечислены пробелы **тестового покрытия**, а не уязвимости; детали незакрытых уязвимостей по-прежнему не пишутся в репозиторий до выката исправления.

**Решения владельца от 01.11.2026** (учтены во всём плане):
1. Ручные и Hermes-прогоны идут **на проде в организации #1 ABH TEAM** (slug `abhteam`): там подключён Stripe в тестовом режиме, настоящих мероприятий нет. Это заменяет прежнее правило «только стенд» для этой организации.
2. Тестовые карты Stripe (4242 4242 4242 4242 и другие из документации Stripe) **разрешены** для тестовых покупок. Вводит их Hermes (своим механизмом) или человек, и только на странице `checkout.stripe.com` с плашкой Test mode. Claude Code карту в чужие формы не вводит.
3. Сценарии выполняются **по навыку Hermes** `arena-ux-tester` (у него уже есть функциональный прогон F0–F10 и прогон «с нуля» Z0–Z9).
4. Нужен новый тест «совсем с нуля»: новая организация → подключение Stripe → первая платная продажа. Отдельно надо сравнить, какими путями организатору легче всего завести аккаунт Stripe и подключить его (раздел 3.14).

---

## 1. Карта системы

| Компонент | Где в коде | Как работает |
|---|---|---|
| arena-api | `B/cmd/arena-api`, маршруты `HS/mount_*.go`, спецификация `B/openapi/openapi.yaml` | Go, REST `/v1`, chi-роутер `B/internal/adapters/http/router.go` |
| arena-worker | `B/cmd/arena-worker/main.go` | 34 регистрации обработчиков задач `worker_jobs` и диспетчер outbox |
| arena-bot | `B/cmd/arena-bot`, `EB/` | Telegram long polling (go-telegram/bot), REST-клиент к arena-api |
| arena-migrate / arena-seed / arena-e2e-seed | `B/cmd/...`, миграции `B/internal/migrations/sql/` | 180 файлов, голова `0183_session_sales_state.sql` |
| PostgreSQL 17, Redis 7 | `docker-compose.yml` | Redis держит сессии (`P/redissession`) |
| Event Center | `EC/` | React, events.arenasoldout.com, и мини-приложение Telegram `/tg*` |
| Admin | `AW/` | Консоль суперадмина |
| Widget | `W/` | Svelte custom element `<arena-tickets>`, Shadow DOM |
| Hosted-страницы | `TP/` | tickets.arenasoldout.com/{promoter}[/{event}] |
| Магазин | `SP/` | Merch-витрина |
| WP-плагин (нативный) | `apps/wp-plugin/arena-events` | Синк фида, шорткод виджета, прокси checkout, вебхук HMAC |
| Bil24-шлюз | `HS/hbil24`, `B/internal/adapters/bil24compat` | `/compat/bil24/json` для сайтов Lampyris и Vino&Co. Их плагин живёт вне репозитория |
| Платёжные адаптеры | `B/internal/adapters/{stripe,flitt,allpay,payplus,stripebilling}` | Реестр `B/internal/app/payments/modules.go` |
| Почта | `P/delivery`, `B/internal/adapters/email`, `P/brevo`, `P/emailstatus` | SMTP/Brevo; `EMAIL_MODE=log` по умолчанию вне прода |
| Генератор схем | `VG/` (FastAPI `service/app.py`, порт 8090), compose-сервис `venue_gen` (профиль `venue-gen`) | **Backend его не вызывает**: клиента в коде нет |
| Маркетинговый сайт и анкета | `C:\Projects\arena-site` (Astro + Cloudflare worker) | Отдельный CI |
| MACS (сканирование) | Внешняя система; исходящий диспетчер `P/macs`, входящий `HS/hmacsscan` | |
| Hermes | Навык `ops/hermes/skills/arena-ux-tester/` | Есть только в рабочей копии владельца, не закоммичен. Отчёты в `docs/testing/hermes_*` |

---

## 2. Как устроен CI сейчас (факты)

Файл: `.github/workflows/ci.yml`.

| Джоб | Что гоняет | Входит в `needs` у `build-and-push`? |
|---|---|---|
| `lint` | golangci-lint | да |
| `test` (Unit / Race) | `go test -race ./...` (ci.yml:88) | да |
| `integration` | migrate + seed на чистой БД, `go test -tags=integration -timeout=25m ./...` (ci.yml:189); сюда же попадают все `HS/bot_e2e_*` | да |
| `openapi-check` | дрейф Go-типов и TS-клиента | да |
| `admin-build` | type-check, vitest, build, Playwright mobile (3 страницы), container smoke | да |
| `widget` | vitest, build, size-limit, Playwright mock (ci.yml:806) | да |
| `widget-acceptance` | реальный arena-api + Stripe-заглушка `W/scripts/stripe-stub.cjs`, `npm run test:e2e:real` (ci.yml:880) | да |
| `event-center-build` | type-check, vitest (~1350 кейсов), build, Playwright mock smoke, container smoke с проверкой заголовков (ci.yml:520) | **нет** |
| `event-center-live` | реальный arena-api (`-tags e2eposter`), `arena-e2e-seed`, ~170 Playwright-тестов в `EC/tests/smoke/live/` (ci.yml:629) | **нет** |
| `tickets-page-build` | vitest, build, container smoke `/health` | **нет** |
| `shop-page-build` | то же для магазина | **нет** |
| `rich-editor-build` | vitest после сборки | **нет** |
| `venue-svg-generator` | pytest (с фильтром по путям), docker build, `/healthz` (ci.yml:726) | **нет** |
| `.github/workflows/accessibility.yml` | axe по **статичным** HTML-снимкам WP-плагина | — |
| `.github/workflows/load-test.yml` | k6, вручную или по метке `load-test` | — |

**Вывод.** Если упадут event-center, hosted-страница, магазин или генератор, сборка образа на `master` всё равно пройдёт. Защищает только branch protection, а её настройки я не видел. Это первый P0-пробел процесса (см. G-02 и PR-01).

---

## 3. Тест-кейсы по системам

### 3.1 Backend: аутентификация, сессии, восстановление доступа (AUTH)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| AUTH-01 | P0 | Организатор | EC → `POST /v1/auth/login`, `/refresh`, `GET /v1/me`, `POST /v1/auth/logout`; Redis-сессии | `e2e-owner@test.arena.local` из `arena-e2e-seed` | 1) login; 2) `/me`; 3) refresh; 4) повторить refresh старым токеном; 5) logout; 6) refresh после logout | 1–3: 200; 4: 401 (ротация, повтор отвергнут); 6: 401 | Ответы API, ключи Redis до и после | integration | да | CI | да | ◐ `HS/auth_production_integration_test.go` (только happy path); повтор refresh и отзыв не проверены; у `P/redissession` нет тестов |
| AUTH-02 | P0 | Аноним | `POST /v1/auth/login`, `trustedRealIP` | `TRUSTED_PROXY_COUNT=0` и `=1` | 6 неверных паролей подряд, затем с подменённым `X-Forwarded-For` | 6-я попытка 429; подмена XFF не сбрасывает лимит; запрос идёт через `router.ServeHTTP` | Ответы API | integration | да | CI | да | ✅ `HS/auth_login_test.go` (TestAuthLogin358_ForgedXFFDoesNotBypassRateLimit), `B/internal/adapters/http/trusted_real_ip_test.go` |
| AUTH-03 | P0 | Организатор | `POST /v1/auth/password-reset/request`, `/confirm`; worker `auth.password_reset_email`; nginx EC | Пользователь из сида; неизвестный e-mail | 1) запрос для неизвестного адреса; 2) запрос для своего; 3) confirm токеном; 4) confirm тем же токеном снова; 5) confirm просроченным | 1: 202 без утечки; 3: 200, вход с новым паролем; 4, 5: 4xx; в БД только хэш токена; в access log nginx нет токена | API, `password_reset_tokens`, письмо (`EMAIL_MODE=log`), access log | integration | да | CI | да | ✅ `HS/auth_password_reset_integration_test.go`, `HS/auth_pr2_03_359_test.go`, шаг проверки access log в `event-center-build`; ✗ лимит частоты на `/password-reset/request` |
| AUTH-04 | P1 | Новый пользователь | `POST /v1/auth/register`, `GET /v1/auth/verify`, job `auth.email_verification` | Свежий адрес `@example.test` | register → письмо → verify → повторный verify → просроченный токен | Вход после verify; повтор и просрочка дают 4xx | API, `worker_jobs`, лог письма | integration | да | CI | нет | ✅ `HS/auth_email_integration_test.go` |
| AUTH-05 | P0 | Приглашённый менеджер | `POST /v1/admin/users`, `POST .../members`, `HS/hbot/invitation_web.go` → `/accept-invite` → `password-reset/confirm` | Новый e-mail | Создать приглашение, открыть ссылку, задать пароль, войти | Ссылка ведёт в event-center, а не в админку; пароль задан; роль = приглашённой; повторное открытие ссылки даёт 4xx | API, `memberships`, письмо | integration + E2E | да | CI | да | ◐ `HS/bot_invitation_web_integration_test.go`; в EC только mock `r1.spec.ts`; ✗ live E2E |
| AUTH-06 | P0 | Владелец | Step-up на денежных маршрутах (`POST /v1/auth/step-up`, `HS/step_up_guard.go`) | Сессия старше окна простоя | Возврат без step-up, затем со step-up | Без step-up отказ; со step-up 200 | API, audit | integration | да | CI | да | ✅ `HS/step_up_guard_test.go` (TestStepUp_EveryMoneyRouteRunsTheGuard), `HS/web_session_stepup_integration_test.go` |
| AUTH-07 | P1 | Организатор в Telegram | `POST /v1/auth/telegram-miniapp` (initData) | Подписанные и неподписанные initData | Валидные, просроченные, с чужим секретом | Только валидные дают сессию на 30 мин; refresh её не продлевает | API | unit + integration | да | CI | нет | ✅ `HS/hauth/telegram_miniapp_test.go`, `HS/telegram_miniapp_integration_test.go` |
| AUTH-08 | P1 | Организатор | `POST /v1/bot/web-login-codes` → `POST /v1/auth/web/link` | Привязанный Telegram | Кнопка «Открыть в браузере», вход, повтор кода, выход «везде» | Код одноразовый; выход отзывает все веб-сессии | API, сообщения заглушки Telegram | E2E (бот) | да | CI | нет | ✅ `HS/bot_e2e_web_login_integration_test.go` |
| AUTH-09 | P1 | Суперадмин | `POST /v1/admin/impersonate` | Суперадмин + `X-Admin-Reason` | Войти как пользователь, проверить срок и действия | Максимум 30 мин; audit с причиной; нет прав выше прав цели | API, `audit_events` | integration | да | CI | нет | ✗ `HS/impersonation_167_test.go` проверяет только существование файла и константы |
| AUTH-10 | P0 | DevOps | `P/config/config.go` `validateProduction` | `APP_ENV=production` | Поднять конфиг с каждым запрещённым значением: dev-auth, `CORS=*`, `STRIPE_API_BASE_URL`, `FLITT_API_BASE_URL`, слабый JWT, `EMAIL_MODE=log`, `EGRESS_ALLOW_PRIVATE_TARGETS` | Каждый вариант отклонён при старте | Вывод тестов | unit | да | CI | да | ◐ `P/config/config_test.go`. Нужна табличная проверка ровно этого списка |
| AUTH-11 | P2 | Организатор | Лимит одновременных сессий | 6+ логинов | Логины с разных клиентов | Срабатывает политика `maxConcurrentSessions` | Redis, API | integration | да | Claude Code | нет | ✗ |

### 3.2 Backend: организации, роли, изоляция, API-ключи (IAM)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| IAM-01 | P0 | Владелец орг. A | Все маршруты `/v1/organizations/{org_id}/...` | Две TEST-организации, у каждой есть дочерние строки | Для каждого дочернего маршрута запрос владельца A к id орг. B (GET и запись) | 404; в орг. B ничего не изменилось; новый маршрут без записи в таблице обхода ломает тест | Таблица результатов, снимок БД до и после | integration + security | да | CI | да | ✅ `HS/org_isolation_sweep_integration_test.go`, страж `HS/org_isolation_sweep_table_test.go` |
| IAM-02 | P0 | Владелец орг. A | Плоские маршруты: `/v1/refunds/{id}*`, `/v1/payment-intents/{id}*`, `/v1/events/{id}`, `/v1/media/{id}`, `/v1/tickets/{id}/cancel` | То же | Чужой id по каждому плоскому маршруту | Собственный 404 маршрута, не 403; ничего не меняется | API, БД | integration + security | да | CI | да | ✅ `HS/org_isolation_flat_sweep_integration_test.go`, `HS/org_isolation_flat_guard_test.go`, `HS/server_orgauth_row_test.go`, `HS/refund_org_isolation_integration_test.go`, `HS/orgread_cross_org_integration_test.go` |
| IAM-03 | P0 | Владелец, менеджер, агент | Реальный роутер, JWT только с membership (без roles claim) | Три человека из `arena-e2e-seed` | Обойти все гейты ивент-центра каждой ролью | Менеджер проходит операционные гейты и получает 400/404 маршрута вместо 403; агент получает 403; владельческие права у менеджера отсутствуют | API | integration | да | CI | да | ✅ `HS/manager_permissions_0129_integration_test.go`, `EC/tests/smoke/live/events-three-roles.spec.ts` |
| IAM-04 | P0 | Суперадмин | `markSuperadminOrgAccess`, `RequireAdminReason` | Суперадмин | Запись без `X-Admin-Reason`, затем с ним | Без заголовка 400 `superadmin.missing_reason`; с ним проходит и пишется в audit | API, `audit_events` | integration | да | CI | да | ✅ `HS/superadmin_org_access_395_test.go`, `HS/admin_orgs_cross_org_integration_test.go` (TestSEC1_AdminOrgRoutesRefuseOwnersAndKeys) |
| IAM-05 | P0 | Владелец | `POST .../api-keys`, `P/apikeys`, `HS/server_apikey_auth.go` | Канал орг. A и канал орг. B | Выпустить ключ с запрещённым scope; привязать к чужому каналу; использовать старый ключ с ныне запрещённым scope | 4xx при выпуске; 422 `api_key.invalid_channel`; эффективные scope без запрещённых | API | unit + integration | да | CI | да | ◐ `P/apikeys/apikeys_test.go`, `HS/server_apikey_auth_test.go`; в `HS/hapikeys` нет тестов |
| IAM-06 | P1 | Владелец | Снятие последнего владельца | Одна организация, один `org_admin` | Снять роль | Отказ | API | integration | да | CI | нет | ✅ `HS/membership_last_owner_integration_test.go` |
| IAM-07 | P0 | Владелец | `PATCH /v1/organizations/{id}` (`kyb_status`) | Организация `unverified` | Поставить `verified` | 403 `org.kyb_status_superadmin_only`; `pending` разрешён | API | integration | да | CI | да | ✅ `HS/org_kyb_self_verify_integration_test.go` |
| IAM-08 | P0 | Владелец | `payment_mode=merchant_of_record` у канала | Канал `direct_merchant` | PATCH на MoR владельцем, затем суперадмином | Владелец получает 403; суперадмин с причиной проходит | API | integration | да | CI | да | ✅ `HS/channels_mor_integration_test.go` |
| IAM-09 | P1 | Менеджер | `/organizations/{org}/promoters`, общее пространство slug с организациями | Две организации | CRUD и попытка занять slug чужой организации | 409 `promoter.duplicate_slug`; чужой промоутер недоступен | API | integration | да | CI | нет | ✅ `HS/promoters_integration_test.go` (TestPromoters0113_CRUDAndOrgIsolation) |
| IAM-10 | P1 | Владелец архивной орг. | `refuseArchivedOrganization` (`HS/org_archived_guard.go`) | Организация с `deleted_at` | Любой запрос | Отказ, данные не меняются | API | integration | да | Claude Code | нет | ◐ тест в обзоре не найден, проверить; EC unit `ArchivedOrgs.test.tsx` |
| IAM-11 | P1 | Заявитель, оператор | `/v1/onboarding/*` → `/v1/admin/onboarding/applications/{id}/approve` → `P/provisioning` | `ONBOARDING_ENABLED=1`, Turnstile в тестовом режиме | Черновик → подтверждение по ссылке → submit → approve | Организация, канал `direct_merchant` с `fee 0.00`, владелец, письмо о задании пароля, уведомление оператору; MoR не включён | API, БД, `worker_jobs` | integration | да | CI | нет | ✅ `HS/onboarding_integration_test.go`, `P/onboarding/service_integration_test.go`, `HS/bot_e2e_onboarding_integration_test.go` |
| IAM-12 | P1 | Аноним | Лимиты заявок (5 в час на e-mail, 10 в час на IP, суточный общий), ротация токена при confirm | — | Превысить лимиты; открыть ссылку во второй вкладке | 429 и понятная ошибка; токен второй вкладки мёртв | API | integration | да | Claude Code | нет | ◐ `HS/honboarding/turnstile_test.go`; лимиты отдельным тестом не найдены |

### 3.3 Backend: безопасность (SEC)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| SEC-01 | P0 | Платёжный провайдер | `POST /v1/payment-intents/webhook`, `/webhook/{config_id}` | Stripe, Flitt, AllPay, PayPlus в тестовом режиме, у каждой организации свой секрет | Для каждого провайдера: валидная подпись; неверная; пустая; старая метка времени (вне 5 мин); повтор доставленного события; событие с подписью чужой организации; событие чужого платежа | 200 и обработка; 401; 401; 401; 200 `processed:false` без второй выдачи билетов; 401 или игнор; 200 `acknowledged, processed:false` | Ответы, `payment_intent_events`, число билетов и писем | integration + security | да | CI | да | ◐ подпись валидна/невалидна: `B/internal/domain/payments/provider_134_test.go`, `B/internal/adapters/stripe/stripe_135_test.go`, `B/internal/adapters/flitt/adapter_test.go`; две организации: `HS/hosted_checkout_integration_test.go` (TwoOrgsUseTheirOwnWebhookSecrets); дубль: `HS/allpay_checkout_integration_test.go` (DuplicateDeliveryAnswers200); ✗ просроченная метка времени и повтор для Stripe и Flitt |
| SEC-02 | P0 | Провайдер | `POST /v1/refunds/webhook`, `/v1/billing/stripe/webhook`, `/v1/billing/bank/webhook/{provider}` | Тестовые секреты | Неверная, пустая и старая подпись на каждом | 401, состояние не меняется | API, БД | integration + security | да | CI | да | ✗ отдельных негативных тестов не найдено (банковский вебхук затронут только в `HS/billing_accounts_integration_test.go`) |
| SEC-03 | P0 | Brevo | `POST /v1/webhooks/brevo/{token}` | Токен задан и не задан | Без токена; неверный токен; огромное тело | Маршрут не смонтирован без токена; отказ на неверный токен и большое тело | API | integration | да | CI | да | ✅ `HS/brevo_webhook_integration_test.go` |
| SEC-04 | P0 | Организатор | `POST /v1/media`, `GET /v1/media/{id}`, `GET /v1/media-files/{id}?expires&sig`, `DELETE` | Файлы: PNG, JPEG, PNG под видом `.pdf`, HTML под видом `image/png`, 33 МиБ | Загрузка каждого; подделка `sig`; просроченный `expires`; удаление чужого | Тип определяется по байтам; 413 сверх 32 МиБ; 403 на подделку и просрочку; чужое даёт 404 | API, `media_objects` | integration + security | да | CI | да | ◐ сниффинг `HS/hmedia/media_sniff_integration_test.go`; чужой файл `HS/org_isolation_media_integration_test.go`; подпись на уровне хранилища `P/mediastore/mediastore_test.go`; ✗ лимит 32 МиБ и подделка подписи на HTTP-уровне |
| SEC-05 | P0 | Организатор | Исходящие запросы по URL пользователя: side-load афиши, WP- и MACS-вебхуки, generic webhook subscribers, импорт клиентов, `hwordpress` | Адреса `127.0.0.1`, `169.254.169.254`, `10.x`, DNS-rebinding, редирект на внутренний адрес | Сохранить такой URL и вызвать доставку или загрузку | Отказ через `P/egress`; старый подписчик не затёрт | API, логи | unit + integration + security | да | CI | да | ◐ `P/egress/egress_test.go`, `HS/outbound_policy_integration_test.go`, `HS/himports/poster_egress_test.go`; не проверено, что generic subscribers, `hwordpress` и импорт клиентов идут через egress |
| SEC-06 | P0 | Любой | Ответы API, логи, audit, ошибки | Платёжные конфиги с `api_key`, `webhook_secret`, токены каналов | Прочитать конфиги, каналы, audit; вызвать ошибки | Секреты не возвращаются и не логируются (только маска или признак наличия) | Ответы, лог-файлы | integration + security | да | Claude Code | да | ✗ сквозного теста «нет секретов в ответах и логах» в обзоре не найдено (есть `B/internal/adapters/http/sensitive_headers_test.go` для заголовков) |
| SEC-07 | P1 | Браузер | `corsMiddleware` | — | Preflight; запрос без Origin; кешируемый публичный маршрут | `Vary: Origin` всегда; Allow-Origin только из списка | Заголовки | unit | да | CI | нет | ✅ `B/internal/adapters/http/router_test.go` |
| SEC-08 | P1 | Покупатель | Три лимита публичного API (токен фида, токен checkout, IP) | — | Всплеск запросов | Каждое ведро считает своё; IPv6 /64 общий бюджет | API | unit | да | CI | нет | ✅ `HS/public_feed_ratelimit_test.go`; ◐ при нескольких репликах не проверяется (лимит в памяти процесса) |
| SEC-09 | P0 | Бот-процесс | `hbot.RequireServiceToken` | `BOT_SERVICE_TOKEN` пустой, неверный, верный | Вызов `/v1/bot/invitations/accept` | 503, 401, 200 | API | unit + integration | да | CI | да | ✅ `HS/hbot/service_auth_test.go` |
| SEC-10 | P1 | Prometheus | `GET /metrics` | Токен задан | Без токена и с неверным | 401 | API | unit | да | CI | нет | ✅ `HS/metrics_bearer_auth_test.go` |
| SEC-11 | P0 | Организатор, покупатель | Rich text: `P/richtext`, `TP/src/lib/richtext.ts` | Общий корпус XSS (script, on*, style, javascript:) | Сохранить описание, отрисовать на hosted-странице | Опасное вырезано и на сервере, и в браузере | Тест-отчёт | unit | да | CI | да | ✅ `P/richtext/*_test.go`, `TP/src/__tests__/richtext_html.test.ts` |
| SEC-12 | P0 | Организатор | SVG-схема: `B/internal/domain/seating/svg_import.go`, `W/src/lib/svg-sanitize.ts` | SVG с `<script>`, `onload`, внешним `href`, `foreignObject` | Загрузить как версию плана, отрисовать в виджете | Импорт отвергает или чистит; в виджете исполнимого нет | Ответ 422, снимок DOM | unit + security | да | CI | да | ◐ виджет `W/src/lib/svg-sanitize.test.ts`; для импорта нужен явный тест на вредоносный SVG (проверить `svg_import_test.go`) |
| SEC-13 | P1 | Браузер | CSP и заголовки контейнеров EC, TP, SP | Собранные образы | `check-headers.sh`, Playwright CSP-спеки | Заголовки на месте; `/tg` без X-Frame-Options | Отчёт | E2E | да | CI | нет | ◐ заголовки EC в CI; `EC/tests/smoke/container/{poster,rich-text}-csp.spec.ts` в CI не гоняются; у TP и SP проверки заголовков нет |
| SEC-14 | P0 | — | `B/tests/staticanalysis/*` (24 стража: денежные единицы, nopanic, context.Background, session_change_guard, billing append-only и др.) | — | `go test ./apps/backend/tests/staticanalysis/...` | Зелёный | Отчёт | regression | да | CI | да | ✅ |

### 3.4 Backend: каталог, события, сеансы, категории (CAT)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| CAT-01 | P0 | Менеджер | `POST .../events/{id}/status`, публикация, `eventpublication` | Черновик с черновыми сеансами | draft → published → archived; publish без обязательных полей | Публикация переводит сеансы в scheduled; гейт отказывает неполному; продажи не задеты; audit | API, БД, audit | integration | да | CI | да | ✅ `HS/event_status_http_integration_test.go`, `HS/event_publish_sessions_integration_test.go`, `HS/event_publish_binding_integration_test.go` |
| CAT-02 | P1 | Покупатель, сайт | Матрица статусов сеанса (draft, scheduled, cancelled, completed) × фид, checkout, шлюз, hosted-страница | По сеансу каждого статуса | Запросы с каждой поверхности | Продаётся только scheduled; cancelled виден, но не продаётся | API | integration | да | Codex | нет | ◐ только cancelled: `HS/cancelled_session_uxh23_integration_test.go`, `CB/cancelled_session_uxh23_integration_test.go` |
| CAT-03 | P0 | Менеджер, покупатель | `sessions.sales_end_at`, `doors_open_at`; `CheckSessionSalesOpen` | Сеанс с окончанием продаж в прошлом | Hold через виджет и шлюз; PDF и письмо | Новый hold отклонён (409 `tier.not_on_sale` / 101); существующий заказ оплачивается; «вход с» в PDF и письме | API, PDF | integration + unit | да | CI | да | ✅ `B/internal/adapters/postgres/gen/sessions_sale_times_integration_test.go`, `P/delivery/presentation_doors_test.go`, `P/delivery/pdf/doors_test.go` |
| CAT-04 | P0 | Менеджер, покупатель | `PUT .../sessions/{id}/sales-state`, `POST /v1/public/feeds/{t}/sessions/{id}/sale-subscriptions`, `GET /v1/public/sale-subscriptions/confirm`, jobs `sales_state.*` | Сеанс «soon» | Подписаться → подтвердить → открыть продажи | Одно письмо-подтверждение; одно письмо «продажи открыты»; soldout блокирует checkout | API, `worker_jobs`, письма | integration | да | CI | да | ✅ `HS/session_sales_state_integration_test.go`, `HS/sale_subscription_public_integration_test.go`, `P/salesubscribe/subscribe_integration_test.go`, `CB/session_sales_state_integration_test.go` |
| CAT-05 | P0 | Менеджер, покупатель | Категории, `price-schedule`, `checkout/quote` | Категория с запланированной сменой цены | Котировка до и после смены; checkout | Котировка = списанная сумма; цена заблокирована на время hold | API, `orders.total` | integration | да | CI | да | ✅ `HS/price_lock_ab48_integration_test.go`, `HS/public_feed_quote_matrix_integration_test.go` |
| CAT-06 | P1 | Покупатель | Цепочка категорий и `sell_limit`, job `tier.chain_sweep` | Цепочка из двух категорий, у первой закончилось окно | Запуск задачи `tier.chain_sweep` | Свободные места переданы, источник закрыт, общее число мест не изменилось | БД | integration | да | Codex | нет | ◐ `HS/gaquota/handover_integration_test.go`, `HS/himports/event_bundle_chain_integration_test.go`; ✗ у самого `P/tierchain` нет тестов |
| CAT-07 | P0 | Менеджер, покупатели | GA-квоты под конкуренцией | Категория на N мест | Параллельно правка количества и hold | Нет 40P01; нельзя опустить квоту ниже held+sold | БД | integration | да | CI | да | ✅ `HS/ga_quota_concurrency_integration_test.go`, `HS/gaquota/quota_integration_test.go` |
| CAT-08 | P0 | Суперадмин, организатор | Схемы: `/v1/venues/{id}/seating-plans`, `/v1/seating-plans/{id}/versions`, `.../sessions/{s}/seating`, `/v1/event-sessions/{id}/schema`, `seat-status` | SVG-план Palác Akropolis (`CB/testdata/wp/svg/palac_akropolis.sbt.svg`) и синтетический | Загрузить версию → привязать к сеансу → назначить категории → seat-status | Места с координатами в корневой системе (с учётом `transform`); категории; seat-status по версиям | API, БД | integration | да | CI | да | ✅ `B/internal/domain/seating/*_test.go`, `HS/hseating/*_test.go`, `HS/seat_batches_integration_test.go`, `HS/seat_layouts_integration_test.go` |
| CAT-09 | P1 | Бот, event center | `POST .../imports/event-bundle` (`dry_run`, `arenaEventId`, `externalRef`) | Пакет события | Импорт, повтор, dry-run, правка через arena-id | Идемпотентно; dry-run ничего не пишет; конфликт ключа даёт 409 | API, БД | integration | да | CI | нет | ✅ `HS/himports/event_bundle_*_integration_test.go` (~17 файлов) |
| CAT-10 | P1 | Менеджер | Площадки и города: `PATCH .../venues/{id}`, `POST .../cities`, `venues.timezone NOT NULL` | — | Создать город дважды с разным регистром; площадку без пояса | Город идемпотентен по имени; без пояса отказ | API | integration | да | Codex | нет | ◐ `HS/venue_sharing_integration_test.go`, `HS/venues_v1_fields_integration_test.go`; `HS/venues_test.go` проверяет только файлы миграций; ✗ создание городов и `P/geoslug` |
| CAT-11 | P1 | Менеджер, покупатель | Материалы: `media-items`, `videos`, `partner-links`, `map-point`, `photo-report` | Событие | CRUD, лимиты, порядок, публичная отрисовка | Лимиты соблюдены; фотоотчёт скрыт до конца события | API, публичный JSON | integration | да | CI | нет | ✅ `HS/event_materials_integration_test.go`, `HS/hfeed/public_materials_integration_test.go` |
| CAT-12 | P0 | Покупатель | `GET /v1/public/pages/{slug}[/{event_slug}]` | Промоутер со slug, канал с `hosted_page.enabled` | Страница организации, промоутера, события; выключенный флаг | 200 только у опубликованных; 404 при выключенном флаге; черновиков нет | JSON | integration | да | CI | да | ✅ `HS/hfeed/public_page_integration_test.go`, `HS/hfeed/public_promoter_page_integration_test.go` |
| CAT-13 | P0 | Менеджер, покупатели | `GET .../change-impact`, PATCH и DELETE сеанса, `P/sessionchange`, job `session.change_email` | Сеанс с оплаченными заказами | Dry-run → перенос → отмена; без контакта; заказ сайта | Письмо каждому оплатившему; 422 `organization.contact_missing`; 422 `session.change_site_unsupported`; заказы не возвращаются автоматически | API, `session_changes`, письма | integration | да | CI | да | ✅ `HS/session_change_http_integration_test.go`, `P/delivery/change_email_integration_test.go` |
| CAT-14 | P1 | Менеджер | `GET .../events/{id}/delete-impact`, `DELETE` | Событие с продажами и без | Удалить оба | С продажами отказ или предупреждение, без продаж мягкое удаление | API | integration | да | Codex | нет | ◐ EC unit; backend-тест удаления в обзоре не найден |

### 3.5 Backend: корзина, заказы, конкурентность (ORD)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ORD-01 | P0 | Покупатель | Widget → `POST /v1/public/feeds/{t}/checkout/start` → hold-транзакция → preflight провайдера → Stripe Checkout Session | Канал `stripe`, тестовый конфиг, Stripe-заглушка | checkout/start с корзиной; затем с ненастроенной оплатой | 201, `redirect_url`, `expires_at`; без конфига 422/503 **до** захвата мест (остаток не меняется) | API, `reservations`, `inventory_ledger` | integration | да | CI | да | ✅ `HS/hosted_checkout_integration_test.go` |
| ORD-02 | P0 | Два покупателя | Один и тот же seat_key | Рассадочный сеанс | Два параллельных hold на одно место | Один 201, второй 409 с `conflictKeys`; виджет подсвечивает конфликт | API, БД | integration + E2E | да | CI | да | ✅ `HS/hcheckout/hold_mutation_concurrency_integration_test.go`, `W/tests/palac-akropolis-real.e2e.ts` (группа 3) |
| ORD-03 | P0 | N покупателей | GA-категория на K мест, N > K | `ops/loadtest/native.js` или Go-тест | N параллельных checkout | Продано ровно K; `ops/loadtest/sql/audit.sql` показывает 0 нарушений | БД, отчёт k6 | integration + load | частично | CI + Человек | да | ◐ `HS/hcheckout/ga_hold_deadlock_race_integration_test.go`, `HS/public_feed_checkout_race_integration_test.go`; нагрузка только вручную (`ops/loadtest`) |
| ORD-04 | P0 | Покупатель | Двойной клик по «Оплатить»: повторный `checkout/start` | Одна корзина | Два одинаковых запроса подряд и параллельно | Один hold, один заказ, одна сессия Stripe (Idempotency-Key у провайдера); второй ответ ведёт на ту же оплату или даёт понятную ошибку | API, `orders`, лог заглушки | integration | да | Codex | да | ◐ общая идемпотентность `P/idempotency/*_test.go`, `HS/idem_race_one_db_write_test.go`; заглушка Stripe учитывает idempotency-key; ✗ тест двойного `checkout/start` у публичного фида |
| ORD-05 | P0 | Система | `reservation.expire_sweep` | Просроченный hold (GA и место) | Запуск задачи | Места, GA и `capacity_held` освобождены | БД | integration | да | CI | да | ✅ `HS/hcheckout/reservation_expire_sweep_integration_test.go` |
| ORD-06 | P0 | Покупатель | Окно оплаты виджета (сессия Stripe 1860 с, hold на 120 с дольше) | — | Оплата после истечения hold | Заказ уходит в `manual_review`, билеты не выданы | БД | integration | да | CI | да | ✅ `HS/webhook_widget_completion_integration_test.go` (PaymentAfterExpiry_ParksForManualReview) |
| ORD-07 | P0 | Покупатель | Бесплатный заказ: `CompleteFreeCheckoutTx` | Категория 0 | checkout/start | Заказ оплачен сразу, без провайдера; билеты и письмо | БД, письмо | integration | да | CI | да | ✅ `HS/hosted_checkout_integration_test.go` (FreeCartNeedsNoProvider) |
| ORD-08 | P0 | Покупатель | Промокоды: `ValidatePromoForLines` + `CheckPromoLimits`, погашение по заказу | Код с `max_uses=1` и ограничением по сеансу | Два параллельных заказа с одним кодом | Скидку получает один; погашение записано один раз | БД | integration | да | CI | да | ✅ `HS/public_feed_promo_concurrent_integration_test.go`, `HS/promo_attempts_integration_test.go`, `CB/promo_*` |
| ORD-09 | P1 | Покупатель | `POST /v1/public/checkout/{token}/recover` | Просроченный hold | Recover на живой БД | Hold восстановлен, если места свободны; иначе понятный код | API, БД | integration | да | Codex | нет | ✗ live-теста нет (только форма в `HS/wid_0c_320_test.go`; в виджете e2e допускает 200/400/404) |
| ORD-10 | P0 | Покупатель | `GET /v1/public/checkout/{token}/tickets/{ticket_id}/pdf` | Два заказа | Свой токен со своим билетом; свой токен с чужим ticket_id; неоплаченный заказ | 200 PDF; 404; отказ | API, PDF | integration + security | да | Codex | да | ✗ отдельного теста нет |
| ORD-11 | P1 | Покупатель | Сервисный сбор канала (`fee_percent`, округление вниз), строка в CartSheet | Канал 1,25 % | Котировка, страница оплаты, заказ | Сумма в корзине = сумма у провайдера = `orders.total` | API, заглушка | integration + unit | да | CI | да | ✅ `HS/hosted_checkout_integration_test.go`, `W/src/lib/cart.test.ts` |
| ORD-12 | P1 | Покупатель | Согласие, уведомление об отказе от покупки | Сеанс с `consent` | checkout без галочки, затем с ней | Без согласия отказ; версия согласия сохранена | БД | integration + E2E | да | CI | да | ✅ `HS/checkout_consent_integration_test.go`, `W/tests/consent.e2e.ts` (mock) |
| ORD-13 | P1 | Покупатель | Атрибуция и `/go/{code}` | Ссылка продаж | Переход → покупка в течение 7 дней | Заказ атрибутирован; клик засчитан один раз | БД | integration | да | CI | нет | ✅ `HS/checkout_attribution_integration_test.go`, `HS/go_redirect_integration_test.go` |
| ORD-14 | P1 | Менеджер | Пригласительные (с местами), пакеты, PDF | Рассадочный сеанс | Выдать пакет → отменить → место вернулось | Один заказ на гостя; повтор не дублирует | БД, CSV | integration | да | CI | нет | ✅ `HS/staff_invitations_inv*_integration_test.go` (8 файлов) |
| ORD-15 | P1 | Менеджер | `POST .../orders/{id}/cancel`, `/resend-tickets` | Неоплаченный и оплаченный заказ | Отмена; повторная отправка | Отменяется только pending; повторная отправка ставит задачу заново (`RequeueDeliveryJob`) | API, `delivery_jobs` | integration + E2E | да | CI | да | ✅ `HS/orders_resend_integration_test.go`, `EC/tests/smoke/live/{cancel-order,resend}.spec.ts` |
| ORD-16 | P1 | Менеджер, агент | CSV: `sales.csv`, `summary.csv`, `redemptions.csv`, `page-stats.csv` | Сеанс с продажами | Выгрузка каждой ролью | Состав колонок; агент не видит денег; только своя организация | CSV | integration | да | CI | нет | ✅ `HS/export_csv_integration_test.go`; в EC только mock `r1.spec.ts` |
| ORD-17 | P2 | Покупатель | Налоги и НДС | — | Проверить PDF и письмо | Расчёта НДС в системе нет (⊘). На билете реквизиты продавца и пометка, что это не налоговый документ | PDF | unit | да | CI | нет | ✅ реквизиты: `P/delivery/seller_ids_integration_test.go`; ⊘ расчёт налогов |

### 3.6 Платежи и возвраты (PAY)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| PAY-01 | P0 | Покупатель | Widget → checkout/start → Stripe (заглушка) → подписанный `checkout.session.completed` (`payment_status: paid`) → заказ paid → `checkout.issue_tickets` → `ticket.deliver` → письмо; место sold; отчёт | Stripe-заглушка, тестовый конфиг | Полный цикл | Заказ `paid`, билеты с EAN-13, одно письмо с PDF всех билетов, место `sold`, итоги сеанса учитывают деньги | API, БД, письмо (лог или SMTP-заглушка), отчёт сеанса | integration + E2E | да | CI | да | ◐ Go: `HS/hosted_checkout_integration_test.go`, `P/delivery/order_letter_integration_test.go`; ✗ браузерный цикл (`W/tests/palac-akropolis-real.e2e.ts:616` пропускается без `STRIPE_TEST_KEY`) |
| PAY-02 | P0 | Провайдер | `checkout.session.completed` с `payment_status != paid`, затем `async_payment_succeeded` | Асинхронный способ оплаты | Два события подряд | Первое не завершает заказ и не съедается дедупликацией; второе завершает | БД | integration | да | Codex | да | ◐ правило описано в коде (`HS/hcheckout`); отдельный тест в обзоре не подтверждён |
| PAY-03 | P0 | Покупатель | Отказ карты, отмена на странице Stripe, `checkout.session.expired` | Заглушка | Каждое событие | Hold освобождён по истечении; заказ `expired`/`cancelled`; билетов и писем нет; повторная покупка возможна | БД | integration | да | Codex | да | ◐ Flitt: `HS/flitt_checkout_integration_test.go` (ExpiredOrderFailsTheIntent); ✗ Stripe expired и async_failed |
| PAY-04 | P0 | Провайдер | Повторная доставка вебхука | Оплаченный заказ | Отправить то же событие ещё 2 раза | 200 `processed:false`; билеты и письма не дублируются | БД (число билетов и писем) | integration | да | CI | да | ✅ AllPay (DuplicateDeliveryAnswers200); ◐ Stripe и Flitt |
| PAY-05 | P0 | Провайдер | Сумма платежа ≠ `orders.total` | Подделанная сумма | Колбэк | Платёж в `manual_review`, билетов нет | БД | integration | да | Codex | да | ✅ AllPay (PaidAmountMismatchParksThePayment), `HS/hcheckout/payment_amount_guard_test.go`; ◐ Stripe и Flitt |
| PAY-06 | P1 | Покупатель | Flitt: все валюты; `declined` не терминален; `expired` | Песочница Flitt (merchant публичный из их документации) или заглушка | Старт, колбэк, отказ, затем approved | Отказ оставляет заказ открытым; approved оплачивает | БД | integration | да | CI | нет | ✅ `HS/flitt_checkout_integration_test.go` |
| PAY-07 | P1 | Покупатель (Израиль) | AllPay, PayPlus, включая частичный возврат | Заглушки `W/scripts/payplus-stub.cjs` (:12114) и AllPay | Покупка и возврат | Статусы и суммы сходятся | БД | integration | да | CI | нет | ✅ `HS/allpay_checkout_integration_test.go`, `HS/allpay_refund_integration_test.go`, `HS/payplus_checkout_integration_test.go` |
| PAY-08 | P0 | Два организатора | Свои Stripe-аккаунты и секреты | Две организации | Вебхук каждой | Каждая проверяется своим секретом | БД | integration | да | CI | да | ✅ TwoOrgsUseTheirOwnWebhookSecrets |
| PAY-09 | P0 | DevOps | Тестовая среда не может провести реальный платёж или письмо | Env стенда | Проверить: ключи только `sk_test_`, `STRIPE_API_BASE_URL` в проде пуст, `EMAIL_MODE` стенда | Нет live-ключей вне прода; заглушки запрещены в проде | Список ключей env (только имена и префиксы) | manual + unit | частично | Человек | да | ◐ `P/config/config_test.go`; инвентаризация env стенда вручную |
| PAY-10 | P1 | Покупатель | `GET\|POST /v1/public/payment-return?r=&checkout_token=` | Чужой origin в `r` | Возврат с провайдера | 303 на страницу покупателя; чужой origin заменяется запасным | API | unit | да | CI | нет | ✅ `HS/hfeed/payment_return_test.go` |
| PAY-11 | P0 | Владелец | Возврат через провайдера: approve → API провайдера → вебхук → гашение билета после принятия → место свободно → `refund.buyer_email` → outbox `ticket.refunded` → сайт, MACS, бот продаж | Оплаченный заказ, заглушка Stripe с возвратами (сумма 777 отклоняется) | Полный возврат; возврат с неизвестным исходом; повтор с тем же Idempotency-Key | Деньги до гашения; одно обращение к провайдеру; одно письмо; место снова в продаже; вебхук сайту с суммой | API, БД, лог заглушки, outbox | integration + E2E | да | CI | да | ✅ `HS/refund_engine_integration_test.go`, `EC/tests/smoke/live/{refund,refund-resilience}.spec.ts`, `HS/bot_e2e_refund_integration_test.go` |
| PAY-12 | P0 | Владелец | Частичный возврат (1 из 3 билетов) | Заказ на 3 билета | Вернуть 1 | Заказ `partially_refunded`; 2 билета живы; отчёт: выручка минус возврат | БД, итоги сеанса | integration | да | CI | да | ✅ PayPlus (PartialRefundThroughRefundByTransactionUID), бот (OwnerReturnsOneTicket); ◐ Stripe через EC |
| PAY-13 | P1 | Владелец | Возврат, сделанный в кабинете провайдера | Событие `refund.created` извне | Вебхук | Возврат учтён, билет погашен, не дублируется | БД | integration | да | CI | нет | ✅ `HS/refund_dashboard_integration_test.go` |
| PAY-14 | P1 | Владелец | Возврат отсканированного билета (политика исключений) | Отсканированный билет | Возврат | Блок или исключение по политике | БД | integration | да | CI | нет | ✅ `HS/refund_scanned_*_integration_test.go` |
| PAY-15 | P2 | Владелец | Чарджбэк и спор | — | — | ⊘ функции нет. Кейс фиксирует ручную процедуру и запрет автодействий | Документ процедуры | manual | нет | Человек | нет | ⊘ |
| PAY-16 | P2 | Покупатель | Перенос билета на другого покупателя или сеанс | — | — | ⊘ функции нет (есть только перенос сеанса целиком, CAT-13) | — | — | — | — | нет | ⊘ |
| PAY-17 | P1 | Владелец | Stripe Connect `/v1/stripe/connect/authorize\|callback` (`HS/hbilling/stripe_connect.go`) | — | Запросить маршруты в проде; если подключат — авторизация и колбэк | Сейчас маршрутов в проде нет (`Options.StripeConnect` нигде не задаётся), это зафиксировать тестом. Если включат: state проверяется, `acct_` сохраняется в организацию | API | integration | да | Codex | нет | ✗; код колбэка возвращает `account_id` в JSON, но не сохраняет его и не сверяет `state` (подробности в NEW-08) |
| PAY-19 | P0 | DevOps | Ни один тестовый контур не пишет посторонним людям: письма (`EMAIL_MODE`), бот продаж, вебхуки сайтам, MACS | Env CI, стенда, стейджингов | Проверить режим почты и адреса получателей в тестовых данных; подписчиков вебхуков и MACS у тестовых организаций | В CI `EMAIL_MODE=log`; на стенде письма только на ящики команды; внешние подписчики — заглушки или тестовые | Список настроек (без значений секретов), выборка `worker_jobs` и `outbox_events` | manual + integration | частично | Человек + Claude Code | да | ✗ **белого списка получателей в коде нет** (`P/config/config.go` знает только `log` и `smtp`): стенд с `EMAIL_MODE=smtp` отправит письмо на любой адрес из данных. Защиту сейчас держит только дисциплина тестовых данных |
| PAY-18 | P1 | Владелец | Биллинг организатора: флаг `BILLING_ORGANIZER_ENABLED`, пакеты, `/v1/billing/stripe/webhook` | Флаг включён и выключен | Маршруты; покупка пакета; списание | При выключенном флаге маршрутов нет; кредиты платят только за продажи после покупки | API, леджер | integration + E2E | да | CI | нет | ✅ `HS/billing_organizer_flag_integration_test.go`, `P/billing/credits/credits_integration_test.go`, `EC/tests/smoke/live/billing*.spec.ts`; ✗ оплата пакета картой через заглушку |

### 3.7 Фоновые задачи, outbox, интеграции, эксплуатация (ASY)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ASY-01 | P0 | Система | `outbox_events` (через `PGEventsWriter`), `multiDispatcher`, ветки salesnotify, generic webhook, MACS, bil24WP, billing | Событие `v1.order.paid` | Доставка при падении одной ветки | Остальные ветки доставили; упавшая повторяется с backoff; после 30 попыток dead-letter; повторной доставки нет | БД | unit + integration | да | CI | да | ✅ `B/cmd/arena-worker/outbox_legs_test.go`, `P/outbox/*_test.go` (no_double_delivery) |
| ASY-02 | P0 | Сайт WP | Вебхуки сайта `order.paid` и `ticket.refunded` | Stub-приёмник `CB/wpstub` | Покупка через шлюз → вебхук | `data.id` = `orders.system_id` = `orderId` из CREATE_ORDER_EXT | Тело вебхука | integration | да | CI | да | ✅ `CB/wp_roundtrip_508_integration_test.go` |
| ASY-03 | P1 | MACS | `P/macs` диспетчер | Stub-приёмник | Покупка | Билеты доставлены, формат конверта, суммы в нужных единицах | Тело | integration | да | CI | нет | ✅ `P/macs/macs_*_integration_test.go` |
| ASY-04 | P1 | Организатор | `P/salesnotify` → Telegram бота продаж | Подписка чата | Продажа, возврат | Одно сообщение на продажу; возврат и отмена схлопнуты; ПДн покупателя только в чат организации | Записи stub | integration | да | CI | нет | ✅ `P/salesnotify/*_test.go` |
| ASY-05 | P0 | Система | `checkout.issue_tickets` (`P/issuejob`), `checkout.convert_reservation` (`P/convertjob`) | Оплаченный checkout | Запустить задачу дважды; упасть посередине | Билеты не дублируются (`tickets_checkout_ordinal_uq`); повтор доводит до конца | БД | integration | да | Codex | да | ✗ своих тестов у пакетов нет (покрыто косвенно вебхук-тестами) |
| ASY-06 | P0 | Покупатель | `ticket.deliver`: одно письмо на заказ, ошибка отправки возвращает в `pending` все билеты заказа | 3 билета, SMTP отказывает, потом принимает | Отправка | Одно письмо со всеми PDF после восстановления; нет `processing` навсегда | БД, SMTP-заглушка | integration | да | CI | да | ✅ `P/delivery/order_letter_integration_test.go`, `P/delivery/delivery_integration_test.go` |
| ASY-07 | P1 | Система | `tier.chain_sweep` | См. CAT-06 | Запуск обработчика задачи | См. CAT-06 | БД | integration | да | Codex | нет | ✗ |
| ASY-08 | P1 | Менеджер | `POST /v1/events/{event_id}/report` → `report.deliver`, `event.generate_report` | — | Запросить отчёт | Отчёт сгенерирован и доставлен | БД, письмо | integration | да | Codex | нет | ✗ **Обработчики `P/reportdelivery` и `P/reporting` нигде не регистрируются в `B/cmd/arena-worker`** (проверено grep), задачи копятся без исполнителя. Сначала решить: удалить функцию или подключить |
| ASY-09 | P1 | Оператор | `ops.watchdog` | Застрявшие оплаты, dead letters | Прогон | Одно оповещение, повтор через 30 мин, «resolved»; без e-mail и телефонов | Тело сообщения | integration | да | CI | нет | ✅ `P/opswatchdog/*_integration_test.go` |
| ASY-10 | P1 | Оператор | `events.change_watch` | Опубликованное событие | Переименовать, сдвинуть дату | Одно сообщение через 60 с стабильности; первый проход молчит | БД | integration | да | CI | нет | ✅ `P/eventwatch` |
| ASY-11 | P1 | Система | `onboarding.sweep`, `worker_jobs.cleanup`, `idempotency.cleanup` | — | Прогон | Просроченное удалено по срокам | БД | integration | да | CI | нет | ✅ `P/onboarding/service_integration_test.go`, `P/jobretention` |
| ASY-12 | P0 | Сайт WP | Bil24-шлюз: GET_ALL_ACTIONS → RESERVATION → CREATE_ORDER_EXT → PAY_ORDER → GET_TICKETS_BY_ORDER → REFUND_TICKET | Харнесс `CB/` | Сценарии харнесса | Golden-ответы совпадают; деньги в major-единицах; окно оплаты | Golden diff | integration + contract | да | CI | да | ✅ `CB/harness_test.go` (TestCompatBil24_450_Harness_Scenarios), `CB/scenario0*_test.go`; ✗ живые сценарии SEND_TICKETS_TO_EMAIL, CHECK_KDP, SCAN_TICKET |
| ASY-13 | P0 | DevOps | Миграции на чистой БД (`migrate up` + `arena-seed`), пин головы, паритет прав суперадмина | Новая миграция | CI-джоб integration | Зелёный; пин `0183` обновлён | Лог CI | integration | да | CI | да | ✅ `B/internal/migrations/migrations_head_test.go`, `superadmin_permission_parity_532_test.go` |
| ASY-14 | P0 | DevOps | Миграции на копии БД со стенда | Дамп стенда в отдельную БД | `arena-migrate up` | Применяется без «missing migrations»; объёмы до и после совпадают | Лог, `count(*)` | manual | частично | Человек + Claude Code | да | ◐ рецепт в AGENTS.md, не автоматизирован |
| ASY-15 | P1 | DevOps | `/healthz` (без БД), `/readyz` (пробы), `arena-healthcheck`, `/metrics` | — | Остановить БД | healthz 200, readyz 503 | Ответы | unit | да | CI | да | ✅ `B/tests/integration/health_endpoints_test.go`, `HS/readiness_probe_registry_112_test.go` |
| ASY-16 | P1 | Аудитор | `audit_events`: `actor_id` uuid, `metadata.via` из `X-Client-Channel` | Запросы из бота и EC | Мутация | Строка audit с via; ключ API даёт `actor_type=api_key` | БД | unit + integration | да | CI | нет | ✅ `P/audit/client_channel_test.go`, `HS/client_channel_middleware_test.go`; ◐ admin-web и плагины заголовок не шлют |
| ASY-18 | P1 | DevOps | Бэкапы: `ops/backup/*.sh` (Postgres каждые 3 ч и ночью, MongoDB MACS, WordPress-сайты) → R2; `backup-check.sh` → Telegram оператора | Одноразовая БД | Восстановить последний дамп в пустую БД; сдвинуть последовательности `compatibility_system_id_seq`, `tickets_system_id_seq`, `session_seats_system_id_seq`; `arena-migrate up` | Дамп свежее 3 ч; восстановление проходит; новые номера заказов не пересекаются со старыми | Лог восстановления, `count(*)` | manual | частично | Человек + Claude Code | нет (да перед миграцией сервера) | ◐ наличие скриптов: `B/tests/staticanalysis/backup_runbook_169_test.go`; само восстановление не автоматизировано |
| ASY-17 | P2 | DevOps | Нагрузка `ops/loadtest` (gateway.js, native.js, capacity.mjs) | Одноразовая копия стенда | Ступени 25→400 заказов в минуту | Ошибки ниже порога, `audit.sql` = 0 | Отчёт k6 в `docs/loadtest` | load | частично | Человек | нет | ◐ ручные прогоны 2026-09-13, 2026-09-22, 2026-09-23 |

### 3.8 Event Center (EC)

Живые тесты запускаются джобом `event-center-live` против реального arena-api и `arena-e2e-seed` (`EC/scripts/live-harness.mjs` отказывается работать с нелокальной БД или БД с именем `arena`).

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| EC-01 | P0 | Владелец | `/login`, `/forgot-password`, `/reset-password`, `/accept-invite` → `/v1/auth/*` | Аккаунты e2e | Вход, неверный пароль, несколько вкладок с refresh, выход | Работает с реальным API; refresh в двух вкладках не выкидывает | Скриншоты Playwright, ответы | E2E | да | CI | да | ◐ mock `EC/tests/smoke/r1.spec.ts`; ✗ live |
| EC-02 | P1 | Новичок | Регистрации в EC нет (⊘), путь идёт через анкету (сайт или бот) | — | Открыть `/login` | Нет формы регистрации; понятная ссылка на заявку | Скриншот | manual | да | Hermes | нет | ⊘ кейс проверяет текст и ссылку |
| EC-03 | P1 | Владелец | `/o/$orgId/org-data` → `PATCH /v1/organizations/{id}`, `seller-profile` | — | Правка реквизитов, неверный формат налогового номера | Сохранение; 400 с понятной ошибкой | Live-отчёт | E2E | да | CI | нет | ✅ `EC/tests/smoke/live/org-data.spec.ts` |
| EC-04 | P0 | Менеджер | Мастер `/events/new` → `POST .../imports/event-bundle` (dry_run, затем запись) | `VITE_FEATURE_WIZARD=1` | Создать событие, двойной клик «Сохранить» | Один POST; черновик не продаётся; время в поясе площадки | БД, сеть Playwright | E2E | да | CI | да | ✅ `EC/tests/smoke/live/create-event.spec.ts`, `wizard-*.spec.ts` |
| EC-05 | P0 | Менеджер | `POST .../events/{id}/status`, публикация сеансов | Черновик | Опубликовать, снять, архив | Статусы и гейты как в CAT-01; продажи открываются только после публикации | Live-отчёт, публичный JSON | E2E | да | CI | да | ✅ `EC/tests/smoke/live/event-status.spec.ts` |
| EC-06 | P0 | Менеджер | Афиша: `POST /v1/media` (`event_poster`) → `poster-facts` | Сборка `-tags e2eposter` (без модели) | Загрузка 4:5; неверное соотношение; большой файл; чтение афиши | Сохранено; понятные отказы; подсказки только кнопками | Live-отчёт | E2E | да | CI | да | ✅ `EC/tests/smoke/live/poster.spec.ts`, `poster-first.spec.ts` |
| EC-07 | P1 | Менеджер | Материалы: галерея, видео, партнёрские ссылки, карта, фотоотчёт | — | CRUD и порядок | Отражается на hosted-странице | Live-отчёт | E2E | да | CI | нет | ✅ `EC/tests/smoke/live/materials.spec.ts`; ◐ `EC/tests/smoke/materials/public.spec.ts` не в CI |
| EC-08 | P1 | Менеджер | Площадка, адрес, пояс, даты: `PATCH .../venues/{id}`, `/v1/geo/*` | — | Новая площадка в мастере, правка адреса | Пояс обязателен; даты в поясе площадки | Live-отчёт | E2E | да | CI | нет | ✅ `wizard-event-venue.spec.ts`, `wizard-dates-tickets.spec.ts` |
| EC-09 | P0 | Менеджер | Категории, расписание цен, окончание продаж, статус продаж | — | Правки каждой сущности | Изменения видны в фиде и корзине | Live-отчёт | E2E | да | CI | да | ✅ `categories.spec.ts`, `sale-times.spec.ts`, `sales-state.spec.ts` |
| EC-10 | P1 | Менеджер | Промокоды, промоутеры, ссылки продаж и QR | — | CRUD; скачивание QR | Код работает в виджете; QR ведёт на `/go/` | Live-отчёт, PNG/SVG | E2E | да | CI | нет | ✅ `promo.spec.ts`, `promoters.spec.ts`, `sales-links.spec.ts`; ◐ скачивание QR |
| EC-11 | P0 | Владелец, менеджер, агент | `/o/$orgId/team` → `bot-team`, `bot-invitations`, `DELETE /members/{id}` | — | Пригласить, повторно отправить, отозвать; войти каждой ролью | Скрытые кнопки отсутствуют, запрос не уходит, API отказывает | Live-отчёт | E2E | да | CI | да | ✅ `team.spec.ts`, `events-three-roles.spec.ts` |
| EC-12 | P0 | Владелец | Заказы: поиск, карточка, отмена, возврат, повторная отправка | Заглушка Stripe для возвратов (:18088) | Каждое действие; неизвестный исход возврата | См. PAY-11 и ORD-15 | Live-отчёт | E2E | да | CI | да | ✅ `cancel-order.spec.ts`, `refund*.spec.ts`, `resend.spec.ts`; ◐ список заказов и карточка покрыты только mock-тестами |
| EC-13 | P1 | Менеджер, агент | CSV-выгрузки из UI | Продажи | Скачать файл каждой ролью | Файл открывается, колонки по роли | Файл | E2E | да | Codex | нет | ◐ только mock `r1.spec.ts` |
| EC-14 | P0 | Менеджер | Перенос и отмена сеанса с письмом покупателям | Оплаченные заказы | Dry-run → правка текста → подтверждение | См. CAT-13; неполное подтверждение ничего не пишет | Live-отчёт | E2E | да | CI | да | ✅ `session-change.spec.ts`, `session-change-resilience.spec.ts` |
| EC-15 | P1 | Менеджер | Менеджер мест: покраска, закрытие, пакеты, автораскладка | Рассадочный сеанс | Действия и откат пакета | Seat-status обновился, откат вернул | Live-отчёт | E2E | да | CI | нет | ◐ `seats.spec.ts` проверяет в основном права, не покраску |
| EC-16 | P1 | Менеджер | Пригласительные с местами, CSV результата, PDF | — | Пакет на 3 гостя | См. ORD-14 | Live-отчёт | E2E | да | CI | нет | ✅ `invitations*.spec.ts` |
| EC-17 | P1 | Владелец | Биллинг: пакеты, баланс, счета | Флаг включён | Покупка пакета (заглушка) | Баланс растёт; списание за продажи | Live-отчёт | E2E | да | CI | нет | ✅ `billing*.spec.ts`; ✗ оплата картой и счета |
| EC-18 | P1 | Менеджер | Админка магазина `/shop/*` | — | Товар, вариант, заказ, отгрузка, возврат | Как у бэкенда (SITE-19) | Отчёт | E2E | частично | Codex | нет | ◐ только vitest и `shop.mock.spec.ts`; нет live-фикстуры |
| EC-19 | P1 | Менеджер | Локализация en/ru/es, валюты | — | Переключить язык на каждом экране | Нет ключей вместо текста; валюта площадки | Скриншоты | unit + visual | частично | CI + Hermes | нет | ✅ ключи и плейсхолдеры `EC/src/lib/i18n` (catalog test); ◐ визуально только Hermes |
| EC-20 | P1 | Менеджер | Валидация форм и отображение ошибок API | — | Пустые поля, неверные даты, 409, 422 | Понятные сообщения, без технических кодов | Отчёт | unit | да | CI | нет | ✅ `EC/src/lib/wizard/{clientErrors,dateErrors,validation}.test.ts`, `lib/errors.test.ts` |
| EC-21 | P1 | Менеджер | Мобильный 375×812 и axe | — | Ключевые экраны | Нет нарушений WCAG A/AA; цели касания не меньше 44 px | Отчёт axe | E2E | да | CI | нет | ✅ в mock-спеках; ✗ axe на живом бэкенде |
| EC-22 | P0 | — | Паритет маршрутов EC с `openapi.yaml`; запрет `/v1/admin/*` | — | `EC/src/parity/parity.test.ts` | Зелёный | Отчёт | contract | да | CI | да | ✅ |
| EC-23 | P1 | Организатор в Telegram | `/tg/edit`, `/tg/invitations`, `/tg?next=` | Одноразовый код или initData | Открыть из бота, отредактировать, сохранить | Сохранено, сессия на 30 мин | Live-отчёт | E2E | да | CI | нет | ✅ `tg-edit.spec.ts`, `tg-enter.spec.ts` |
| EC-24 | P1 | Менеджер | Сообщения покупателю: текст в письме с билетом (`POST .../events/{e}/email-note/test`), письмо о переносе (EC-14), повторная отправка (EC-12) | Тестовый адрес | Задать текст, отправить себе тестовое письмо, купить билет | Текст в письме покупателя ровно один раз; тестовое письмо только на адрес отправителя | Письмо, live-отчёт | E2E | да | CI | нет | ✅ `EC/tests/smoke/live/email-note.spec.ts`, `P/delivery/event_note_letter_integration_test.go` |
| EC-25 | P1 | Менеджер, агент | Сканы, VIP-билеты, статистика страницы (`page-stats`, `.csv`) | Продажи и сканы из фикстур | Открыть экраны каждой ролью | Данные своей организации; агент без денег | Live-отчёт | E2E | да | CI | нет | ✅ `EC/tests/smoke/live/{scans,vip,page-stats}.spec.ts` |

### 3.9 Arena Admin (ADM)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| ADM-01 | P0 | Организатор, суперадмин | Вход в admin-web | Организатор без роли персонала | Войти организатором, затем суперадмином | Организатор не попадает в консоль (UXH-08); суперадмин попадает | Скриншот, API | unit + E2E | да | Codex | да | ◐ `AW/src/routes/loginStaffOnly.test.tsx` (unit); ✗ браузер с живым API |
| ADM-02 | P0 | Суперадмин | `/venues` → вкладка Seating plans: `POST /v1/media` (`seating_plan_svg`) → `POST /v1/seating-plans/{id}/versions`, `fork` | Корректный SVG, SVG с ошибками, генерированный `VG/arena_export/testdata/synthetic_authoring.svg` | Загрузка каждого; fork; новая версия | Корректный создаёт версию; ошибочный даёт 422 с ошибками по элементам; версии не затирают проданное | Ответы, скриншоты, хэш SVG | E2E | да | Codex | да | ◐ `AW/src/routes/venueSeatingPlans.test.ts` и `.render.test.tsx` (mock fetch); ✗ браузер и live |
| ADM-03 | P0 | Суперадмин | Привязка плана к сеансу, категории мест, доступность (`sessionSeatingBind.tsx`, `/v1/event-sessions/{id}/seats/category`) | Сеанс | Привязать, назначить, закрыть места | Виджет показывает ровно эти места и категории | Скриншот виджета, seat-status | E2E | да | Codex | да | ◐ unit `sessionSeatingBind.test.ts`, `sessionSeats.test.ts` |
| ADM-04 | P1 | Суперадмин | `/events`: события, сеансы, категории, pricing-bulk, публикации | — | Создать и опубликовать | Изменения видны в EC и фиде | Отчёт | E2E | частично | Codex | нет | ◐ unit `events.test.ts` |
| ADM-05 | P0 | Суперадмин | `POST /v1/admin/organizations` с реквизитами и участниками; запрос причины | — | Создать организацию | Окно причины обязательно; `X-Admin-Reason` уходит; audit | Сеть, audit | unit + integration | да | CI | да | ◐ unit `AW/src/lib/api/reason*.test.ts`; бэкенд ✅ IAM-04 |
| ADM-06 | P1 | Суперадмин | Заявки онбординга | Заявка | Одобрить, отклонить, запросить данные | См. IAM-11 | Отчёт | E2E | частично | Codex | нет | ◐ `onboarding.render.test.tsx` |
| ADM-07 | P1 | Суперадмин | Возвраты, билеты, повторная доставка, сканы | — | Действия | Аналогично EC | Отчёт | unit | частично | Codex | нет | ◐ `refunds.test.ts`, `tickets.test.ts` |
| ADM-08 | P1 | Суперадмин | `/channels`, `/payments` | — | Модалки каналов и платёжных конфигов | Секреты не показываются; MoR только суперадмину | Отчёт | E2E | да | CI | нет | ◐ `AW/tests/channels-payments/*.spec.ts` не в CI |
| ADM-09 | P1 | Суперадмин | `/webhooks`, `/audit`, `/observability` | — | Просмотр | Данные видны, фильтры работают | Отчёт | unit | частично | Codex | нет | ◐ unit |
| ADM-10 | P2 | Суперадмин | Мобильные 360 и 768 px, Lighthouse | — | 12 маршрутов | Нет горизонтальной прокрутки, a11y ≥ 90 | Отчёт | E2E | да | CI | нет | ◐ в CI только 3 страницы (`AW/tests/admin-bootstrap/mobile.spec.ts`); `AW/tests/mobile/smoke.spec.ts` и Lighthouse не в CI |
| ADM-11 | P1 | Суперадмин | Админ-биллинг: прайсы, счета, комиссии, банк | — | CRUD с причиной | Только суперадмин с причиной | Отчёт | integration | да | CI | нет | ✅ бэкенд `HS/billing_admin_integration_test.go`; ◐ UI render-тесты |

### 3.10 Сайты продаж, виджет, магазины (SITE)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| SITE-01 | P0 | Покупатель | `TP` → `GET /v1/public/pages/{org}/{event}` → виджет | Опубликованное событие с материалами | Открыть страницу | Афиша, заголовок, возраст, «когда» и «вход с» в поясе площадки, описание (очищенное), площадка и адрес, материалы, блок продавца, контакт, виджет | Скриншот, DOM | E2E + visual | да | Codex | да | ◐ unit `TP/src/__tests__/render.test.ts`; ✗ браузерного теста TP нет |
| SITE-02 | P0 | Покупатель | Страница промоутера | Промоутер с 2 событиями другой организации рядом | Открыть | Только связанные события; виджет в каждой карточке | DOM | E2E | да | Codex | да | ◐ unit |
| SITE-03 | P0 | Покупатель | Виджет: загрузка, фид, метки cancelled/soon/soldout | Реальный бэкенд (`widget-acceptance`) | Холодная загрузка на Fast 3G | Не дольше 5 с; без ошибок консоли; schema не кешируется | Отчёт Playwright | E2E | да | CI | да | ✅ `W/tests/palac-akropolis-real.e2e.ts` (группы 1–2) |
| SITE-04 | P0 | Покупатель | Схема мест: выбор, легенда, GA-полигон, клавиатура, 409 | 260 мест Palác (`W/scripts/seed-palac-e2e.sql`) | Выбор, конфликт со второго контекста | Корректно, доступно с клавиатуры | Отчёт | E2E | да | CI | да | ✅ real-группы 3 и 6, mock `W/tests/keyboard.e2e.ts`, `conflict-409.e2e.ts` |
| SITE-05 | P0 | Покупатель | Корзина: лимиты, промокод, строка сервисного сбора | — | Добавить больше лимита; ввести код | Лимит соблюдён; сумма = сумма у провайдера | Отчёт | E2E + unit | да | CI | да | ✅ `W/tests/promo-code.e2e.ts`, `W/tests/palac-promo-real.e2e.ts`, `cart.test.ts` |
| SITE-06 | P0 | Покупатель | Форма покупателя, согласие с T&C (ссылки terms и privacy, имя продавца), уведомление об отказе | Сеанс с согласием | Отправить без галочки и с ней | См. ORD-12 | Отчёт | E2E | да | CI | да | ✅ mock `consent.e2e.ts`, `widget_withdrawal_notice.test.ts` |
| SITE-07 | P0 | Покупатель | **Полный браузерный путь**: виджет → `redirect_url` → `/pay/:id` заглушки → подписанный вебхук → возврат `?checkout_token` → экран «оплачено» → PDF по ссылке → письмо | Заглушка Stripe с отправкой вебхука, SMTP-заглушка | Пройти путь | Экран paid с местами и human_code; PDF скачивается и валиден; одно письмо | Видео или трасса Playwright, PDF, `.eml` | E2E | да | Codex | да | ✗ |
| SITE-08 | P0 | Покупатель | Экраны failed, cancelled, expired с реальным бэкендом | Заглушка | Отказ, отмена, истечение | Понятный экран и возврат к выбору; места освобождены | Отчёт | E2E | да | Codex | да | ✗ (только mock `custom-events.e2e.ts`) |
| SITE-09 | P0 | Покупатель | Разделение токенов: `arena_checkout_token:<event>`, `arena_attr:<feedToken>` | Два события двух организаций на tickets.arenasoldout.com | Купить на первом, открыть второе в той же вкладке; чужой event_id в фиде своего токена | Второе событие показывает выбор, а не чужой заказ; чужое событие даёт 404 | Отчёт, sessionStorage | E2E + security | да | Codex | да | ◐ unit `W/src/lib/store.test.ts`, `TP render.test.ts`; ✗ браузер |
| SITE-10 | P1 | Покупатель | «Сообщить об открытии продаж» в виджете | Сеанс soon | Подписаться | Запрос уходит, письмо-подтверждение | Отчёт | E2E | да | Codex | нет | ◐ unit `widget_sale_state.test.ts`; бэкенд ✅ CAT-04 |
| SITE-11 | P1 | Покупатель | Локали виджета en/ru/cs/he (es, de, fr частично), страницы en/ru/cs/he/es; RTL | — | `?lang=` для каждой | Нет пустых строк; he с `dir=rtl` | Скриншоты | unit + visual | частично | CI + Hermes | нет | ◐ таблицы по модулям; ✗ полнота es/de/fr |
| SITE-12 | P1 | Покупатель | Время в поясе площадки при другом поясе браузера; переход на летнее время | Браузер в UTC+3, площадка в Мадриде | Открыть страницу и виджет | Время Мадрида | Скриншот | E2E | да | Codex | нет | ◐ unit `session-label.test.ts` |
| SITE-13 | P1 | Покупатель | Мобильная вёрстка 375 px виджета и страницы | — | Пройти покупку | Нет горизонтальной прокрутки; лист корзины удобен | Скриншоты | E2E + visual | да | Codex | нет | ✗ |
| SITE-14 | P1 | Покупатель | Доступность TP и SP | — | axe и клавиатура | Нет нарушений AA | Отчёт axe | E2E | да | Codex | нет | ✗ |
| SITE-15 | P1 | Покупатель | Галерея, видео по клику (youtube-nocookie, vimeo, rutube), «где ещё купить» (только https), карта | — | Открыть | Ничего стороннего не грузится до клика | Сеть Playwright | E2E | да | CI | нет | ✅ `W/tests/materials.e2e.ts` (mock) |
| SITE-16 | P0 | Покупатель, аноним | Публичные ответы фида и страниц | Две организации, черновики | Сравнить ответы | Нет e-mail и телефонов покупателей, нет черновиков, нет чужих событий | JSON | integration + security | да | CI | да | ◐ изоляция ✅ (IAM-01/02), `HS/orgread_cross_org_integration_test.go`; ✗ явная проверка отсутствия ПДн в публичном JSON |
| SITE-17 | P1 | Сайт клиента | WP-плагин `arena-events`: синк по cron, шорткод и блок, прокси checkout, вебхук HMAC | WordPress в Docker | Синк, покупка, вебхук с верной и неверной подписью | Записи CPT; покупка; неверная подпись отклонена | Отчёт | integration | да | Codex | нет | ✗ PHP-тестов нет; `accessibility.yml` проверяет статичные HTML-имитации |
| SITE-18 | P0 | Сайт (Lampyris, Vino&Co) | Плагин Bil24 на сайте ↔ `/compat/bil24/json` ↔ вебхуки сайту | Стейджинги сайтов на `staging-1`, тестовая организация | Каталог, резерв, заказ, оплата WooCommerce в тестовом режиме, билеты, возврат | Заказ WC завершён; билеты от сайта; возврат отражён | Скриншоты, логи сайта, `outbox_events` | E2E | частично | Человек + Hermes | да для релизов, трогающих шлюз | ◐ серверная часть ✅ ASY-12; плагин вне репозитория |
| SITE-19 | P1 | Покупатель | Магазин SP: каталог, корзина, checkout (AllPay/PayPlus), статус, загрузки, проверка возраста, Turnstile | Заглушки AllPay и PayPlus | Покупка | Заказ оплачен; списание склада; 409 out_of_stock урезает корзину | Отчёт | integration + E2E | частично | CI + Codex | нет | ✅ бэкенд `HS/shop_*_integration_test.go`; ◐ SP только vitest |
| SITE-20 | P2 | Аноним | Маркетинговый сайт `arena-site`: сборка, i18n, ссылки, JSON-LD, Lighthouse, `onboarding-lib` | — | CI сайта | Зелёный | Лог CI | regression | да | CI | нет | ✅ `C:\Projects\arena-site\.github\workflows\ci.yml` |
| SITE-21 | P1 | Покупатель | Форма связи с организатором (Turnstile, согласие) → `POST /v1/public/organizer-contact/{slug}` → письмо | — | Отправить | Письмо организатору; ПДн удалены по сроку | БД, письмо | integration + E2E | да | Codex | нет | ✅ `HS/organizer_contact_integration_test.go`; ✗ браузер |
| SITE-22 | P0 | Покупатель, контролёр | PDF-билет: QR = цифры EAN-13, штрихкод чёрными прямоугольниками с тихими зонами, номер вместо UUID, шрифт с кириллицей и диакритикой | Билеты en/ru/cs/he | Отрендерить; отсканировать телефоном и сканером | Код читается; совпадает с `barcodes`; нет UUID | PDF, фото скана | unit + manual | частично | CI + Человек | да | ✅ `P/delivery/pdf/*_test.go`; ✗ физический скан |
| SITE-23 | P1 | Покупатель | Письмо с билетом: язык = `buyer_locale`, блок оплаты, вложения, he RTL | 7 локалей | Рендер | Корректно в популярных клиентах | `.eml`, скриншоты | unit + manual | частично | CI + Человек | нет | ✅ `P/delivery/templates/templates_*_test.go`; ✗ рендер в реальных почтовых клиентах |

### 3.11 Telegram-бот (BOT) и Hermes (HRM)

Транспорт: long polling, один воркер, отдельная очередь на каждый аккаунт (`EB/serial.go`); входящего вебхука у бота нет (`EB/bot.go`). Заглушка Telegram для e2e: `stubTelegram` в `HS/bot_e2e_integration_test.go`.

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| BOT-01 | P0 | Приглашённый | `/start inv_<code>` → e-mail → `POST /v1/bot/invitations/accept` (service token) → `bot_telegram_links` | Приглашение | Пройти; повторить код; чужой e-mail | Привязка; повтор и чужой отклонены; пригласивший уведомлён | Записи stub, БД | E2E | да | CI | да | ✅ `HS/bot_e2e_integration_test.go` (TestBotE2E_InvitationToMyEvents), `HS/bot_invitations_integration_test.go` |
| BOT-02 | P0 | Менеджер | Мастер «+ Event» → event-bundle → черновик → видно в EC → публикация привязывает канал → образец билета | — | Диалог создания | Событие в БД с нужными полями; в EC тот же черновик; PDF образца отправлен | БД, записи stub | E2E | да | CI | да | ✅ `TestBotE2E_WizardCreatesAnEvent`, `_WizardSeatedDate`, `_WizardPerDateCategories`; ◐ сверка с экраном EC |
| BOT-03 | P0 | Менеджер | Афиша: фото (сжатое), документ PNG/JPEG, PDF, больше 5 МиБ, не изображение, неверные пропорции, меньше 1000 px | Набор файлов | Отправить каждый | Принимаются только png/jpeg 4:5 или 1:1 шириной от 1000; PDF даёт `poster_is_pdf`; остальное понятный отказ | Тексты stub | E2E | да | Codex | да | ◐ e2e только валидный PNG; unit `TestPosterCheck`, `EB/uxh_texts_test.go` |
| BOT-04 | P1 | Менеджер | Чтение афиши (`posterread`: Anthropic, Gemini, fallback) → подсказки кнопками | Ключ модели есть и нет | Отправить афишу | Подсказки только кнопками; без ключа пауза 10 мин; ничего не применяется без нажатия | Тексты, логи | unit + manual | частично | CI + Человек | нет | ✅ `P/posterread/*_test.go`, `EB/wizard_hints_test.go`; ✗ живой вызов модели (`B/cmd/arena-poster-bench` вручную) |
| BOT-05 | P0 | Менеджер | Загрузка схемы в боте: только SVG-документ ≤ 3 МиБ → `seating.ImportSVG` локально → `CreateSeatingPlan`/`CreatePlanVersion`; при ошибке план архивируется | SVG корректный и с ошибками; PDF, JPEG, XLSX, фото, SVG больше 3 МиБ | Отправить каждый | SVG: версия плана и места; ошибки перечислены; PDF/JPEG/XLSX получают `plan_up_not_svg` или `plan_up_need_file`; большой даёт `plan_up_too_big`; мусорных планов не остаётся | Записи stub, БД | E2E | да | CI | да | ◐ `HS/bot_e2e_plan_upload_integration_test.go` (не-SVG, фото, размер, плохой SVG); ✗ явные XLSX и PDF; ✗ путь очистки при `plan_up_failed` |
| BOT-06 | P1 | Менеджер | PDF/JPEG/XLSX → генератор → SVG через бота | — | — | ⊘ функции нет: бот генератор не вызывает. Кейс проверяет, что отказ объясняет, что прислать (SVG), а также будущий контракт | Тексты | E2E | да | Codex | нет | ⊘ |
| BOT-07 | P1 | Менеджер | Неполная нумерация, проданные или серые места, служебные зоны, вопросы организатору | Схемы с такими участками | — | ⊘ в боте вопросов нет; в генераторе только QUARANTINE (VSG-02). Кейс фиксирует требование к будущему диалогу уточнений | — | — | — | — | нет | ⊘ |
| BOT-08 | P1 | Менеджер | Материалы: фото (≤ 10 МиБ), видео, партнёрские ссылки, адрес, точка карты (геолокация или координаты) | — | Каждое действие; фото больше 10 МиБ; http-ссылка | Сохранено; отказы понятны; на странице видно | Тексты, публичный JSON | E2E | да | CI | нет | ◐ `TestBotE2E_Materials`; лимиты только unit |
| BOT-09 | P0 | Менеджер | Сеансы: перенос (календарь, dry-run, текст письма), отмена со словом-подтверждением | Оплаченные заказы | Перенос; отмена с неверным словом; отмена после перезапуска | Неверное слово ничего не отменяет; письма как в CAT-13 | БД, stub | E2E | да | CI | да | ✅ `TestBotE2E_SessionsMoveKeepsTheSessionAndWritesToBuyers`, `_SessionCancelPhraseAfterRestart` |
| BOT-10 | P0 | Владелец | Возврат в боте: выбор, сумма, причина, слово, Idempotency-Key, восстановление после перезапуска | Оплаченный заказ | Возврат; перезапуск посреди отправки | Одно обращение к провайдеру; после перезапуска предложено повторить | БД, stub | E2E | да | CI | да | ✅ `HS/bot_e2e_refund_integration_test.go` |
| BOT-11 | P1 | Владелец | Команда: приглашение переживает перезапуск, менеджер только читает | — | Пригласить, перезапустить бота | Диалог продолжается | stub | E2E | да | CI | нет | ✅ `TestBotE2E_TeamInviteSurvivesBotRestart` |
| BOT-12 | P0 | Менеджер | Дедупликация: двойное нажатие, повторная доставка того же `update_id`, нажатие на устаревшее сообщение | — | Два одинаковых апдейта подряд | Одно действие; устаревшее отвечает «out of date» | БД, stub | unit + E2E | да | Codex | да | ◐ `EB/feed_test.go` (двойное нажатие); ✗ повтор `update_id` (отдельной дедупликации по `update_id` в коде нет, есть только offset getUpdates) |
| BOT-13 | P1 | Менеджер | Лимит: 30 сообщений и 120 нажатий в минуту | Лимит включён | 31 сообщение | Одно сообщение «слишком много»; дальше тишина до окна | stub | E2E | да | Codex | нет | ◐ unit `EB/ratelimit_test.go`; e2e выключают лимит |
| BOT-14 | P1 | — | `callback_data` ≤ 64 байт во всех клавиатурах | — | Статическая или табличная проверка всех построителей клавиатур с UUID | Ни одна не длиннее 64 байт | Отчёт | unit | да | Codex | нет | ◐ только сеансы (`TestSesKeyboard_CallbacksFitTelegram`) и частично другие |
| BOT-15 | P1 | Менеджер | Языки en/ru/es | — | Пройти мастер на каждом | Нет ключей вместо текста; плейсхолдеры подставлены | Тексты | unit + E2E | да | CI | нет | ✅ ключи (`TestEventBot_LocaleBundleHasEveryKey`); ◐ e2e в основном ru |
| BOT-16 | P1 | Менеджер | Истечение диалога, напоминание о черновике (24 ч), удаление (7 дней) | — | Сдвиг времени | «Время вышло» ровно один раз; напоминание; удаление с уведомлением | stub | E2E | да | CI | нет | ✅ `TestBotE2E_DraftReminderThenExpiry`, `EB/dialogs_test.go` |
| BOT-17 | P1 | Менеджер | Мини-приложение из бота (флаги `BOT_MINIAPP_*`) | — | Открыть редактор | Кнопка web_app ведёт на `/tg/edit#code=` | stub | E2E | да | CI | нет | ✅ `HS/bot_e2e_miniapp*_integration_test.go` |
| BOT-18 | P2 | Заявитель | Анкета в боте: код из 6 цифр, кнопка контакта, три согласия | — | Пройти | См. IAM-11 | stub | E2E | да | CI | нет | ✅ `HS/bot_e2e_onboarding_integration_test.go` |
| BOT-19 | P1 | DevOps | Один поллер на токен, нет публичного порта, токен только в env | Два экземпляра | Запустить второй | Второй получает 409 и не обрабатывает апдейты | Логи | manual | нет | Человек | нет | ✗ |
| BOT-20 | P1 | Оператор | `/start` незнакомца без приглашения | Флаг анкеты выключен | `/start` | `bot.not_invited`, никаких данных | stub | E2E | да | CI | нет | ◐ проверить в `bot_e2e_onboarding` |
| HRM-01 | P1 | Hermes как организатор | Бот + EC + hosted-страница + Stripe test + письма | **Прод, организация #1 ABH TEAM** (Stripe test), правила навыка | Сценарий F0–F10 (`ops/hermes/skills/arena-ux-tester/references/functional-run.md`) с проверкой страницы после каждой правки (`page-checks`) | Журнал без S1/S2; деньги сходятся | Журнал, скриншоты, номера заказов | E2E + manual | частично | Hermes | да для релизов, меняющих путь покупки | ◐ прогоны 26–27.10 частичные (не пройдены W15, M6, M7) |
| HRM-02 | P1 | Hermes | Бот B1–B17 | То же | `features-bot.md` | Как в чек-листе | Журнал | E2E | частично | Hermes | нет | ✗ полного прогона по боту в репозитории нет |
| HRM-03 | P1 | Hermes | Веб W1–W16 | То же | `features-web.md` | — | Журнал | E2E | частично | Hermes | нет | ◐ |
| HRM-04 | P1 | Hermes как менеджер | M1–M9, второй тестовый Telegram | Приглашение | `manager.md` | Права менеджера соблюдены | Журнал | E2E | частично | Hermes + Человек | нет | ✗ |
| HRM-05 | P2 | Hermes как новичок | Z0–Z9: сайт → анкета → одобрение → первое событие → бесплатный билет | Одобряет владелец | `clean-run.md` | Новичок проходит без подсказок | Журнал | E2E | частично | Hermes + Человек | нет | ◐ 26.10 пройдены Z0–Z7 |
| HRM-06 | P1 | Hermes | Схема зала в боте: SVG (успех), PDF и JPEG (понятный отказ); проверка мест в виджете | Синтетический SVG из генератора | Загрузить, привязать, открыть виджет | Места совпадают с исходником | Скриншоты, хэш SVG | E2E | частично | Hermes | нет | ✗ |

### 3.12 Venue SVG Generator (VSG)

Продуктовый конвейер в коде сейчас только один: **XLSX → seating-json-v1 → Arena authoring SVG** (`VG/xlsx_seating_adapter`, `VG/arena_export`, сервис `VG/service`). PDF и JPEG существуют только как диагностика и ручные скрипты в неотслеживаемых каталогах `VSG/` (`pdf_diagnostics`, `m2_fixtures/results/*/build_package.py`).

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| VSG-01 | P0 | Оператор схем | XLSX → seating-json-v1 (профили hunter, aribau, synthetic) | Hunter (907 мест), Aribau (1166) — приватные; synthetic (24) — в репо | `python -m m1_product` / адаптер | Число мест, рядов и секций совпадает с замороженными эталонами | JSON, SHA-256 | unit + regression | да | CI | да (для релизов генератора) | ◐ `VG/xlsx_seating_adapter/tests/test_adapter.py`; **9 тестов на реальных залах пропускаются в CI** без приватных данных (`VG/conftest.py` NEEDS_PRIVATE_DATA) |
| VSG-02 | P0 | Оператор | Непонятные номера → QUARANTINE; сервис отвечает 409 `job.quarantined`, отдаёт только диагностический SVG | XLSX с нечисловыми и неизвестными номерами | `POST /generate` → `GET /jobs/{id}/svg` | 409; `diagnostic.svg` с группой `quarantine` | Ответы, отчёт | unit + integration | да | CI | да | ✅ `test_unknown_is_quarantined`, `VG/service/tests/test_service.py` (test_quarantine_has_report_and_diagnostic_but_no_svg) |
| VSG-03 | P0 | Оператор | Дубли идентичностей → QUARANTINE | XLSX с повтором | Прогон | QUARANTINE `duplicate_identity_keys` | Отчёт | unit | да | CI | да | ✅ `test_duplicate_is_quarantined` |
| VSG-04 | P0 | Оператор | Запрет выдумывать места и номера: в SVG только `number_status=explicitly_read` | Ряд с пропусками и ненумерованными | Экспорт | Ненумерованных нет в SVG; пропуски в `gaps`, а не заполнены | SVG, JSON | unit | да | CI | да | ✅ `VG/arena_export/tests/test_exporter.py` (test_unnumbered_seats_stay_out_of_the_svg) |
| VSG-05 | P0 | Оператор | Provenance: `source_sha256` = SHA-256 загруженных байт, размер, версии адаптера и генератора; без путей хоста и имени файла | Любой XLSX | `POST /generate`, `provenance.json` | Хэш совпадает с вычисленным независимо | Хэш, JSON | unit | да | Codex | да | ◐ `test_nothing_of_the_host_or_the_client_file_leaks` проверяет 64 hex-символа, но не равенство байтам; ✗ отдельный тест `provenance.json` M1 |
| VSG-06 | P0 | Оператор | Source-only: `reference_svg_used=false`, генератор не импортирует оценщик | — | Тесты | Отказ при `reference_svg_used=true` | Отчёт | unit | да | CI | да | ✅ `test_schema_and_source_guards`, `test_generator_does_not_import_evaluator` |
| VSG-07 | P0 | Оператор → Arena | Выход генератора → `seating.ImportSVG` | `VG/arena_export/testdata/synthetic_authoring.svg` + `.expected.json`, затем реальные залы | Go-тест импорта | 0 ошибок, 0 предупреждений, место в место | Отчёт Go, хэш SVG | contract | да | CI | да | ✅ synthetic: `B/internal/domain/seating/venue_gen_authoring_test.go`; ✗ реальные залы |
| VSG-08 | P1 | — | Golden-фикстура актуальна | — | `test_committed_fixture_is_current` | Совпадает (перегенерация `VENUE_GEN_REGEN_FIXTURE=1`) | diff | regression | да | CI | нет | ✅ |
| VSG-09 | P1 | — | Лимиты сервиса: 413, тайм-аут, очередь, TTL, память, падение воркера | — | `test_service.py` | Корректные коды и отсутствие утечек | Отчёт | integration | да | CI | нет | ✅ (лимит памяти через RLIMIT_AS работает только на Linux) |
| VSG-10 | P1 | — | Объединённые заголовки, проходы, повторяющиеся метки рядов, служебные ячейки | Aribau | Прогон | Пропуски H21/U21, 108 меток рядов отвергнуты как не места | JSON | unit | да | CI | нет | ◐ `test_merged_headers_service_cells_repeated_labels_and_gaps` (приватные данные) |
| VSG-11 | P1 | Оператор | Векторный PDF (BIGHALL) → JSON | `VSG/` BIGHALL PDF | — | ⊘ адаптера нет; есть только `VSG/pdf_diagnostics/diagnose_pdf.py` | — | — | — | — | нет | ⊘ |
| VSG-12 | P1 | Оператор | JPEG (Calle Ruiz Covers 6: 312 мест, 16 рядов) → JSON с явной описью рядов | `VSG/Calle Ruiz Covers 6.jpeg` | — | ⊘ ручная сборка в неотслеживаемом `build_package.py` | — | — | — | — | нет | ⊘ |
| VSG-13 | P1 | Оператор | Кривые и прямые ряды, выравнивание по исходнику | — | — | ⊘ в XLSX-конвейере только сетка | — | — | — | — | нет | ⊘ / ✗ |
| VSG-14 | P1 | Оператор | Категории: несколько, назначение | — | — | Сейчас ровно одна временная категория `#00aaff`, `commercial_data:false`; цен нет | SVG | unit | да | CI | нет | ✅ одна категория (`test_profile_must_declare_non_commercial`); ⊘ несколько |
| VSG-15 | P1 | Оператор | Проданные, серые места, служебные зоны | — | — | ⊘ не моделируются; в SVG только легенда Sold/None/Reserved/MyTickets | — | — | — | — | нет | ⊘ |
| VSG-16 | P1 | Организатор | Уточнение непонятных участков у организатора | — | — | ⊘ процесса нет, есть только статус QUARANTINE с `rejected_cells` | — | — | — | — | нет | ⊘ |
| VSG-17 | P1 | Покупатель | Визуальная читаемость: SVG → PNG, сравнение с эталоном, минимальный размер места и подписи; ручная оценка | synthetic + реальные залы | Отрендерить, сравнить | Расхождение ниже порога; человек подтверждает | PNG, diff, хэш | visual | частично | Codex + Человек | нет | ✗ в CI нет (есть неотслеживаемые `VSG/arena_importsvg_proof/visual_check`) |
| VSG-18 | P1 | — | Scope: правки генератора не выходят за `apps/venue-svg-generator/`; стражи оркестрации | — | Path-filter CI; node-тесты `VSG/orchestration` (`supervisor.test.mjs`, `m1-write-mock.test.mjs`) | Зелёные | Отчёт | regression | да | CI | нет | ◐ в CI только фильтр путей; node-тесты не подключены |
| VSG-19 | P1 | Суперадмин, покупатель | Сквозь: SVG генератора → загрузка в admin (ADM-02) → привязка к сеансу → виджет показывает ровно эти места | synthetic | Пройти | Число и номера мест в виджете = JSON генератора | Скриншот, seat-status, хэш | E2E | да | Codex | нет | ✗ |
| VSG-20 | P1 | — | Регрессия на реальных залах в CI | Приватные фикстуры | Зашифрованные фикстуры или приватный раннер | 9 пропущенных тестов выполняются | Отчёт | regression | частично | Человек (решение) + CI | нет | ✗ |

### 3.13 Сквозные межсистемные сценарии (X)

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| X-01 | P0 | Организатор → покупатель → организатор | Бот → API → EC → hosted-страница → виджет → Stripe test → вебхук → worker → письмо → EC → возврат → outbox → MACS → бот продаж | Прод, ABH TEAM, Stripe test, тестовые ящики команды | 1) событие с SVG-схемой в боте; 2) цены в EC; 3) публикация; 4) покупка места картой 4242…; 5) письмо и PDF; 6) заказ в EC; 7) возврат 1 билета; 8) место снова в продаже; 9) уведомления | Все переходы видны; деньги сходятся; ничего не ушло наружу (MACS и сайты — заглушки или тестовые) | Номера заказов, скриншоты, `outbox_events`, письма | E2E | частично | Hermes + Человек | да | ◐ части есть по отдельности (BOT-02, EC-12, PAY-11); целиком только ручные прогоны Hermes |
| X-02 | P0 | Покупатель сайта WP | Стейджинг Lampyris или Vino → шлюз → вебхук сайту → письмо сайта | `staging-1` поднят из снапшота | Покупка | `data.id` = номер заказа WC; билеты дошли | Логи сайта и API | E2E | нет | Человек | да для релизов шлюза | ◐ серверная часть ✅ ASY-02 |
| X-03 | P0 | Менеджер → покупатели | EC или бот → sessionchange → письма → hosted-страница и PDF | Оплаченные заказы | Перенести сеанс | Письма; новое время на странице, в PDF и в выдаче шлюза | Письма, PDF | E2E | частично | Codex | да | ◐ CAT-13, EC-14 |
| X-04 | P0 | Владелец орг. A | EC, бот, виджет, admin | Две организации | Подставлять id и токены чужой организации в URL EC, в callback бота, в фид виджета | Везде 404 или отказ, без данных | Скриншоты, ответы | security E2E | частично | Codex + Hermes | да | ◐ API ✅ IAM-01/02; ✗ через UI |
| X-05 | P0 | Новичок | arena-site → анкета → код в боте → одобрение → письмо с паролем → EC → подключение Stripe → бесплатный и платный билет | Тестовый e-mail, тестовый Telegram, новый тестовый аккаунт Stripe | Раздел 3.14 целиком | Работающая организация, продающая за деньги | Журнал, номера заказов | E2E | частично | Hermes + Человек | да для релизов онбординга и платежей | ◐ HRM-05 (до бесплатного билета); подключение Stripe ни разу не проверялось (NEW-06..NEW-13) |
| X-06 | P1 | Покупатель магазина | SP → API → AllPay/PayPlus тест → колбэк → письмо → EC shop orders | Тестовый магазин | Купить, отгрузить, вернуть | Статусы сходятся | Журнал | E2E | частично | Hermes | нет | ◐ прогоны Hermes 29–30.10 (`docs/testing/hermes_shop_*`) |

---

### 3.14 Новая организация совсем с нуля и подключение Stripe (NEW)

Такого прогона ещё не было. Прогон Hermes 26.10 (Z0–Z7) дошёл до первого мероприятия и остановился до продажи. Подключение Stripe новой организацией не проверялось никогда.

**Где проводить.** На проде, как прогон 26.10: новая организация-песочница «UX Hermes <дата>» создаётся через анкету и после прогона архивируется (NEW-15). Тестовый аккаунт Stripe заводится на тестовый e-mail команды и работает только в Test mode. Это прямое следствие решения «тест совсем с нуля»: ABH TEAM уже существует и с нуля пройти не может, поэтому он остаётся площадкой для функционального прогона F0–F10. Владелец подтвердил 01.11: песочницу на проде создавать.

**Как устроено сейчас (по коду `origin/master`):**
1. **Анкета.** Сайт `arena-site` или бот (`/start apply`). В форме есть вопросы `payment_provider` (stripe, flitt, other, undecided) и `has_payment_account` (`P/onboarding/schema.go:128–129`).
2. **Одобрение.** `P/provisioning` создаёт организацию, канал `direct_merchant` с комиссией 0.00 и включённой hosted-страницей, и владельца. Платёжного конфига нет. Бот и EC показывают «Платежи ещё не подключены», пока нет активного конфига с `verification_status=ok` (`EB/payments.go`, `EC/src/lib/orgData.ts` `paymentsNotConnected`). Текст в боте говорит: подключение после одобрения делает оператор.
3. **Конфиг Stripe.** Маршруты `POST|PATCH|DELETE /v1/organizations/{org_id}/payment-configs[/{id}]` и `POST .../{id}/verify` (`HS/mount_iam.go:113–131`). Право `payment_config.write` у владельца (`org_admin`) есть с миграции 0046. Но **экрана подключения у владельца нет**: EC только читает список, в боте раздела нет. Интерфейс есть только у суперадмина в admin `/payments` (`AW/src/routes/payments.tsx`).
4. **Поля.** Секретный ключ с префиксом `sk_test_`, `sk_live_`, `rk_test_` или `rk_live_` (ограниченные ключи `rk_` принимаются); режим test или live сверяется по префиксу. Плюс секрет подписи вебхука `whsec_` (`B/internal/adapters/stripe/module.go`).
5. **Проверка.** `POST .../verify` спрашивает Stripe `GET /v1/balance`. Затем ищет в аккаунте вебхук на `/v1/payment-intents/webhook/{config_id}` и подписку на события возвратов. Предупреждения: `webhook_endpoint_not_found`, `refund_events_not_subscribed` (`HS/hpayments/payment_config_verify.go`). Проверка никогда не блокирует сохранение.
6. **Вебхук в Stripe платформа сама не создаёт.** Его адрес содержит id конфига, поэтому порядок такой: сохранить конфиг без `whsec_` → получить адрес → завести endpoint в кабинете Stripe → вставить `whsec_` → Verify.
7. **Stripe Connect Standard (OAuth).** Код `HS/hbilling/stripe_connect.go` есть, но в проде маршруты не смонтированы: `Options.StripeConnect` нигде не задаётся. Колбэк только возвращает `account_id` в JSON: не сохраняет его в организацию и не сверяет `state`. Текущая платёжная модель — «каждый организатор со своим аккаунтом Stripe, без Connect» (AGENTS.md).

**Варианты подключения, которые сравнивает прогон:**

| Вариант | Как проходит | Что есть в коде | Действий организатора (оценка по коду, измерить в NEW-14) | Риски | Статус |
|---|---|---|---|---|---|
| V1 «Ключи через оператора» | Организатор регистрирует Stripe → Test mode → копирует секретный ключ → передаёт оператору → оператор создаёт конфиг в admin `/payments` → организатор заводит endpoint по адресу, который назвал оператор, и отдаёт `whsec_` → оператор вставляет его и жмёт Verify | Всё | 8–12 действий, два человека, два обмена сообщениями | Секретный ключ идёт мимо системы (почта, мессенджер); ошибки копирования; задержка на ожидание оператора | Работает, ни разу не прогонялся с нуля |
| V2 «Владелец сам» | То же, но владелец вводит ключи сам | API и право есть; экрана в EC и боте нет | Для нетехнического организатора недоступно | — | ⊘ экран |
| V3 «Ограниченный ключ» | V1 или V2 с ключом `rk_test_` только с нужными правами | Префикс принимается | +настройка прав в Stripe | Если у ключа нет права читать вебхуки, проверка молча не выдаст предупреждений; минимальный набор прав неизвестен | Проверить (NEW-09) |
| V4 «Кнопка Stripe Connect (OAuth Standard)» | «Подключить Stripe» → страница Stripe: войти или зарегистрироваться → возврат в arena | Частично: построение URL и обмен кода; нет хранения `acct_`, проверки `state`, работы с ключом платформы и заголовком `Stripe-Account`, вебхуков Connect | 2–4 действия, без копирования ключей | Меняет модель «direct merchant без Connect»; нужен Connect-аккаунт платформы и решение владельца | ⊘ нужна разработка |
| V5 «Платформа создаёт аккаунт Stripe за организатора» (Connect Express/Custom, Account Links) | Организатор заполняет анкету Stripe внутри потока платформы | Нет | Минимум | Платформа берёт на себя часть ответственности за проверку продавцов и споры; меняется юридическая модель | ⊘ решение владельца |
| V6 «Автосоздание вебхука» | При сохранении ключа arena сама создаёт endpoint с нужными событиями и сохраняет `whsec_` | Нет: сейчас только чтение списка вебхуков | Минус 3–4 действия в V1–V3 | Ключу нужно право записи вебхуков | ⊘ кандидат на улучшение |

Как Stripe ведёт себя у нового пользователя (выдаёт ли тестовые ключи сразу после регистрации, что спрашивает, можно ли зарегистрироваться прямо на странице OAuth) — это не код arena. Это фиксируется скриншотами в NEW-06 и NEW-08 по актуальному интерфейсу Stripe.

| ID | P | Роль | Сервисы и точки | Предусловия и данные | Шаги | Ожидаемый результат | Evidence | Уровень | Авто | Исп. | Блок | Покрытие сейчас |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| NEW-01 | P0 | Заявитель | `arena-site` → `/v1/onboarding/*` или бот `/start apply` → код из 6 цифр → submit | Новый e-mail команды (`+`-алиас), Telegram-аккаунт без привязок | Навык Hermes `clean-run.md` Z0–Z3 | Заявка `submitted`; оператор получил уведомление; письмо «получено» | Журнал, скриншоты, строка `onboarding_applications` | E2E | частично | Hermes | да для релизов онбординга | ◐ бэкенд ✅ (IAM-11, BOT-18); Hermes 26.10 прошёл Z0–Z3 |
| NEW-02 | P0 | Оператор | admin `/onboarding/$applicationId` → approve → `P/provisioning` | Заявка из NEW-01 | Z4: одобрить | Организация; канал `direct_merchant`, комиссия 0.00, hosted-страница включена; владелец `org_admin`; письмо задания пароля; привязка Telegram; MoR выключен; `kyb_status` не тронут | БД, письмо | E2E | частично | Человек | да | ✅ бэкенд `P/onboarding/service_integration_test.go`; ◐ экран admin |
| NEW-03 | P0 | Владелец | Письмо → `/accept-invite` в EC → вход; бот `/start` | — | Z5–Z6 | Ссылка ведёт в EC, а не в admin (UXH-08); вход работает; бот и EC показывают «Платежи ещё не подключены» | Скриншоты | E2E | частично | Hermes | да | ◐ unit `paymentsNotConnected`, `EB/payments.go`; проверить исправление UXH-08 |
| NEW-04 | P0 | Владелец, покупатель | Бесплатное событие и бесплатный билет без платёжного конфига | — | Z7–Z9 | Билет, письмо, продажа видна организатору | Номер заказа, письмо | E2E | частично | Hermes | да | ✅ бэкенд ORD-07; ◐ Hermes 26.10 остановился на Z8 |
| NEW-05 | P0 | Покупатель, владелец | Платное событие без конфига: виджет → `checkout/start` → preflight | Платная категория | Попытаться купить; посмотреть остаток | 422/503 `checkout.payment_not_configured` **до** захвата мест, остаток не изменился; покупатель видит понятный текст; организатор заранее предупреждён в боте и EC | Ответ API, остаток, скриншот виджета | integration + E2E | да | CI + Hermes | да | ◐ код `checkout.payment_not_configured` проверяется в `HS/hosted_checkout_integration_test.go:961`, `HS/flitt_checkout_integration_test.go`, `HS/allpay_checkout_integration_test.go`; ✗ текст виджета и предупреждение при публикации платного события |
| NEW-06 | P0 | Организатор | Сайт Stripe: регистрация нового аккаунта → Test mode → ключи API | Новый e-mail команды | Зарегистрироваться; найти секретный и (для V3) ограниченный ключ; записать, что Stripe спрашивает до выдачи тестовых ключей | Тестовые ключи получены; шаги и время записаны | Скриншоты (ключи замазаны), время | manual | нет | Hermes или Человек | нет | ✗ ни разу |
| NEW-07 | P0 | Оператор + организатор | V1: admin `/payments` → `POST .../payment-configs` (stripe, test, без `whsec_`) → адрес `/v1/payment-intents/webhook/{config_id}` → endpoint в Stripe с событиями `checkout.session.completed`, `checkout.session.async_payment_succeeded`, `checkout.session.async_payment_failed`, `checkout.session.expired`, `refund.created`, `refund.updated`, `refund.failed` (набор как у ABH TEAM) → `whsec_` → PATCH → Verify | Ключи из NEW-06 | Пройти; затем открыть бот и EC | `verification_status=ok`, `webhook_warnings=[]`; предупреждение «Платежи не подключены» исчезло | Ответ verify, скриншоты, audit `v1.payment_config.verify` | E2E | частично | Человек + Hermes | да | ◐ бэкенд `HS/provider_config_ab41_integration_test.go`, `HS/hcheckout/provider_config_test.go`; ✗ сквозной путь с новым аккаунтом |
| NEW-08 | P1 | Владелец | V2 и V4: попытаться подключить самому в EC, в боте и через `GET /v1/stripe/connect/authorize` на проде | — | Поискать путь подключения в интерфейсе | Фиксирует факт: экрана нет, Connect отвечает 404. Это UX-находка, а не дефект | Скриншоты, ответ API | manual + integration | частично | Hermes + Codex | нет | ⊘ |
| NEW-09 | P1 | Организатор | V3: ограниченный ключ `rk_test_` | Аккаунт из NEW-06 | Выдать минимальные права; затем по одному убрать: создание Checkout Session, возвраты, чтение баланса, чтение вебхуков; на каждом шаге Verify и тестовая покупка или возврат | Задокументирован минимальный набор прав; при нехватке права ошибка понятна оператору | Таблица «право → что ломается» | manual | частично | Человек | нет | ✗ |
| NEW-10 | P0 | Оператор | Ошибки подключения | Тестовый аккаунт | По очереди: публикуемый ключ `pk_` вместо секретного; ключ live в test-конфиге; отозванный ключ; `whsec_` от другого endpoint; endpoint без событий возврата; ключ с пробелами | Формат даёт 4xx; несовпадение режима — отказ; отозванный ключ — `failed` с текстом Stripe; чужой `whsec_` — вебхук 401, оплата висит, watchdog шлёт `payment_not_completed`; предупреждение `refund_events_not_subscribed`; пробелы обрезаны | Ответы API, оповещение ops | integration + E2E | частично | Codex + Человек | да | ◐ unit (префиксы, `normalizeSecretPatch`), watchdog ✅ ASY-09; ✗ сквозной |
| NEW-11 | P0 | Покупатель | Первая платная покупка в новой организации | Конфиг из NEW-07, платное событие | Карты: 4242 4242 4242 4242 (успех); 4000 0000 0000 0002 (отказ); 4000 0025 0000 3155 (3-D Secure); 4000 0000 0000 9995 (недостаточно средств) | Успех: paid, билет, письмо, продажа в боте продаж; отказы: билетов нет, места освобождаются; 3-D Secure проходит | Номера заказов, письма, скриншоты Stripe | E2E | частично | Hermes | да | ✗ для новой организации (для ABH TEAM прогон 26–27.10, F4) |
| NEW-12 | P0 | Владелец | Возврат первой продажи: из бота, из EC и из кабинета Stripe | Оплаченный заказ из NEW-11 | Три возврата на трёх заказах | Как PAY-11 и PAY-13; деньги, билеты и места сходятся | Номера, скриншоты | E2E | частично | Hermes | да | ◐ бэкенд ✅; ✗ новая организация |
| NEW-13 | P1 | Владелец | Граница test и live | Только тестовые ключи | Проверить, что в тестовом конфиге Stripe отклоняет настоящую карту, и что интерфейс показывает режим (test или live) | Реальная оплата невозможна; режим виден оператору | Скриншот | manual | нет | Человек | нет | ✗ live-ключи в тестах не вводятся никогда |
| NEW-14 | P1 | Hermes | UX-метрики: время и число действий от заявки до первой платной продажи для V1 и V3 (V4–V6 — после разработки); где организатор застрял, какие тексты не поняты | Прогоны NEW-01..NEW-12 | Таблица в отчёте Hermes | Сравнение вариантов с цифрами; рекомендация владельцу | Отчёт в `docs/testing` | manual | нет | Hermes | нет | ✗ |
| NEW-15 | P1 | Hermes + оператор | Уборка после прогона | — | Архив организации; отключить токен фида (`is_active=false` **и** `revoked_at`); отвязать Telegram (`bot_telegram_links.revoked_at`); деактивировать конфиг; удалить тестовый endpoint в Stripe | Песочница ничего не продаёт, подписок не осталось | SQL-выборки, скриншоты | manual | частично | Человек + Claude Code | нет | ◐ порядок описан в навыке Hermes |

## A. Матрица коммуникаций

Формат по умолчанию — JSON поверх HTTPS, snake_case. Исключения: Bil24-шлюз и вебхуки сайтам используют camelCase, это разрешено стражем `HS/snake_case_json_test.go`.

| # | Канал | Вход | Выход | Формат | Авторизация | Retry / идемпотентность | Обработка ошибки | Тесты |
|---|---|---|---|---|---|---|---|---|
| 1 | Браузер → hosted-страница TP | URL `/{slug}[/{event}]`, `?lang`, `?checkout_token`, `?session` | Статика nginx + `GET /v1/public/pages/...` | HTML, JSON | Нет (публично) | `Cache-Control: public, max-age=30`, `Vary: Origin` | 404 и экран «не найдено»; ошибка сети даёт экран ошибки | CAT-12, SITE-01, SITE-02, SEC-07 |
| 2 | Сайт клиента → виджет → публичный фид | `feed_token` (в пути), event-id, session-id | `GET /v1/public/feeds/{t}/events[/{id}]`, `/v1/event-sessions/{id}/schema`, `seat-status?since_version` | JSON, SVG | Токен фида (у отключённого канала `is_active=false` и `revoked_at`) | Опрос seat-status дельтами; ETag у schema | Три лимита (429); старые бэкенды: синтетический сеанс | SITE-03, SITE-04, SEC-08 |
| 3 | Виджет → checkout/start → hold-транзакция → провайдер | Корзина, покупатель, согласие, атрибуция, `return_url`, locale | 201 `redirect_url`, `checkout_token`, `expires_at` | JSON | Токен фида; preflight конфига провайдера до захвата мест | Блокировки: `sessions` раньше `inventory_ledger`; `retryOnSerializationFailure`; Idempotency-Key к Stripe | 409 конфликт мест; 422/503 оплата не настроена (503, не 502, из-за Cloudflare); 400 `invalid_return_url` | ORD-01, ORD-02, ORD-04, ORD-11 |
| 4 | Платёжный провайдер → вебхук | Событие Stripe, Flitt, AllPay, PayPlus | `POST /v1/payment-intents/webhook/{config_id}` → завершение checkout, `worker_jobs`, заказ paid | JSON (у Flitt подпись в теле) | Подпись секретом конфига; у Flitt `merchant_id`; `ExpectedProvider` | Дедуп `(provider_payment_id, event_type)`; терминальное состояние даёт 200 `processed:false` | Неверная подпись 401; чужой платёж 200 `acknowledged`; ошибка в транзакции 500 для повторной доставки; сумма не та: `manual_review` | SEC-01, PAY-01..05, PAY-08 |
| 5 | Провайдер → возврат покупателя | GET или POST с провайдера | `/v1/public/payment-return?r=&checkout_token=` → 303 | HTTP-редирект | `r` проверяется `ReturnURLPolicy` | — | Чужой origin заменяется запасным адресом | PAY-10 |
| 6 | Виджет и TP → статус заказа и билет | `checkout_token` | `GET /v1/public/checkout/{t}`, `POST .../recover`, `GET .../tickets/{id}/pdf` | JSON, PDF | Токен checkout (свой лимит 120 в минуту) | Recover идемпотентен по токену | 401/expired: тихий recover; терминальный статус очищает токен | ORD-09, ORD-10, SITE-07, SITE-08, SITE-09 |
| 7 | Worker: выдача билетов | `checkout.issue_tickets`, `checkout.convert_reservation` | `tickets`, `barcodes` (`mint.EAN13`), `ticket_credentials` | Строки БД | — | Уникальность `tickets_checkout_ordinal_uq`; повтор задачи | Ошибка: задача повторяется; dead-letter | ASY-05 |
| 8 | Worker → покупатель (письмо) | `ticket.deliver` | SMTP или Brevo, одно письмо на заказ с PDF | MIME, PDF | Учётные данные SMTP | Захват `delivery_jobs` одним оператором; при отказе все возвращаются в pending; resend через `RequeueDeliveryJob` | Отказ SMTP: retry; статус в `emailstatus` | ASY-06, SITE-22, SITE-23 |
| 9 | Brevo → вебхук статусов писем | События доставки | `POST /v1/webhooks/brevo/{token}` → `emailstatus` | JSON | Токен в пути | Идемпотентно по событию | Без токена маршрута нет; большое тело отвергается | SEC-03 |
| 10 | Outbox → сайты WP (Bil24WP), generic подписчики, MACS, бот продаж, биллинг | `outbox_events` | HTTP-вебхуки, Telegram | JSON (camelCase для Bil24) | HMAC или ключ подписи подписчика; egress-фильтр | До 30 попыток с backoff (~24 ч), затем dead-letter; ветки независимы; бот продаж: одно объявление (`sales_notification_deliveries`) | Dead-letter переигрывается только SQL-ом (runbook `docs/ops/bil24_gateway.md` §4); watchdog оповещает | ASY-01..04, SEC-05 |
| 11 | Сайт WP (плагин Bil24) → шлюз | Команды `/compat/bil24/json` (fid + token) | Ответы resultCode, деньги в major-единицах | JSON Bil24 | bcrypt-токен канала, кеш по хэшу; `settings.gateway.enabled` | -1 повторяемая ошибка, -99 неповторяемая; 101 `open_order_exists` | `resultCode`, HTTP всегда 200 (кроме `/image`) | ASY-12, SITE-18, X-02 |
| 12 | WP-плагин `arena-events` → API | Cron-синк, шорткод, прокси checkout | Фид, checkout/start; входящий вебхук на `/wp-json/arena-events/v1/webhook` | JSON | Токен фида; `X-Arena-Signature: sha256=` | Синк раз в час | — | SITE-17 |
| 13 | Event Center → API | Действия организатора | `/v1/...` с JWT, refresh, step-up | JSON, multipart | JWT (сессии в Redis), membership-роль, step-up на деньгах | Мастер: один POST на двойной клик; возврат с Idempotency-Key | Коды ошибок с переводом; 401 запускает refresh | EC-01..23, AUTH-01, AUTH-06 |
| 14 | Admin → API | Действия суперадмина | `/v1/admin/*` | JSON | JWT суперадмина + `X-Admin-Reason` на записи | — | 400 `superadmin.missing_reason` | ADM-*, IAM-04 |
| 15 | Telegram ↔ arena-bot | getUpdates (long polling) | sendMessage, edit, answerCallback, sendDocument | Bot API JSON и multipart | Токен бота (env), только приватные чаты | Offset getUpdates, отдельная очередь на аккаунт (≤ 64), «живое» сообщение принимает нажатие, Idempotency-Key у возвратов; **нет дедупликации по update_id** | Лимиты 30/120 в минуту; 403 «бот заблокирован» засчитывается как доставка | BOT-*, BOT-12, BOT-13, BOT-19 |
| 16 | arena-bot → API | REST от имени пользователя | `/v1/...` | JSON | JWT на 5 мин, подписанный общим `JWT_SIGNING_SECRET`; `/v1/bot/*` защищён `BOT_SERVICE_TOKEN`; `X-Client-Channel: telegram_bot`; `X-Admin-Reason` всегда | Диалоги в `bot_dialogs` переживают перезапуск | 503 без сервисного токена | BOT-01, SEC-09, ASY-16 |
| 17 | Бот или EC → чтение афиши → LLM | Изображение афиши | `POST .../media/{id}/poster-facts` → Anthropic или Gemini | JSON (tool call) | Ключ модели на сервере | Fallback между моделями; тайм-аут 45 с | Без ключа: подсказок нет, пауза 10 мин в боте | BOT-04, EC-06 |
| 18 | Telegram WebApp → EC `/tg` → API | initData или одноразовый код | `POST /v1/auth/telegram-miniapp`, `/auth/web/link` | JSON | HMAC initData; сессия 30 мин без продления | Код одноразовый | Просрочка не служит оракулом | AUTH-07, AUTH-08, EC-23 |
| 19 | Сайт arena-site и бот → анкета → оператор | Ответы формы, Turnstile, код из e-mail | `/v1/onboarding/*`, `/v1/bot/onboarding/*` → approve → provisioning → письма, Telegram оператору | JSON | Токен доступа заявителя (`X-Onboarding-Token`, хранится SHA-256), Turnstile, сервисный токен бота | Лимиты в БД; уведомление о решении не больше одного раза | 503 без секрета Turnstile в проде | IAM-11, IAM-12, BOT-18, X-05 |
| 20 | Генератор схем → Arena | XLSX (multipart) | `POST /generate` → job → `GET /jobs/{id}/svg` | JSON, SVG | Нет: сервис только во внутренней сети compose | Задачи в памяти, TTL 1 ч, очередь 20 | 409 quarantined, 413, тайм-аут | VSG-02, VSG-09. **Backend сервис не вызывает**: SVG переносит человек (ADM-02) или бот (BOT-05) |
| 21 | Admin и бот → импорт схемы | SVG-файл | `POST /v1/media` (`seating_plan_svg`) → `POST /v1/seating-plans/{id}/versions` → `ImportSVG` | SVG, JSON | Суперадмин или менеджер | Новая версия, проданное не затирается | 422 с ошибками по элементам | CAT-08, ADM-02, BOT-05, VSG-07 |
| 22 | Магазин SP → API → провайдер | Корзина, Turnstile | `/v1/public/shop/*` → AllPay/PayPlus → колбэк | JSON | Подпись провайдера | Поздний колбэк после отмены обрабатывается | 409 out_of_stock | SITE-19, X-06 |
| 23 | Ссылки продаж | `/go/{code}` | 302 на страницу + запись атрибуции | HTTP | Нет | Клик засчитывается клиенту один раз | Неизвестный код → `/go` | ORD-13, EC-10 |
| 24 | Подписка «сообщить о продажах» | E-mail + согласие | `sale-subscriptions` → письмо-подтверждение → `sale_open` | JSON, письмо | Токен фида, подтверждение по ссылке | Одна подписка на адрес | — | CAT-04, SITE-10 |
| 25 | Перенос и отмена сеанса | Действие EC или бота | `sessionchange.Apply` → журнал → `session.change_email` по каждому заказу | Строки БД, письма | `session.update` | Журнал в той же транзакции | 422 contact_missing, site_unsupported, too_long | CAT-13, EC-14, BOT-09, X-03 |
| 26 | Возврат | EC или бот | refunds engine → API провайдера → вебхук возврата → гашение → письмо → outbox `ticket.refunded` | JSON | Step-up, `refund.approve` | Idempotency-Key к провайдеру; один вызов при неизвестном исходе | Повтор и разрешение вручную (`/retry`, `/resolve`) | PAY-11..14, BOT-10, EC-12 |
| 27 | Сканирование MACS | Скан билета | `HS/hmacsscan` (вебхук с подписью), шлюзовая команда SCAN_TICKET | JSON | Подпись MACS; fid и токен | — | Образец билета: `-2 bil24.sample_ticket`, не гасится | ASY-03, ASY-12 |
| 28 | Эксплуатация | — | `/healthz`, `/readyz`, `/metrics` (bearer) → Prometheus; `ops.watchdog` → Telegram оператора; ночные бэкапы → R2 | HTTP, Telegram | Bearer для метрик | Дедуп оповещений (`ops_alerts`) | Без токенов Telegram оповещения только в лог | ASY-09, ASY-15, SEC-10 |
| 30 | Аккаунт Stripe организатора ↔ arena (подключение) | Секретный или ограниченный ключ, `whsec_` | Конфиг `payment_provider_configs`; проверка `GET /v1/balance` и `GET /v1/webhook_endpoints` | JSON (секреты не возвращаются) | `payment_config.write` (владелец через API, суперадмин через admin `/payments`); audit `v1.payment_config.verify` | Проверка повторяема; сохранение не зависит от доступности Stripe | `failed` только при отказе Stripe; при недоступности `unverified`; предупреждения вебхука | NEW-06..NEW-10, PAY-17 |
| 29 | Изменения событий → оператор | Снимки опубликованных событий | `events.change_watch` → бот продаж (только операторская подписка) | Telegram HTML | Токен бота продаж | Стабильность 60 с, запись до отправки | Потерянное сообщение не повторяется | ASY-10 |

---

## B. P0 release gates

**Merge в master запрещён, пока не пройдены гейты:**

| Гейт | Проверка | Кейсы | Как сейчас |
|---|---|---|---|
| G-01 | Все джобы `ci.yml` зелёные на коммите: lint, Unit/Race, Integration (чистая БД с migrate + seed), OpenAPI drift, admin, widget, widget-acceptance, **event-center-build, event-center-live, tickets-page, shop-page, rich-editor, venue-svg-generator** | ASY-13, EC-22, SEC-14 | Автоматически для первых семи; остальные не входят в `needs` (G-02) |
| G-02 | `build-and-push` зависит от всех джобов из G-01 | — | ✗ сейчас не так (ci.yml:1078). До исправления (PR-01) проверять вручную |
| G-03 | Изоляция организаций: обход `{org_id}` и плоских маршрутов, стражи дрейфа | IAM-01, IAM-02, IAM-03, IAM-04 | ✅ в Integration |
| G-04 | Вебхуки: подпись, повтор, чужой платёж | SEC-01, SEC-02, PAY-04 | ◐ (PR-03 закрывает просроченные метки и повторы) |
| G-05 | Деньги: оплата, бесплатный заказ, окно оплаты, две организации, возврат полный и частичный | PAY-01, PAY-05, PAY-08, PAY-11, PAY-12, ORD-06, ORD-07 | ✅ Go-интеграция |
| G-06 | Конкурентность и остатки: одно место, GA, истечение hold, промокод | ORD-02, ORD-03 (Go-часть), ORD-05, ORD-08, CAT-07 | ✅ |
| G-07 | Выдача и доставка билетов без дублей | ASY-05 (после PR-04), ASY-06 | ◐ |
| G-08 | Outbox и шлюз: доставка сайтам, `system_id`, golden-ответы шлюза | ASY-01, ASY-02, ASY-12 | ✅ |
| G-09 | Бот: приглашение, мастер, сеансы, возврат (e2e в Integration) | BOT-01, BOT-02, BOT-09, BOT-10 | ✅ |
| G-10 | Миграции: голова закреплена, паритет прав суперадмина | ASY-13 | ✅ |

**Production deploy дополнительно запрещён, пока не пройдены гейты:**

| Гейт | Проверка | Исполнитель |
|---|---|---|
| G-11 | Миграции на копии БД стенда (`arena-migrate up` без missing; объёмы до и после) | Человек + Claude Code |
| G-12 | Production-конфиг: `APP_ENV=production`, `EMAIL_MODE=smtp`, пустые `STRIPE_API_BASE_URL` и `FLITT_API_BASE_URL`, dev-auth выключен, `CORS` без `*`, `TRUSTED_PROXY_COUNT` = реальное число прокси, `BOT_SERVICE_TOKEN` задан (AUTH-10, PAY-09); у стенда, где идёт приёмка, почта и внешние подписчики не достают посторонних (PAY-19) | Человек (смотрит имена и наличие, не значения) |
| G-13 | Сразу после выката на прод: сквозной сценарий X-01 в ABH TEAM со Stripe test (полностью или урезанный до бесплатного билета, если не меняется платёжный путь); по навыку Hermes `functional-run.md`. Это гейт «релиз принят»: при провале S1 — откат на прежний образ по записанной ссылке (G-16) | Hermes + Человек |
| G-17 | Для релизов, трогающих онбординг, провижининг или платёжные конфиги: сценарий «с нуля» NEW-01..NEW-12 | Hermes + Человек |
| G-14 | Для релизов, трогающих шлюз или вебхуки: X-02 на стейджинге сайта | Человек |
| G-15 | Для релизов, трогающих схемы: CAT-08, ADM-02, VSG-07 | CI + Codex |
| G-16 | После выката, только чтение: `/healthz`, `/readyz`, версия образа = коммит, `GET` публичной страницы тестового события возвращает 200, нет новых dead-letter и ошибок в логах 15 мин; дамп БД до выката снят, ссылка на откат записана | Человек + Claude Code |

---

## C. Реестр тестовых данных

**Общее правило.** Никаких реальных заказов, карт и рассылок, никаких данных реальных организаций прода. Покупки и возвраты делаются только в тестовых организациях: на локальной БД, в CI, на стенде (`*-staging.arenasoldout.com`) и, по решению владельца от 01.11, **на проде в организации #1 ABH TEAM** (Stripe test) и в песочницах «UX Hermes <дата>» сценария «с нуля». Карты — только тестовые карты провайдера в тестовом режиме. Письма вне прода по умолчанию уходят в лог (`EMAIL_MODE=log`). Реальная доставка допустима только на ящики команды с `+`-алиасами.

### C.1 Организации и пользователи

| Набор | Где задан | Содержимое | Для чего |
|---|---|---|---|
| `arena-seed` | `B/cmd/arena-seed/main.go` | Организации «TEST Arena Tel Aviv / Tallinn / Riga» (`test-arena-*`), площадки «TEST …» с поясами, пользователи `super@`, `admin@`, `organizer@test.arena.local` (пароль `SeedPassword` в коде фикстуры) | CI Integration, локальная БД |
| `arena-e2e-seed` | `B/cmd/arena-e2e-seed/main.go` и `*_fixture.go` (21 фикстура) | «E2E Event Center Org», событие «E2E Twin Show», `e2e-owner@`, `e2e-manager@`, `e2e-agent@test.arena.local`, Telegram id 9100000001–3 (фиктивные), покупатели `*@example.test`, чужая организация и промоутер `e2e-foreign-promoter` | `event-center-live`, бот |
| Виджет | `W/scripts/seed-palac-e2e.sql` | Palác Akropolis, 260 мест, GA «Galérie» | `widget-acceptance` |
| Go-фикстуры | Каждый integration-тест создаёт свои строки | Значения для глобально уникальных индексов (телефоны, `provider_payment_id`) рандомизировать | Integration |
| **Прод: организация #1 ABH TEAM** | slug `abhteam`, Stripe в тестовом режиме, канал «ABH TEAM OU», вебхук `/v1/payment-intents/webhook/{config_id}`; настоящих мероприятий нет | Тестовые события прогона, их заказы и билеты | Hermes F0–F10, X-01, G-13 |
| Прод: песочница «UX Hermes <дата>» | Создаётся анкетой в NEW-01, архивируется в NEW-15 | Новая организация, новый тестовый аккаунт Stripe | Сценарий «с нуля» (3.14) |
| Стенд: тестовая организация | ABH TEAM на `api-staging`, свой конфиг Stripe test | Для проверок до выката | Ручные проверки PR |
| Учётки Hermes | `.local/staging_test_accounts.md` (gitignored) | Тестовые e-mail команды с `+`-алиасами, тестовые Telegram-аккаунты; пароли не в репозитории | Hermes |

### C.2 Платежи

| Что | Где | Правило |
|---|---|---|
| Stripe-заглушка (checkout) | `W/scripts/stripe-stub.cjs`, порт 12111, `STRIPE_API_BASE_URL=http://localhost:12111/v1` | Запрещена в проде конфигом |
| Stripe-заглушка (возвраты) | `EC/scripts/live-harness.mjs`, порт 18088; сумма 777 отклоняется | Только live-тесты EC |
| PayPlus-заглушка | `W/scripts/payplus-stub.cjs`, порт 12114 | Стенд и тесты |
| Flitt | Публичная песочница (merchant из документации Flitt) или заглушка, `FLITT_API_BASE_URL` | Запрещено в проде |
| Stripe test (ABH TEAM на проде и на стенде, песочницы) | Ключи `sk_test_`/`rk_test_` тестовых аккаунтов | Разрешены владельцем 01.11. Наборы: 4242 4242 4242 4242 (успех), 4000 0000 0000 0002 (отказ), 4000 0025 0000 3155 (3-D Secure), 4000 0000 0000 9995 (недостаточно средств); срок — любая будущая дата, CVC — любые 3 цифры. Вводит Hermes своим механизмом или человек, только на `checkout.stripe.com` с плашкой Test mode. Claude Code карту не вводит. Live-ключи в тестах не используются никогда |
| AllPay, PayPlus test | Тестовые учётки провайдеров | Только на стенде |

### C.3 Почта и Telegram

| Что | Правило |
|---|---|
| Адреса в автотестах | `@test.arena.local`, `@example.test` (не доставляются) |
| SMTP в тестах | Встроенная SMTP-заглушка Go-тестов (`P/delivery/delivery_integration_test.go`). Рекомендация: добавить Mailpit в compose для SITE-07 (PR-02) |
| Реальная доставка на стенде | Только ящики команды с `+`-алиасами; адреса в `.local` |
| Telegram в автотестах | `stubTelegram` (`HS/bot_e2e_integration_test.go`), фиктивные id |
| Telegram на стенде | **Отдельный тестовый бот** (токен прода ставить нельзя: два поллера дают 409), тестовые аккаунты владельца; привязку снимать после прогона (`bot_telegram_links.revoked_at`) |
| Бот продаж на стенде | Тестовый чат; подписки — строки `sales_notification_subscriptions` |

### C.4 Схемы залов

| Схема | Где | Статус |
|---|---|---|
| Синтетическая (24 места) | `VG/arena_export/testdata/synthetic_authoring.svg` + `.expected.json` | В репозитории, CI |
| Hunter College (907), Aribau (1166) | Приватные XLSX и эталоны (вне git) | 9 тестов пропускаются в CI |
| Calle Ruiz Covers 6 (JPEG и XLSX, 312 мест) | `VSG/` (неотслеживаемые) | Ручная сборка |
| BIGHALL (векторный PDF) | `VSG/` | Только диагностика |
| Palác Akropolis (sbt) | `CB/testdata/wp/svg/palac_akropolis.sbt.svg` | Golden для импорта |

### C.5 Внешние получатели, которые надо отключить для тестов

- **MACS.** Билеты тестовых покупок ABH TEAM уходят в боевой MACS. Владелец согласился на это 24.10 и сам удаляет лишние мероприятия в админке MACS. Других внешних получателей у ABH TEAM быть не должно: перед прогоном сверить `webhook_subscribers` и подписки бота продаж (PAY-19).
- **Прогоны на проде** разрешены решением 01.11 только в ABH TEAM и песочницах «UX Hermes». Любые действия с письмами покупателям — только на мероприятиях своего прогона. Письма — только на ящики команды.

---

## D. Текущие пробелы

### D.1 Можно считать проверенным (по существующим тестам, при условии зелёного CI)

- Изоляция организаций на уровне API: обход всех `{org_id}`-маршрутов и плоских маршрутов, стражи дрейфа, роли владельца, менеджера и агента через реальный роутер (IAM-01..04).
- Платёжные вебхуки: валидная и невалидная подпись, отдельные секреты организаций, дубль AllPay (SEC-01 частично, PAY-08).
- Основной денежный цикл на уровне Go: hosted checkout Stripe, Flitt, AllPay, PayPlus, бесплатный заказ, окно оплаты, возвраты через провайдера и из кабинета, частичный возврат (PAY-01 Go-часть, PAY-06, PAY-07, PAY-11..14).
- Конкурентность: одно место, GA-квоты, дедлок GA-hold, промокоды, истечение hold (ORD-02, ORD-05, ORD-08, CAT-07).
- Одно письмо на заказ, повторная отправка, PDF с EAN-13 и шрифтом UTF-8 (ASY-06, SITE-22 без физического скана).
- Bil24-шлюз: харнесс и golden-ответы, доставка вебхуков сайту с `system_id` (ASY-02, ASY-12).
- Перенос и отмена сеанса с письмами (CAT-13), статус продаж и «сообщить о продажах» на бэкенде (CAT-04).
- Event Center на живом бэкенде: мастер, афиша, статус, категории, сеансы, команда, возвраты, пригласительные, промокоды, ссылки, мини-приложение (~170 тестов `event-center-live`), но этот джоб не блокирует сборку образа.
- Бот: приглашение, мастер, загрузка SVG-схемы, сеансы, возвраты, команда, черновики, анкета (~65 e2e с заглушкой Telegram).
- Генератор: QUARANTINE, запрет выдумывать места, source-only, контракт с `ImportSVG` на синтетическом зале, лимиты сервиса.

### D.2 Не покрыто или покрыто частично (по убыванию риска)

1. **CI-гейт:** event-center, tickets-page, shop-page, rich-editor и генератор не блокируют `build-and-push` (G-02).
2. **Браузерный путь покупки целиком** (виджет → оплата → возврат → paid → PDF → письмо) и экраны failed/cancelled/expired на реальном бэкенде: SITE-07, SITE-08. Тест «full Stripe purchase» в `W/tests/palac-akropolis-real.e2e.ts:616` пропускается.
3. **Выдача билетов как задача:** у `P/issuejob` и `P/convertjob` нет своих тестов на повтор и падение посередине (ASY-05).
4. **Билет покупателя по ссылке** `/public/checkout/{token}/tickets/{id}/pdf` и live-recover: тестов нет (ORD-09, ORD-10).
5. **Вебхуки:** нет тестов просроченной метки времени и повтора для Stripe и Flitt; нет негативных тестов подписи для `/v1/refunds/webhook`, `/v1/billing/stripe/webhook`, `/v1/billing/bank/webhook/{provider}` (SEC-01, SEC-02).
6. **Двойной `checkout/start` у публичного фида** (ORD-04) и асинхронная оплата Stripe (PAY-02), истечение сессии Stripe (PAY-03).
7. **Отчёты после события:** `report.deliver` и `event.generate_report` ставятся в очередь (`HS/hreports/report_delivery_enqueue.go`), но обработчики не регистрируются в `B/cmd/arena-worker`. Это функциональный дефект или мёртвая функция (ASY-08).
8. **Admin:** нет ни одного браузерного или живого теста загрузки SVG-схемы, привязки к сеансу и создания организации (ADM-02, ADM-03, ADM-05).
9. **Hosted-страница и магазин:** нет браузерных тестов, axe и мобильной вёрстки (SITE-01, SITE-13, SITE-14).
10. **Разделение токенов двух событий в одном origin** проверено только unit-тестами (SITE-09).
11. **Медиа:** лимит 32 МиБ, подделка и просрочка подписи на HTTP-уровне (SEC-04); полнота egress для всех исходящих URL (SEC-05); сквозная проверка «секреты не в ответах и логах» (SEC-06).
12. **Бот:** негативные файлы афиши в e2e, XLSX и PDF вместо схемы, повтор `update_id`, лимит в e2e, глобальная проверка `callback_data` (BOT-03, BOT-05, BOT-12..14).
13. **Генератор:** реальные залы в CI пропускаются; provenance сверяется по длине, а не по байтам; нет визуальной регрессии; PDF, JPEG, кривые ряды, категории, проданные места и служебные зоны не реализованы (VSG-01, VSG-05, VSG-11..17).
14. **Задачи без тестов:** `tier.chain_sweep`, пакеты `P/txretry`, `HS/hinventory`, `HS/hreports`, `HS/hreconciliation`, `HS/hwordpress`, `HS/hcontact`, `HS/hapikeys`.
15. **Каталог:** матрица статусов сеанса, создание городов и площадок, удаление события (CAT-02, CAT-10, CAT-14).
16. **Аутентификация:** повтор refresh и отзыв, Redis-хранилище сессий, impersonation в работе (AUTH-01, AUTH-09).
17. **WP-плагин `arena-events`** без PHP-тестов; плагин Bil24 у сайтов вне репозитория (SITE-17, SITE-18).
18. **Stripe Connect, оплата пакетов биллинга картой, Stripe и Flitt в магазине** (PAY-17, PAY-18, SITE-19).
19. **Hermes:** навык и бэклог UXH не закоммичены (репозиторий публичный), полного прогона по боту B1–B17 нет.
20. **Новая организация с нуля до первой платной продажи** ни разу не проходилась. Подключение Stripe новой организацией не проверялось никогда. У владельца нет экрана подключения платежей (только API). Stripe Connect в проде не смонтирован. Вебхук в Stripe заводится вручную (3.14, NEW-01..NEW-15).
21. **Защита от писем посторонним вне прода:** белого списка получателей в коде нет, только `EMAIL_MODE=log|smtp` (PAY-19).
22. **Восстановление из бэкапа** не автоматизировано; тест проверяет только наличие скриптов (ASY-18).

### D.3 Невозможно проверить без доступа или ручного участия

- Оплата тестовыми картами Stripe на проде (ABH TEAM, песочницы): карту вводит Hermes или человек. Живые песочницы AllPay, PayPlus и Flitt.
- Регистрация нового аккаунта Stripe и выдача ключей (NEW-06, NEW-09): это сайт Stripe, нужен тестовый e-mail команды и подтверждение по почте.
- Одобрение заявки оператором (NEW-02).
- Плагины Bil24 на сайтах Lampyris и Vino&Co (внешние репозитории, стейджинги на `staging-1`, который поднимается из снапшота по требованию).
- Физическое сканирование PDF-билета сканером и камерой (SITE-22), рендер писем в реальных почтовых клиентах (SITE-23).
- Живой вызов модели чтения афиши (ключ модели, стоимость).
- Приватные схемы залов (Hunter, Aribau, клиентские XLSX и JPEG) и визуальная оценка читаемости человеком.
- Настройки branch protection на GitHub, env прода и стенда (Dokploy), Cloudflare (WAF, кеш), DNS.
- Telegram: второй тестовый аккаунт и отдельный тестовый бот на стенде; одобрение заявок владельцем.
- MACS: боевой получатель (нужен stub или решение владельца).

---

## E. План внедрения (короткие PR-пакеты, сначала P0)

Каждый пакет — отдельный PR от `master`, только тесты и CI, без изменения поведения продукта (кроме PR-05, где сначала нужно решение владельца).

| Пакет | P | Изменяемые области | Тесты | Риски | Критерий готовности |
|---|---|---|---|---|---|
| PR-01 «CI-гейт» | P0 | `.github/workflows/ci.yml`: добавить в `needs` у `build-and-push` джобы `event-center-build`, `event-center-live`, `tickets-page-build`, `shop-page-build`, `rich-editor-build`, `venue-svg-generator`; path-gated джоб генератора должен давать «success», когда пропущен | G-01, G-02 | Нестабильный (flaky) live-тест EC начнёт блокировать выкаты; генератор пропускается фильтром путей | Сборка образа ждёт все джобы; три подряд зелёных прогона на master |
| PR-02 «Полная покупка в браузере» | P0 | `W/tests/` (новый real-спек), `W/scripts/stripe-stub.cjs` (страница `/pay/:id` отправляет подписанный `checkout.session.completed`), SMTP-заглушка (Mailpit) в `widget-acceptance` | SITE-07, SITE-08, PAY-01 браузерная часть, PAY-03 | Секрет вебхука заглушки должен совпадать с конфигом сида; время в CI | Тест проходит путь до экрана paid, скачивает PDF, находит одно письмо; failed/expired тоже |
| PR-03 «Вебхуки: негативы» | P0 | `HS/` integration-тесты, `B/internal/domain/payments` | SEC-01 (просроченная метка, повтор для Stripe и Flitt), SEC-02 (refunds, billing stripe, bank) | Нужны тестовые секреты в фикстурах | Каждая точка вебхука имеет тесты: верно, неверно, пусто, старо, повтор |
| PR-04 «Выдача билетов и checkout» | P0 | `P/issuejob`, `P/convertjob` (тестовые файлы), `HS/` integration | ASY-05, ORD-04, ORD-09, ORD-10 | Фикстуры должны убирать `worker_jobs` (правило AGENTS.md) | Повтор задачи и двойной checkout/start не дают дублей; PDF по чужому `ticket_id` даёт 404 |
| PR-05 «Отчёты после события» | P0 (решение) | Сначала решение владельца: подключить `reportdelivery`/`reporting` в `B/cmd/arena-worker` или убрать функцию и маршрут | ASY-08 | Изменение поведения, не только тесты | Решение записано; функция работает с тестом или удалена |
| PR-06 «Изоляция через UI и токены» | P0 | `EC/tests/smoke/live/` (новый спек: чужие id в URL), `W/tests/` (два события в одном origin, чужой event_id в фиде), `HS/` (нет ПДн в публичном JSON) | X-04, SITE-09, SITE-16 | Нужна вторая организация в `arena-e2e-seed` (чужая уже есть) | Все подстановки дают 404 или отказ без данных |
| PR-07 «Admin: схемы» | P0 | `AW/tests/` (Playwright против live arena-api по образцу `EC/scripts/live-harness.mjs`), CI-джоб | ADM-01, ADM-02, ADM-03, VSG-19 | Новый live-харнесс для admin; время CI | Загрузка корректного и ошибочного SVG, привязка, виджет показывает места генератора |
| PR-08 «Медиа и egress» | P1 | `HS/hmedia`, `P/egress` (тесты), проверка generic subscribers, `hwordpress`, импорта клиентов | SEC-04, SEC-05, SEC-06 | Можно найти реальный пробел; тогда это отдельный fix-PR без деталей в публичном описании | Лимит, подпись, egress и отсутствие секретов покрыты |
| PR-09 «Бот: файлы и транспорт» | P1 | `HS/bot_e2e_*` (PDF и большая афиша, XLSX и PDF вместо схемы, лимит включён), `EB/` (табличная проверка `callback_data`, повтор `update_id`) | BOT-03, BOT-05, BOT-12, BOT-13, BOT-14 | Заглушка записывает только текст; ждать `callCount` | Все отказы проверены текстом; ни одна клавиатура не длиннее 64 байт |
| PR-10 «Hosted-страница и магазин в браузере» | P1 | `TP/` и `SP/` (Playwright + axe + 375 px), CI-джобы | SITE-01, SITE-02, SITE-13, SITE-14, SITE-19 | Нужен живой API или реалистичный mock | Смоук, axe без нарушений AA, мобильные снимки |
| PR-11 «Генератор: целостность и визуал» | P1 | `VG/service/tests` (sha256 = байты), `VG/m1_product/tests` (provenance.json), визуальная регрессия SVG → PNG, подключение node-тестов `VSG/orchestration` или их копии | VSG-05, VSG-17, VSG-18 | Нестабильность растеризации (фиксировать шрифты и движок) | Хэш сверяется с байтами; diff изображений в CI |
| PR-12 «Каталог, задачи без тестов» | P1 | `P/tierchain`, `P/txretry`, `HS/hinventory`, матрица статусов сеанса, города и площадки, удаление события | CAT-02, CAT-06, CAT-10, CAT-14, ASY-07 | — | У каждого пакета есть свой тестовый файл |
| PR-13 «Auth: сессии» | P1 | `P/redissession`, `HS/` integration | AUTH-01 (повтор и отзыв), AUTH-09, AUTH-11 | Redis в CI уже есть | Повтор refresh и выход отзывают; impersonation ограничена и в audit |
| PR-14 «Hermes: функциональный прогон» | P1 | Навык Hermes `functional-run.md` (закоммитить в `ops/hermes/`, репозиторий уже приватный), прогон на проде в ABH TEAM | HRM-01..06, X-01, G-13 | Письма и MACS (C.5) | F0–F10 пройдены без S1/S2, журнал в `docs/testing` |
| PR-17 «Hermes: с нуля до первой платной продажи» | P0 | **Файл написан 01.11:** `ops/hermes/skills/arena-ux-tester/references/zero-to-paid.md` (шаги N0–N13, таблица соответствия NEW-кейсам; навык 2.2.0). Он опирается на Z1–Z9 из `clean-run.md` и добавляет подключение Stripe по V1 и V3, четыре тестовые карты, возвраты из бота, веба и кабинета Stripe, ошибку вебхука, замеры `connect-metrics.md` и уборку. Осталось: залить на acer-server (сверив md5 с серверной копией) и запустить словом «старт прогона с нуля до оплаты» | NEW-01..NEW-15, X-05, G-17 | Нужны новый тестовый e-mail и аккаунт Stripe; одобряет оператор | Прогон пройден, таблица сравнения вариантов V1/V3 с цифрами и рекомендацией в отчёте |
| PR-18 «Подключение Stripe владельцем» | P1 (решение) | По итогам NEW-14 владелец выбирает V2 (экран в EC и боте поверх существующего API) + V6 (автосоздание вебхука) или V4 (Connect). Затем отдельная спецификация и автотесты: создание конфига владельцем, проверка, предупреждения, изоляция | NEW-07..NEW-10 как автотесты; PAY-17 | V4 меняет платёжную модель | Владелец подключает Stripe без оператора, тест проходит в `event-center-live` |
| PR-16 «Получатели вне прода» | P0 (решение) | Решение владельца: белый список доменов получателей (или перенаправление всех писем на один ящик) при `APP_ENV != production`; конфиг в `P/config`, проверка в `P/delivery` и `B/internal/adapters/email`; запрет в проде | PAY-19 | Изменение поведения стенда | На стенде письмо на адрес вне списка не уходит и пишется в лог; в проде настройка отклоняется |
| PR-15 «Реальные залы в CI» | P2 | Зашифрованные фикстуры или приватный раннер | VSG-01, VSG-10, VSG-20 | Клиентские данные; решение владельца | 9 пропущенных тестов выполняются |

---

## Опорные файлы (ссылки)

CI и конфигурация:
- [.github/workflows/ci.yml](../../.github/workflows/ci.yml): джобы и `needs` у `build-and-push` (строка 1078)
- [.github/workflows/accessibility.yml](../../.github/workflows/accessibility.yml), [.github/workflows/load-test.yml](../../.github/workflows/load-test.yml)
- [docker-compose.yml](../../docker-compose.yml): сервисы, `venue_gen`
- [apps/backend/internal/platform/config/config.go](../../apps/backend/internal/platform/config/config.go): production-запреты
- [apps/backend/openapi/openapi.yaml](../../apps/backend/openapi/openapi.yaml)

Сиды и заглушки:
- [apps/backend/cmd/arena-seed/main.go](../../apps/backend/cmd/arena-seed/main.go)
- [apps/backend/cmd/arena-e2e-seed/main.go](../../apps/backend/cmd/arena-e2e-seed/main.go)
- [apps/widget/scripts/stripe-stub.cjs](../../apps/widget/scripts/stripe-stub.cjs), [apps/widget/scripts/seed-palac-e2e.sql](../../apps/widget/scripts/seed-palac-e2e.sql)
- [apps/event-center/scripts/live-harness.mjs](../../apps/event-center/scripts/live-harness.mjs)

Backend (ключевые тесты):
- [HS/org_isolation_sweep_integration_test.go](../../apps/backend/internal/platform/httpserver/org_isolation_sweep_integration_test.go), [HS/org_isolation_sweep_table_test.go](../../apps/backend/internal/platform/httpserver/org_isolation_sweep_table_test.go), [HS/org_isolation_flat_sweep_integration_test.go](../../apps/backend/internal/platform/httpserver/org_isolation_flat_sweep_integration_test.go)
- [HS/manager_permissions_0129_integration_test.go](../../apps/backend/internal/platform/httpserver/manager_permissions_0129_integration_test.go)
- [HS/hosted_checkout_integration_test.go](../../apps/backend/internal/platform/httpserver/hosted_checkout_integration_test.go), [HS/webhook_widget_completion_integration_test.go](../../apps/backend/internal/platform/httpserver/webhook_widget_completion_integration_test.go)
- [HS/flitt_checkout_integration_test.go](../../apps/backend/internal/platform/httpserver/flitt_checkout_integration_test.go), [HS/allpay_checkout_integration_test.go](../../apps/backend/internal/platform/httpserver/allpay_checkout_integration_test.go), [HS/payplus_checkout_integration_test.go](../../apps/backend/internal/platform/httpserver/payplus_checkout_integration_test.go)
- [HS/refund_engine_integration_test.go](../../apps/backend/internal/platform/httpserver/refund_engine_integration_test.go)
- [HS/hcheckout/hold_mutation_concurrency_integration_test.go](../../apps/backend/internal/platform/httpserver/hcheckout/hold_mutation_concurrency_integration_test.go), [HS/hcheckout/reservation_expire_sweep_integration_test.go](../../apps/backend/internal/platform/httpserver/hcheckout/reservation_expire_sweep_integration_test.go)
- [HS/session_change_http_integration_test.go](../../apps/backend/internal/platform/httpserver/session_change_http_integration_test.go)
- [P/delivery/order_letter_integration_test.go](../../apps/backend/internal/platform/delivery/order_letter_integration_test.go)
- [B/internal/domain/payments/provider_134_test.go](../../apps/backend/internal/domain/payments/provider_134_test.go)
- [B/cmd/arena-worker/main.go](../../apps/backend/cmd/arena-worker/main.go), [B/cmd/arena-worker/outbox_legs_test.go](../../apps/backend/cmd/arena-worker/outbox_legs_test.go)
- [P/reportdelivery/handler.go](../../apps/backend/internal/platform/reportdelivery/handler.go) и [HS/hreports/report_delivery_enqueue.go](../../apps/backend/internal/platform/httpserver/hreports/report_delivery_enqueue.go) (пробел ASY-08)
- [B/internal/migrations/migrations_head_test.go](../../apps/backend/internal/migrations/migrations_head_test.go)
- [B/tests/staticanalysis/](../../apps/backend/tests/staticanalysis/)
- [CB/harness_test.go](../../apps/backend/tests/compat/bil24/harness_test.go), [CB/wp_roundtrip_508_integration_test.go](../../apps/backend/tests/compat/bil24/wp_roundtrip_508_integration_test.go)

Бот:
- [HS/bot_e2e_integration_test.go](../../apps/backend/internal/platform/httpserver/bot_e2e_integration_test.go) (`stubTelegram`)
- [EB/plan_upload.go](../../apps/backend/internal/platform/eventbot/plan_upload.go) (`planMaxBytes`), [EB/wizard_bot.go](../../apps/backend/internal/platform/eventbot/wizard_bot.go) (`posterMaxBytes`), [EB/ratelimit.go](../../apps/backend/internal/platform/eventbot/ratelimit.go), [EB/serial.go](../../apps/backend/internal/platform/eventbot/serial.go)

Фронтенды:
- [EC/tests/smoke/live/](../../apps/event-center/tests/smoke/live/), [EC/tests/smoke/r1.spec.ts](../../apps/event-center/tests/smoke/r1.spec.ts), [EC/src/parity/parity.test.ts](../../apps/event-center/src/parity/parity.test.ts)
- [AW/tests/admin-bootstrap/mobile.spec.ts](../../apps/admin-web/tests/admin-bootstrap/mobile.spec.ts), [AW/src/routes/venueSeatingPlans.tsx](../../apps/admin-web/src/routes/venueSeatingPlans.tsx)
- [W/tests/palac-akropolis-real.e2e.ts](../../apps/widget/tests/palac-akropolis-real.e2e.ts) (строка 616: пропуск полной покупки), [W/src/lib/store.ts](../../apps/widget/src/lib/store.ts)
- [TP/src/lib/render.ts](../../apps/tickets-page/src/lib/render.ts), [SP/src/lib/api.ts](../../apps/shop-page/src/lib/api.ts)

Генератор:
- [VG/service/app.py](../../apps/venue-svg-generator/service/app.py), [VG/arena_export/exporter.py](../../apps/venue-svg-generator/arena_export/exporter.py), [VG/conftest.py](../../apps/venue-svg-generator/conftest.py)
- [B/internal/domain/seating/venue_gen_authoring_test.go](../../apps/backend/internal/domain/seating/venue_gen_authoring_test.go)
- Внешний репозиторий: `C:\Projects\Venue_SVG_Generator\PRODUCT_SPEC.md`, `ROADMAP.md`, `pdf_diagnostics\diagnose_pdf.py`, `orchestration\`

Hermes (только локально, не в git):
- `ops/hermes/skills/arena-ux-tester/references/{functional-run,features-bot,features-web,manager,clean-run}.md`
- `docs/testing/ux_hermes_backlog_2026-10-24_ru.md`; в git — `docs/testing/hermes_shop_*`

---

## Проверка полноты плана

| Система из задания | Разделы плана |
|---|---|
| 1. Backend и API (аутентификация, организации и роли, площадки, события, сеансы, билеты и категории, заказы, возвраты, контракты, фоновые задачи, БД, audit и наблюдаемость, безопасность) | 3.1–3.7 (AUTH, IAM, SEC, CAT, ORD, PAY, ASY), A, B |
| 2. Event Center | 3.8 (EC-01..23), X-01, X-03..05 |
| 3. Arena Admin | 3.9 (ADM-01..11), VSG-19 |
| 4. Сайты продаж и магазины | 3.10 (SITE-01..23), X-02, X-06 |
| 5. Платежи и заказы | 3.5, 3.6, PAY-09 (запрет реальных платежей), C.2 |
| 6. Telegram-бот и Hermes | 3.11 (BOT-01..20, HRM-01..06), A-15..18 |
| 7. Venue SVG Generator | 3.12 (VSG-01..20), A-20..21 |
| Дополнение 01.11: новая организация с нуля и подключение Stripe | 3.14 (NEW-01..15), A-30, G-17, PR-17, PR-18 |

Каждый канал матрицы A сослан хотя бы на один кейс. Компоненты без конкретного теста или evidence помечены ✗ или ⊘ и не считаются проверенными.

---

## Executive summary

**Что уже можно считать функционально проверенным** (существуют тесты; актуальный зелёный прогон CI на `23b7d49e` нужно подтвердить):
- изоляция организаций и роли на уровне API;
- денежный цикл на уровне Go (Stripe hosted, Flitt, AllPay, PayPlus, бесплатные заказы, возвраты, в том числе частичные);
- конкурентность мест и GA-остатков;
- одно письмо на заказ и PDF с EAN-13;
- Bil24-шлюз и вебхуки сайтам;
- перенос и отмена сеансов с письмами;
- Event Center на живом бэкенде (~170 тестов);
- Telegram-бот с заглушкой Telegram (~65 e2e);
- защитные правила генератора (QUARANTINE, никаких выдуманных мест, контракт с `ImportSVG` на синтетике).

**Пять самых опасных непроверенных сценариев:**
1. Сборка образа не ждёт тестов Event Center, hosted-страницы, магазина и генератора. Красный live-тест EC не остановит выкат (G-02).
2. Полный путь покупателя в браузере (оплата → возврат на страницу → экран «оплачено» → PDF → письмо) и экраны отказа и истечения против реального бэкенда не проверяются никогда (SITE-07, SITE-08).
3. Повтор задачи выдачи билетов, двойной `checkout/start` и выдача PDF по ссылке покупателя (в том числе с чужим `ticket_id`) не имеют тестов (ASY-05, ORD-04, ORD-10).
4. Вебхуки: нет проверки просроченной метки времени и повтора для Stripe и Flitt, нет негативных тестов подписи у вебхуков возвратов и биллинга (SEC-01, SEC-02).
5. Admin не имеет ни одного браузерного теста загрузки SVG-схемы и привязки к сеансу. Генератор проверен в CI только на синтетическом зале из 24 мест, а backend его не вызывает (ADM-02, VSG-01, VSG-07).

Отдельно найдены:
- функциональный дефект: задачи `report.deliver` и `event.generate_report` ставятся в очередь, но в воркере нет их обработчиков (ASY-08);
- пробел защиты тестовых контуров: вне прода нет белого списка получателей писем (PAY-19, PR-16).

**Дополнение 01.11 — сценарий «с нуля».** Новая организация ещё ни разу не доходила до первой платной продажи, а подключение Stripe новой организацией не проверялось никогда. По коду сейчас работает только путь «организатор отдаёт ключи оператору, оператор вставляет их в admin, вебхук в Stripe заводится руками» (V1). У владельца экрана подключения нет, Stripe Connect в проде не подключён. Сценарий NEW-01..NEW-15 (раздел 3.14) проходит путь целиком и сравнивает варианты подключения по числу действий и времени. По его итогам владелец выбирает, что строить: экран подключения с автосозданием вебхука или Connect.

**Следующий P0-пакет:** PR-01 «CI-гейт». Это одна правка `ci.yml`, она сразу превращает уже существующие ~1500 тестов фронтендов и генератора в настоящий барьер. Следом идёт PR-02 «Полная покупка в браузере».

**Что потребует вашего ручного участия:**
- новый тестовый e-mail и регистрация тестового аккаунта Stripe для этой песочницы; одобрение её заявки;
- решение по отчётам после события (PR-05) и по белому списку получателей писем на стенде (PR-16);
- запуск прогонов Hermes словом владельца (функциональный в ABH TEAM, «с нуля» в песочнице);
- поднятие `staging-1` и прогон покупок на стейджингах Lampyris и Vino (X-02);
- тестовый Telegram-бот и второй аккаунт для стенда;
- физический скан билета и проверка писем в почтовых клиентах;
- доступ к приватным схемам залов (или решение о зашифрованных фикстурах);
- проверка branch protection на GitHub и env прода и стенда в Dokploy;
- дать слово на коммит плана и навыка Hermes в репозиторий (он уже приватный).
