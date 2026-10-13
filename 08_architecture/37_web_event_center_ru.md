# Веб-ивент-центр для организаторов (WEB-0) — спецификация

Статус: черновик 2026-10-13. Решения владельца взяты по рекомендациям раздела 11.9 плана
`docs/plans/hosted_page_legal_web_center_wave_plan_2026-10-13_ru.md` (волна 7). Открытые вопросы — раздел 14.
Дополняет `28_telegram_event_center_bot_ru.md` и `35_telegram_event_center_full_ru.md` (бот), `36_payment_modules_refunds_acquiring_ru.md` (возвраты).
Тексты по стандарту `docs/text_style_standard_ru.md`.

## 1. Цель и границы

1. Организатор работает с ивент-центром в браузере, если у него нет Telegram или ему удобнее большой экран.
2. Веб делает то же, что бот, по тем же правилам. Правила, суммы и проверки живут в API. Веб и бот только рисуют экраны.
3. Веб-ивент-центр — для организаторов. Консоль суперадмина остаётся в `apps/admin-web`. Экраны админки в веб не переносим и общих компонентов с ней не делаем.
4. Суперадмин платформы в первый выпуск не входит. Если суперадмин входит в веб, веб говорит, что его рабочее место — админка, и не пускает дальше выбора организации.

## 2. Решения владельца (приняты по рекомендациям 11.9)

| № | Решение |
|---|---|
| 1 | Домен `events.arenasoldout.com`. |
| 2 | Суперадмин в первый выпуск не входит. |
| 3 | Серверный слой A3 «план мероприятия» (в плане волны — «план события») делаем до веб-мастера (R2). |
| 4 | Одноразовая ссылка для входа из бота в веб (раздел 4.3). |
| 5 | Денежные действия (возвраты) — только в фазе R5, после выпуска стека возвратов. |

## 3. Пользователи

1. Пользователь веба — строка таблицы `users` (тот же человек, что в боте). Отдельной таблицы нет.
2. Право работать в организации даёт активная строка `memberships`. Роли: `org_admin` (владелец), `organizer` (менеджер), `agent`.
3. Человек, которого пригласили через бота, мог ни разу не задать пароль. Он задаёт пароль через «Забыли пароль» (раздел 4.2) или входит одноразовой ссылкой из бота (раздел 4.3).

## 4. Вход и сессия

Слово «сессия» в этом разделе значит вход человека в веб (login session), а не сеанс мероприятия.

### 4.1 Вход по e-mail и паролю (есть в API)

1. Экран «Вход» отправляет `POST /v1/auth/login` (`email`, `password`).
2. Ответ: access-токен (JWT на 15 минут) и refresh-токен (30 дней). Refresh-токен меняется при каждом `POST /v1/auth/refresh`. Повтор старого refresh-токена отзывает все сессии человека (`auth.session_compromised`).
3. Ошибки входа показываем словами: неверная пара e-mail и пароль, учётная запись отключена, слишком много попыток (подождите минуту).

### 4.2 Забыли пароль (есть в API)

1. Экран «Забыли пароль» отправляет `POST /v1/auth/password-reset/request`. Ответ всегда 202, чтобы нельзя было узнать, есть ли такой адрес.
2. Ссылка в письме ведёт на `APP_PUBLIC_URL` — это сейчас адрес админки (`/reset-password`, `/accept-invite`). Эти две страницы публичные и не показывают экраны суперадмина, поэтому в R1 их можно оставить.
3. Задача A2: письмо о пароле и приглашение ведут на адрес того канала, откуда пришёл человек (`web` или `telegram`). Для этого серверу нужен второй адрес, например `EVENT_CENTER_PUBLIC_URL`, и страницы `/reset-password`, `/accept-invite` в вебе.

### 4.3 Одноразовая ссылка из бота (новое, задача WEB-AUTH-2)

