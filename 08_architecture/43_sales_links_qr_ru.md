# 43. Отслеживаемые ссылки продаж и QR-коды для афиш (24.10.2026)

Статус: **готова к прогону, этапы 1–3; этапы 4–6 — по мере подхода.** Решения владельца 23–24.10: домен редиректа
`go.arenasoldout.com`; атрибуция «последнее касание, окно 7 дней»; остальное — как в разделе 2. Протокол прогона,
классы сложности, общие правила и стоп-условия — те же, что в `40_event_center_execution_spec_ru.md` (разделы 1–4);
здесь только дополнения. Оркестратор — Claude-сессия; исполнители по классам (haiku / sonnet / opus); разведка и
ревью — через `ops/claude-sdk-runner`, пока действуют промо-кредиты.

Факты о коде в карточках взяты из `origin/master` на 24.10 (83d8692, голова миграций 0166). Ветка
`feat/ec-01-bot-dialogs` устарела, прогон идёт от master.

## 1. Что строим и почему так

Организатор печатает афишу, листовку, табличку у входа — и хочет, чтобы человек навёл камеру и купил билет, а
организатор потом увидел, сколько людей пришло именно с этой афиши. Решение — **отслеживаемая ссылка продаж**
(`sales_link`): короткий код, цель (куда ведёт), подпись (где размещена), UTM. **QR-код — одно из представлений
ссылки**, не отдельная сущность: та же ссылка идёт в био Instagram, пост в Telegram, письмо.

Три решения, которые нельзя переигрывать в прогоне:

1. **QR кодирует короткий адрес редиректа на арене** (`https://go.arenasoldout.com/<code>`), не конечную
   страницу. Напечатанное неизменяемо, а цель меняется: распродано, отменено, переименован slug, сайт переехал.
   Редирект разрешает цель в момент сканирования; организатор может перенацелить уже напечатанный код. Побочно:
   скан считается у нас даже для чужой цели (мерч на стороннем магазине), короткий адрес даёт редкий QR.
2. **Атрибуция заказа — последнее касание в окне 7 дней** (решение владельца). Первое касание сохраняется в json
   для будущих отчётов, но в счётчиках и в «заказах по ссылке» побеждает последнее.
3. **Все продающие поверхности пишут атрибуцию одним и тем же полем** `attribution` на `checkout_sessions`,
   откуда её копирует заказ: виджет (hosted-страница, нативный плагин WordPress `apps/wp-plugin`, любой встроенный
   виджет) — через `checkout/start`; сайты на протоколе Bil24 — через расширение арены в `CREATE_ORDER_EXT`.

Что уже есть и переиспользуется (не изобретать заново): `publicurl.Page(base, orgSlug, promoterSlug, eventSlug)` —
единственная функция «адрес страницы продаж» (`internal/platform/publicurl`, уже отдаёт `public_url` в
`hcatalog/events.go` и в `eventbot.salesLink`); `pagestats` + таблица `event_page_stats` (миграция 0153) — маяк
hosted-страницы уже считает просмотры по дням и парам `utm_source`/`utm_campaign` и уже умеет отделять
подозрительные User-Agent; `github.com/skip2/go-qrcode` уже в `go.mod` (рисует QR на билете,
`delivery/pdf/layout.go`); `eventbot.DialogStore` (`dialogs.go`, миграция 0130) для коротких диалогов бота;
`audit.WithVia` + `X-Client-Channel` для ответа «откуда создана ссылка»; идиома `mint.EAN13` «нарисуй кандидата,
INSERT … ON CONFLICT DO NOTHING, перерисуй» для уникального кода; `hfeed.Handler.enforceRateLimit` и пакет
`internal/platform/ratelimit` для публичного маршрута.

## 2. Решения (владельца и прогона)

| № | Решение | Кто |
|---|---|---|
| 1 | Домен редиректа `go.arenasoldout.com`; адрес печатается под QR текстом | владелец 24.10 |
| 2 | Последнее касание, окно 7 дней; первое касание хранится в json | владелец 24.10 |
| 3 | Умолчания UTM: `utm_source=qr`, `utm_medium=offline`, `utm_campaign=<slug мероприятия или организации>`, `utm_content=<code>`; для размещения `social` — `utm_source=social`, `utm_medium=social`; для `email` — `utm_source=email`, `utm_medium=email`; для `messenger` — `utm_source=messenger`, `utm_medium=social` (Р-QL12); организатор может переписать любое поле | предложение принято 24.10 |
| 4 | Архивная ссылка, удалённая/неопубликованная цель — редирект на страницу всех мероприятий организации (цепочка §4.3), никогда не 404 на бумаге | принято 24.10 |
| 5 | Первая версия: PNG и SVG; PDF-вкладыш для печати — этап 5 | принято 24.10 |
| 6 | Плагины WordPress на протоколе Bil24 — отдельный этап 4 после hosted-страницы; до него сканы считаются, заказы с таких сайтов остаются без привязки к ссылке | принято 24.10 |
| 7 | Кастомные домены организаторов (линия Д спеки 40) **не используются для QR в этой волне**; редирект проектируется host-независимым, чтобы линия Д могла подключить проверенный домен позже без миграции (§7). Когда дойдёт: **только как отдельно оплачиваемая опция** организатора, не часть базового тарифа | владелец 24.10 |
| 8 | Коды: 7 символов из алфавита `abcdefghjkmnpqrstuvwxyz23456789` (без `0 o 1 l i`), `crypto/rand`, сравнение без учёта регистра; никаких «красивых» кодов организатора в v1 | решение прогона |
| 9 | Дедупликация сканов — в памяти процесса (sha256 от `code|ip|ua|день|секрет`, TTL сутки, потолок записей); пересчёт после рестарта допустим: цифры — ориентир организатору, не доказательство для рекламного кабинета (та же формула, что у `pagestats`) | решение прогона |
| 10 | Права `sales_link.read` / `sales_link.manage`: `org_admin`, `organizer`, `platform_superadmin` (та же миграция, парность 532); ключ организации может ими пользоваться (ивент-центр сайта работает ключом), в `apikeys.IsForbiddenScope` НЕ попадают | решение прогона |
| 11 | Статистика ссылки = сканы (редирект) + заказы/билеты/выручка по `orders.sales_link_id`. Отдельный «просмотр страницы по ссылке» не считается: редирект и есть посадка, а `event_page_stats` и так получает строку `qr / <campaign>` через UTM. PK `event_page_stats` не трогаем | решение прогона |

