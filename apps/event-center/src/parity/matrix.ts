/**
 * Parity matrix "function × bot × web" of the event center.
 *
 * ONE source for three things:
 *   1. the table in 08_architecture/37_web_event_center_ru.md (section 11),
 *      rendered by renderParityTable() between the PARITY markers;
 *   2. the check that every route named here exists in openapi.yaml;
 *   3. the check that every route the web app calls (src/lib/api/routes.ts)
 *      belongs to a function of this table.
 *
 * `bot` names the bot screen (file and callback prefix) from
 * apps/backend/internal/platform/eventbot. `web` is the phase that brings the
 * function to the web: R1 read, R2 wizard and edit, R3 promo codes,
 * invitations, resend, team and promoters, R4 sessions and categories,
 * R5 refunds. "—" means the web does not plan it.
 *
 * Texts are Russian: the table is part of a Russian spec. Terms follow the
 * glossary of docs/text_style_standard_ru.md.
 */

export type Phase = "R1" | "R2" | "R3" | "R4" | "R5" | "—";

/**
 * Marks a web function as "готово" (done). The rule (plan of 2026-10-14, stage
 * 0): a function is done only when its twin test exists - a Playwright scenario
 * in apps/event-center/tests/smoke that walks the bot's e2e scenario through
 * the real API. `twin` is the file name inside tests/smoke.
 *
 * `twin: null` with `legacy: true` is the one waiver: R1 shipped before the
 * rule, and its smoke tests run against a mocked API, so they are not twins.
 * The waiver is allowed only for phase R1. Every later phase needs a real file.
 * `smoke` names the existing (mocked-API) spec that covers the function today.
 */
export interface Ready {
  readonly twin: string | null;
  readonly legacy?: true;
  readonly smoke?: string;
}

export interface ParityRow {
  readonly area: string;
  readonly fn: string;
  readonly bot: string;
  readonly web: Phase;
  /** Present = the function is done on the web (see Ready). */
  readonly ready?: Ready;
  /** "METHOD /path" exactly as openapi.yaml spells the path template. */
  readonly routes: readonly string[];
  readonly note?: string;
}

const O = "/v1/organizations/{org_id}";

