# Хостинговая страница продажи билетов — рантайм и деплой

Кратко: как страница `https://tickets.arenasoldout.com/{org_slug}/{event_slug}`
(и страница-афиша организатора `https://tickets.arenasoldout.com/{org_slug}`)
находит событие(я), как сделать событие видимым там без ручной работы для
каждого события, и как это раскатать в Dokploy.

## 1. Как работает резолвинг

`apps/tickets-page` — статическое SPA (Vite + vanilla TypeScript, без
фреймворка). При загрузке `main.ts`:

1. Разбирает свой собственный путь (`src/lib/route.ts`, terpит завершающий
   слеш): **один** сегмент — `/{org_slug}` → страница-афиша организатора
   (список всех видимых событий), **два** сегмента — `/{org_slug}/{event_slug}`
   → страница конкретного события. Три и более сегмента, пустой путь или `/`
   → брендированная страница "не найдено" без обращения к API.
2. Определяет локаль: `?lang=` → `navigator.language` (сопоставление по
   языковому подтегу: `ru-RU` → `ru`) → `en` (`src/lib/locale.ts`).
   У СТРАНИЦЫ (не у виджета) свой, более широкий набор локалей: `en`, `ru`,
   `cs`, `he` (he — RTL) и `es` (добавлена для организатора из Испании).
   Виджет `<arena-tickets>` понимает только `en`/`ru`/`cs`/`he` — когда
   локаль страницы `es`, в атрибут `locale` виджета передаётся `en`; это
   отображение живёт в ОДНОЙ функции, `toWidgetLocale` (`src/lib/locale.ts`),
   и нигде больше не дублируется.
3. Для события: зовёт
   `GET {VITE_API_BASE_URL}/v1/public/pages/{org_slug}/{event_slug}`. При
   200 — рендерит хиро-блок (обложка/название/дата/площадка/описание),
   ссылку "назад к афише" (`/{org_slug}`, с сохранением query — в т.ч.
   `?lang=`) и монтирует `<arena-tickets feed-token=… event-id=… locale=…
   api-base=…>`. Виджет отдаётся с ТОГО ЖЕ контейнера nginx по пути
   `/widget/v1/arena-tickets.js` — никогда не jsDelivr.
4. Для афиши: зовёт `GET {VITE_API_BASE_URL}/v1/public/pages/{org_slug}`.
   При 200 — рендерит название/логотип организации и сетку карточек, по
   одной на видимое событие, отсортированных по ближайшей дате (дата/время
   ПЕРВЫМИ и заметно на карточке, в часовом поясе площадки события, если он
   известен — не в поясе зрителя). У каждой карточки одна ссылка на её
   страницу события (`/{org_slug}/{event_slug}`, с сохранением query).
   Организация с нулём видимых событий, но с настроенным каналом — 200 с
   пустым списком и локализованным текстом "пока нет ближайших дат", это НЕ
   состояние "не найдено".
5. При 404 (любой из двух маршрутов) — брендированная страница "не найдено".
   Любая другая ошибка — состояние с кнопкой "повторить".
6. Если в query есть `checkout_token` (виджет возвращается с оплаты —
   `getCheckoutTokenFromSearch` в `apps/widget/src/lib/store.ts`), query
   string НЕ трогается и после рендера блок виджета скроллится в вид.

## 2. Правило резолвинга на бэкенде

### 2.1 Страница события — `GET /v1/public/pages/{org_slug}/{event_slug}`

Без авторизации, лимит по IP, `Cache-Control: public, max-age=30`.
Реализация: `apps/backend/internal/platform/httpserver/hfeed/public_page.go`
(`HandlePublicPage`), SQL —
`apps/backend/internal/adapters/postgres/queries/public_page.sql`
(`GetHostedPageResolution`).

Цепочка резолвинга (одним запросом к БД):

```
активная организация по slug (без учёта регистра)
  → неудалённое событие этой организации по slug (без учёта регистра),
    статус = published
  → активная публикация этого события на канале ТОЙ ЖЕ организации,
    у которого settings.hosted_page.enabled = true и который сам не удалён
  → самый новый активный (не отозванный) feed-токен этого канала
```

Любое звено отсутствует → **один и тот же** `404 page.not_found` — ответ
никогда не показывает, какой именно шаг не прошёл (неизвестный org/event,
событие не published, ни у одного канала не включён флаг, токен отозван —
всё выглядит одинаково снаружи).

Ответ также несёт `event.first_session_timezone` — IANA-имя часового пояса
(`venues.timezone`) площадки ближайшей сессии события (дешёвый коррелированный
подзапрос), `null` когда сессий ещё нет или у площадки не задан пояс.