## 3. Модель данных (одна миграция)

Номер — следующий свободный на `origin/master` в момент вливания (правило 42 §1; голова на 24.10 — 0166).

```sql
CREATE TABLE sales_links (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          uuid NOT NULL REFERENCES organizations(id),
    code            text NOT NULL,                      -- 7 символов, lower
    target_kind     text NOT NULL CHECK (target_kind IN ('org_page','promoter_page','event','session','url')),
    event_id        uuid REFERENCES events(id),
    session_id      uuid REFERENCES sessions(id),
    promoter_id     uuid REFERENCES org_promoters(id),
    url             text,                               -- только для target_kind='url', http(s), ≤ 2048
    label           text NOT NULL CHECK (length(label) BETWEEN 1 AND 120),  -- «Афиша у входа в театр»
    placement_kind  text NOT NULL DEFAULT 'poster'
                    CHECK (placement_kind IN ('poster','flyer','sign','social','email','messenger','other')),
    utm             jsonb NOT NULL DEFAULT '{}'::jsonb,  -- ключи utm_source/utm_medium/utm_campaign/utm_content/utm_term, ≤64 символа каждое
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active','archived')),
    scan_count      bigint NOT NULL DEFAULT 0,
    suspect_scan_count bigint NOT NULL DEFAULT 0,
    last_scanned_at timestamptz,
    created_by      uuid REFERENCES users(id),           -- NULL для ключа API
    created_via     text,                                -- telegram_bot / admin_web / site_plugin / api
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (code ~ '^[a-z2-9]{7}$'),
    CHECK ((target_kind = 'event')    = (event_id IS NOT NULL AND session_id IS NULL)
        OR target_kind IN ('session','org_page','promoter_page','url')),
    CHECK ((target_kind = 'session')  = (session_id IS NOT NULL)),
    CHECK ((target_kind = 'promoter_page') = (promoter_id IS NOT NULL)),
    CHECK ((target_kind = 'url')      = (url IS NOT NULL))
);
CREATE UNIQUE INDEX sales_links_code_uq ON sales_links (code);
CREATE INDEX sales_links_org_idx   ON sales_links (org_id, status, created_at DESC);
CREATE INDEX sales_links_event_idx ON sales_links (event_id) WHERE event_id IS NOT NULL;

CREATE TABLE sales_link_hits (            -- сканы по дням, без личных данных
    sales_link_id uuid NOT NULL REFERENCES sales_links(id) ON DELETE CASCADE,
    day           date NOT NULL,
    country       text NOT NULL DEFAULT '',   -- CF-IPCountry, 2 буквы или ''
    hits          bigint NOT NULL DEFAULT 0,
    suspect_hits  bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (sales_link_id, day, country)
);

ALTER TABLE checkout_sessions ADD COLUMN attribution jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE orders ADD COLUMN sales_link_id uuid REFERENCES sales_links(id);
ALTER TABLE orders ADD COLUMN attribution   jsonb NOT NULL DEFAULT '{}'::jsonb;
CREATE INDEX orders_sales_link_idx ON orders (sales_link_id) WHERE sales_link_id IS NOT NULL;

-- права: INSERT INTO permissions ('sales_link.read','sales_link.manage') + гранты org_admin, organizer,
-- platform_superadmin в ЭТОЙ миграции (идиома 0129 + парность 532).
```

Для `session` заполняются и `session_id`, и `event_id` (денормализация для списка «ссылки этого мероприятия»).
`sales_links` живёт при удалении мероприятия: FK без каскада, редирект падает по цепочке §4.3. Удаления ссылок нет —
только `archived`.

Форма `attribution` (одна и та же в `checkout_sessions`, `orders`, `CREATE_ORDER_EXT`):

```json
{
  "ref": "x7k2qpm",                     // код ссылки, может отсутствовать
  "utm_source": "qr", "utm_medium": "offline", "utm_campaign": "koncert-2026", "utm_content": "x7k2qpm", "utm_term": "",
  "landed_at": "2026-10-24T18:02:11Z",  // когда покупатель пришёл по ссылке (последнее касание)
  "first": { "ref": "...", "utm_source": "...", "utm_campaign": "...", "landed_at": "..." }   // первое касание, если отличается (ref, source или campaign)
}
```

Чистка — один пакет `internal/platform/attribution` (`Clean(raw) (Attribution, bool)`): те же правила, что у
`pagestats` (≤ 64 символа, `^[A-Za-z0-9._~-]*$`), `ref` по §2 п. 8, `landed_at` RFC 3339 не старше 7 дней и не из
будущего (иначе касание отбрасывается целиком — окно решения 2 проверяет сервер, не клиент). `ref` переводится в
`orders.sales_link_id` ТОЛЬКО если ссылка существует и принадлежит организации заказа; чужой или неизвестный код
остаётся в json без привязки.

## 4. Поведение сервера

### 4.1 Публичный маршрут редиректа

`GET /go/{code}` на arena-api (вне `/v1`, без авторизации). Хост `go.arenasoldout.com` ведёт на тот же origin;
Traefik-роутер по Host добавляет префикс `/go` (ops-шаг QL-13), поэтому на бумаге адрес `go.arenasoldout.com/x7k2qpm`,
а обработчик один. Маршрут **host-независим**: код разрешается одинаково с любого хоста — задел под §7.