1. В боте кнопка «Открыть в браузере».
2. Бот вызывает новый служебный маршрут (`BOT_SERVICE_TOKEN`) и получает одноразовый код для привязанного пользователя.
3. Бот отправляет ссылку `https://events.arenasoldout.com/login/link#code=…` только в личный чат. Код стоит после `#`, поэтому он не попадает в журналы сервера и в заголовок Referer.
4. Страница входа по ссылке сразу убирает код из адреса (`history.replaceState`), до любого запроса. Так код не остаётся в истории браузера.
5. Веб меняет код на сессию запросом `POST` на новый маршрут входа. Код идёт в теле запроса, никогда в строке запроса (query): строку запроса пишут журналы прокси и Cloudflare.
6. Код привязан к двум вещам: к пользователю и к Telegram-аккаунту, который нажал кнопку. Сервер выдаёт сессию только этому пользователю. Новый код отменяет прежний неиспользованный код этого человека.
7. Правила кода: действует 5 минут, работает один раз, в базе хранится только SHA-256, каждое использование пишется в журнал аудита (`via=event_center_web`).
8. Пересланное сообщение. Тот, кто получил пересланную ссылку в течение 5 минут, войдёт под именем владельца ссылки. Защита: ссылка уходит только в личный чат, код одноразовый и короткий. После входа бот пишет человеку «Вы вошли в веб-ивент-центр в 14:05. Если это были не вы, нажмите „Выйти везде“». Кнопка отзывает все сессии веба этого человека.
9. Код не даёт ничего сверх прав человека. Суперадмину код не выдаётся (раздел 1, п. 4).

### 4.4 Хранение сессии

**Фаза R1 (без изменений сервера):**

1. Access-токен — только в памяти страницы. Refresh-токен — в `sessionStorage` вкладки. Это модель админки.
2. Закрыли вкладку — сессия закончилась. Обновили страницу — сессия продолжается.
3. Запросы идут с заголовком `Authorization: Bearer` и `X-Client-Channel: event_center_web`.
4. Токены не пишутся в журналы, в адрес страницы и в `localStorage`.
5. Несколько вкладок одной сессии. Браузер копирует `sessionStorage` в дубликат вкладки, и вместе с ним refresh-токен. Если две вкладки обменяют один refresh-токен, сервер увидит повтор и отзовёт все сессии человека, в том числе в админке. Поэтому refresh-токен меняет только одна вкладка сессии — ведущая (Web Lock с ключом сессии). Остальные вкладки просят у неё access-токен через `BroadcastChannel` и сохраняют каждый новый refresh-токен у себя. Закрылась ведущая вкладка — ведущей становится следующая, и у неё уже последний токен. Выход в одной вкладке закрывает сессию во всех вкладках этой сессии. Вкладка, где человек вошёл отдельно, живёт своей сессией.
6. Ошибка сети при обновлении не выкидывает человека. Выход наступает только тогда, когда сервер отказал (401 или 400 на `/v1/auth/refresh`).

**Риски модели R1 (остаются до WEB-AUTH-1):**

1. XSS. Скрипт, внедрённый в страницу, прочитает refresh-токен из `sessionStorage` и получит сессию на 30 дней. Защита в R1: строгий CSP без внешних и встроенных скриптов (раздел 10.6), React экранирует текст. Это довод за сессию не дольше 12 часов (вопрос 2) и за WEB-AUTH-1.
2. Браузер без Web Locks или `BroadcastChannel` (очень старый). Каждая вкладка тогда работает сама, и дубликат вкладки снова может отозвать все сессии. Все поддерживаемые браузеры эти функции имеют.

**Цель (задача WEB-AUTH-1, до R5):** токены в httpOnly-cookie.

1. Контейнер веба проксирует `https://events.arenasoldout.com/api/*` на arena-api. Браузер видит один источник, CORS для веба не нужен.
2. Новые маршруты `POST /v1/auth/web/login`, `/refresh`, `/logout` ставят cookie:
   - `__Host-arena_ec_at` — access-токен, `HttpOnly; Secure; SameSite=Strict; Path=/`, 15 минут;
   - `__Host-arena_ec_rt` — refresh-токен, `HttpOnly; Secure; SameSite=Strict; Path=/`, срок сессии;
   - `arena_ec_csrf` — случайный CSRF-токен, читается скриптом.
