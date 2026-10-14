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

export interface ParityRow {
  readonly area: string;
  readonly fn: string;
  readonly bot: string;
  readonly web: Phase;
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
    routes: ["POST /v1/auth/login", "POST /v1/auth/refresh", "POST /v1/auth/logout"],
  },
  {
    area: "Вход и организация",
    fn: "Забыли пароль",
    bot: "—",
    web: "R1",
    routes: ["POST /v1/auth/password-reset/request"],
    note: "Ссылка из письма пока ведёт на APP_PUBLIC_URL (задача A2).",
  },
  {
    area: "Вход и организация",
    fn: "Выбор организации",
    bot: "bot.go, `org:`",
    web: "R1",
    routes: ["GET /v1/me"],
  },
  {
    area: "Вход и организация",
    fn: "Роль и права в организации",
    bot: "bot.go (`canViewSales`, `isOwner` по роли из /v1/me)",
    web: "R1",
    routes: [`GET ${O}/me`],
    note: "Бот пока решает по роли. Веб решает по правам A1.",
  },
  {
    area: "Вход и организация",
    fn: "Язык интерфейса",
    bot: "bot.go, `lang:`",
    web: "R1",
    routes: [],
    note: "Хранится на устройстве.",
  },
  {
    area: "Вход и организация",
    fn: "Заметка «оплата не подключена»",
    bot: "payments.go",
    web: "R3",
    routes: [`GET ${O}/payment-configs`],
  },
  // ── Мероприятия ─────────────────────────────────────────────────────
  {
    area: "Мероприятия",
    fn: "Список мероприятий",
    bot: "events_list.go, `el:`",
    web: "R1",
    routes: [`GET ${O}/events`],
  },
  {
    area: "Мероприятия",
    fn: "Цифры продаж в списке",
    bot: "— (в боте только на карточке)",
    web: "R1",
    routes: [`GET ${O}/events/{event_id}/summary`],
    note: "Один запрос на строку. Нужен пакетный маршрут.",
  },
  {
    area: "Мероприятия",
    fn: "Карточка мероприятия с сеансами",
    bot: "event_card.go, `ec:o:`",
    web: "R1",
    routes: [`GET ${O}/events`, `GET ${O}/events/{event_id}/summary`, `GET ${O}/events/{event_id}/sessions`],
  },
  {
    area: "Мероприятия",
    fn: "Сводка мероприятия",
    bot: "event_summary.go, `ec:es:`",
    web: "R1",
    routes: [`GET ${O}/events/{event_id}/summary`],
  },
  {
    area: "Мероприятия",
    fn: "CSV продаж мероприятия",
    bot: "event_csv.go, `ec:ce:`",
    web: "R1",
    routes: [`GET ${O}/events/{event_id}/sales.csv`],
  },
  {
    area: "Мероприятия",
    fn: "Образец билета (PDF)",
    bot: "sample.go, `sample:`",
    web: "R1",
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
    fn: "Публикация, снятие с продажи, архив",
    bot: "evstatus.go, `ec:ev:`",
    web: "R4",
    routes: ["GET /v1/events/{id}", `POST ${O}/events/{id}/status`],
  },
  {
    area: "Мероприятия",
    fn: "Удаление мероприятия",
    bot: "evstatus.go, `ec:ev:…:del` (слово)",
    web: "R4",
    routes: [`GET ${O}/events/{event_id}/delete-impact`, `DELETE ${O}/events/{id}`],
  },
  {
    area: "Мероприятия",
    fn: "Уведомления о продажах",
    bot: "notifications.go, `ec:nt`",
    web: "R1",
    routes: [],
    note: "Только ссылка на @ArenaSoldOutSalesBot.",
  },
  // ── Сеансы ──────────────────────────────────────────────────────────
  {
    area: "Сеансы",
    fn: "Список сеансов и карточка сеанса",
    bot: "sessions_dialog.go, `ses:list:`, `ses:o:`",
    web: "R1",
    routes: [`GET ${O}/events/{event_id}/sessions`, `GET ${O}/sessions/{session_id}/summary`],
  },
  {
    area: "Сеансы",
    fn: "Сводка сеанса",
    bot: "event_summary.go, `ec:ss:`, `ses:sm`",
    web: "R1",
    routes: [`GET ${O}/sessions/{session_id}/summary`],
  },
  {
    area: "Сеансы",
    fn: "CSV продаж и сводки сеанса",
    bot: "event_csv.go, `ec:cs:`, `ec:cm:`, `ses:csv`",
    web: "R1",
    routes: [`GET ${O}/sessions/{session_id}/sales.csv`, `GET ${O}/sessions/{session_id}/summary.csv`],
  },
  {
    area: "Сеансы",
    fn: "Перенос сеанса",
    bot: "sessions_dialog.go, `ses:mv`, `ses:go`",
    web: "R4",
    routes: [`GET ${O}/sessions/{session_id}/change-impact`, `PATCH ${O}/events/{event_id}/sessions/{id}`],
  },
  {
    area: "Сеансы",
    fn: "Отмена сеанса",
    bot: "sessions_dialog.go, `ses:cx`, `ses:cxall` (слово)",
    web: "R4",
    routes: [`GET ${O}/sessions/{session_id}/change-impact`, `PATCH ${O}/events/{event_id}/sessions/{id}`],
  },
  {
    area: "Сеансы",
    fn: "Контакт организатора для писем",
    bot: "sessions_dialog.go (`sesSaveContact`)",
    web: "R4",
    routes: [`PUT ${O}/events/{event_id}/contact`],
  },
  {
    area: "Сеансы",
    fn: "Конец продаж и «вход с»",
    bot: "sessions_sale_times.go, `ses:se`, `ses:dr`",
    web: "R4",
    routes: [`PATCH ${O}/events/{event_id}/sessions/{id}`],
  },
  // ── Категории ───────────────────────────────────────────────────────
  {
    area: "Категории",
    fn: "Категории сеанса (чтение)",
    bot: "categories.go, `ct:e:`, `ct:v:`",
    web: "R1",
    routes: [`GET ${O}/events/{event_id}/sessions/{session_id}/tiers`],
  },
  {
    area: "Категории",
    fn: "Открыть или закрыть продажи категории",
    bot: "categories.go, `ct:op`, `ct:cl`",
    web: "R4",
    routes: [`PATCH ${O}/events/{event_id}/sessions/{session_id}/tiers/{id}`],
  },
  {
    area: "Категории",
    fn: "Цена, окно продаж, количество",
    bot: "categories.go, `ct:pr`, `ct:wn`, `ct:qt`",
    web: "R4",
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
    routes: [`POST ${O}/orders/search`],
    note: "Поиск идёт в теле запроса, не в адресе: так e-mail и телефон покупателя не попадают в журналы прокси. Бот тоже.",
  },
  {
    area: "Заказы",
    fn: "Карточка заказа и статус письма с билетами",
    bot: "order_card.go, `or:v:`",
    web: "R1",
    routes: [`GET ${O}/orders/{id}`],
  },
  {
    area: "Заказы",
    fn: "Отмена неоплаченного заказа",
    bot: "order_card.go, `or:c:` (слово)",
    web: "R3",
    routes: [`POST ${O}/orders/{id}/cancel`],
  },
  {
    area: "Заказы",
    fn: "Повторная отправка билетов",
    bot: "resend.go, `or:r:`",
    web: "R3",
    routes: [`POST ${O}/orders/{order_id}/resend-tickets`],
  },
  // ── Возвраты ────────────────────────────────────────────────────────
  {
    area: "Возвраты",
    fn: "Возврат по заказу",
    bot: "refund_dialog.go, `or:f:` (слово и кнопка)",
    web: "R5",
    routes: [`GET ${O}/orders/{order_id}/refund-options`, `POST ${O}/orders/{order_id}/refunds`],
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
    routes: [`POST ${O}/promo-codes`, `PATCH ${O}/promo-codes/{id}`, `DELETE ${O}/promo-codes/{id}`],
  },
  // ── Пригласительные ─────────────────────────────────────────────────
  {
    area: "Пригласительные",
    fn: "Выдача пригласительных",
    bot: "invite.go, `iv:i`",
    web: "R3",
    routes: [`POST ${O}/complimentary`],
  },
  {
    area: "Пригласительные",
    fn: "Список и аннулирование",
    bot: "invite_list.go, `iv:l`, `iv:rv` (слово)",
    web: "R3",
    routes: [`GET ${O}/complimentary`, "POST /v1/complimentary/{id}/revoke"],
  },
  // ── Команда ─────────────────────────────────────────────────────────
  {
    area: "Команда",
    fn: "Команда, приглашение, удаление",
    bot: "team.go, `team:`",
    web: "R3",
    routes: [
      `GET ${O}/bot-team`,
      `POST ${O}/bot-invitations`,
      `DELETE ${O}/bot-invitations/{id}`,
      `POST ${O}/bot-invitations/{id}/resend`,
      `DELETE ${O}/members/{user_id}`,
    ],
    note: "Ждёт A2: приглашение с каналом web или telegram.",
  },
  // ── Промоутеры ──────────────────────────────────────────────────────
  {
    area: "Промоутеры",
    fn: "Список, правка, архив",
    bot: "promoters.go, `pr:`",
    web: "R3",
    routes: [`GET ${O}/promoters`, `PATCH ${O}/promoters/{id}`],
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
    note: "В вебе: письмо с установкой пароля (A2).",
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
    "| Раздел | Функция | Бот | Веб | Маршруты API | Примечание |",
    "|---|---|---|---|---|---|",
  ];
  for (const row of rows) {
    const routes = row.routes.length === 0 ? "—" : row.routes.map((r) => `\`${r}\``).join("<br>");
    lines.push(
      `| ${cell(row.area)} | ${cell(row.fn)} | ${cell(row.bot)} | ${row.web} | ${cell(routes)} | ${cell(row.note ?? "")} |`,
    );
  }
  return lines.join("\n");
}

/** Count of functions per web phase, for the spec's summary line. */
export function phaseCounts(rows: readonly ParityRow[] = PARITY): Record<Phase, number> {
  const out: Record<Phase, number> = { R1: 0, R2: 0, R3: 0, R4: 0, R5: 0, "—": 0 };
  for (const row of rows) out[row.web] += 1;
  return out;
}