export const PARITY: readonly ParityRow[] = [
  // ── Вход и организация ──────────────────────────────────────────────
  {
    area: "Вход и организация",
    fn: "Вход по e-mail и паролю",
    bot: "— (бот знает человека по Telegram)",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: ["POST /v1/auth/login", "POST /v1/auth/refresh", "POST /v1/auth/logout"],
  },
  {
    area: "Вход и организация",
    fn: "Вход по одноразовой ссылке из бота",
    bot: "web_login.go, `web:open`, `web:out`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "link.spec.ts" },
    routes: ["POST /v1/auth/web/link"],
    note: "Кнопка «Открыть в браузере» для всех ролей, кроме суперадмина (раздел 4.3). Код и «Выйти везде» — служебные маршруты бота.",
  },
  {
    area: "Вход и организация",
    fn: "Забыли пароль",
    bot: "—",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: ["POST /v1/auth/password-reset/request"],
    note: "Письмо организатору ведёт на EVENT_CENTER_PUBLIC_URL (раздел 4.2).",
  },
  {
    area: "Вход и организация",
    fn: "Новый пароль и первый пароль по ссылке из письма",
    bot: "—",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: ["POST /v1/auth/password-reset/confirm"],
    note: "Страницы `/reset-password` и `/accept-invite`, без входа.",
  },
  {
    area: "Вход и организация",
    fn: "Повторный пароль перед денежным действием",
    bot: "— (в Telegram не спрашивают, решение владельца)",
    web: "R5",
    routes: ["POST /v1/auth/step-up"],
    note: "Механизм готов (раздел 4.6); экраны возвратов — в R5.",
  },
  {
    area: "Вход и организация",
    fn: "Выбор организации",
    bot: "bot.go, `org:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: ["GET /v1/me"],
  },
  {
    area: "Вход и организация",
    fn: "Роль и права в организации",
    bot: "bot.go (`canViewSales`, `isOwner` по роли из /v1/me)",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/me`],
    note: "Бот пока решает по роли. Веб решает по правам A1.",
  },
  {
    area: "Вход и организация",
    fn: "Язык интерфейса",
    bot: "bot.go, `lang:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [],
    note: "Хранится на устройстве.",
  },
  {
    area: "Вход и организация",
    fn: "Заметка «оплата не подключена»",
    bot: "payments.go",
    web: "R3",
    // 4-7
    ready: { twin: "live/org-data.spec.ts" },
    routes: [`GET ${O}/payment-configs`],
  },
  // ── Мероприятия ─────────────────────────────────────────────────────
  {
    area: "Мероприятия",
    fn: "Список мероприятий",
    bot: "events_list.go, `el:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/events`],
  },
  {
    area: "Мероприятия",
    fn: "Цифры продаж в списке",
    bot: "— (в боте только на карточке)",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/events/{event_id}/summary`],
    note: "Один запрос на строку. Нужен пакетный маршрут.",
  },
  {
    area: "Мероприятия",
    fn: "Карточка мероприятия с сеансами",
    bot: "event_card.go, `ec:o:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/events`, `GET ${O}/events/{event_id}/summary`, `GET ${O}/events/{event_id}/sessions`],
  },
  {
    area: "Мероприятия",
    fn: "Сводка мероприятия",
    bot: "event_summary.go, `ec:es:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/events/{event_id}/summary`],
  },
  {
    area: "Мероприятия",
    fn: "CSV продаж мероприятия",
    bot: "event_csv.go, `ec:ce:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/events/{event_id}/sales.csv`],
  },
  {
    area: "Мероприятия",
    fn: "Образец билета (PDF)",
    bot: "sample.go, `sample:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/sessions/{session_id}/sample-ticket`],
  },
  {
    area: "Мероприятия",
    fn: "Создание мероприятия (мастер)",
    bot: "wizard*.go, `wz:`",
    web: "R2",
    routes: [
      "GET /v1/geo/countries",
      "GET /v1/geo/cities",
      `GET ${O}/venues`,
      `POST ${O}/venues`,
      `POST ${O}/cities`,
      `GET ${O}/promoters`,
      `POST ${O}/promoters`,
      `GET ${O}/channels`,
      "POST /v1/media",
      "GET /v1/media/{id}",
      `POST ${O}/imports/event-bundle`,
    ],
    note: "Ждёт A3 «план мероприятия»: бот и веб должны сохранять одним серверным слоем.",
  },
  {
    area: "Мероприятия",
    fn: "Чтение афиши",
    bot: "wizard_bot.go, `wz:hint`",
    web: "R2",
    routes: ["POST /v1/media"],
    note: "Ждёт A4: чтение афиши на сервере, ключ модели не в клиенте.",
  },
  {
    area: "Мероприятия",
    fn: "Правка и копия мероприятия",
    bot: "wizard_load.go, `wz:edit:`, `wz:copy:`",
    web: "R2",
    routes: [
      "GET /v1/events/{id}",
      `GET ${O}/events/{event_id}/sessions`,
      `GET ${O}/events/{event_id}/sessions/{session_id}/tiers`,
      `GET ${O}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule`,
      `POST ${O}/imports/event-bundle`,
    ],
  },
  {
    area: "Мероприятия",
    fn: "Афиша: загрузка, просмотр и замена",
    bot: "wizard_bot.go (UploadPoster), arena_client_refs.go",
    web: "R4",
    // 7-1
    ready: { twin: "live/poster.spec.ts" },
    routes: ["POST /v1/media", "GET /v1/media/{id}", `PATCH ${O}/events/{id}`],
    note: "7-1: афиша в мастере (загрузка, затем черновик, затем привязка) и кнопка «Загрузить / Заменить афишу» на карточке. Загрузка и привязка — два отдельных шага: сбой привязки не создаёт второе мероприятие и не удаляет файл. Права: просмотр `media.read`, изменение `media.write` и `event.update`. Афиша — первый шаг мастера: загрузить её или осознанно нажать «Продолжить без афиши» (жёсткого требования к афише для публикации и продажи нет). Замена афиши на карточке мероприятия не читает афишу и не меняет данные мероприятия.",
  },
  {
    area: "Мероприятия",
    fn: "Афиша: предложить данные мероприятия по загруженной афише",
    bot: "wizard_bot.go (readPosterHints), wizard_hints.go",
    web: "R4",
    // poster-first creation
    ready: { twin: "live/poster-first.spec.ts" },
    routes: [`POST ${O}/media/{media_id}/poster-facts`],
    note: "Афиша загружается сразу после выбора (один media id: для чтения и для привязки при создании). Чтение моделью — отдельный явный платный шаг после пояснения, что картинка уходит на распознавание; ключ модели только на сервере. Результат — подсказки на проверку: ничего не подставляется без «Подставить выбранное», набранный текст не заменяется без отдельной отметки, площадка выбирается из списка организации (по названию не создаётся и не назначается), цена — только после выбора категории и при известной валюте, одна дата афиши идёт в первую дату. Сбой, отсутствие настройки и лимит не мешают ручному заполнению и не теряют загруженную афишу; повтор только явный. Как у бота: подсказки держатся до решения организатора.",
  },
  {
    area: "Мероприятия",
    fn: "Публикация, снятие с продажи, архив",
    bot: "evstatus.go, `ec:ev:`",
    web: "R4",
    // 5-1
    ready: { twin: "live/event-status.spec.ts", smoke: "event-status.mock.spec.ts" },
    routes: ["GET /v1/events/{id}", `POST ${O}/events/{id}/status`],
    note: "5-1: блок «Статус» на карточке. Кнопки идут из таблицы переходов сервера (её копию сверяет тест с Go-файлом) и из прав `event.publish` / `event.delete`. «Снять с продажи» и «В архив» — одно подтверждение с текстом «проданные билеты остаются действительными».",
  },
  {
    area: "Мероприятия",
    fn: "Удаление мероприятия",
    bot: "evstatus.go, `ec:ev:…:del` (слово)",
    web: "R4",
    // 5-1
    ready: { twin: "live/event-status.spec.ts", smoke: "event-status.mock.spec.ts" },
    routes: [`GET ${O}/events/{event_id}/delete-impact`, `DELETE ${O}/events/{id}`],
    note: "5-1: сначала пробный расчёт (`delete-impact`). Если что-то продано, показаны цифры и предложен архив. Иначе нужно набрать слово удаления.",
  },
  {
    area: "Мероприятия",
    fn: "Уведомления о продажах",
    bot: "notifications.go, `ec:nt`",
    web: "R1",
    ready: { twin: null, legacy: true },
    routes: [],
    note: "Только ссылка на @ArenaSoldOutSalesBot.",
  },
  // ── Сеансы ──────────────────────────────────────────────────────────
  {
    area: "Сеансы",
    fn: "Список сеансов и карточка сеанса",
    bot: "sessions_dialog.go, `ses:list:`, `ses:o:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/events/{event_id}/sessions`, `GET ${O}/sessions/{session_id}/summary`],
  },
  {
    area: "Сеансы",
    fn: "Сводка сеанса",
    bot: "event_summary.go, `ec:ss:`, `ses:sm`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/sessions/{session_id}/summary`],
  },
  {
    area: "Сеансы",
    fn: "CSV продаж и сводки сеанса",
    bot: "event_csv.go, `ec:cs:`, `ec:cm:`, `ses:csv`",
    web: "R1",
    ready: { twin: null, legacy: true },
    routes: [`GET ${O}/sessions/{session_id}/sales.csv`, `GET ${O}/sessions/{session_id}/summary.csv`],
  },
  {
    area: "Сеансы",
    fn: "Перенос сеанса",
    bot: "sessions_dialog.go, `ses:mv`, `ses:go`",
    web: "R4",
    ready: { twin: "live/session-change.spec.ts" },
    routes: [
      `GET ${O}/sessions/{session_id}/change-impact`,
      `PATCH ${O}/events/{event_id}/sessions/{id}`,
      `GET ${O}/venues`,
    ],
    note: "Сначала сухой прогон, потом одно нажатие. Заказы сайта-партнёра блокируют перенос, текст честный.",
  },
  {
    area: "Сеансы",
    fn: "Отмена сеанса",
    bot: "sessions_dialog.go, `ses:cx`, `ses:cxall` (слово)",
    web: "R4",
    ready: { twin: "live/session-change.spec.ts" },
    routes: [`GET ${O}/sessions/{session_id}/change-impact`, `PATCH ${O}/events/{event_id}/sessions/{id}`],
    note: "Только со словом сеанса. Письма уходят, деньги не возвращаются, заказы остаются оплаченными.",
  },
  {
    area: "Сеансы",
    fn: "Контакт организатора для писем",
    bot: "sessions_dialog.go (`sesSaveContact`)",
    web: "R4",
    ready: { twin: "live/session-change.spec.ts" },
    routes: [`PUT ${O}/events/{event_id}/contact`],
    note: "Форма e-mail появляется внутри переноса или отмены, когда сухой прогон говорит contact_missing.",
  },
  {
    area: "Сеансы",
    fn: "Конец продаж и «вход с»",
    bot: "sessions_sale_times.go, `ses:se`, `ses:dr`",
    web: "R4",
    routes: [`PATCH ${O}/events/{event_id}/sessions/{id}`],
    ready: { twin: "live/sale-times.spec.ts", smoke: "sale-times.mock.spec.ts" }, // 5-3
  },
  // ── Категории ───────────────────────────────────────────────────────
  {
    area: "Категории",
    fn: "Категории сеанса (чтение)",
    bot: "categories.go, `ct:e:`, `ct:v:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/events/{event_id}/sessions/{session_id}/tiers`],
  },
  {
    area: "Категории",
    fn: "Открыть или закрыть продажи категории",
    bot: "categories.go, `ct:op`, `ct:cl`",
    web: "R4",
    // 5-4
    ready: { twin: "live/categories.spec.ts" },
    routes: [`PATCH ${O}/events/{event_id}/sessions/{session_id}/tiers/{id}`],
  },
  {
    area: "Категории",
    fn: "Цена, окно продаж, количество",
    bot: "categories.go, `ct:pr`, `ct:wn`, `ct:qt`",
    web: "R4",
    // 5-4
    ready: { twin: "live/categories.spec.ts" },
    routes: [
      `PATCH ${O}/events/{event_id}/sessions/{session_id}/tiers/{id}`,
      `GET ${O}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule`,
      `PUT ${O}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule`,
    ],
  },
  // ── Заказы ──────────────────────────────────────────────────────────
  {
    area: "Заказы",
    fn: "Список заказов, вкладки, поиск",
    bot: "orders_list.go, `or:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`POST ${O}/orders/search`],
    note: "Поиск идёт в теле запроса, не в адресе: так e-mail и телефон покупателя не попадают в журналы прокси. Бот тоже.",
  },
  {
    area: "Заказы",
    fn: "Карточка заказа и статус письма с билетами",
    bot: "order_card.go, `or:v:`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "r1.spec.ts" },
    routes: [`GET ${O}/orders/{id}`],
  },
  {
    area: "Заказы",
    fn: "Отмена неоплаченного заказа",
    bot: "order_card.go, `or:c:` (слово)",
    web: "R3",
    // 4-2
    ready: { twin: "live/cancel-order.spec.ts" },
    routes: [`POST ${O}/orders/{id}/cancel`],
    note: "Слово отмены заказа не равно слову отмены сеанса. Из веба перед отменой просят пароль ещё раз (раздел 4.6).",
  },
  {
    area: "Заказы",
    fn: "Повторная отправка билетов",
    bot: "resend.go, `or:r:`",
    web: "R3",
    // 4-1
    ready: { twin: "live/resend.spec.ts" },
    routes: [`POST ${O}/orders/{order_id}/resend-tickets`],
    note: "Окно «на адрес заказа / на другой адрес», подтверждение, результат. Заказ сайта-продавца отклоняет API (`order.seller_site_order`). Строки статуса писем (`letters[]`) стоят на карточке заказа. Кнопка видна по праву `ticket.update` в `GET …/me`.",
  },
  // ── Возвраты ────────────────────────────────────────────────────────
  {
    area: "Возвраты",
    fn: "Возврат по заказу",
    bot: "refund_dialog.go, `or:f:` (слово и кнопка)",
    web: "R5",
    // 8-1
    ready: { twin: "live/refund.spec.ts" },
    routes: [`GET ${O}/orders/{order_id}/refund-options`, `POST ${O}/orders/{order_id}/refunds`],
    note: "Кнопка на карточке оплаченного заказа (`refund.create`). Ветка по `route` сервера: сайт продавца, провайдер без возвратов и заказ без оплаты только объясняются. Билеты не отмечены сначала, часть суммы — для одного билета, причина обязательна, флажок письма; последняя проверка, слово ВЕРНУТЬ и пароль ещё раз (раздел 4.6). Один `Idempotency-Key` на операцию: потерянный ответ проверяется тем же ключом, деньги дважды не уходят.",
  },
  {
    area: "Возвраты",
    fn: "Очередь возвратов и повтор",
    bot: "refunds_queue.go, `rq:`",
    web: "R5",
    routes: [`GET ${O}/refunds`, `GET ${O}/refunds/{refund_id}`, `POST ${O}/refunds/{refund_id}/retry`],
  },
  // ── Промокоды ───────────────────────────────────────────────────────
  {
    area: "Промокоды",
    fn: "Список, карточка, использование",
    bot: "promo_dialog.go, `pm:`",
    web: "R3",
    // 4-3
    ready: { twin: "live/promo.spec.ts", smoke: "promo.mock.spec.ts" },
    routes: [
      `GET ${O}/promo-codes`,
      `GET ${O}/promo-codes/{id}`,
      `GET ${O}/promo-code-redemptions`,
      `GET ${O}/promo-codes/{promo_code_id}/redemptions.csv`,
    ],
  },
  {
    area: "Промокоды",
    fn: "Создание, пауза, сеансы, удаление",
    bot: "promo_create.go, `pm:n`, `pm:ps`, `pm:se`, `pm:del`",
    web: "R3",
    // 4-3
    ready: { twin: "live/promo.spec.ts", smoke: "promo.mock.spec.ts" },
    routes: [`POST ${O}/promo-codes`, `PATCH ${O}/promo-codes/{id}`, `DELETE ${O}/promo-codes/{id}`],
  },
  // ── Пригласительные ─────────────────────────────────────────────────
  {
    area: "Пригласительные",
    fn: "Выдача пригласительных",
    bot: "invite.go, `iv:i`",
    web: "R3",
    ready: { twin: "live/invitations.spec.ts" },
    routes: [`POST ${O}/complimentary`],
  },
  {
    area: "Пригласительные",
    fn: "Список и аннулирование",
    bot: "invite_list.go, `iv:l`, `iv:rv` (слово)",
    web: "R3",
    ready: { twin: "live/invitations.spec.ts" },
    routes: [`GET ${O}/complimentary`, "POST /v1/complimentary/{id}/revoke"],
    note: "Статус письма гостя (`letters[]`) идёт через билет пригласительного, у пригласительного нет заказа.",
  },
  // ── Команда ─────────────────────────────────────────────────────────
  {
    area: "Команда",
    fn: "Команда, приглашение, удаление",
    bot: "team.go, `team:`",
    web: "R3",
    // 4-5
    ready: { twin: "live/team.spec.ts" },
    routes: [
      `GET ${O}/bot-team`,
      `POST ${O}/bot-invitations`,
      `DELETE ${O}/bot-invitations/{id}`,
      `POST ${O}/bot-invitations/{id}/resend`,
      `DELETE ${O}/members/{user_id}`,
    ],
    note: "Приглашение с каналом web или telegram. Для web-приглашения нет повторной отправки и отзыва: потерянную ссылку заменяет «Забыли пароль». Удаление называет роль участника в теле запроса. Своя строка не удаляется: последнего владельца снять нельзя.",
  },
  // ── Промоутеры ──────────────────────────────────────────────────────
  {
    area: "Промоутеры",
    fn: "Список, правка, архив",
    bot: "promoters.go, `pr:`",
    web: "R3",
    ready: { twin: "live/promoters.spec.ts" },
    routes: [`GET ${O}/promoters`, `POST ${O}/promoters`, `PATCH ${O}/promoters/{id}`],
    note: "4-6: список с фильтром архива, карточка с правкой по одному полю, смена адреса страницы с вопросом «было → стало», архив и возврат, ссылка на публичную страницу с кнопкой копирования.",
  },
  // ── Данные организации ──────────────────────────────────────────────
  {
    area: "Данные организации",
    fn: "Данные организации, блок продавца и контакт на странице продаж",
    bot: "org_data.go, `og:` (владелец и оператор платформы)",
    web: "R3",
    // 4-7
    ready: { twin: "live/org-data.spec.ts" },
    routes: [
      `GET ${O}/seller-profile`,
      `PATCH ${O}/seller-profile`,
      `PATCH ${O}/channels/{channel_id}/seller-display`,
      "GET /v1/organizations/{id}",
      "PATCH /v1/organizations/{id}",
      `GET ${O}/me`,
    ],
    note: "Этап 4, задача 4-7 плана; близнец сценария `org_data` бота. Только владелец (`org.update`). Ключ организации сюда не допущен. На сайте-плагине данные организации не правятся.",
  },
  // ── Не в ивент-центре ───────────────────────────────────────────────
  {
    area: "Не в ивент-центре",
    fn: "Анкета организатора",
    bot: "onboarding_dialog.go, `onb`",
    web: "—",
    routes: ["GET /v1/onboarding/form-schema", "POST /v1/onboarding/applications"],
    note: "В вебе анкета живёт на сайте arenasoldout.com.",
  },
  {
    area: "Не в ивент-центре",
    fn: "Принять приглашение в команду",
    bot: "bot.go, `/start inv_…`",
    web: "—",
    routes: ["POST /v1/bot/invitations/accept"],
    note: "В вебе: письмо с установкой пароля ведёт на `/accept-invite`. Приглашение из «Команды» с каналом web — экран «Команда» (4-5).",
  },
  // 6-1a
  {
    area: "Мероприятия",
    fn: "Мастер: мероприятие и площадка",
    bot: "wizard*.go, `wz:` (шаги названия, возраста, промоутера, площадки)",
    web: "R2",
    ready: { twin: "live/wizard-event-venue.spec.ts" },
    routes: [
      `POST ${O}/imports/event-bundle`,
      `GET ${O}/venues`,
      `GET ${O}/promoters`,
      "GET /v1/geo/countries",
      "GET /v1/geo/cities",
      "GET /v1/geo/countries/{iso2}/timezones",
      "GET /v1/geo/countries/{iso2}/currency",
    ],
    note: "6-1a: форма из двух разделов с проверкой на лету (`?dry_run=true`, не чаще раза в секунду, ошибки по `errors[].field`), черновик в браузере. Под флагом сборки `VITE_FEATURE_WIZARD=1`. Даты, билеты и сохранение — 6-1b и 6-1c.",
  },
  // 6-1b
  {
    area: "Мероприятия",
    fn: "Мастер: даты и билеты",
    bot: "wizard.go `BuildBundle`, wizard_dates.go, wizard_sale_times.go, wizard_date_tickets.go",
    web: "R2",
    ready: { twin: "live/wizard-dates-tickets.spec.ts" },
    routes: [`POST ${O}/imports/event-bundle`],
    note: "6-1b: сеансы (день, время, конец продаж, «вход с», места), у каждого свои билеты или «как у прошлой»: одна категория со ступенями цены, несколько сразу, несколько по очереди (этапы по дню и/или числу билетов). Проверка на лету по каждому сеансу. Пакет сравнивается с пакетом бота на общих golden-файлах (`eventbot/testdata/wizard_bundles`). Сохранение — 6-1c.",
  },
  // 6-1c
  {
    area: "Мероприятия",
    fn: "Мастер: создание черновика (GA)",
    bot: "wizard_io.go `BuildBundle`, шаг «Создать»",
    web: "R2",
    ready: { twin: "live/create-event.spec.ts" },
    routes: [`POST ${O}/imports/event-bundle`],
    note: "6-1c: кнопка «Создать мероприятие» (список и меню, флаг `VITE_FEATURE_WIZARD`, право `import.bil24_session`): один явный POST проверенного пакета с `publish:false`, ключ идемпотентности `externalRef` = `web:<org>:<черновик>:1`. Создаётся черновик события и черновик сеанса, не в продаже. В этом выпуске одна дата и существующая площадка. Двойной клик даёт один запрос; при потерянном ответе черновик остаётся, повтор идёт с тем же ключом и второго мероприятия не создаёт.",
  },
];

