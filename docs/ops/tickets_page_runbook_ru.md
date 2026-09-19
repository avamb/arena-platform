# Хостинговая страница продажи билетов — рантайм и деплой

Кратко: как страница `https://tickets.arenasoldout.com/{org_slug}/{event_slug}`
находит событие, как сделать событие видимым там без ручной работы для
каждого события, и как это раскатать в Dokploy.

## 1. Как работает резолвинг

`apps/tickets-page` — статическое SPA (Vite + vanilla TypeScript, без
фреймворка). При загрузке `main.ts`:

1. Разбирает свой собственный путь `/{org_slug}/{event_slug}`
   (`src/lib/route.ts`, terpит завершающий слеш).
2. Определяет локаль: `?lang=` → `navigator.language` (сопоставление по
   языковому подтегу: `ru-RU` → `ru`) → `en` (`src/lib/locale.ts`).
   Поддерживаются `en`, `ru`, `cs`, `he` (he — RTL), как и в самом виджете.
3. Зовёт `GET {VITE_API_BASE_URL}/v1/public/pages/{org_slug}/{event_slug}`.
4. При 200 — рендерит хиро-блок (обложка/название/дата/площадка/описание) и
   монтирует `<arena-tickets feed-token=… event-id=… locale=… api-base=…>`.
   Виджет отдаётся с ТОГО ЖЕ контейнера nginx по пути
   `/widget/v1/arena-tickets.js` — никогда не jsDelivr.
5. При 404 — брендированная страница "событие не найдено". Любая другая
   ошибка — состояние с кнопкой "повторить".
6. Если в query есть `checkout_token` (виджет возвращается с оплаты —
   `getCheckoutTokenFromSearch` в `apps/widget/src/lib/store.ts`), query
   string НЕ трогается и после рендера блок виджета скроллится в вид.

## 2. Правило резолвинга на бэкенде

`GET /v1/public/pages/{org_slug}/{event_slug}` (без авторизации, лимит по
IP, `Cache-Control: public, max-age=30`). Реализация:
`apps/backend/internal/platform/httpserver/hfeed/public_page.go`,
SQL — `apps/backend/internal/adapters/postgres/queries/public_page.sql`
(`GetHostedPageResolution`).

Цепочка резолвинга (одним запросом к БД):

```
активная организация по slug
  → неудалённое событие этой организации по slug, статус = published
  → активная публикация этого события на канале ТОЙ ЖЕ организации,
    у которого settings.hosted_page.enabled = true и который сам не удалён
  → самый новый активный (не отозванный) feed-токен этого канала
```

Любое звено отсутствует → **один и тот же** `404 page.not_found` — ответ
никогда не показывает, какой именно шаг не прошёл (неизвестный org/event,
событие не published, ни у одного канала не включён флаг, токен отозван —
всё выглядит одинаково снаружи).

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
шага `GET /v1/public/pages/{org_slug}/{event_slug}` должен вернуть 200.

### Проверка

```
curl https://api.arenasoldout.com/v1/public/pages/{org_slug}/{event_slug}
```

Ожидаемый ответ — `{"org": {...}, "event": {...}, "feed_token": "...",
"default_locale": "en"}`. 404 — сверить все 4 шага выше по порядку (org
slug/deleted, event slug/status, channel flag/settings/deleted, token
active).

## 4. Codegen

При изменении контракта — `apps/backend/openapi/openapi.yaml`
(`HostedPageResponse`, `HostedPageOrg`, `HostedPageEvent`, путь
`/v1/public/pages/{org_slug}/{event_slug}`), затем регенерировать Go-типы
и TS-клиент (см. AGENTS.md «Codegen & spec»).

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