### 2.2 Страница-афиша организатора — `GET /v1/public/pages/{org_slug}`

Тот же режим (без авторизации, лимит по IP, тот же `Cache-Control`).
Реализация: тот же файл, `HandlePublicPromoterPage`. SQL —
`GetHostedPromoterPageOrg` (org + флаг `has_hosted_channel`, одним запросом)
и `ListHostedPromoterPageEvents` (список событий, `DISTINCT ON` на случай
публикации через несколько токенов).

Правило видимости ровно то же, что у страницы события, применённое к
КАЖДОМУ событию организации: неудалённое, `status = published`, есть slug,
опубликовано через активный feed-токен канала ТОЙ ЖЕ организации с
`settings.hosted_page.enabled = true`. Событие, у которого
`last_session_at` уже в прошлом, из списка исключается целиком; событие без
сессий (`first_session_at IS NULL`) остаётся и сортируется в конец. Сортировка
— по `first_session_at` по возрастанию (ближайшее раньше), лимит 200.

Организация без единого видимого события, но с корректно настроенным
hosted-page каналом — **200 с пустым `events: []`**, это НЕ 404. Неизвестная
организация ИЛИ организация без единого hosted-page-канала вовсе — **тот же**
`404 page.not_found`, что и у страницы события (тот же принцип
неразличимости причины отказа).

### 2.3 Регистронезависимость slug'ов

`org_slug` и `event_slug` сравниваются без учёта регистра (`lower()` с ОБЕИХ
сторон в SQL) на ОБОИХ эндпоинтах — организатор может напечатать ссылку как
`MasterClassTeatro`. Важная асимметрия на запись: slug организации
нормализуется в нижний регистр на запись
(`hiam/orgs.go`, `hiam/admin_orgs.go` — `strings.ToLower` перед INSERT/UPDATE),
а slug СОБЫТИЯ — нет (`hcatalog/events.go`, PATCH метаданных пишет
`req.Slug` как есть). Поэтому сравнение `lower()` с обеих сторон обязательно
именно для события, а для организации это защита "на всякий случай" сверху
уже существующей нормализации.

## 3. Как сделать событие видимым на странице — ровно 4 шага

Ничего в коде трогать не нужно. Порядок такой:

### Шаг 1 — slug у организации и события

`organizations.slug` обычно уже задан при создании организации. Если нет —
`PATCH /v1/organizations/{org_id}` с `{"slug": "arenasoldout"}`.

У события slug выставляется тем же PATCH, что и остальные метаданные
(`PATCH /v1/organizations/{org_id}/events/{id}`, tri-state поле — отсутствие
ключа не трогает значение):

```
PATCH /v1/organizations/{org_id}/events/{event_id}
{"slug": "summer-festival-2026"}
```

Событие также должно быть в статусе `published`
(`POST /v1/organizations/{org_id}/events/{id}/status`).

### Шаг 2 — включить флаг `hosted_page` на канале

**ВАЖНО:** `PATCH /v1/organizations/{org_id}/channels/{id}` с ключом
`settings` в теле **заменяет весь JSON-блоб `settings` целиком** — SQL
делает `settings = $new` без слияния
(`apps/backend/internal/adapters/postgres/gen/channels.sql.go`,
`UpdateSalesChannel`). Ручку менять НЕЛЬЗЯ (это чужой код, и правило
AGENTS.md про `settings.gateway` — тот же самый класс ловушки). Поэтому
оператор обязан:

1. Прочитать текущий канал: `GET /v1/organizations/{org_id}/channels/{id}`
   → взять поле `settings` из ответа как есть.
2. Слить в него `hosted_page: {"enabled": true}`, ничего не выбрасывая
   (особенно `settings.gateway`, если канал уже настроен на Bil24-шлюз).
3. Отправить PATCH с ПОЛНЫМ объединённым объектом:

```
PATCH /v1/organizations/{org_id}/channels/{channel_id}
{
  "settings": {
    "gateway": { "...": "как было, не трогать" },
    "hosted_page": { "enabled": true }
  }
}
```

Если у канала до этого settings был пустым (`{}`), можно просто отправить
`{"settings": {"hosted_page": {"enabled": true}}}`.

Канал также не должен быть удалён (soft-delete) — обычная активная запись.

### Шаг 3 — активный feed-токен у этого канала

Если у канала ещё нет токена:
`POST /v1/organizations/{org_id}/channels/{channel_id}/feed-tokens`
(`{"label": "hosted page"}`). Токен должен быть активным (не отозванным —
`is_active=true`); если использовать уже существующий токен, страница
подхватит именно САМЫЙ НОВЫЙ активный токен этого канала.

### Шаг 4 — опубликовать событие на этот канал