/** Every route of the matrix, without duplicates. */
export function parityRoutes(): string[] {
  const set = new Set<string>();
  for (const row of PARITY) for (const r of row.routes) set.add(r);
  return [...set].sort();
}

function cell(text: string): string {
  return text.replace(/\|/g, "\\|");
}

/** Renders the matrix as the Markdown table of the spec. */
export function renderParityTable(rows: readonly ParityRow[] = PARITY): string {
  const lines = [
    "| Раздел | Функция | Бот | Веб | Готово | Маршруты API | Примечание |",
    "|---|---|---|---|---|---|---|",
  ];
  for (const row of rows) {
    const routes = row.routes.length === 0 ? "—" : row.routes.map((r) => `\`${r}\``).join("<br>");
    lines.push(
      `| ${cell(row.area)} | ${cell(row.fn)} | ${cell(row.bot)} | ${row.web} | ${readyCell(row)} | ${cell(routes)} | ${cell(row.note ?? "")} |`,
    );
  }
  return lines.join("\n");
}

/** The "Готово" cell: "—" while not done, the twin file, or the R1 waiver. */
function readyCell(row: ParityRow): string {
  const r = row.ready;
  if (r === undefined) return "—";
  if (r.twin !== null) return `✔ \`${r.twin}\``;
  return r.smoke === undefined ? "✔ (до правила)" : `✔ (до правила, \`${r.smoke}\`)`;
}