3. Cookie превращает в авторизацию прокси nginx веба, на каждом вызове `/api/*`. Он удаляет из входящего запроса заголовки `Authorization` и `X-Arena-Web-Proxy`. Затем ставит `Authorization: Bearer <значение cookie __Host-arena_ec_at>` и свой заголовок `X-Arena-Web-Proxy`. Для всех остальных маршрутов API это обычный Bearer-запрос, их менять не нужно.
4. API узнаёт прокси по заголовку `X-Arena-Web-Proxy`. Его значение — общий секрет из переменной окружения контейнера веба и arena-api. Браузер этот заголовок подделать не может: nginx удаляет присланный и ставит свой. Маршруты `/v1/auth/web/*` без верного секрета отвечают 403. Cookie с префиксом `__Host-` браузер отправляет только на `events.arenasoldout.com`. Прямой запрос к `api.arenasoldout.com` cookie не несёт.
5. Защита от CSRF: каждый запрос, кроме GET и HEAD, несёт заголовок `X-CSRF-Token`, равный cookie `arena_ec_csrf` (двойная отправка). Проверяет прокси nginx на каждом изменяющем вызове `/api/*`, вместе с заголовком `Origin` (только `https://events.arenasoldout.com`). Без совпадения — 403, запрос до API не доходит. API повторяет ту же проверку на запросах с заголовком прокси, на случай ошибки в настройке nginx.
6. Бот и API-ключи работают как раньше: они ходят прямо на `api.arenasoldout.com` и cookie не имеют.
7. Прокси добавляет один шаг в цепочку адресов: на сервере нужно учесть `TRUSTED_PROXY_COUNT` (иначе лимит входа считает всех людей одним адресом).
8. Почему не сейчас: сервер пока не ставит cookie и не знает заголовок прокси. Это отдельная серверная задача с тестами.

### 4.5 Выход и тайм-аут

1. Кнопка «Выйти» вызывает `POST /v1/auth/logout` и стирает токены во всех вкладках этой сессии. Маршрут требует действующий access-токен. Если он истёк, веб сначала обновляет сессию, затем отзывает последний refresh-токен: старый уже отозван заменой, и его отзыв оставил бы сессию живой. Если сервер недоступен, вкладки всё равно выходят.
2. Если refresh не прошёл, веб показывает экран входа с текстом «Сессия закончилась. Войдите снова.» и возвращает человека на тот же адрес после входа.

## 5. Организация и права

1. После входа веб читает `GET /v1/me` и берёт список `organization_memberships`.
2. Одна организация — веб открывает её сразу. Несколько — экран выбора. Последний выбор хранится на устройстве (`localStorage`, только id).
3. Адрес каждого экрана содержит организацию: `/o/{org_id}/…`. Ссылку можно переслать коллеге. Переключатель организации стоит в шапке.
4. Права в организации веб берёт из `GET /v1/organizations/{org_id}/me` (задача A1, сделана). Поле `permissions` — те же права, что проверяет шлюз маршрута. Веб прячет действие, если права нет. `role` — только для подписи («Владелец», «Менеджер», «Агент»).
5. `GET /v1/me` нельзя читать как «можно здесь»: это объединение по всем организациям.
6. Если API всё равно ответил 403, веб пишет «У вас нет доступа к этому в организации …» и даёт ссылку «Вернуться к списку мероприятий».

Права экранов R1:

| Экран | Право |
|---|---|
| Список мероприятий | `event.read` |
| Цифры продаж, сводки | `order.read` |
| CSV-выгрузки | `order.read` и `session.read` (как у маршрутов выгрузки) |
| Сеансы мероприятия, образец билета | `session.read` |
| Категории сеанса | `tier.read` |
| Заказы и карточка заказа | `order.read` |

## 6. Устройство приложения

1. Новое приложение `apps/event-center`. Стек как у админки: React 18, Vite 5, TanStack Router (дерево маршрутов в коде), TanStack Query, TypeScript в строгом режиме. Формы R1 простые, поэтому react-hook-form и zod подключаем в R2.
2. Типы ответов API — из общего сгенерированного файла `apps/backend/openapi/clients/ts/index.d.ts` (алиас `@openapi`). Свой генератор не нужен.
3. Все маршруты API, которые зовёт веб, записаны в одной таблице `src/lib/api/routes.ts`. Тест сверяет её с `openapi.yaml` (раздел 11).
4. Структура:
   - `src/lib/api` — клиент, сессия, таблица маршрутов, обёртки запросов;
   - `src/lib/i18n` — каталоги en, ru, es и проверка полноты;
   - `src/lib/format` — деньги и даты в часовом поясе площадки;
   - `src/routes` — экраны;
   - `src/parity` — матрица паритета (раздел 11);
   - `tests/smoke` — Playwright против подменённого API.