Шаги обработчика: (1) нормализовать код (`lower`, отбросить всё после 7 символов, кроме `?`); не по шаблону → 302
на `publicurl.Page(base, "", "", "")`-подобную «главную витрину» платформы (константа `SHORT_LINK_FALLBACK_URL`,
по умолчанию `https://tickets.arenasoldout.com/`); (2) лимит по IP как у публичных маршрутов (`PUBLIC_API_IP_RATE_LIMIT`,
`TrustedClientIP(r, 0)`); (3) найти ссылку; нет → тот же fallback; (4) разрешить цель по §4.3; (5) к адресу цели
дописать `utm_*` из `sales_links.utm` (пустые не дописывать) и `ref=<code>`; существующие параметры цели `url`
сохраняются дословно (без перекодирования), наши — побеждают при совпадении имён, но заменяется только ключ, который мы реально
дописываем (пустой `utm` ссылки не стирает `utm_source` цели); (6) посчитать скан: `pagestats`-правило подозрительного UA →
`suspect_hits`, иначе дедупликация §2 п. 9 → `hits`; `UPDATE sales_links SET scan_count…, last_scanned_at` +
`INSERT … ON CONFLICT (sales_link_id, day, country) DO UPDATE` в `sales_link_hits`, страна из `CF-IPCountry`
(только когда `TRUSTED_PROXY_COUNT > 0`, иначе `''`); (7) `302 Found`, `Cache-Control: no-store`,
`Referrer-Policy: no-referrer-when-downgrade`. Счётчик пишется ПОСЛЕ формирования ответа и никогда не роняет редирект
(ошибка БД → лог, 302 всё равно). Ошибки сервера на этом маршруте — 503, не 502 (Cloudflare). HEAD обрабатывается
как GET без счёта.

### 4.2 Маршруты организации (`sales_link.read` / `sales_link.manage`)

```
POST   /v1/organizations/{org_id}/sales-links
       {target:{kind, event_id?, session_id?, promoter_id?, url?}, label, placement_kind?, utm?{…}}
       → 201 SalesLink
GET    /v1/organizations/{org_id}/sales-links?event_id=&session_id=&status=active|archived|all&limit=&cursor=
       → {items:[SalesLink], next_cursor}
GET    /v1/organizations/{org_id}/sales-links/{id}             → SalesLink
PATCH  /v1/organizations/{org_id}/sales-links/{id}             {label?, placement_kind?, utm?, target?, status?}
GET    /v1/organizations/{org_id}/sales-links/{id}/qr?format=png|svg&size=256..2048&margin=2..8
       → image/png | image/svg+xml, Cache-Control: private, max-age=3600
GET    /v1/organizations/{org_id}/sales-links/{id}/stats?from=&to=
       → {scans_total, suspect_total, by_day:[{day, hits, suspect_hits}], by_country:[{country, hits}],
          orders:{count, tickets, revenue:[{currency, amount_minor}]}}
```

`SalesLink` = поля таблицы + `short_url` (`SHORT_LINK_BASE_URL` + `/` + code) + `destination_url` (разрешённая
сейчас цель, §4.3, без utm — чтобы организатор видел, куда ведёт) + `orders_count` (только в списке и карточке,
подзапрос). `target.kind` в openapi — **plain string с перечислением в description**, не `enum` (ловушка
переименования констант, AGENTS.md); то же для `placement_kind` и `status`.

Валидация: цель должна принадлежать организации (`event_id`/`session_id`/`promoter_id` проверяются JOIN'ом по
`org_id`; чужая → 422 `sales_link.invalid_target`, не 404); `url` — только `http`/`https`, без учётных данных в
адресе, ≤ 2048 (`sales_link.invalid_url`); `label` 1–120 (`sales_link.invalid_label`); `utm` — через
`attribution.Clean` (`sales_link.invalid_utm`). `PATCH target` — перенацеливание напечатанного кода, пишет
`audit.Event` `v1.sales_link.retarget` с прежней и новой целью. Нет `DELETE`; `status=archived` через PATCH.
Чужая ссылка — собственный 404 `sales_link.not_found` (правило `orgread`: отказ читается как отсутствие).

Выручка в `stats` считается по правилам `session_summary`: `orders.total` по заказам, которые когда-либо были
оплачены (`paid`, `partially_refunded`, `refunded`), минус `refunds.status='succeeded'`, по валютам; билеты —
`count(tickets)` этих заказов. Один SQL, не цикл.

### 4.3 Разрешение цели (цепочка падения)

```
session       → сеанс жив (не deleted, не cancelled, мероприятие published) ? page(event) + "?session=<id>" : ↓
event         → мероприятие published и slug есть ? publicurl.Page(base, org, promoter, slug) : ↓
promoter_page → промоутер не archived и slug есть ? publicurl.Page(base, org, promoter, "") : ↓
org_page      → publicurl.Page(base, orgSlug, "", "")
url           → как есть (без проверок доступности)
archived      → сразу org_page
```

`promoter` в `publicurl.Page` — промоутер МЕРОПРИЯТИЯ (как в `hcatalog/events.go`), не произвольный. Ступень
`promoter_page` при падении с `event`/`session` берёт промоутера ссылки, если он задан (у ссылок `event`/`session` его нет),
иначе промоутера мероприятия; без промоутера — сразу `org_page`. Достижимость hosted-страницы (канал с
`hosted_page.enabled`, живой feed token) цепочка НЕ проверяет — только `published` и slug (Р-QL11). Функция
`salelinks.Resolve(ctx, q, link) (destination string, degraded bool)` — чистая по данным, с юнит-тестами на каждую
ступень; `degraded` отдаётся в `destination_url` карточки флагом `destination_degraded`, чтобы бот и веб показали
«ведёт на страницу организации: мероприятие снято с продажи».

### 4.4 Атрибуция в checkout/start и в шлюзе

`PublicFeedCheckoutStartRequest.attribution` (объект §3, optional) и то же поле в `checkout/recover` НЕ нужно
(recover продолжает существующую сессию). `HandlePublicFeedCheckoutStart` (`hfeed/public_feed_checkout.go`) читает
его, чистит, разрешает `ref` → `sales_link_id` **на пуле до `BeginTx`** (правило AGENTS.md о чтениях в транзакции
холда), пишет json в `InsertCheckoutSessionWithToken` (новый параметр) и передаёт `SalesLinkID`/`Attribution` в
`ordering.CreateInput` из `createPublicOrder`. `ordering.CreateOrderFromCheckout` копирует их на заказ. Пустой или
невалидный объект — не ошибка: покупка не должна сорваться из-за метки.

Шлюз: `CREATE_ORDER_EXT` принимает необязательное поле `attribution` (расширение арены, рядом с `complimentary` в
`hbil24/cmd_order_create.go`), то же разрешение `ref` в пределах организации канала, запись в
`InsertCheckoutSessionWithToken`/заказ тем же кодом (общий помощник в `attribution`, не копия). Повторный
CREATE_ORDER_EXT той же корзины с другим `attribution` — последнее побеждает (касание обновилось), без `-2`.