/**
 * The "done = a twin test exists" rule. Returns one message per broken row;
 * `exists` tells whether a file is in apps/event-center/tests/smoke.
 */
export function readyViolations(rows: readonly ParityRow[], exists: (file: string) => boolean): string[] {
  const out: string[] = [];
  for (const row of rows) {
    const r = row.ready;
    if (r === undefined) {
      if (row.web === "R1") out.push(`${row.fn}: phase R1 is built, so it needs a "ready" entry`);
      continue;
    }
    if (row.web === "—") out.push(`${row.fn}: the web does not plan it, so it cannot be done`);
    if (r.twin !== null) {
      if (!exists(r.twin)) out.push(`${row.fn}: twin test tests/smoke/${r.twin} does not exist`);
    } else if (r.legacy !== true || row.web !== "R1") {
      out.push(`${row.fn}: no twin test; only an R1 function may be waived with legacy: true`);
    }
    if (r.smoke !== undefined && !exists(r.smoke)) out.push(`${row.fn}: smoke test tests/smoke/${r.smoke} does not exist`);
  }
  return out;
}

/** Count of functions per web phase, for the spec's summary line. */
export function phaseCounts(rows: readonly ParityRow[] = PARITY): Record<Phase, number> {
  const out: Record<Phase, number> = { R1: 0, R2: 0, R3: 0, R4: 0, R5: 0, "—": 0 };
  for (const row of rows) out[row.web] += 1;
  return out;
}