5. Экраны админки не импортируются. Общий код с админкой — только сгенерированные типы.

## 7. Экраны R1 (только чтение)

| Экран | Адрес | Что показывает | Маршруты API |
|---|---|---|---|
| Вход | `/login` | e-mail, пароль, «Забыли пароль» | `POST /v1/auth/login` |
| Забыли пароль | `/forgot-password` | e-mail | `POST /v1/auth/password-reset/request` |
| Выбор организации | `/orgs` | организации человека и его роль | `GET /v1/me` |
| Мероприятия | `/o/{org}/events` | вкладки «Идут» и «Архив», ближайшие сверху, состояние продаж, ближайший сеанс, число сеансов; для `order.read` — продано, свободно, деньги | `GET …/events`, `GET …/events/{id}/summary` |
| Мероприятие | `/o/{org}/events/{id}` | итоги, сеансы с цифрами, CSV, образец билета | `GET …/events/{id}/summary`, `GET …/events/{id}/sessions` |
| Сеанс | `/o/{org}/events/{id}/sessions/{sid}` | сводка сеанса, категории, CSV, образец билета, заказы сеанса | `GET …/sessions/{sid}/summary`, `GET …/tiers` |
| Заказы | `/o/{org}/orders` | вкладки «Последние», «Оплаченные», «Неоплаченные», поиск одной строкой, фильтр по мероприятию или сеансу | `GET …/orders` |
| Заказ | `/o/{org}/orders/{id}` | статус, сумма, покупатель, билеты, статус письма с билетами по каждому билету, причина неоплаты | `GET …/orders/{id}` |
| Уведомления | `/o/{org}/notifications` | ссылка на @ArenaSoldOutSalesBot | — |

Даты и время — в часовом поясе площадки сеанса, с подписью пояса. Номер заказа — `system_id`, не UUID. Контакты покупателя видит только тот, у кого есть `order.read` (владелец и менеджер).

## 8. Языки

1. С первого дня en, ru, es. Язык берётся из браузера, человек меняет его в шапке, выбор хранится на устройстве.
2. Каталоги — плоские ключи в TypeScript. Тест падает, если в каком-то языке нет ключа или у ключа другие подстановки `{name}`.
3. Тексты по стандарту: глоссарий (мероприятие, сеанс, бронь, площадка, возврат), тон раздела 2.1, ошибка всегда говорит, что делать дальше.
4. Иврит и письмо справа налево: атрибут `dir` уже переключается по языку. Каталог `he` добавим, когда владелец решит (раздел 14, вопрос 3).

## 9. Доступность

1. Цель — WCAG 2.2 AA.
2. Мобильная вёрстка первой: экран 375 пикселей без горизонтальной прокрутки, цель нажатия не меньше 44×44 пикселей.
3. Всё доступно с клавиатуры. Фокус видно. Первая ссылка — «Перейти к содержимому».
4. Каждое поле подписано (`label`). Ошибка формы связана с полем (`aria-describedby`) и объявляется (`role="alert"`).
5. Загрузка и пустые списки объявляются (`aria-live="polite"`). Состояние не передаётся только цветом: у значка есть слово.
6. Таблицы на телефоне превращаются в карточки, у каждой цифры есть подпись.
7. Контраст текста не ниже 4.5:1, крупного текста — 3:1. Тёмная тема по `prefers-color-scheme`.

## 10. Развёртывание