### 4.5 Сводка сеанса «по источникам»

`GET …/sessions/{session_id}/summary` (`horders/session_summary.go`, `queries/session_summary.sql`) получает блок
`sources: [{kind:'sales_link'|'utm'|'direct', sales_link_id?, label?, utm_source?, utm_campaign?, orders, tickets,
revenue:[{currency, amount_minor}]}]` — группировка оплаченных заказов сеанса по `sales_link_id`, затем по
`attribution->>'utm_source'`/`utm_campaign` для заказов без ссылки, остаток — `direct`. Один SQL. Это единственный
отчёт «по каналам» в v1; отдельного экрана аналитики нет.

## 5. Клиенты

### 5.1 Hosted-страница и виджет

- `apps/widget/src/lib/attribution.ts`: при монтировании читает `ref`, `utm_*` из `window.location.search`; если
  что-то есть — пишет `{…, landed_at: now}` в `localStorage` под ключом `arena_attr:<feedToken>` с `expires_at`
  = +7 дней, перенося прежнюю запись в `first` (если `ref`/`utm_source` отличаются); если в адресе ничего нет —
  читает сохранённое и отбрасывает просроченное. Каждое чтение/запись в try/catch (приватное окно). Scope по
  `feedToken`, не по мероприятию: один канал = один сайт, а касание с афиши организации должно дожить до покупки
  на другом его мероприятии. Виджет отдаёт объект в `startCheckout` (`api.ts`) как `attribution`. Виджет НИЧЕГО
  не вырезает из адреса (как `route.ts` страницы: `checkout_token` живёт в том же query).
- Предвыбор сеанса: `?session=<uuid>` в адресе страницы мероприятия → `render.ts` передаёт `session-id` виджету
  (атрибут уже существует, `ArenaTickets.svelte` `sessionId`); неизвестный/чужой id игнорируется молча (виджет уже
  фильтрует `parseSessionId`). Чипы остаются, сеанс просто выбран.
- Нативный плагин `apps/wp-plugin/arena-events` встраивает тот же виджет (`class-widget.php`, блок
  `arena-tickets`) — атрибуция приходит бесплатно; в плагине ничего не меняется, только проверка в приёмке QL-7.

### 5.2 Telegram-бот (`internal/platform/eventbot`)

- Карточка мероприятия (`event_card.go`): кнопка «QR / ссылка» → `ql:e:<eventID>`; карточка сеанса — `ql:s:<sessionID>`;
  главное меню — «Ссылки и QR» (`ql:menu`): список ссылок организации с цифрами и «+ Новая».
- Диалог создания на `DialogStore` (kind `sales_link`, TTL 30 мин, состояние `{target, label, placement, msg_id}`):
  **шаг 1 цель** — если вошли с карточки, цель уже выбрана, иначе кнопки: «Это мероприятие», «Сеанс…» (чипы дат),
  «Все мероприятия» (страница организации или промоутера), «Другое мероприятие…» (список), «Свой адрес» (ввод url;
  это путь для мерча); **шаг 2 где разместите** — кнопки `placement_kind` + свободный текст подписи (одно сообщение:
  «Афиша у входа»); **шаг 3 готово** — бот создаёт ссылку, присылает PNG 1024 (как фото) + SVG (как документ) +
  короткий адрес текстом + куда ведёт сейчас; кнопки «Статистика», «Перенацелить», «В архив». UTM не спрашиваются
  (умолчания §2 п. 3); правка UTM — только в вебе.
- «Статистика»: сканы всего / за 7 дней / сегодня, заказы, билеты, выручка; «Перенацелить» — тот же выбор цели, что в
  шаге 1, с подтверждением одним нажатием (не деньги и не отмена — слово не требуется).
- Тексты `bot.ql.*` (`ql_keys.go`, en + ru + es; `TestEventBot_LocaleBundleHasEveryKey`). Стандарт текстов
  `docs/text_style_standard_ru.md`.
- `ArenaClient` получает методы `CreateSalesLink`, `ListSalesLinks`, `SalesLinkStats`, `UpdateSalesLink`,
  `SalesLinkQR(format)`; `X-Client-Channel: telegram_bot` уже шлётся.

### 5.3 Веб-ивент-центр (`apps/event-center`)

Маршруты `/o/$orgId/links` (список: подпись, цель, где, сканы, заказы, выручка; фильтр по мероприятию) и
`/o/$orgId/links/$linkId` (карточка: QR большой, скачать PNG/SVG, адрес с кнопкой «скопировать», статистика по дням,
перенацелить, UTM-поля, архив); кнопка «QR / ссылка» на `EventScreen.tsx` и `SessionScreen.tsx` открывает создание
с предвыбранной целью. Строка в `parity/matrix.ts`, близнец-тест в `tests/smoke` («создать ссылку → редирект ведёт
на страницу мероприятия → скан посчитан»).

### 5.4 Сайты на протоколе Bil24 (внешние репозитории lampyrisevents, vinoandco)

Общий фрагмент `arena-attribution.php` (mu-plugin или часть `bil24-*` плагина): на любой странице читает `ref` и
`utm_*` из query, пишет cookie `arena_attr` (JSON §3, 7 дней, `SameSite=Lax`, без личных данных), при повторном
касании переносит прежнее в `first`; в `CREATE_ORDER_EXT` добавляет `attribution` из cookie. Экран ивент-центра
сайта («Ссылки и QR») — те же маршруты §4.2 ключом организации (`api_keys.channel_id` канала сайта); цели
«мероприятие/сеанс» — арены (сайт знает соответствие product ↔ `actionEventId`), «страница товара/мерч» — как `url`.
Деплой на боевые сайты — ST-7.

## 6. Каталог задач

Классы и модели — 40 §2. Ветки `feat/ql-<номер>-<слаг>`, журнал `docs/plans/exec_ledger_2026-10_ru.md`.

### 6.0 Разведка