```
POST /v1/events/{event_id}/publications
{"feed_token_id": "<id токена из шага 3>"}
```

(`city_id` опционален — не нужен для хостинговой страницы). После этого
шага `GET /v1/public/pages/{org_slug}/{event_slug}` должен вернуть 200, и
это же событие само появится в списке `GET /v1/public/pages/{org_slug}` —
шаги идентичны, отдельной настройки для афиши не требуется. Пример:
организатор заводит шесть отдельных мастер-классов (каждый своей датой,
названием, преподавателем) через шаги 1–4 выше, публикует все шесть на ОДИН
и тот же канал/токен — и ссылка `tickets.arenasoldout.com/{org_slug}` сразу
показывает все шесть с датой/временем каждого.

### Проверка

```
curl https://api.arenasoldout.com/v1/public/pages/{org_slug}/{event_slug}
curl https://api.arenasoldout.com/v1/public/pages/{org_slug}
```

Ожидаемый ответ страницы события — `{"org": {...}, "event": {...},
"feed_token": "...", "default_locale": "en"}`. Ожидаемый ответ афиши —
`{"org": {...}, "default_locale": "en", "events": [...]}` (пустой массив —
это валидный 200, не ошибка). 404 — сверить все 4 шага выше по порядку (org
slug/deleted, event slug/status, channel flag/settings/deleted, token
active); для афиши шаг с event slug/status не применяется — 404 там означает
либо неизвестный org, либо ни одного канала с включённым флагом вообще.

## 4. Codegen

При изменении контракта — `apps/backend/openapi/openapi.yaml`
(`HostedPageResponse`, `HostedPromoterPageResponse`, `HostedPageOrg`,
`HostedPageEvent`, пути `/v1/public/pages/{org_slug}/{event_slug}` и
`/v1/public/pages/{org_slug}`), затем регенерировать Go-типы и TS-клиент
(см. AGENTS.md «Codegen & spec»).

## 5. Деплой (Dokploy)

- **Dockerfile:** `apps/tickets-page/Dockerfile`. Контекст сборки — КОРЕНЬ
  РЕПОЗИТОРИЯ (образ также компилирует `apps/widget` как отдельный стейдж).
  Собирает `<arena-tickets>` из `apps/widget` через `vite build` (НЕ
  `npm run build` — postbuild-скрипт виджета требует `apps/widget/demo/`,
  которая исключена из контекста сборки корневым `.dockerignore`; странице
  нужен только сам `arena-tickets.js`, без iframe-фолбэка).
- **Build arg:** `VITE_API_BASE_URL` — публичный origin API
  (`https://api.arenasoldout.com`), без завершающего слеша. Зашивается и в
  JS-бандл (Vite), и в директиву `connect-src` Content-Security-Policy
  nginx-конфига (`apps/tickets-page/nginx.conf.template`, плейсхолдер
  `__API_ORIGIN__` подставляется `sed`-ом на этапе сборки образа — см.
  `security-headers.conf.template`).
- **Домен:** `tickets.arenasoldout.com` → контейнер, порт 8080 (без
  CAP_NET_BIND_SERVICE, non-root, как и `admin-web`). SPA-фолбэк
  (`try_files $uri /index.html`) отдаёт 200 для любого неизвестного пути —
  это ожидаемо сейчас (см. README «Known gaps»).
- **CORS:** бэкенд разрешает Origin только из `CORS_ALLOWED_ORIGINS`
  (`apps/backend/internal/platform/config/config.go`). На проде туда
  ОБЯЗАТЕЛЬНО добавить `https://tickets.arenasoldout.com`, иначе браузер
  заблокирует `fetch` к `/v1/public/pages/...` из-за CORS, хотя сам запрос
  на бэкенде отработает нормально (это легко спутать с 404 при отладке —
  сначала проверяйте вкладку Network/консоль на CORS-ошибку, а не код
  ответа).
- **Healthcheck:** `GET /health` → `200 ok` (без логирования).

## 6. Будущий шаг — клиентские домены

Резолвинг сейчас работает ТОЛЬКО по пути (`{org_slug}/{event_slug}`) —
никакой логики по `Host`/поддоменам нет (осознанно, для этой волны). Когда
появится задача повесить ту же страницу на домен клиента (`tickets.client.com`
без пути), резолвинг по Host нужно добавлять ОТДЕЛЬНЫМ шагом сверху
существующего — например, резолвить домен → org_slug на отдельном
маленьком эндпоинте/таблице, а дальше использовать тот же
`GetHostedPageResolution` без изменений. Не трогайте
`apps/tickets-page/src/lib/route.ts` ради этого — держите резолвинг слага и
тему (theming) раздельно, как и просили в задаче.