1. Dokploy, приложение (Application) как `tickets-page`: Dockerfile `apps/event-center/Dockerfile`, контекст сборки — корень репозитория, порт 8080, проверка `GET /health`.
2. Аргумент сборки `VITE_API_BASE_URL=https://api.arenasoldout.com`. Он попадает в сборку. Чтобы сменить адрес API, пересоберите образ.
3. Домен `events.arenasoldout.com` → порт 8080 контейнера, сертификат через Traefik, Cloudflare в режиме Full (strict) после выпуска сертификата.
4. На arena-api добавить `https://events.arenasoldout.com` в `CORS_ALLOWED_ORIGINS`. Без этого браузер блокирует запросы, и это похоже на 404.
5. `APP_PUBLIC_URL` не меняем: письма пока ведут в админку (раздел 4.2). Отдельный адрес для писем — задача A2.
6. Заголовки безопасности. Файл `security-headers.conf.template` подключается (`include`) в каждый location nginx, как в `tickets-page`. Иначе location со своим `add_header` теряет заголовки уровня server. Каждый адрес приложения (`/`, `/index.html`, глубокая ссылка, файлы `/assets/`) отдаёт:
   - `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self' <адрес API>; manifest-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'`;
   - `X-Content-Type-Options: nosniff`;
   - `X-Frame-Options: DENY`;
   - `Referrer-Policy: no-referrer`;
   - `Cross-Origin-Opener-Policy: same-origin`;
   - `Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=()`.

   Устаревший `X-XSS-Protection` не отдаётся. Проверка в CI: `apps/event-center/scripts/check-headers.sh` на собранном контейнере, затем Playwright smoke против того же контейнера, с включённым CSP.
7. CI: задача `event-center-build` — проверка типов, vitest, сборка, Playwright smoke против подменённого API (с проверкой доступности axe), сборка образа, ожидание `healthy`, проверка заголовков и тот же smoke против контейнера.
8. После выхода WEB-AUTH-1 в контейнер добавляется прокси `/api/` (раздел 4.4), и CORS для веба больше не нужен.

## 11. Матрица паритета «функция × бот × веб»

Источник — `apps/event-center/src/parity/matrix.ts`. Таблицу ниже рисует `renderParityTable()`. Тест `src/parity/parity.test.ts` проверяет три вещи:

1. каждый маршрут таблицы есть в `openapi.yaml` с тем же методом;
2. каждый маршрут, который зовёт веб (`src/lib/api/routes.ts`), относится к функции таблицы;
3. таблица в этом документе совпадает с выводом `renderParityTable()`. Обновить её: `UPDATE_PARITY=1 npm --prefix apps/event-center test`.

Итог по фазам: R1 — 18 функций, R2 — 3, R3 — 9, R4 — 8, R5 — 2, не планируется в вебе — 2.

