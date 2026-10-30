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
    fn: "Вход из мини-приложения Telegram",
    bot: "кнопка web_app «Схема» и другие, `/tg?next=<путь>`",
    web: "R1",
    ready: { twin: null, legacy: true, smoke: "tg-enter.mock.spec.ts" },
    routes: ["POST /v1/auth/telegram-miniapp"],
    note: "Страница `/tg` на events.arenasoldout.com: подписанные данные запуска (`Telegram.WebApp.initData`) обмениваются на сессию 30 минут (S3b). `next` — только путь вида `/o/<uuid>/...`: страница отбрасывает чужой адрес до запроса и ещё раз перед переходом, сервер проверяет его сам, а подписанный `start_param` сильнее. Кнопка «Назад» Telegram идёт по истории приложения. Каждый отказ сервера (данные устарели, аккаунт не привязан, нет ключа и др.) показан понятным текстом на en/ru/es. Живой двойник появится, когда маршрут сервера войдёт в master.",
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
    fn: "Материалы страницы продаж: галерея, видео, где ещё купить, адрес и точка, фотоотчёт",
    bot: "— (редакторы бота: PR #171, не слит)",
    web: "R4",
    // sales page materials
    ready: { twin: "live/materials.spec.ts" },
    routes: [
      `GET ${O}/events/{event_id}/media-items`,
      `POST ${O}/events/{event_id}/media-items`,
      `PUT ${O}/events/{event_id}/media-items/order`,
      `PATCH ${O}/events/{event_id}/media-items/{item_id}`,
      `DELETE ${O}/events/{event_id}/media-items/{item_id}`,
      `GET ${O}/events/{event_id}/videos`,
      `POST ${O}/events/{event_id}/videos`,
      `PUT ${O}/events/{event_id}/videos/order`,
      `DELETE ${O}/events/{event_id}/videos/{video_id}`,
      `GET ${O}/events/{event_id}/partner-links`,
      `POST ${O}/events/{event_id}/partner-links`,
      `PUT ${O}/events/{event_id}/partner-links/order`,
      `PATCH ${O}/events/{event_id}/partner-links/{link_id}`,
      `DELETE ${O}/events/{event_id}/partner-links/{link_id}`,
      `GET ${O}/events/{event_id}/map-point`,
      `PUT ${O}/events/{event_id}/map-point`,
      `DELETE ${O}/events/{event_id}/map-point`,
      `GET ${O}/events/{event_id}/photo-report`,
      `POST ${O}/events/{event_id}/photo-report`,
      `PUT ${O}/events/{event_id}/photo-report/order`,
      `PATCH ${O}/events/{event_id}/photo-report/{photo_id}`,
      `DELETE ${O}/events/{event_id}/photo-report/{photo_id}`,
      `PATCH ${O}/venues/{id}`,
    ],
    note: "Раздел «Материалы страницы продаж» на карточке мероприятия. Читать может `event.read`, менять — `event.update` (сервер проверяет каждый вызов), загрузка фото — ещё `media.write`, адрес площадки — `venue.update`. Лимиты 6 / 4 / 3 / 30. Адрес берётся с площадки сеанса, а не из юридического адреса продавца. Видео: изменение ссылки — добавить новую, затем удалить старую (PATCH у API нет). Фотоотчёт публичен только после окончания последнего сеанса (решает сервер).",
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
      `POST ${O}/texts/format`,
    ],
    note: "Сначала сухой прогон, потом одно нажатие. Заказы сайта-партнёра блокируют перенос, текст честный. RT-11: сообщение покупателям — редактор с профилем session_message (1000 видимых знаков, 3 ссылки), уходит как `notice.message` с `message_format: \"art\"`, письма выводят его через `richtext.Render`.",
  },
  {
    area: "Сеансы",
    fn: "Отмена сеанса",
    bot: "sessions_dialog.go, `ses:cx`, `ses:cxall` (слово)",
    web: "R4",
    ready: { twin: "live/session-change.spec.ts" },
    routes: [`GET ${O}/sessions/{session_id}/change-impact`, `PATCH ${O}/events/{event_id}/sessions/{id}`, `POST ${O}/texts/format`],
    note: "Только со словом сеанса. Письма уходят, деньги не возвращаются, заказы остаются оплаченными. RT-11: сообщение покупателям — тот же редактор, что при переносе.",
  },
  {
    area: "Сеансы",
    fn: "Места сеанса на схеме: кисть категорий, закрыть и открыть",
    bot: "— (бот открывает экран «Зал» как мини-приложение, S3b)",
    web: "R4",
    ready: { twin: "live/seats.spec.ts" },
    routes: [
      "GET /v1/event-sessions/{id}/schema",
      "GET /v1/event-sessions/{id}/seats/admin",
      `GET ${O}/sessions/{session_id}/seat-batches`,
      `POST ${O}/sessions/{session_id}/seat-batches`,
      `POST ${O}/sessions/{session_id}/seat-batches/{batch_id}/revert`,
    ],
    note: "S2b. Изменения копятся в лотке и уходят ОДНИМ пакетом по кнопке «Применить»; проданные и места в брони не перекрашиваются (сервер пропускает их тоже). Откат пакета одним нажатием. Касание — основной ввод, `?embed=1` убирает шапку для мини-приложения Telegram. Схема читается публичным маршрутом, поэтому у неопубликованного мероприятия её пока нет (разрыв API).",
  },
  {
    area: "Сеансы",
    fn: "Автораскладка категорий по залу",
    bot: "— (веб и мини-приложение, S2c)",
    web: "R4",
    ready: { twin: "live/seats.spec.ts" },
    routes: [`GET ${O}/sessions/{session_id}/seat-layout/algorithms`, `POST ${O}/sessions/{session_id}/seat-layout/suggest`],
    note: "S2c. Список алгоритмов и форма параметров читаются с сервера, в вебе ничего не зашито. Предпросмотр ничего не пишет; «Перенести как изменения» кладёт места в лоток, дальше обычный пакет. Выручка — «проданные по факту + остаток по новым ценам».",
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
  {
    area: "Заказы",
    fn: "VIP-билет",
    bot: "order_vip.go, `or:vip:`, `iv:vip:`",
    web: "R3",
    // V1
    ready: { twin: "live/vip.spec.ts" },
    routes: [`PATCH ${O}/tickets/{ticket_id}/vip`],
    note: "Отметка видна только команде организации: её нет на PDF-билете и в письмах покупателю. Кнопка видна по праву `ticket.update` в `GET …/me`. Приглашение можно выдать сразу с отметкой (поле `vip` при выдаче).",
  },
  {
    area: "Мероприятия",
    fn: "Статистика страницы мероприятия",
    bot: "page_stats.go, строка на карточке `ec:o:`",
    web: "R2",
    // 2-3
    ready: { twin: "live/page-stats.spec.ts" },
    routes: [`GET ${O}/events/{event_id}/page-stats`, `GET ${O}/events/{event_id}/page-stats.csv`],
    note: "Просмотры страницы, запуски видео и переходы по ссылкам партнёров за 30 дней, с подозрительными (боты) отдельно, и источники (utm). Это ориентир, а не доказательство для рекламного кабинета. Нужно право `event.read`. Блок не показывается, пока никто не открыл страницу. CSV только в вебе.",
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
    ready: { twin: "live/invitations.spec.ts", smoke: "invitations-org.mock.spec.ts" },
    routes: [`POST ${O}/sessions/{session_id}/invitations`],
    note: "Экран «Пригласительные» организации выдаёт по одному заказу на гостя, одно место категории без места (спека 46, INV-04b). Номер запроса свой у каждого гостя и не меняется при повторе. Сеанс со схемой зала открывает экран с местами.",
  },
  {
    area: "Пригласительные",
    fn: "Список по организации, отмена, PDF и повторное письмо",
    bot: "invite_list.go, `iv:l`",
    web: "R3",
    ready: { twin: "live/invitations.spec.ts", smoke: "invitations-org.mock.spec.ts" },
    routes: [`GET ${O}/invitations`],
    note: "Все выдачи организации, кто бы их ни выдал: фильтры по мероприятию и сеансу, вкладки «Действуют / Отменены / Все», постраничное чтение по курсору. Отмена заказа или одного билета, PDF и повторное письмо идут теми же маршрутами, что на экране сеанса (строка ниже).",
  },
  {
    area: "Пригласительные",
    fn: "Прежние пригласительные: список и аннулирование",
    bot: "— (бот читает только заказы)",
    web: "R3",
    ready: { twin: "live/invitations.spec.ts", smoke: "invitations-org.mock.spec.ts" },
    routes: [`GET ${O}/complimentary`, `POST ${O}/complimentary`, "POST /v1/complimentary/{id}/revoke"],
    note: "Пригласительные, выданные старым маршрутом (`complimentary_issuances`), остаются в разделе «Прежние пригласительные» со словом-подтверждением АННУЛИРОВАТЬ. Статус письма гостя (`letters[]`) идёт через билет. Новые выдачи веб старым маршрутом не делает: на сеансе со схемой он отвечает 422 `complimentary.use_invitations_route`.",
  },
  {
    area: "Пригласительные",
    fn: "Пригласительные на места схемы (закрытые места тоже)",
    bot: "Mini App `/tg/invitations` (INV-05, INV-05m)",
    web: "R3",
    ready: { twin: "live/invitations-seats.spec.ts" },
    routes: [
      `POST ${O}/sessions/{session_id}/invitations`,
      `GET ${O}/sessions/{session_id}/invitations`,
      `POST ${O}/invitations/{order_id}/cancel`,
      `GET ${O}/invitations/{order_id}/tickets.pdf`,
      `POST ${O}/invitations/{order_id}/resend`,
    ],
    note: "Спека 46. Отмена «возвращает место» туда, откуда оно взято; «Скачать PDF» — один файл на все места заказа, «Отправить ещё раз» — гостю или на разовый адрес. Мини-приложение Telegram `/tg/invitations` (INV-05m): те же части, что у веб-экрана, в раскладке телефона и без CSV (вместо него ссылка «Открыть в браузере»). Вход — одноразовый код назначения `miniapp`, как у редактора. «Скачать PDF» в Telegram идёт через системное меню «Поделиться» окна, а при его отсутствии обычной загрузкой с подсказкой: `Telegram.WebApp.downloadFile` не годится, потому что клиент Telegram сам скачивает адрес и не может передать заголовок `Authorization`, а адреса без заголовка у маршрута PDF пока нет.",
  },
  {
    area: "Пригласительные",
    fn: "Пригласительные из CSV-файла (проверка, выдача, файл с результатом)",
    bot: "— (Mini App, INV-05)",
    web: "R3",
    routes: [
      `POST ${O}/sessions/{session_id}/invitation-batches`,
      `GET ${O}/invitation-batches/{id}`,
      `GET ${O}/invitation-batches/{id}/result.csv`,
    ],
    note: "Спека 46, INV-06b. Загрузка multipart: сначала проверка (`dry_run=true`, ничего не пишет), потом выдача с `request_id` на файл и способ доставки, повтор после обрыва идёт с тем же id. Предел файла: 100 гостей, 250 мест, 512 КБ. Проверено мок-тестом `invitations-batch.mock.spec.ts`. Живого двойника пока нет: готовность функции не отмечена.",
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
  // ── Ссылки и QR ─────────────────────────────────────────────────────
  {
    area: "Ссылки и QR",
    fn: "Список, карточка, QR, статистика",
    bot: "sales_link_list.go, sales_link_dialog.go, `ql:list`, `ql:v`, `ql:st`, `ql:qr`",
    web: "R3",
    // QL-12 (спека 43 §5.3)
    ready: { twin: "live/sales-links.spec.ts" },
    routes: [`GET ${O}/sales-links`, `GET ${O}/sales-links/{id}`, `GET ${O}/sales-links/{id}/stats`, `GET ${O}/sales-links/{id}/qr`],
    note: "Список с фильтрами активные / архив / все и по мероприятию, курсорная пагинация API. Карточка: большой QR, «Скачать PNG» и «Скачать SVG», короткий адрес с «Скопировать», «Сейчас ведёт на: …» и причина из `destination_reason`, статистика по дням и странам. Выручка только на карточке: ответ списка её не несёт. Право `sales_link.read`.",
  },
  {
    area: "Ссылки и QR",
    fn: "Создание, перенацелить, правка, архив",
    bot: "sales_link_dialog.go, `ql:e`, `ql:s`, `ql:new`, `ql:rt`, `ql:ar`, `ql:ua`",
    web: "R3",
    // QL-12 (спека 43 §5.3)
    ready: { twin: "live/sales-links.spec.ts" },
    routes: [`POST ${O}/sales-links`, `PATCH ${O}/sales-links/{id}`],
    note: "Те же шаги, что в боте: цель (мероприятие, сеанс, страница организации или промоутера, свой адрес), размещение, подпись. Кнопка «QR / ссылка» на карточках мероприятия и сеанса открывает создание с выбранной целью. Перенацелить подтверждается одним нажатием, та же цель — без запроса. Правка подписи, размещения и меток utm только в вебе (PATCH шлёт весь набор utm). Архив и возврат. Право `sales_link.manage`.",
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
      `POST ${O}/texts/format`,
    ],
    note: "RT-11: описание — редактор с профилем description (жирный, курсив, подчёркнутый, ссылки, списки, заголовки, цитата), черновик хранит текст Arena (ART) и шлёт `descriptionFormat: \"art\"`; длину считает по видимому тексту (4000), кнопка «Оформить» — `POST …/texts/format`, при `ai_applied: false` нейтральная строка, без слов об ошибке. Подсказки афиши входят как простой текст, переведённый в ART. 6-1a: форма из двух разделов с проверкой на лету (`?dry_run=true`, не чаще раза в секунду, ошибки по `errors[].field`), черновик в браузере. Под флагом сборки `VITE_FEATURE_WIZARD=1`. Даты, билеты и сохранение — 6-1b и 6-1c.",
  },
  // RT-11
  {
    area: "Мероприятия",
    fn: "Текст в письме",
    bot: "textfmt_note.go, `tf:n`, `tf:e`, `tf:d`, `tf:dy`, `tf:t`",
    web: "R4",
    ready: { twin: "live/email-note.spec.ts" },
    routes: [
      `GET ${O}/events`,
      `PATCH ${O}/events/{id}`,
      `POST ${O}/events/{event_id}/email-note/test`,
      `POST ${O}/texts/format`,
    ],
    note: "RT-11: раздел на карточке мероприятия (право `event.update`): редактор с профилем email_note (2000 видимых знаков, 5 ссылок, только https), «Сохранить» — один PATCH с `email_note` и `email_note_format: \"art\"`, «Удалить» — после вопроса, «Тестовое письмо себе» — черновик уходит на адрес вошедшего (422 `event.email_note_empty` и 429 имеют свои строки). Экран прямо говорит, что приветствие, дату и площадку письмо добавляет само: подстановок `{name}`, `{date}` в Arena нет (решение владельца, спека 44, п. 9.3). Текст читается из списка мероприятий: только он отдаёт `email_note`.",
  },
  // RT-12b
  {
    area: "Мероприятия",
    fn: "Описание в мини-приложении Telegram",
    bot: "textfmt_miniapp.go `tf:m`, textfmt_editor.go",
    web: "R4",
    ready: { twin: "live/tg-edit.spec.ts" },
    routes: [
      "GET /v1/me",
      `GET ${O}/me`,
      `GET ${O}/events`,
      `PATCH ${O}/events/{id}`,
      `POST ${O}/texts/format`,
    ],
    note: "RT-12b: страница `/tg/edit` на events.arenasoldout.com, кнопка бота «Править в редакторе» под предпросмотром описания. Вход — одноразовый код назначения `miniapp` (`POST /v1/auth/web/link`, сессия 30 минут). Мероприятие из ссылки не доверенное: страница ищет его в списках организаций самого человека, а чужое или без права `event.update` показывает как «Нет доступа к этому мероприятию». «Сохранить» — ОДИН PATCH мероприятия с `description` и `description_format: \"art\"` и больше ничем: название, афиша, даты, билеты и переводы остаются как были. После сохранения `Telegram.WebApp.close()`, вне Telegram — «Сохранено» и ссылка на мероприятие. Бот при следующем действии организатора перечитывает мероприятие и один раз говорит «Изменения из редактора сохранены».",
  },
  // RT-12b
  {
    area: "Мероприятия",
    fn: "Текст в письме в мини-приложении Telegram",
    bot: "textfmt_miniapp.go `tf:m:<id>`, textfmt_editor.go",
    web: "R4",
    ready: { twin: "live/tg-edit.spec.ts" },
    routes: [
      "GET /v1/me",
      `GET ${O}/me`,
      `GET ${O}/events`,
      `PATCH ${O}/events/{id}`,
      `POST ${O}/events/{event_id}/email-note/test`,
      `POST ${O}/texts/format`,
    ],
    note: "RT-12b: тот же экран, что «Текст в письме» на карточке (`EmailNoteSection`: сохранить, удалить после вопроса, тестовое письмо себе), открытый из бота на странице `/tg/edit`. Закрывает окно Telegram только после «Сохранить», не после тестового письма и не после удаления.",
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
  // M2
  {
    area: "Заказы",
    fn: "Проходки в заказе",
    bot: "order_card.go, `or:v:` (строка билета)",
    web: "R3",
    ready: { twin: "live/scans.spec.ts" },
    routes: [`GET ${O}/orders/{id}`],
    note: "У билета до пяти последних сканов: время, итог (прошёл или отказ), кто сообщил (сканер Arena или МАКС), вход. Первый вход одинаково берётся из сканера Arena и из МАКС (`used_at`).",
  },
  // M2
  {
    area: "Заказы",
    fn: "Билеты сеанса и фильтр «Не прошли»",
    bot: "session_not_entered.go, `ses:ne:`",
    web: "R4",
    ready: { twin: "live/scans.spec.ts" },
    routes: [`GET ${O}/sessions/{session_id}/tickets`],
    note: "Постраничный список билетов сеанса: номер, заказ, покупатель, вход. «Не прошли» — активные билеты без входа. Ключ организации сюда не допущен (имена и e-mail покупателей). Столбцы «Время входа», «Источник входа», «Сканер / вход» есть и в файле продаж.",
  },
  // ── Биллинг ─────────────────────────────────────────────────────────
  {
    area: "Биллинг",
    fn: "Использование: что платформа начислила организации",
    bot: "— (только веб)",
    web: "R3",
    ready: { twin: "live/billing.spec.ts" },
    routes: [`GET ${O}/billing/charges`, `GET ${O}/billing/events/{event_id}/summary`],
    note: "B2-7: «Биллинг → Использование», только чтение. Видят те, у кого в организации есть `billing.read` (владелец); менеджер экрана не видит и запросов не шлёт. Цифры предварительные: счета пока не выставляются, исправление приходит строкой сторно. Суммы по валютам, итог за месяц считает API по всему месяцу.",
  },
  {
    area: "Биллинг",
    fn: "Баланс кредитов и пакеты на продажу",
    bot: "— (только веб)",
    web: "R3",
    ready: { twin: "live/billing-packs.spec.ts" },
    routes: [`GET ${O}/billing/balance`, `GET ${O}/billing/packs`],
    note: "B3-3: «Биллинг → Баланс и пакеты». Видят те, у кого есть `billing.read`. Остаток по валютам (купленные и бонусные кредиты, ближайшее сгорание бонуса, текущая ставка), пакеты прайс-листа с комиссией платёжной системы при оплате картой. Менеджер экрана не видит и запросов не шлёт.",
  },
  {
    area: "Биллинг",
    fn: "Заказ пакета кредитов: банковский перевод или карта",
    bot: "— (только веб)",
    web: "R3",
    ready: { twin: "live/billing-packs.spec.ts" },
    routes: [`POST ${O}/billing/packs/{pack_id}/order`, `POST ${O}/billing/account-invoices/{invoice_id}/pay-by-card`],
    note: "B3-3: только владелец (`billing.purchase`, ключ API его не получает). Один заказ — один `Idempotency-Key`: повтор после потерянного ответа идёт с тем же ключом и второго счёта не создаёт. Банковский перевод показывает номер счёта и код платежа; карта открывает страницу Stripe в той же вкладке и возвращается на страницу баланса (`?invoice=…&paid=1` или `&cancelled=1`). Кредиты появляются только после подтверждения оплаты сервером.",
  },
  {
    area: "Биллинг",
    fn: "Счета платформы и журнал кредитов",
    bot: "— (только веб)",
    web: "R3",
    ready: { twin: "live/billing-packs.spec.ts" },
    routes: [`GET ${O}/billing/account-invoices`, `GET ${O}/billing/account-invoices/{invoice_id}`, `GET ${O}/billing/ledger`],
    note: "B3-3: «Биллинг → Счета»: список с фильтром по состоянию, карточка счёта (позиции, итоги, код платежа, срок), кнопка «Оплатить картой» для открытого карточного счёта, ниже журнал кредитов (новые сверху, знак по типу записи).",
  },
  {
    area: "Биллинг",
    fn: "Комиссия как сервисный сбор",
    bot: "— (только веб)",
    web: "R3",
    ready: { twin: "live/billing-packs.spec.ts" },
    routes: [`GET ${O}/billing/service-fee-quote`],
    note: "B4: цена билета, валюта и «место на схеме» → комиссия платформы за один билет, которую организатор добавляет как сервисный сбор. Число считает сервер тем же кодом, что начисляет за продажу, веб ничего не считает сам.",
  },
  {
    area: "Биллинг",
    fn: "Данные плательщика в счетах",
    bot: "— (только веб)",
    web: "R3",
    ready: { twin: "live/billing-packs.spec.ts" },
    routes: [`PUT ${O}/billing/payer`],
    note: "B4: форма только для `billing.payer_manage` (владелец). Чтения у API нет, форма начинается с юридических данных организации. Лимиты те же, что у API, проверяются до отправки.",
  },  // ── Магазин (спека 47) ──────────────────────────────────────────────
  {
    area: "Магазин",
    fn: "Товары: список, карточка, создание и правка",
    bot: "— (только веб)",
    web: "R3",
    routes: [
      `GET ${O}/shop/products`,
      `GET ${O}/shop/products/{product_id}`,
      `POST ${O}/shop/products`,
      `PATCH ${O}/shop/products/{product_id}`,
      `DELETE ${O}/shop/products/{product_id}`,
    ],
    note: "Раздел «Магазин → Товары». Видят те, у кого есть `shop.read`, менять могут с `shop.manage` (владелец и менеджер). Вид товара (физический, цифровой, услуга), названия опций и файл цифрового товара (`owner_type=shop_digital`). Описание правится тем же редактором, что описание мероприятия, адрес товара только для чтения.",
  },
  {
    area: "Магазин",
    fn: "Варианты товара и остатки",
    bot: "— (только веб)",
    web: "R3",
    routes: [`PUT ${O}/shop/products/{product_id}/variants`],
    note: "Список вариантов отправляется целиком. Остаток, который человек видел, уходит обратно как `expected_stock`: если полка за это время изменилась, API отвечает 409 `shop.stock_changed`, экран перечитывает варианты и говорит об этом. Карточка товара сохраняется одной кнопкой «Сохранить» (товар, варианты и описания картинок тремя запросами подряд), а остаток можно поправить прямо в списке товаров, в том числе с телефона.",
  },
  {
    area: "Магазин",
    fn: "Картинки товара",
    bot: "— (только веб)",
    web: "R3",
    routes: [`POST ${O}/shop/products/{product_id}/images`, `DELETE ${O}/shop/products/{product_id}/images/{media_id}`, "POST /v1/media"],
    note: "Загрузка идёт через общий медиа-API (`owner_type=shop_product`), порядок меняется повторной привязкой с новой позицией.",
  },
  {
    area: "Магазин",
    fn: "Заказы магазина: список и карточка",
    bot: "— (только веб)",
    web: "R3",
    routes: [`GET ${O}/shop/orders`, `POST ${O}/shop/orders/search`, `GET ${O}/shop/orders/counts`, `GET ${O}/shop/orders/{order_id}`, `GET ${O}/shop/orders/{order_id}/courier-text`],
    note: "Фильтры по статусу, поиск по номеру, e-mail и имени, период. Список читается запросом `POST .../shop/orders/search`: фильтры в теле, строка поиска не попадает в адрес запроса и страницы, а дни периода считаются по поясу магазина (RFC 3339). Над списком быстрые вкладки «Все», «К отправке», «Не оплачены», «Отправлены», «Возвраты» (параметр `view`), числа на двух из них берутся одним запросом `counts`. «Скопировать для курьера» кладёт в буфер обмена текст, который собирает сервер (тот же, что получает логист в Telegram).",
  },
  {
    area: "Магазин",
    fn: "Заметка к заказу магазина и повторная отправка письма",
    bot: "— (только веб)",
    web: "R3",
    routes: [`PATCH ${O}/shop/orders/{order_id}`, `POST ${O}/shop/orders/{order_id}/resend-email`],
    note: "Заметка видна только команде магазина (покупатель, письма и витрина её не получают) и сохраняется кнопкой «Сохранить заметку» или Ctrl+Enter, с именем автора и временем. Пока текст не сохранён, уход со страницы спрашивает. Если коллега успел сохранить свою заметку, показывается его текст и выбор: заменить или оставить. «Отправить письмо ещё раз» ставит в очередь подтверждение заказа или письмо об отправке, не чаще раза в минуту на заказ (429 `shop.rate_limited`).",
  },
  {
    area: "Магазин",
    fn: "Отправка, отмена и возврат заказа магазина",
    bot: "— (только веб)",
    web: "R3",
    routes: [`POST ${O}/shop/orders/{order_id}/ship`, `POST ${O}/shop/orders/{order_id}/cancel`, `POST ${O}/shop/orders/{order_id}/refund`, `POST ${O}/shop/orders/{order_id}/resolve`],
    note: "Отмена (только неоплаченный заказ) и возврат спрашивают пароль ещё раз (`X-Step-Up-Token`) и слово подтверждения, как возвраты билетов. Возврат по умолчанию на всю оставшуюся сумму и требует права `refund.create` и `refund.approve` рядом с `shop.manage`. Одна операция возврата идёт с одним `Idempotency-Key`, повтор после потерянного ответа не вернёт деньги дважды; после ошибки заказ читается заново и показывается остаток от сервера. Заказ в статусе `manual_review` (оплата пришла после конца брони) можно отправить, и тогда товар спишется в этот момент, или вернуть полностью, и тогда заказ закроется.",
  },
  {
    area: "Магазин",
    fn: "Настройки магазина",
    bot: "— (только веб)",
    web: "R3",
    routes: [`GET ${O}/shop/settings`, `PUT ${O}/shop/settings`, `GET ${O}/channels`],
    note: "Валюта, доставка, страна, возраст, объявление, контакт, канал продаж. Домен и флаг «витрина открыта» только для чтения: меняет их оператор платформы. Список каналов виден тем, у кого есть `channel.read` (владелец).",
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