| ID | Задача | Класс | Зависит | Приёмка |
|---|---|---|---|---|
| QL-R | Собрать точные анкоры: где монтируются `/v1/public/pages/*` и маяк `/track` (файл mount, обёртка лимита); сигнатура `InsertCheckoutSessionWithToken` и `ordering.CreateInput`; как `hbil24` читает `complimentary` из params; как `event_card.go` собирает ряды кнопок; как `team.go` ведёт диалог на `DialogStore`; как `EventScreen.tsx` зовёт API и где живёт `parity/matrix.ts`; формула подозрительного UA в `pagestats`; где `config.go` держит `PUBLIC_*` и `APP_PUBLIC_URL` | R haiku | — | список «файл:строка → что менять» в журнале; карточки ниже дополняются до старта |

### 6.1 Этап 1 «Сервер» (одна стопка PR, порядок как в таблице)

| ID | Задача | Класс | Зависит | Приёмка | Стоп |
|---|---|---|---|---|---|
| QL-1 | Миграция §3 + права + `gen` запросы: `InsertSalesLinkIfCodeFree` (ON CONFLICT DO NOTHING RETURNING), `GetSalesLinkByCode` (lower), `GetSalesLinkByID` (с org), `ListSalesLinks` (курсор по `created_at,id`), `UpdateSalesLink` (tri-state по образцу `optionalInt32`), `BumpSalesLinkScan`, `UpsertSalesLinkHit`, `SalesLinkOrdersAggregate`, `SalesLinkHitsByDay/ByCountry`; `checkout_sessions.attribution` в `selectCheckoutSessionColumns` + ВСЕ запросы этого сканера (включая `superadmin.sql.go`, грепом по вызовам) | M sonnet | QL-R | миграция применяется на свежей `arena_ci` и на копии стенда; `TestSuperadminPermissionParity532*` зелёные; голова миграций в тесте поднята; column-count тесты сканеров зелёные | ST-5 |
| QL-2 | Пакет `internal/platform/attribution` (`Clean`, `Window = 7*24h`, `MergeTouch(prev, next)` для `first`) и пакет `internal/platform/salelinks` (`NewCode`, `Mint` по идиоме `mint.EAN13` до 8 попыток, `Resolve` §4.3, `BuildDestination` с utm/ref, `DefaultUTM(link, event, org)` §2 п. 3, `Dedup` §2 п. 9 с потолком 100k записей и TTL) | M sonnet | QL-1 | юнит-тесты: алфавит и длина кода; каждая ступень цепочки §4.3; слияние utm с параметрами `url`; `landed_at` вне окна отбрасывает касание; дедуп по дню | — |
| QL-3 | Публичный редирект `GET /go/{code}` §4.1 — новый пакет `httpserver/hgo` (или в `hfeed` рядом с маяком, по QL-R), монтирование в корневом роутере, лимит по IP, 503 не 502, счёт после ответа | **L opus** (публичная запись без входа) | QL-2 | интеграционный тест: 302 на страницу мероприятия с `utm_*` и `ref`; неизвестный код → fallback 302; архивная → страница организации; скан посчитан ровно 1 раз на два запроса одного клиента; подозрительный UA → `suspect`; 1000 запросов → лимит 429 без падения; HEAD без счёта. opus-ревью до слияния | — |
| QL-4 | Маршруты организации §4.2 — пакет `httpserver/hsalelinks` (handler ≤ 400 строк, разнести), openapi (`SalesLink*`, plain string вместо enum, `$ref` с description, block-style ошибки), `gen-openapi` + `gen-ts-client`, аудит `v1.sales_link.{create,update,retarget,archive}` | M sonnet | QL-2 | интеграционный тест: создать на event/session/promoter/org/url; чужая цель → 422; чужая ссылка → 404; PATCH target пишет аудит; список с курсором; `destination_degraded` для снятого мероприятия; `TestOpenAPIDocs_*` зелёные | — |
| QL-5 | QR-рендер: `GET …/{id}/qr` PNG через `go-qrcode` (уровень `qrcode.Medium`, `size`, `margin` модулей) и SVG из `qrcode.New(...).Bitmap()` собственным писателем (прямоугольники по рядам, чёрное на белом, без зависимостей); кодируемая строка — ровно `short_url` | S sonnet | QL-4 | PNG декодируется тестовой библиотекой обратно в `short_url` (если декодера в репо нет — проверка размера и magic + сравнение SVG с эталоном); SVG валиден и содержит N×N модулей; `Cache-Control: private` | — |
| QL-6 | Атрибуция в `checkout/start` §4.4 + `ordering.CreateInput` + `CREATE_ORDER_EXT` `attribution` + сводка «по источникам» §4.5 | **L opus** (транзакция холда и денежный путь шлюза) | QL-1, QL-2 | `TestHostedCheckout_*`-подобный тест: start с `attribution{ref}` → заказ с `sales_link_id` и json; чужой `ref` → json без привязки; невалидный объект → заказ создан, атрибуция пустая; сценарий шлюза `CREATE_ORDER_EXT` с `attribution` → заказ привязан; повторная отправка с другим ref → обновлено; `summary.sources` группирует 3 заказа (ссылка/utm/direct); `-tags integration` на свежей базе; существующие сценарии harness не меняются | — |

**Анкоры по разведке QL-R (master 83d8692, 24.10; пути от `apps/backend/`).**