<!-- PARITY:BEGIN -->
| Раздел | Функция | Бот | Веб | Маршруты API | Примечание |
|---|---|---|---|---|---|
| Вход и организация | Вход по e-mail и паролю | — (бот знает человека по Telegram) | R1 | `POST /v1/auth/login`<br>`POST /v1/auth/refresh`<br>`POST /v1/auth/logout` |  |
| Вход и организация | Забыли пароль | — | R1 | `POST /v1/auth/password-reset/request` | Ссылка из письма пока ведёт на APP_PUBLIC_URL (задача A2). |
| Вход и организация | Выбор организации | bot.go, `org:` | R1 | `GET /v1/me` |  |
| Вход и организация | Роль и права в организации | bot.go (`canViewSales`, `isOwner` по роли из /v1/me) | R1 | `GET /v1/organizations/{org_id}/me` | Бот пока решает по роли. Веб решает по правам A1. |
| Вход и организация | Язык интерфейса | bot.go, `lang:` | R1 | — | Хранится на устройстве. |
| Вход и организация | Заметка «оплата не подключена» | payments.go | R3 | `GET /v1/organizations/{org_id}/payment-configs` |  |
| Мероприятия | Список мероприятий | events_list.go, `el:` | R1 | `GET /v1/organizations/{org_id}/events` |  |
| Мероприятия | Цифры продаж в списке | — (в боте только на карточке) | R1 | `GET /v1/organizations/{org_id}/events/{event_id}/summary` | Один запрос на строку. Нужен пакетный маршрут. |
| Мероприятия | Карточка мероприятия с сеансами | event_card.go, `ec:o:` | R1 | `GET /v1/organizations/{org_id}/events`<br>`GET /v1/organizations/{org_id}/events/{event_id}/summary`<br>`GET /v1/organizations/{org_id}/events/{event_id}/sessions` |  |
| Мероприятия | Сводка мероприятия | event_summary.go, `ec:es:` | R1 | `GET /v1/organizations/{org_id}/events/{event_id}/summary` |  |
| Мероприятия | CSV продаж мероприятия | event_csv.go, `ec:ce:` | R1 | `GET /v1/organizations/{org_id}/events/{event_id}/sales.csv` |  |
| Мероприятия | Образец билета (PDF) | sample.go, `sample:` | R1 | `GET /v1/organizations/{org_id}/sessions/{session_id}/sample-ticket` |  |
| Мероприятия | Создание мероприятия (мастер) | wizard*.go, `wz:` | R2 | `GET /v1/geo/countries`<br>`GET /v1/geo/cities`<br>`GET /v1/organizations/{org_id}/venues`<br>`POST /v1/organizations/{org_id}/venues`<br>`POST /v1/organizations/{org_id}/cities`<br>`GET /v1/organizations/{org_id}/promoters`<br>`POST /v1/organizations/{org_id}/promoters`<br>`GET /v1/organizations/{org_id}/channels`<br>`POST /v1/media`<br>`GET /v1/media/{id}`<br>`POST /v1/organizations/{org_id}/imports/event-bundle` | Ждёт A3 «план мероприятия»: бот и веб должны сохранять одним серверным слоем. |
| Мероприятия | Чтение афиши | wizard_bot.go, `wz:hint` | R2 | `POST /v1/media` | Ждёт A4: чтение афиши на сервере, ключ модели не в клиенте. |
| Мероприятия | Правка и копия мероприятия | wizard_load.go, `wz:edit:`, `wz:copy:` | R2 | `GET /v1/events/{id}`<br>`GET /v1/organizations/{org_id}/events/{event_id}/sessions`<br>`GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers`<br>`GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule`<br>`POST /v1/organizations/{org_id}/imports/event-bundle` |  |
| Мероприятия | Публикация, снятие с продажи, архив | evstatus.go, `ec:ev:` | R4 | `GET /v1/events/{id}`<br>`POST /v1/organizations/{org_id}/events/{id}/status` |  |
| Мероприятия | Удаление мероприятия | evstatus.go, `ec:ev:…:del` (слово) | R4 | `GET /v1/organizations/{org_id}/events/{event_id}/delete-impact`<br>`DELETE /v1/organizations/{org_id}/events/{id}` |  |
| Мероприятия | Уведомления о продажах | notifications.go, `ec:nt` | R1 | — | Только ссылка на @ArenaSoldOutSalesBot. |
| Сеансы | Список сеансов и карточка сеанса | sessions_dialog.go, `ses:list:`, `ses:o:` | R1 | `GET /v1/organizations/{org_id}/events/{event_id}/sessions`<br>`GET /v1/organizations/{org_id}/sessions/{session_id}/summary` |  |
| Сеансы | Сводка сеанса | event_summary.go, `ec:ss:`, `ses:sm` | R1 | `GET /v1/organizations/{org_id}/sessions/{session_id}/summary` |  |
| Сеансы | CSV продаж и сводки сеанса | event_csv.go, `ec:cs:`, `ec:cm:`, `ses:csv` | R1 | `GET /v1/organizations/{org_id}/sessions/{session_id}/sales.csv`<br>`GET /v1/organizations/{org_id}/sessions/{session_id}/summary.csv` |  |
| Сеансы | Перенос сеанса | sessions_dialog.go, `ses:mv`, `ses:go` | R4 | `GET /v1/organizations/{org_id}/sessions/{session_id}/change-impact`<br>`PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}` |  |
| Сеансы | Отмена сеанса | sessions_dialog.go, `ses:cx`, `ses:cxall` (слово) | R4 | `GET /v1/organizations/{org_id}/sessions/{session_id}/change-impact`<br>`PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}` |  |
| Сеансы | Контакт организатора для писем | sessions_dialog.go (`sesSaveContact`) | R4 | `PUT /v1/organizations/{org_id}/events/{event_id}/contact` |  |
| Сеансы | Конец продаж и «вход с» | sessions_sale_times.go, `ses:se`, `ses:dr` | R4 | `PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{id}` |  |
| Категории | Категории сеанса (чтение) | categories.go, `ct:e:`, `ct:v:` | R1 | `GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers` |  |
| Категории | Открыть или закрыть продажи категории | categories.go, `ct:op`, `ct:cl` | R4 | `PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}` |  |
| Категории | Цена, окно продаж, количество | categories.go, `ct:pr`, `ct:wn`, `ct:qt` | R4 | `PATCH /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}`<br>`GET /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule`<br>`PUT /v1/organizations/{org_id}/events/{event_id}/sessions/{session_id}/tiers/{id}/price-schedule` |  |
| Заказы | Список заказов, вкладки, поиск | orders_list.go, `or:` | R1 | `GET /v1/organizations/{org_id}/orders` |  |
| Заказы | Карточка заказа и статус письма с билетами | order_card.go, `or:v:` | R1 | `GET /v1/organizations/{org_id}/orders/{id}` |  |
| Заказы | Отмена неоплаченного заказа | order_card.go, `or:c:` (слово) | R3 | `POST /v1/organizations/{org_id}/orders/{id}/cancel` |  |
| Заказы | Повторная отправка билетов | resend.go, `or:r:` | R3 | `POST /v1/organizations/{org_id}/orders/{order_id}/resend-tickets` |  |
| Возвраты | Возврат по заказу | refund_dialog.go, `or:f:` (слово и кнопка) | R5 | `GET /v1/organizations/{org_id}/orders/{order_id}/refund-options`<br>`POST /v1/organizations/{org_id}/orders/{order_id}/refunds` |  |
| Возвраты | Очередь возвратов и повтор | refunds_queue.go, `rq:` | R5 | `GET /v1/organizations/{org_id}/refunds`<br>`GET /v1/organizations/{org_id}/refunds/{refund_id}`<br>`POST /v1/organizations/{org_id}/refunds/{refund_id}/retry` |  |
| Промокоды | Список, карточка, использование | promo_dialog.go, `pm:` | R3 | `GET /v1/organizations/{org_id}/promo-codes`<br>`GET /v1/organizations/{org_id}/promo-codes/{id}`<br>`GET /v1/organizations/{org_id}/promo-code-redemptions`<br>`GET /v1/organizations/{org_id}/promo-codes/{promo_code_id}/redemptions.csv` |  |
| Промокоды | Создание, пауза, сеансы, удаление | promo_create.go, `pm:n`, `pm:ps`, `pm:se`, `pm:del` | R3 | `POST /v1/organizations/{org_id}/promo-codes`<br>`PATCH /v1/organizations/{org_id}/promo-codes/{id}`<br>`DELETE /v1/organizations/{org_id}/promo-codes/{id}` |  |
| Пригласительные | Выдача пригласительных | invite.go, `iv:i` | R3 | `POST /v1/organizations/{org_id}/complimentary` |  |
| Пригласительные | Список и аннулирование | invite_list.go, `iv:l`, `iv:rv` (слово) | R3 | `GET /v1/organizations/{org_id}/complimentary`<br>`POST /v1/complimentary/{id}/revoke` |  |
| Команда | Команда, приглашение, удаление | team.go, `team:` | R3 | `GET /v1/organizations/{org_id}/bot-team`<br>`POST /v1/organizations/{org_id}/bot-invitations`<br>`DELETE /v1/organizations/{org_id}/bot-invitations/{id}`<br>`POST /v1/organizations/{org_id}/bot-invitations/{id}/resend`<br>`DELETE /v1/organizations/{org_id}/members/{user_id}` | Ждёт A2: приглашение с каналом web или telegram. |
| Промоутеры | Список, правка, архив | promoters.go, `pr:` | R3 | `GET /v1/organizations/{org_id}/promoters`<br>`PATCH /v1/organizations/{org_id}/promoters/{id}` |  |
| Не в ивент-центре | Анкета организатора | onboarding_dialog.go, `onb` | — | `GET /v1/onboarding/form-schema`<br>`POST /v1/onboarding/applications` | В вебе анкета живёт на сайте arenasoldout.com. |
| Не в ивент-центре | Принять приглашение в команду | bot.go, `/start inv_…` | — | `POST /v1/bot/invitations/accept` | В вебе: письмо с установкой пароля (A2). |
<!-- PARITY:END -->

