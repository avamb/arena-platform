# Прогон P0-1: merge-гейты G-01, G-03…G-10

Дата: 2026-11-01. Исполнитель: Claude Code (обычный вход, без API-runner).
План: `docs/testing/full_functional_test_plan_2026-11-01.md`, редакция 2, раздел B.
Формат: раздел B.1 плана.

**Редакция 2 протокола (14:30, после ревью).** Исправлены три вывода:
1. G-01. Шаги генератора, которые CI пропустил, прогнаны отдельно на том же коммите: pytest на Python 3.12, сборка образа, оба smoke. Только после этого G-01 записан как пройденный.
2. ORD-02. Одно правило для всех обязательных шагов (раздел 2, «Правило evidence»). Часть ORD-02 через виджет прогнана локально и прошла.
3. Node. Убрано утверждение, что Node 22 не повлиял на результат. Доказано только одно: выбранные тесты прошли на Node 22.13.1.

**Вывод: гейты НЕ пройдены.** Ни один тест продукта не упал: 0 FAIL, 0 BLOCKED. Но в шести гейтах из девяти есть шаги в статусе `NOT RUN`, потому что для них нет теста. По правилу приёмки (раздел B) `NOT RUN` блокирует гейт. Пройдены G-01, G-03 и G-10.

## 1. Манифест версий

Сверено до первого теста (13:39) и после последнего (14:28). Версии за время прогона не менялись.