- **QL-1.** Голова миграций: `internal/migrations/sql/0166_promo_code_attempts.sql`; пины `internal/migrations/migrations_head_test.go:43` (`expectedHead`) и `lock_timeout_test.go:13`; первый оператор Up — `SET LOCAL lock_timeout = '5s'` (образец `0161_session_seats_manage_permission.sql:22`). Идиома права + грантов — там же `:24-41` (`INSERT INTO permissions … ON CONFLICT DO NOTHING`; `INSERT INTO role_permissions … SELECT r.id, p.id FROM roles r JOIN permissions p … WHERE r.name IN ('org_admin','organizer') AND r.org_id IS NULL`; отдельный блок `WHERE r.name = 'platform_superadmin'`). Сканер чекаута: `internal/adapters/postgres/gen/checkout_sessions.sql.go:91` (`selectCheckoutSessionColumns`), `:59` (`scanCheckoutSessionRow`), `:28` (`CheckoutSessionRow`); вызовы сканера — все в том же файле (`:115, :145, :186, :222, :249, :273, :293, :315, :336, :361, :383, :416`) плюс `gen/superadmin.sql.go:48` (`ListAllCheckoutSessions`, константа `:17`). Образец «INSERT … ON CONFLICT DO NOTHING RETURNING» — `gen/barcodes.sql.go:220-239` (`insertBarcodeIfUnique`). Tri-state для PATCH — `httpserver/hcatalog/events.go:55` (`optionalInt32`). SQL-файлы запросов — `internal/adapters/postgres/queries/*.sql`, ручные обёртки — `gen/*.sql.go` (по одному файлу на тему, например `queries/event_page_stats.sql` + `gen/event_page_stats.sql.go`). Колонка `short_host text NOT NULL DEFAULT 'go.arenasoldout.com'` — по §7, в этой же миграции.
- **QL-2.** Образец уникального кода — `internal/platform/barcodes/mint/mint.go:74` (`EAN13(ctx, ins Inserter, …)`, до 8 попыток, вставка через узкий интерфейс). Правила UTM — `internal/platform/pagestats/pagestats.go:52` (`CleanUTM`, ≤64, `^[A-Za-z0-9._~-]*$`), подозрительный UA — `:87` (`IsSuspectAgent`, пустой UA подозрителен), день в зоне — `:107` (`Day`). Хэша/дедупликации в `pagestats` НЕТ (разведка): `Dedup` пишется с нуля, формула §2 п. 9. Адрес страницы — `internal/platform/publicurl/publicurl.go:28` (`Page(base, orgSlug, promoterSlug, eventSlug)`; промоутер побеждает `:33-36`). Факты для `Resolve` берутся из тех же условий, что у страницы: `queries/public_page.sql:19` (`GetHostedPageResolution`: `e.status = 'published'`, канал `settings #>> '{hosted_page,enabled}' = 'true'`, активный feed token) и `:185` (`…ByPromoter`: `p.archived_at IS NULL`). Окно времени — `internal/platform/ratelimit/ratelimit.go:44` (`SlidingWindow`, `Take :123` не считает отказ) как образец структуры с TTL и `Now func()`.
- **QL-3.** Корневые (вне `/v1`) маршруты монтирует `httpserver/server.go:125` (`mountOperationalRoutes`: `/healthz`, `/readyz`, `/metrics`), вызов из `httpserver/wire.go:370`; сам роутер строится в `wire.go:200` через `internal/adapters/http/router.go:192` (`NewRouter` — только middleware). `trustedRealIP` — `router.go:1104`, включается при `TrustedProxyCount > 0` (`:219-221`); лог запроса — `router.go:804` (`requestLogMiddleware`), пример подавления полей для одного маршрута — `:837-840, :880-882` + `isPageTrackBeacon :891`. IP клиента — `httpserver/httputil/helpers.go:119` (`TrustedClientIP(r, trustedProxies)`); лимитер публичных маршрутов — `httpserver/public_feed_ratelimit.go:50` (`publicFeedRateLimiter`), общий бакет по IP — `CheckIP` (интерфейс `hfeed/handler.go:56-69`), образец его применения — `hfeed/handler.go:240` (`enforceRateLimit`, 429 + `Retry-After`) и `hfeed/public_page_track.go:73-178` (маяк: лимит до чтения тела, `IsSuspectAgent`, запись одним statement'ом, страна/UA не хранятся). Поле лимитера на сервере — `server_struct.go:89` (`publicFeedRL`), инъекция в hfeed — `feed_shims.go:44`. Решение прогона Р-QL1: пакет `httpserver/hgo`, монтирование новым файлом `httpserver/mount_go.go` рядом с `mountOperationalRoutes`.
- **QL-4.** Аудит в транзакции — `httpserver/hcatalog/event_status.go:37-90` (`applyEventStatus`, `audit.Event{Action: "v1.event.status_update"}`, `h.audit.WriteTx(ctx, tx, ev)`); `audit.Event` — `internal/platform/audit/audit.go:34`, `WithVia` — `audit/client_channel.go:80`. Монтирование маршрутов организации — `httpserver/mount_catalog.go` (образец блока с `{org_id}`), шимы — `*_shims.go`; порог 400 строк на файл `httpserver` (`TestHttpserverFileSize175`). Правила org-изоляции: маршрут с `{org_id}` + id ребёнка попадает в sweep-таблицу или `orgIsolationAuditedSafe` (`org_isolation_sweep_integration_test.go`). openapi: `openapi/openapi.yaml` — образец block-style ошибок `:42856-42873` (`/v1/public/pages/...`), `$ref` с соседним `description` `:13005-13009`; кодген — `make gen-openapi` (Makefile:46-52, конфиг `openapi/oapi-codegen.yaml`, типы `internal/adapters/http/openapi/types_gen.go`) и `node scripts/gen-ts-client.mjs` (корень репо; клиент `openapi/clients/ts/index.d.ts`). Запрещённые скоупы ключа — `internal/platform/apikeys/apikeys.go:64-86` (`sales_link.*` туда НЕ добавлять).
- **QL-5.** `github.com/skip2/go-qrcode` — `go.mod:21`; единственное использование — `internal/platform/delivery/pdf/layout.go:48, :918` (`qrcode.Encode(code, qrcode.High, px)`). Для SVG — `qrcode.New(...).Bitmap()` (`[][]bool`, включает quiet zone).
- **QL-6.** `hfeed/public_feed_checkout.go`: запрос `:98-130` (`PublicFeedCheckoutStartRequest`), обработчик `:191`, предполётные чтения `:544` (`preparePaymentPreflight`, `payment_start.go:524`), транзакция холда `:554`; подтверждение — `confirmPublicCheckout :1225`, вторая транзакция `:1276`, `InsertCheckoutSessionWithToken :1307` (сигнатура `gen/checkout_sessions.sql.go:136`: `(ctx, orgID, channelID, reservationID, userID *uuid.UUID, checkoutToken string, buyerLocale *string)`; INSERT `:123`), `createPublicOrder :1359` (объявление `:1516`). `ordering.CreateInput` — `internal/platform/ordering/create.go:32-65`, `CreateOrderFromCheckout :100`, `InsertOrder` — `gen/orders.sql.go:100-125` (23 колонки). Шлюз: `hbil24/cmd_order_create.go` — расширение `complimentary` декодируется в `internal/adapters/bil24compat/wire.go:163, :246, :282` (`flexWireBool :289`), `orderPersist :637` (транзакция `:648`, `InsertCheckoutSessionWithToken :695`, `orderWriteAggregate :728`), ветка повторной отправки той же корзины `:847` (`existing.ReservationID == res.ID`, `UpdateOrderFromCheckout :855`), новый заказ `:883`. Сводка: `horders/session_summary.go:38` (`sessionSummary`), `summary_core.go:149` (`summaryCore`), `:169` (`summaryRows`), `:182` (`loadSummaryRows`), `:284` (`buildSummaryCore`); SQL `queries/session_summary.sql` (`ListSessionSummaryOrders :100` — образец агрегата по массиву сеансов); схема `openapi.yaml:5210` (`SessionSummary`), маршрут `:48479`. Батч `events/sales-summary` сравнивается с per-event summary `DeepEqual` — новый блок `sources` добавлять только в сводку сеанса (и события, если `summaryCore`), НЕ в батч, либо в оба одновременно.
- **Общее.** Конфиг: `internal/platform/config/config.go:110` (`APP_PUBLIC_URL`), `:131` (`EVENT_CENTER_PUBLIC_URL`), `:159` (`TRUSTED_PROXY_COUNT`), `:176` (`PUBLIC_API_IP_RATE_LIMIT`), `:199-200` (`PUBLIC_PAGE_TRACK_*`), `:211` (`PUBLIC_TICKETS_BASE_URL`, метода-обёртки нет — читается поле); `Validate :1181`, production-правило https — образец `:1543-1558`; способности ролей — `config/roles.go:55-60` (`RoleAPI: {capJWT, capEdge, capMedia, capPublicURL}`), новая переменная — в таблицу `deploy/DOKPLOY.md:278` («2.6») под тестом `tests/staticanalysis/dokploy_env_table_test.go`. Клиенты (этапы 2–5): `eventbot/event_card.go:83-173` (`showEventCard`, ряды `rows`, `writeCardFigures :200`, пример `"ec:es:" + eventID`), `eventbot/team.go:122-147` (`DialogStore.Save/Load/Delete`), `eventbot/dialogs.go:55`; `apps/event-center/src/screens/EventScreen.tsx:79-83` (`useQuery` + `fetchEventSummary`), `src/lib/api/endpoints.ts:84`, `src/lib/api/routes.ts:22-40`, `src/parity/matrix.ts:54`.

### 6.2 Этап 2 «Страница и виджет» (после QL-4; параллельно боту)

| ID | Задача | Класс | Зависит | Приёмка | Стоп |
|---|---|---|---|---|---|
| QL-7 | `apps/widget/src/lib/attribution.ts` §5.1 + передача в `startCheckout`; `?session=` → `session-id` в `render.ts`; vitest на окно 7 дней, `first`, try/catch хранилища; e2e `test:e2e` (mock) «адрес с `ref` → тело checkout/start содержит attribution» | Ф sonnet | QL-4 | vitest + Playwright mock зелёные; `type-check`; счётчик тест-файлов вырос (ловушка `*.test.ts`); проверено вручную на `apps/wp-plugin` демо-странице, что атрибуция уходит и оттуда | — |

### 6.3 Этап 3 «Бот» (после QL-5)

| ID | Задача | Класс | Зависит | Приёмка | Стоп |
|---|---|---|---|---|---|
| QL-8 | `ArenaClient` методы + диалог §5.2 на `DialogStore` (`sales_link_dialog.go`, `ql_keys.go`), кнопки на карточках мероприятия и сеанса, пункт меню; отправка PNG как фото и SVG как документа | M sonnet | QL-5, QL-6 | e2e `bot_e2e_sales_links_integration_test.go` (реальный роутер + стаб Telegram): с карточки → «Афиша у входа» → в базе строка с `event_id`, в чат ушли `[photo]`, `[document …svg]` и текст с `short_url`; «Статистика» показывает сканы после одного GET на редирект; перенацеливание пишет аудит; ключи во всех трёх локалях; тексты по стандарту | — |
| QL-9 | Экран «Ссылки и QR» в меню бота: список с цифрами, пагинация `paging.go`, фильтр «только это мероприятие» | S sonnet | QL-8 | e2e: 3 ссылки → список → карточка → архив → в списке «архив» | — |

### 6.4 Этап 4 «Сайты на протоколе Bil24» (внешние репозитории; после QL-6)

| ID | Задача | Класс | Зависит | Приёмка | Стоп |
|---|---|---|---|---|---|
| QL-10 | `arena-attribution.php` §5.4 в репозиториях lampyrisevents и vinoandco (ветки, staging-1) | M sonnet | QL-6 | на staging: заход по `go.<стенд>/<code>` → cookie → заказ через CREATE_ORDER_EXT привязан к ссылке в базе стенда; cookie без личных данных | ST-7 (деплой на прод) |
| QL-11 | Экран «Ссылки и QR» в ивент-центре сайта (WP admin): список, создание на product→action/сеанс, своя страница как `url`, картинка через `/qr`, ключ организации канала | M sonnet | QL-10 | ручной прогон на staging по чек-листу в журнале | ST-7 |

### 6.5 Этап 5 «Печать и веб» (после этапов 2–3)

| ID | Задача | Класс | Зависит | Приёмка | Стоп |
|---|---|---|---|---|---|
| QL-12 | Веб-ивент-центр §5.3: `LinksScreen.tsx`, `LinkScreen.tsx`, кнопки на `EventScreen`/`SessionScreen`, `matrix.ts`, близнец-тест smoke | Ф sonnet | QL-5, QL-6 | smoke зелёный в CI; паритет с ботом по строке матрицы | — |
| QL-13 | PDF-вкладыш `GET …/{id}/qr?format=pdf&paper=a6|a5`: `internal/platform/salelinks/pdfinsert` на gofpdf с шрифтами из `delivery/pdf/fonts.go` (экспортировать байты шрифтов функцией, не копировать файлы): QR, название мероприятия или организации, «Отсканируйте и купите билеты» на языке организатора (en/ru/es/cs/he, `i18n` бандл), адрес текстом, логотип организации через `MediaResolver` если есть; бот присылает PDF третьим документом | M sonnet | QL-5, QL-8 | детерминизм байтов (ловушки gofpdf: разная ширина картинок, `SetCellMargin(0)`); текст извлекается `pdfDrawnText`; RTL для he как у билета (глифы, не шейпинг) | — |

### 6.6 Ops

| ID | Задача | Класс | Зависит | Приёмка | Стоп |
|---|---|---|---|---|---|
| QL-14 | Конфиг: `SHORT_LINK_BASE_URL` (default `https://go.arenasoldout.com`), `SHORT_LINK_FALLBACK_URL` (default `APP_PUBLIC_URL`-витрина), `config.Validate` требует https в production; документ `docs/ops/sales_links_ru.md` (как завести хост, как перенацелить, как читать `sales_link_hits`) | S sonnet | QL-3 | юнит-тест конфига; документ | — |
| QL-15 | **Шаг владельца/ops**: DNS `go.arenasoldout.com` (Cloudflare, proxied) → origin arena-prod; Traefik-роутер `Host(go.arenasoldout.com)` → сервис arena-api с middleware `addPrefix: /go`; то же на стенде (`go-staging.arenasoldout.com`); проверка `curl -I https://go.arenasoldout.com/<code>` → 302 | ST-7 + R haiku (чек-лист) | QL-3 на проде | факт в журнал; Cloudflare НЕ кэширует (`no-store` + правило bypass для хоста) | ST-7 |

## 7. Кастомные домены организаторов и QR — анализ для решения владельца

Линия Д спеки 40 (домен организатора для страницы продаж через Cloudflare for SaaS) **не начата**: `org_domains`
нет, шаг Д0 (включить Cloudflare for SaaS, ST-7) не сделан. Вопрос: должен ли QR-код организатора вести на ЕГО домен
(`tickets.teatr.es/x7k2qpm`), а не на `go.arenasoldout.com`?

**Плюсы для организатора.** Доверие на бумаге: человек видит знакомый домен театра, а не чужой сервис. Белый лейбл:
на афише нет бренда платформы. Независимость: домен принадлежит организатору, и если он когда-нибудь уйдёт с
платформы, он сможет перенаправить напечатанные афиши куда захочет (для платформы это минус — меньше привязки).

**Минусы и сложности.** (1) Самое важное для печати: цепочка «CNAME организатора → Cloudflare for SaaS → наш origin»
имеет звено, которым владеет организатор. Снятый или истёкший домен, смена DNS-провайдера, забытая оплата домена —
и ВСЕ его афиши мёртвые, а мы ничем помочь не можем. `go.arenasoldout.com` отказоустойчив ровно потому, что
полностью наш. (2) Стоимость и сложность: Cloudflare for SaaS платный за hostname, сертификат на каждый домен,
проверка владения TXT, суточная перепроверка, Host-режим маршрутов — это задача класса L (Д1) и она ещё впереди.
(3) Короткий домен организатора редко бывает коротким: `tickets.teatr-kolybel.es/x7k2qpm` в QR плотнее, чем
`go.arenasoldout.com/x7k2qpm`, и печатать его текстом под кодом неудобнее. (4) Маркетинг: каждая афиша с
`go.arenasoldout.com` — бесплатная реклама платформы в офлайне; с белым лейблом её нет.

**Решение владельца 24.10:** в этой волне — только `go.arenasoldout.com`; домен организатора для QR — потом, и только
как отдельно оплачиваемая опция (появится в прайс-листе биллинга, спека 41/42), не в базовом тарифе.

**Рекомендация (принята).** В этой волне — только `go.arenasoldout.com`. Но спроектировать так, чтобы линия Д подключилась
позже без миграции и без перевыпуска кодов: редирект уже host-независим (§4.1), `short_url` строится функцией
`salelinks.ShortURL(baseForOrg, code)`, где `baseForOrg` сегодня всегда платформенный хост, а после Д1 может
стать проверенным доменом организации для НОВЫХ ссылок (у каждой ссылки хранится `short_host`, под которым она
выпущена — добавить колонку `short_host text NOT NULL DEFAULT 'go.arenasoldout.com'` сразу в миграции QL-1, чтобы
потом не менять напечатанное). Цель редиректа и так следует за Д2: `publicurl.Page` станет отдавать домен
организации, когда он проверен, и старые коды начнут вести на него автоматически.

Если владелец всё же хочет белый лейбл в печати уже сейчас, дешёвый промежуточный вариант — **один** платформенный
короткий домен без бренда (например, нейтральное имя вроде `tkt.link`-подобного, купленный платформой): те же
плюсы по отказоустойчивости, минус бренд на афише; это решение ST-4 (новый домен и расход), не задача прогона.

## 8. Стоп-условия, добавленные к 40 §4

| Код | Условие |
|---|---|
| ST-QL1 | Любое предложение считать или хранить IP, User-Agent, cookie-id покупателя в `sales_link_hits` или в `attribution` — только агрегаты по дню и стране и хэш в памяти |
| ST-QL2 | Любая попытка сделать редирект платным/лимитированным по кредитам биллинга или ограничить число ссылок на организацию — не в этой волне, решение владельца |
| ST-QL3 | Смена домена редиректа после первой напечатанной афиши клиента (ABH TEAM тест не считается) |

## 9. Порядок прогона

1. QL-R (haiku) → дописать анкоры в карточки 6.1. 2. QL-1 → QL-2 → QL-3 ∥ QL-4 → QL-5; QL-6 после QL-2 (opus).
3. После QL-4: QL-7 (виджет) ∥ QL-8/QL-9 (бот) ∥ QL-12 (веб). 4. QL-14 с QL-3; QL-15 — просьба владельцу сразу после
слияния QL-3 на master (можно раньше: хост с 302 на fallback не вредит). 5. Выкат на стенд → ABH TEAM печатает тестовый
QR и покупает → проверка `sales_link_hits`, `orders.sales_link_id`, `summary.sources`. 6. QL-10/QL-11 на staging-1,
затем ST-7 на прод Lampyris и Vino. 7. QL-13 — последним.

Что оркестратор проверяет перед «готово» — 40 §7, плюс: `go vet` чистый на новых пакетах; `gofmt -l` по файлам с
тегом integration; `TestSnakeCase_*`, `TestOpenAPIDocs_*`, `TestErrorResponses_*`, парность 532, голова миграций;
ни одного `fmt.Sprintf("…/go/…")` вне `salelinks.ShortURL`; ни одного чтения `*gen.Queries` на пуле внутри
транзакции холда.