## 12. Фазы и серверные задачи

| Задача | Что | Статус |
|---|---|---|
| A1 | `GET /v1/organizations/{org_id}/me` — роль и права в организации | сделано, PR avamb/arena-platform#81 |
| R1 | чтение: вход, организации, мероприятия, сеансы, сводки, заказы, CSV, образец билета | этот этап |
| WEB-AUTH-1 | httpOnly-cookie, прокси `/api/`, CSRF (раздел 4.4) | до R5 |
| WEB-AUTH-2 | одноразовая ссылка из бота (раздел 4.3) | после R1 |
| WEB-CSP | заголовки безопасности и CSP в nginx веба | сделано в R1 (раздел 10.6) |
| A2 | приглашение с каналом `web` или `telegram`, адрес писем по каналу | до R3 |
| A3 | «план мероприятия»: проверка, атомарное сохранение всех дат, чтение, значения по умолчанию | до R2 |
| A4 | чтение афиши на сервере | до R2 |
| A5 | геопомощники: часовые пояса, города, валюта | до R2 |
| A6 | публичная ссылка мероприятия в ответе API | до R2 |
| WEB-LIST | пакетные цифры продаж для списка мероприятий (сейчас один запрос на строку) | после R1 |
| WEB-ORDER-EVENT | имя мероприятия и время сеанса в `GET …/orders/{id}` (сейчас веб и бот берут их из строки списка) | после R1 |
| R2 | мастер и правка | после A3–A6 |
| R3 | промокоды, пригласительные, повторная отправка, команда, промоутеры | после A2 |
| R4 | перенос и отмена сеансов, категории, конец продаж, статус мероприятия | после R2 |
| R5 | возвраты | после выпуска стека возвратов и WEB-AUTH-1 |