| Компонент | Как узнали | Значение |
|---|---|---|
| Код под тестом | `git rev-parse origin/master` до и после; отдельный detached-worktree на этом коммите | `23b7d49e13037ad8f91ec60d33d234487c653caf` (Merge PR #342, session-sales-state) |
| arena-api | Собран локально из коммита выше (`commit=local` в логах). Для справки, только GET `/v1/info`: прод `23b7d49e13037ad8f91ec60d33d234487c653caf`, стенд `b8ce1678` | Прод = коммит под тестом. Стенд на другом коммите, в прогоне не участвовал |
| arena-worker | Отдельно не запускался: обработчики задач и диспетчер outbox работают внутри тестов | — |
| arena-bot | Отдельно не запускался: e2e-тесты поднимают бота в процессе теста с заглушкой Telegram | — |
| Голова миграций | `select max(version_id) from schema_migrations` на базах прогона | `183` (`0183_session_sales_state.sql`) |
| Event Center, widget, rich-editor | Исходники коммита под тестом, `npm ci` по lock-файлам | Тот же коммит |
| venue_gen | Образ собран локально из `apps/venue-svg-generator` коммита под тестом | `arena-venue-gen:p0-23b7d49e` |
| admin-web, tickets-page, shop-page | Локально не гонялись | — |
| Навык Hermes | Не применимо | — |
| Окружение | Локально: Windows 11 Pro 10.0.26200; Go 1.26.9 (как в CI); Node 22.13.1 (в CI Node 20, см. F-06); Python 3.12.15 в контейнере `python:3.12-slim` (как в CI); Docker 29.8.2; PostgreSQL 17.10 в контейнере `arena_postgres`, порт 45432 | Базы прогона: `arena_p0_20261101` (Go-гейты), `arena_p0_20261101_eclive` (live-спеки event-center), `arena_p0_20261101_widget` (widget-acceptance). Общая база `arena` не тронута |

Условия прогона:
- все базы созданы пустыми, `arena-migrate up` → 183:
  - Go-гейты и widget-acceptance: затем `arena-seed` (3 организации, 9 площадок, 4 пользователя, 3 канала, 1 событие) — `evidence/migrate_seed.txt`; для widget-acceptance ещё `apps/widget/scripts/seed-palac-e2e.sql`, как в CI;
  - live-спеки event-center: остальное харнесс сделал сам через `arena-e2e-seed`;
- Go-тесты: `go test -tags integration -count=1 -p 1 -timeout 30m -v <пакет> -run <regex>`, по одному запуску на гейт и пакет, последовательно. Скрипт: `.p0run/run_gates.sh` (в репозиторий не кладётся). Код возврата каждого запуска записан отдельно — `evidence/go_summary.txt`;
- `JWT_SIGNING_SECRET` — значение соответствующего CI-джоба, `TESTCONTAINERS_RYUK_DISABLED=true` — как в CI;
- `arena_worker` НЕ останавливался: он читает базу `arena`, а не базы прогона, и строки `outbox_events` прогона забрать не может;
- внешние провайдеры не вызывались: только заглушки внутри тестов и Stripe-заглушки (`apps/widget/scripts/stripe-stub.cjs`) на `127.0.0.1:18188` (харнесс event-center) и `127.0.0.1:12211` (widget-acceptance);
- в прод и на стенд — только GET `/v1/info`;
- testcontainers поднял локальные контейнеры `postgres:17-alpine` для `TestDelivery_*` и убрал их сам: после прогона новых контейнеров нет. Контейнер smoke генератора удалён сразу после проверки.

## 2. Результат по каждому ID плана

Время — по Мадриду (CET, UTC+1). Evidence — пути внутри этой папки. В выдержках только имена тестов и итоговые строки.

**Правило evidence.** Обязательный шаг получает `PASS` только по прогону на коммите `23b7d49e`, где видно имя теста и его результат. Прогон в CI на том же SHA засчитывается, если его лог показывает имя и результат теста. На этом коммите так не получилось с двумя джобами:
- `widget-acceptance` печатает только точки и итог «23 passed, 1 skipped»; отчёт Playwright в артефакты не попал;
- в `venue-svg-generator` все шаги пропущены фильтром изменений.

Поэтому оба прогнаны локально. Деления на «обязательные» и «необязательные» строки внутри кейса больше нет: всё, что перечислено в кейсе гейта, обязательно.

| ID плана | Шаг исполнителя | Статус | Время (Мадрид) | Evidence | Находка | Комментарий |
|---|---|---|---|---|---|---|
| G-01 | Статус CI коммита через `gh`, только чтение | PASS | 13:39 | `evidence/g01_ci_jobs.txt`: run [41262841049](https://github.com/avamb/arena-platform/actions/runs/41262841049), conclusion `success`, все 15 джобов `success` | F-04, F-05 | Повторы CI не запускались. Шаги генератора CI пропустил (`evidence/g01_venue_gen_steps.txt`), они закрыты строкой ниже |
| G-01 / venue-svg-generator | Шаги CI-джоба локально на том же коммите: pytest, сборка образа, smoke CLI, smoke `/healthz` | PASS | 14:24 | `evidence/vsg_local.txt`: `48 passed, 10 skipped`; `docker build` EXIT 0; `python -m m1_product --help` EXIT 0; `GET /healthz` → `{"status":"ok"}` | F-04 | 10 пропусков заложены в сам набор: этим тестам нужны приватные данные клиентов, которых нет в репозитории. В CI они пропускаются так же |
| IAM-01 | G-03: обход `{org_id}`-маршрутов и страж таблицы | PASS | 13:43–13:44 | `evidence/g03_hs.txt`: `TestOrgIsolationSweep_*` (3), `TestOrgIsolationCoverage_EveryOrgChildRouteIsSwept`, `ok …/httpserver` | — | — |
| IAM-02 | G-03: плоские маршруты | PASS | 13:43–13:44 | `evidence/g03_hs.txt`: `TestOrgIsolationFlatSweep_*` (5), `TestOrgIsolationFlatRoutes_EveryFlatRouteIsClassified`, `TestRowOrgAccess_DecisionTable`, `TestRefundOrgIsolation_FlatRoutesStayInsideTheOrganization`, `TestOrgRead_EventRoutesStayInsideTheOrganization` | — | — |
| IAM-03 | G-03: Go-часть, реальный роутер, JWT только с membership | PASS | 13:43–13:44 | `evidence/g03_hs.txt`: `TestManagerPermissions0129_OrganizerPassesEveryEventCenterGate` | — | — |
| IAM-03 | G-03: EC live `events-three-roles.spec.ts` | PASS | 13:53 | `evidence/ec_live_3specs.txt`: тесты 1–3 `ok`, итог `18 passed` | F-06 | Харнесс `live-harness.mjs`, своя база, порты 18186/18190/5277/18188 |
| IAM-04 | G-03: `X-Admin-Reason` суперадмина | PASS | 13:43–13:44 | `evidence/g03_hs.txt`: `TestAB12_SuperadminOrgAccessRequiresRolePermissionReasonAndAudit`, `TestSEC1_AdminOrgRoutesRefuseOwnersAndKeys`, `TestSEC1_SuperadminKeepsAdminOrgRoutes` | — | — |
| SEC-01 / Stripe | G-04: верная подпись, чужой секрет, пустой заголовок, нет метки, нет v1, подмена тела, просроченная метка (unit); две организации и чужой платёж (HTTP) | PASS | 13:44–13:45 | `evidence/g04_domain_payments.txt`: 21 тест `TestPayment134_*`, в т. ч. `…_StripeSignature_ExpiredTimestamp`; `evidence/g04_hs.txt`: `TestHostedCheckout_TwoOrgsUseTheirOwnWebhookSecrets`, `…_ConfigRoute_RejectsAnotherOrgsSignature`, `…_ConfigRoute_ForeignPaymentIsAcknowledged`, `…_ConfigRoute_ForeignOrgIntentIsNotOurs` | F-01 | — |
| SEC-01 / Stripe | Метка вне окна даёт 401 на HTTP-маршруте | NOT RUN | — | — | F-01 | Теста на уровне HTTP нет. Unit-проверка верификатора есть и прошла (строка выше) |
| SEC-01 / Stripe | Повтор события после терминального состояния | NOT RUN | — | — | — | Теста нет (✗ в SEC-01a) |
| SEC-01 / Flitt | Подпись в теле, «мусор» после JSON, истёкший заказ, все валюты, две организации | PASS | 13:44–13:45 | `evidence/g04_flitt_adapter.txt` (5 тестов `TestSign_*`, `TestVerifyCallback_*`, `TestLooksLikeCallback`); `evidence/g04_hs.txt`: `TestFlitt_*` (7) | — | — |
| SEC-01 / Flitt | Чужой `merchant_id` | NOT RUN | — | — | — | Теста нет (✗ в SEC-01a) |
| SEC-01 / Flitt | Повтор `approved` | NOT RUN | — | — | — | Теста нет (✗ в SEC-01a) |
| SEC-01 / AllPay | Подпись, повтор, несовпадение суммы, две организации | PASS | 13:45 | `evidence/g04_allpay_adapter.txt` (4); `evidence/g04_hs.txt`: `TestAllPay_*` (7) | — | — |
| SEC-01 / AllPay | Чужой ключ на чужом конфиге | NOT RUN | — | — | — | Теста нет (✗ в SEC-01a). `TestAllPay_TwoOrganizationsAreIsolated` прошёл, но план не считает его покрытием этой строки |
| SEC-01 / PayPlus | Покупка, несовпадение суммы, поздний колбэк после отмены (магазин) | PASS | 13:45 | `evidence/g04_hs.txt`: `TestPayPlus_*` (3), `TestShopPayPlus_*` (2) | — | — |
| SEC-01 / PayPlus | Неверный `hash` на HTTP-уровне | NOT RUN | — | — | — | Теста нет (✗ в SEC-01a) |
| SEC-02 | Неверная и пустая подпись на `/v1/refunds/webhook`, `/v1/billing/stripe/webhook`, `/v1/billing/bank/webhook/{provider}` | NOT RUN | — | — | — | Отдельных негативных тестов нет (✗ в плане) |
| PAY-04 / AllPay | Повторная доставка | PASS | 13:45 | `evidence/g04_hs.txt`: `TestAllPay_DuplicateDeliveryAnswers200` | — | — |
| PAY-04 / Stripe, Flitt | Повторная доставка, проверка числа билетов и писем | NOT RUN | — | — | — | Отдельного теста нет (◐ в плане) |
| PAY-01 | G-05: Go-часть: hosted checkout → вебхук → заказ paid; одно письмо со всеми билетами | PASS | 13:45, 13:46 | `evidence/g04_hs.txt`: `TestHostedCheckout_StartCreatesStripeSessionAndWebhookPaysTheOrder`; `evidence/g07_delivery.txt`: `TestOrderLetter_OneEmailCarriesEveryTicketAndThePayment` | — | Тот же прогон пакета httpserver, что для G-04 |
| PAY-01 | Браузерный цикл | NOT RUN | — | `evidence/widget_acceptance_local.txt`: тест 15 `-` (skipped) | — | Теста нет: `W/tests/palac-akropolis-real.e2e.ts:616` пропускается без `STRIPE_TEST_KEY` (✗ в плане). Пропуск подтверждён в локальном прогоне |
| PAY-05 / AllPay, PayPlus, guard | Сумма ≠ `orders.total` → `manual_review` | PASS | 13:45 | `evidence/g04_hs.txt`: `TestAllPay_PaidAmountMismatchParksThePayment`, `TestPayPlus_PaidAmountMismatchIsParked`; `evidence/g05_hcheckout.txt`: `TestPaidAmountMismatch` (8 подтестов) | — | — |
| PAY-05 / Stripe, Flitt | То же | NOT RUN | — | — | — | Отдельного теста нет (◐ в плане). `TestFlitt_PaidCallbackWithoutCurrencyIsParked` проверяет валюту, а не сумму |
| PAY-08 | Две организации со своими секретами | PASS | 13:45 | `evidence/g04_hs.txt`: `TestHostedCheckout_TwoOrgsUseTheirOwnWebhookSecrets` | — | — |
| PAY-11 | Go-часть: approve → Stripe-заглушка → гашение после принятия; бот | PASS | 13:45 | `evidence/g05_hs.txt`: `TestRefundEngineHTTP_ApproveDrivesStripeAndCancelsAfterwards`, `TestBotE2E_Refund_OwnerReturnsOneTicket`, `TestBotE2E_Refund_SendLostToARestartIsOfferedAgain` | — | — |
| PAY-11 | EC live `refund.spec.ts`, `refund-resilience.spec.ts` | PASS | 13:53 | `evidence/ec_live_3specs.txt`: тесты 4–18 `ok` | F-06 | — |
| PAY-12 / PayPlus, бот | Частичный возврат | PASS | 13:45 | `evidence/g04_hs.txt`: `TestPayPlus_PartialRefundThroughRefundByTransactionUID`; `evidence/g05_hs.txt`: `TestBotE2E_Refund_OwnerReturnsOneTicket` | — | — |
| PAY-12 / Stripe через EC | 1 из 3 билетов, `partially_refunded`, отчёт | NOT RUN | — | — | — | Теста на этот сценарий нет (◐ в плане). `refund.spec.ts` проверяет часть суммы одного билета, но не «1 из 3» и не итоги сеанса |
| ORD-06 | Оплата после истечения hold → `manual_review` | PASS | 13:45 | `evidence/g05_hs.txt`: `TestWebhookWidgetCompletion_PaymentAfterExpiry_ParksForManualReview` (+ 2 соседних `TestWebhookWidgetCompletion_*`) | — | — |
| ORD-07 | Бесплатный заказ без провайдера | PASS | 13:45 | `evidence/g04_hs.txt`: `TestHostedCheckout_FreeCartNeedsNoProvider` | — | — |
| ORD-02 | G-06: Go-часть, параллельные мутации hold | PASS | 13:45 | `evidence/g06_hcheckout.txt`: `TestW1A5a_*_LiveDB` (4) | — | Regex зацепил ещё 8 unit-тестов `TestW1A5a_*`, все PASS |
| ORD-02 | Виджет, `palac-akropolis-real.e2e.ts`, группа 3: второй контекст получает 409 и `conflictKeys` | PASS | 14:28 | `evidence/widget_acceptance_local.txt`: тесты 11 (`:338`) и 12 (`:418`) `ok`; весь набор `23 passed, 1 skipped` | F-06, F-07 | Локальная копия джоба `widget-acceptance` на чистой базе. Первый запуск был с моей ошибкой окружения и в зачёт не идёт (F-07) |
| ORD-03 | G-06: только Go-часть | PASS | 13:45 | `evidence/g06_hcheckout.txt`: `TestGAHoldDeadlockRace_LiveDB`; `evidence/g06_hs.txt`: `TestPublicFeedCheckout_ConcurrentSameNewBuyer_NoRaceAbortedTx` | — | Нагрузочная часть вне гейта |
| ORD-05 | `reservation.expire_sweep` | PASS | 13:45 | `evidence/g06_hcheckout.txt`: `TestReservationExpireSweep_*_LiveDB` (3) | — | — |
| ORD-08 | Тесты из плана: бюджет неверных кодов, журнал попыток, лимиты шлюза | PASS | 13:45–13:46 | `evidence/g06_hs.txt`: `TestPublicPromoBudget_*` (3), `TestPromoAttemptsRoutes_ReadOwnJournalAndStayInsideTheOrganization`; `evidence/g06_bil24promo.txt`: 6 тестов | F-02 | — |
| ORD-08 | Два параллельных заказа с одним кодом `max_uses=1` | NOT RUN | — | — | F-02 | Теста нет: ни один из перечисленных тестов не запускает два параллельных заказа на `max_uses=1` |
| CAT-07 | GA-квоты под конкуренцией | PASS | 13:45–13:46 | `evidence/g06_hs.txt`: `TestGAQuota_SetQuantityVsGAHold_NoDeadlock`; `evidence/g06_gaquota.txt`: 14 тестов `TestGAQuota_*` | — | — |
| ASY-05 | `checkout.issue_tickets` и `checkout.convert_reservation` дважды и после падения | NOT RUN | — | — | — | Теста нет (✗ в плане, ждёт PR-04) |
| ASY-06 | Одно письмо на заказ; ошибка отправки возвращает все билеты в `pending` | PASS | 13:46 | `evidence/g07_delivery.txt`: `TestOrderLetter_*` (3), `TestDelivery_*` (6) | F-03 | `TestDelivery_*` работают через `pgtest` (testcontainers) — на этом хосте контейнеры поднялись |
| ASY-01 | Outbox: ветки, повторы, без двойной доставки | PASS | 13:46–13:47 | `evidence/g08_worker.txt`: `TestOutboxLegs_*` (2); `evidence/g08_outbox.txt`: весь пакет, 104 теста верхнего уровня, в т. ч. `TestNoDoubleDelivery_*` | — | — |
| ASY-02 | `data.id` = `orders.system_id` = `orderId` | PASS | 13:47 | `evidence/g08_bil24.txt`: `TestCompatBil24_508_WPReceiverRoundTrip` | — | — |
| ASY-12 | Харнесс шлюза и golden-ответы | PASS | 13:47, 13:49 | `evidence/g08_bil24.txt`: `TestCompatBil24_450_Harness_Scenarios`, `…_ScenarioCoverage`; `evidence/g08_bil24_scenarios.txt`: 4 теста `scenario0*` | — | — |
| ASY-12 | Живые сценарии SEND_TICKETS_TO_EMAIL, CHECK_KDP, SCAN_TICKET | NOT RUN | — | — | — | Теста нет (✗ в плане) |
| BOT-01 | Приглашение и привязка Telegram | PASS | 13:47–13:48 | `evidence/g09_hs.txt`: `TestBotE2E_InvitationToMyEvents`, `TestBotInvitationIntegration_*` (2) | — | — |
| BOT-02 | Мастер «+ Event» в боте | PASS | 13:47–13:48 | `evidence/g09_hs.txt`: `TestBotE2E_WizardCreatesAnEvent`, `…_WizardSeatedDate`, `…_WizardPerDateCategories` | — | — |
| BOT-02 | Сверка черновика с экраном EC | NOT RUN | — | — | — | Теста нет (◐ в плане) |
| BOT-09 | Перенос и отмена сеанса | PASS | 13:47–13:48 | `evidence/g09_hs.txt`: `TestBotE2E_SessionsMoveKeepsTheSessionAndWritesToBuyers`, `TestBotE2E_SessionCancelPhraseAfterRestart` | — | — |
| BOT-10 | Возврат в боте и восстановление после перезапуска | PASS | 13:47–13:48 | `evidence/g09_hs.txt`: `TestBotE2E_Refund_*` (3) | — | — |
| ASY-13 | `migrate up` на чистой базе, пин головы, паритет прав суперадмина | PASS | 13:40, 13:48 | `evidence/migrate_seed.txt` (до 183); `evidence/g10_migrations.txt`: весь пакет, 29 тестов, в т. ч. `TestHead_KnownCurrentHead`, `TestSuperadminPermissionParity532_*`; `evidence/g10_hs_parity.txt` | — | — |
| SEC-14 | Стражи `apps/backend/tests/staticanalysis` | PASS | 13:48 | `evidence/g10_staticanalysis.txt`: 101 тест, `ok …/tests/staticanalysis` | — | — |
| EC-22 | Vitest event-center, в т. ч. паритет маршрутов | PASS | 13:50–13:51 | `evidence/fe_event_center_vitest.txt`: 120 файлов, 1656 тестов, `parity.test.ts (169 tests)` | F-06 | Перед этим `npm ci`, сборка rich-editor, type-check — `evidence/fe_summary.txt` |
| — | Vitest widget (дополнительно, без ID плана) | PASS | 13:51 | `evidence/fe_widget_vitest.txt`: 31 файл, 892 теста | F-06 | Приведено для полноты, в счётчики P0 не входит |

## 3. Находки

Дефектов продукта не найдено: ни одного FAIL продукта, перепроверять изоляцией было нечего. Ниже — находки по плану, CI, окружению и моей ошибке в одном запуске. Уязвимостей не найдено, приватных заметок нет.

| ID | Что | Где | Что сделать |
|---|---|---|---|
| F-01 | План в SEC-01a ставит «✗ метка вне окна» для Stripe. Unit-тест `TestPayment134_StripeSignature_ExpiredTimestamp` есть и прошёл. Нет только проверки на HTTP-маршруте | `apps/backend/internal/domain/payments/provider_134_test.go` | Уточнить строку SEC-01a; в PR-03 добавить HTTP-тест |
| F-02 | Для ORD-08 план приводит тесты, которые проверяют другое: бюджет неверных кодов, журнал, лимиты шлюза. Гонки двух заказов на `max_uses=1` нет ни в одном | `HS/public_feed_promo_concurrent_integration_test.go`, `CB/promo_0108_test.go` | Поменять отметку ORD-08 на ◐ и завести тест |
| F-03 | `pgtest` (testcontainers v0.36.0) на этом хосте работает с Docker 29.8.2: `TestDelivery_*` прошли в контейнере. Запись в AGENTS.md про панику «rootless Docker is not supported» для этого хоста устарела | `AGENTS.md`, раздел FRESH-базы | Обновить AGENTS.md отдельным PR (в этом прогоне не правлю) |
| F-04 | Джоб `venue-svg-generator` зелёный, но на этом коммите фильтр изменений пропустил все шаги: pytest, сборку образа и smoke. Зелёный джоб здесь не доказывает, что генератор проверен. Для G-01 эти шаги прогнаны отдельно на том же коммите, все прошли | `evidence/g01_venue_gen_steps.txt`, `evidence/vsg_local.txt` | В правиле G-01 записать: джоб с пропущенными шагами — не прогон. Пропущенные шаги гонять отдельно или обосновать неприменимость к релизу |
| F-05 | Подтверждено: у `build-and-push` (ci.yml:1082–1089) и `widget-publish` (ci.yml:1157–1164) в `needs` только 7 джобов. Не входят: `event-center-build`, `event-center-live`, `tickets-page-build`, `shop-page-build`, `rich-editor-build`, `venue-svg-generator`. На этом коммите все шесть были зелёными | `.github/workflows/ci.yml` | Это G-02 / PR-01, вне этого пакета |
| F-06 | Фронтенд (vitest event-center и widget, Playwright event-center live и widget-acceptance) гонялся на Node 22.13.1, в CI — Node 20. Подтверждено только, что эти тесты прошли на Node 22. Что они так же пройдут на Node 20, этим прогоном не доказано: тут опора только на зелёные джобы CI того же коммита | — | Для приёмки фронтенда по правилу B.1 гонять на Node 20, как в CI |
| F-07 | Моя ошибка в окружении, не дефект продукта. Первый локальный запуск widget-acceptance (14:27) был с Stripe-заглушкой на порту 12211, без `STRIPE_STUB_ORIGIN`. Тесты ждут заглушку на 12111 по умолчанию, поэтому 2 теста группы 4 упали на проверке адреса платёжной страницы: `21 passed, 2 failed, 1 skipped`. База пересоздана, повтор с `STRIPE_STUB_ORIGIN=http://localhost:12211` прошёл: `23 passed, 1 skipped`. Первый запуск в зачёт не идёт | `evidence/widget_acceptance_local_run1_invalid.txt` | — |

## 4. Итог

Счётчики по строкам таблицы раздела 2. Один кейс может занимать несколько строк: отдельно покрытый шаг и отдельно непокрытый.

| Статус | Всего строк | Из них P0 |
|---|---|---|
| PASS | 38 | 37 |
| FAIL | 0 | 0 |
| BLOCKED | 0 | 0 |
| SKIP | 0 | 0 |
| NOT RUN | 15 | 15 |

PASS по P0: 37 = G-01 (2) + G-03 (5) + G-04 (5) + G-05 (8) + G-06 (6) + G-07 (1) + G-08 (3) + G-09 (4) + G-10 (2) + EC-22 (1). Ещё одна строка PASS — vitest widget, у неё нет ID плана.

Обязательные кейсы не в PASS (все `NOT RUN`, причина у всех — теста нет или он не покрывает шаг):
1. SEC-01 / Stripe — метка вне окна на HTTP.
2. SEC-01 / Stripe — повтор после терминального состояния.
3. SEC-01 / Flitt — чужой `merchant_id`.
4. SEC-01 / Flitt — повтор `approved`.
5. SEC-01 / AllPay — чужой ключ на чужом конфиге.
6. SEC-01 / PayPlus — неверный `hash` на HTTP.
7. SEC-02 — все три вебхука.
8. PAY-04 — Stripe и Flitt.
9. PAY-01 — браузерный цикл.
10. PAY-05 — Stripe и Flitt.
11. PAY-12 — Stripe через EC, «1 из 3».
12. ORD-08 — два параллельных заказа на `max_uses=1`.
13. ASY-05 — повторный запуск и падение `checkout.issue_tickets` / `convert_reservation`.
14. ASY-12 — SEND_TICKETS_TO_EMAIL, CHECK_KDP, SCAN_TICKET.
15. BOT-02 — сверка черновика с экраном EC.

Вердикт по гейтам:

| Гейт | Вердикт | Что мешает |
|---|---|---|
| G-01 | пройден | — (шаги генератора, пропущенные CI, прогнаны отдельно, F-04) |
| G-03 | пройден | — |
| G-04 | не пройден | строки 1–8 |
| G-05 | не пройден | строки 9–11 |
| G-06 | не пройден | строка 12 |
| G-07 | не пройден | строка 13 |
| G-08 | не пройден | строка 14 |
| G-09 | не пройден | строка 15 |
| G-10 | пройден | — |

**Гейты G-01, G-03…G-10 в целом не пройдены.** Все существующие тесты гейтов зелёные на коммите прода `23b7d49e`. Блокируют только пробелы покрытия, перечисленные в плане (разделы D и E, PR-03 и PR-04).

## 5. Что проверено и что нет

Проверено в этом прогоне:
- все Go-тесты, перечисленные в столбце «Покрытие сейчас» кейсов G-03…G-10, — на чистой базе, `-p 1`, без единого падения и пропуска;
- три live-спеки event-center из кейсов IAM-03 и PAY-11 — на отдельной чистой базе через харнесс;
- `widget-acceptance` целиком — на отдельной чистой базе, как в CI;
- шаги CI-джоба `venue-svg-generator` — pytest на Python 3.12, сборка образа, оба smoke;
- vitest event-center и widget.

Не проверено:
- фронтенд на Node 20 (F-06);
- `event-center-live` целиком (~170 тестов) — запускались только три спеки из кейсов гейтов;
- admin-build, tickets-page, shop-page локально — их кейсов нет в гейтах этого пакета;
- всё, что делает Hermes: Telegram, веб и Stripe test на проде.