## 13. Что R1 нашёл в API (не сделано в этом этапе)

1. Нет `GET /v1/organizations/{org_id}/events/{id}`. Есть только `PATCH` и `DELETE`. Карточку веб собирает из строки списка (`GET …/events`) и сводки. Плоский `GET /v1/events/{id}` есть, но веб его не берёт: адрес без организации.
2. Список мероприятий не несёт цифр продаж. Веб запрашивает сводку для каждой видимой строки (до 10 запросов). Нужна задача WEB-LIST.
3. `GET …/orders/{id}` не несёт имя мероприятия и время сеанса. Веб берёт их из строки списка, как бот. Нужна задача WEB-ORDER-EVENT.
4. Ссылки в письмах о пароле ведут в админку (раздел 4.2, задача A2).
5. Поиск заказов передаёт e-mail или телефон покупателя в строке запроса `GET …/orders?q=`. arena-api пишет в журнал только путь, но строку запроса видят журналы Traefik и Cloudflare. Веб держит поиск в памяти вкладки, не в адресе страницы. Перенос поиска в тело запроса — вопрос 8.

## 14. Вопросы владельцу

1. **Защита денежных действий.** Перед возвратом (R5) спрашивать пароль ещё раз или включить вторую защиту (код из приложения или письмо)? Рекомендация: повторный пароль перед возвратом в R5, вторая защита — отдельной задачей позже.
2. **Срок сессии.** Сейчас вкладка держит сессию до закрытия, refresh живёт 30 дней. Для веба на общем компьютере предлагаю сессию не дольше 12 часов без действий. Согласны?
3. **Иврит.** План волны говорит «иврит и письмо справа налево с первого дня». В этом этапе сделаны en, ru, es и переключение `dir`. Добавлять `he` в R1 или вместе с R2?
4. **Письма о пароле.** Оставить ссылки в админке до задачи A2 или сразу завести второй адрес (`EVENT_CENTER_PUBLIC_URL`)?
5. **Одноразовая ссылка из бота.** Кнопка «Открыть в браузере» в главном меню бота для всех ролей или только для владельца?
6. **Суперадмин.** Подтвердите: суперадмин в вебе видит только выбор организации и подсказку «работайте в админке».
7. **Агент (`agent`).** У агента почти нет прав в ивент-центре. Показывать ему веб (список мероприятий без цифр) или сразу писать «попросите владельца выдать роль менеджера»?
8. **Поиск покупателя в адресе запроса.** Веб больше не пишет поиск в адрес страницы. Но запрос к API по-прежнему несёт e-mail или телефон в строке запроса, и его видят журналы прокси (раздел 13, п. 5). Бот делает так же. Завести `POST`-поиск заказов (поиск в теле запроса) для веба и бота?
9. **Приложение на телефон и ссылки на веб.** Делать веб устанавливаемым приложением (PWA: иконка на экране телефона)? Ставить ссылку на веб-ивент-центр на страницу промоутера и в письма организатору (приглашение, пароль, уведомления)?
