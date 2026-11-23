# Пакет для проверки приложения Meta «Arena Sold Out» (App Review)

Версия от 23.11.2026. Приложение «Arena Sold Out», App ID `1130175216502536`, портфолио Worldbm
(141351680808490), компания ABH TEAM OÜ подтверждена, статус Tech Provider, режим разработки.
Конфигурация Facebook Login for Business: `1110037748538625` «Arena Sold Out Connect».

Этот документ готовит подачу. В Meta ничего не отправлено, в панели ничего не изменено.
Тексты для полей Meta даны по-английски, каждый в своём блоке кода: скопируйте блок целиком.

Источники:
- спека `08_architecture/66_meta_ads_integration_ru.md` §4 и §6, ловушки `docs/agents/meta-integration.md`
  (обе пока в ветке `feat/meta-m1-connect`, PR avamb/arena-platform#467);
- код М1 в той же ветке; код М2 (ветка `feat/meta-m2-insights`) на 23.11 ещё не начат: в рабочей копии нет правок;
- страницы https://arenasoldout.com/legal/privacy (версия 22.11.2026), /legal/terms (9.10.2026),
  /legal/data-deletion (22.11.2026);
- документация Meta (ссылки в §5 и в тексте).

## 0. Коротко: что готово и что мешает подаче

Без этих пунктов подача будет отклонена или часть разрешений не одобрят.

| # | Что мешает | Кто делает |
|---|---|---|
| 1 | Код М1 (PR #467) не на проде и, возможно, не на стенде. Рецензент Meta должен войти и пройти весь путь | слияние #467, выкат |
| 2 | Нет кода М2 (расходы). Без него нечем доказать `ads_read` (Insights) и нечем набрать 500 вызовов Marketing API | сессия М2 |
| 3 | Нет кода для `ads_management` (ручная пауза кампании). Без записи экрана с ним Meta это разрешение не одобрит | решение владельца, §1.3 |
| 4 | `pages_read_engagement` в коде не используется отдельно: читаем только `id,name` страниц | маленькая правка, §1.5 |
| 5 | Каждое разрешение: хотя бы 1 успешный вызов за 30 дней до подачи. Учёт в панели идёт до 2 суток | §4.4 |
| 6 | Полный Marketing API Access Tier: 500 успешных вызовов за 15 дней, ошибок < 15 % среди последних 500 | §4.3, 15 дней от старта |
| 7 | Расхождения политики и кода (раздел 3.3) | владелец, сайт |

Вывод: **утром можно подать `business_management`, `pages_show_list` и, после правки §1.5,
`pages_read_engagement`**, если М1 уже выкачен. `ads_read` и Access Tier — когда М2 на стенде и вызовы набраны.
`ads_management` — только с кнопкой паузы. Рекомендация: подать двумя заявками, а не ждать всё сразу (§1.7).

## 1. Что запрашиваем

Meta пишет: «Only select permissions that your app needs» — лишнее разрешение — частая причина отказа
([Permissions Reference](https://developers.facebook.com/docs/permissions)). Поэтому каждое разрешение ниже привязано к реальному вызову в коде.

Зависимости по справочнику Meta: `ads_management` и `business_management` зависят от
`pages_read_engagement` и `pages_show_list`; `pages_read_engagement` зависит от `pages_show_list`;
у `ads_read` и `pages_show_list` зависимостей нет.

Что Meta хочет видеть на записи для каждого рекламного разрешения: весь вход Facebook и выдачу прав,
бизнес, который пользуется функцией, и показанные данные (расходы, показы, клики, конверсии).

### 1.1. `business_management`

Вызовы в коде М1 (`metaconnect/graph.go`), все — после входа, токеном BISU из `finish`:
- `GET /me?fields=id,client_business_id` — бизнес клиента и app-scoped id;
- `GET /me/permissions` — какие права выдал организатор;
- `GET /{business}/owned_pixels`, `/{business}/client_pixels`, `/{business}/owned_ad_accounts`,
  `/{business}/client_ad_accounts`, `/{business}/owned_pages`, `/{business}/client_pages`
  (список задаёт `META_ASSET_EDGES`);
- `DELETE /me/permissions` при отключении (как получится).

Статус: **есть в коде**. Риск: какие рёбра реально отвечают токену BISU, не проверено живым входом (ловушка в
`meta-integration.md`). Проверить на стенде до записи: в экране должны появиться пиксели, кабинеты и страницы.

На записи: вход Meta → выбор бизнеса и объектов → в ивент-центре виден Business ID, пиксели, кабинеты, страницы.

```text
Arena Sold Out is a ticketing platform for event organizers. When an organizer connects Meta through Facebook Login for Business, we use business_management to read the organizer's business ID and the pixels (datasets), ad accounts and Pages that the organizer shared with our app (owned_pixels, client_pixels, owned_ad_accounts, client_ad_accounts, owned_pages, client_pages). We show these assets in the Event Center, so that the owner of the organization can select the dataset for server-side purchase events and the ad account for spend reports. We do not create, change or claim any business assets.
```

### 1.2. `ads_read`

Вызовы:
- М1: отправка покупок по Conversions API, `POST /{pixel_id}/events` с `partner_agent: arenasoldout`
  (`conversions/senders.go`). Справочник Meta относит CAPI к разрешённому использованию `ads_read`.
- М2 (**нужен код**): `GET /act_{ad_account_id}/insights?level=ad&time_increment=1&fields=spend,impressions,clicks,actions,action_values,campaign_id,campaign_name,adset_id,adset_name,ad_id,ad_name` — задача `meta.insights_sync` и кнопка «обновить» (спека §5).
- М1 уже читает у кабинета `currency,timezone_name` через рёбра бизнеса.

На записи: тестовая покупка доходит в Events Manager → Test events; экран «Реклама» события с расходами, показами, кликами, покупками.

```text
We use ads_read for two features. First, the Conversions API: when a buyer pays for a ticket, our server sends a Purchase event to the dataset that the organizer selected (POST /{pixel_id}/events with partner_agent "arenasoldout"), so the organizer can measure their ads. Second, ad spend reports: once a day, and when the owner presses "Refresh", we read Ads Insights of the selected ad account (spend, impressions, clicks, purchases and purchase value by campaign, ad set, ad and day). The Event Center shows these numbers next to our own ticket sales for each event, so the organizer sees the return on ad spend. We only read data; we do not change ads with this permission.
```

Абзац про отчёты о расходах вставлять **только когда М2 выкачен** и экран расходов есть на записи.
До того — только первые два предложения и последнее.

### 1.3. `ads_management`

Вызов, который нужен: `POST /{campaign_id}` с `status=PAUSED` (и обратно `ACTIVE`) по кнопке владельца,
плюс `GET /act_{id}/campaigns?fields=id,name,status,effective_status` для списка кампаний.

Статус: **нужен код**. В М1 и в планах М2 изменения рекламы нет. Это деньги клиента: по правилу проекта
красная зона, код и ревью на opus, журнал действий, кнопка только у владельца (`org.update`).

Варианты для владельца:
1. **Рекомендую:** не подавать `ads_management` в первой заявке. Meta прямо отклоняет «permissions you may need later».
   Подать его вместе с М4/М5 или после минимальной кнопки паузы.
2. Подать сразу: тогда сначала сделать кнопку «Pause / Resume» кампании в экране «Реклама» события,
   с подтверждением и записью в аудит, и показать её на записи на своём кабинете.

Текст для поля — только для варианта 2, когда кнопка есть:

```text
We use ads_management so that the owner of an organization can pause and resume their own Meta campaigns from the Event Center, for example when an event is sold out. The Event Center lists the campaigns of the selected ad account (GET /act_{id}/campaigns) and shows a "Pause" or "Resume" button for each one. A change is made only after the owner presses the button and confirms it (POST /{campaign_id} with status PAUSED or ACTIVE), and every change is written to the organization's audit log. We never create campaigns or change budgets with this permission.
```

### 1.4. `pages_show_list`

Вызовы М1: `GET /{business}/owned_pages`, `/{business}/client_pages`, `GET /me/accounts`, поля `id,name`.
Статус: **есть в коде** (экран подключения, блок «Pages»).

На записи: в окне Meta выбрать страницу → в ивент-центре в блоке «Pages» видно её имя.

```text
We use pages_show_list to list the Facebook Pages that the organizer shared with our app during Facebook Login for Business. The Event Center shows the name of each Page in the Meta connection screen, so the owner can see which Pages are connected to their organization. This permission is also a dependency of business_management, which we request for the same connection.
```

### 1.5. `pages_read_engagement`

Сейчас код читает у страниц только `id,name` — это `pages_show_list`. Отдельного использования
`pages_read_engagement` нет. Спека подаёт его как обязательное для CAPI-платформы и как зависимость
`business_management`/`ads_management` (так и в справочнике Meta).

**Нужен код (маленький):** при подключении читать у выбранной страницы метаданные, которые даёт это разрешение,
например `GET /{page_id}?fields=name,link,picture,followers_count`, и показывать ссылку, картинку и число подписчиков
в блоке «Pages». Это честное и видимое использование. Без такой правки текст ниже нельзя подавать, а
рецензент увидит только список страниц.

```text
We request pages_read_engagement because it is a dependency of business_management, and because the Event Center shows the metadata of each connected Page. After the organizer connects Meta, we read the Page name, link, profile picture and follower count (GET /{page_id}?fields=name,link,picture,followers_count) and show them in the Meta connection screen, so the owner can confirm that the right Page of their brand is connected. We do not read or store Page posts, comments or messages.
```

### 1.6. Marketing API Access Tier (полный доступ)

Условия Meta ([Authorization](https://developers.facebook.com/docs/marketing-api/overview/authorization),
[Access Levels](https://developers.facebook.com/docs/graph-api/overview/access-levels)):
не меньше **500 успешных вызовов Marketing API за последние 15 дней** и доля ошибок **ниже 15 %** среди последних 500.
Уровень надо держать и после одобрения. Подача — кнопка «+Upgrade» у функции Marketing API Access Tier в панели,
затем App Review. Ограниченный уровень — только для разработки, не для живых рекламодателей.

Чем набираем: синхронизация Insights М2 (по часам для пилота), кнопка «обновить», чтение кампаний и кабинетов.
Ежедневная синхронизация 1–2 кабинетов даёт около 30 вызовов за 15 дней — мало. Как набрать — §4.3.

```text
Arena Sold Out connects the Meta ad accounts of many independent event organizers through Facebook Login for Business. For every connected ad account we sync Ads Insights at least once a day (hourly during the pilot) and on demand when the owner presses "Refresh", and we read the account's campaigns, currency and time zone. We need the full Marketing API access tier so that these syncs work for organizers who are not people with a role in our app, and so that the rate limits fit many ad accounts.
```

### 1.7. `public_profile`

Справочник Facebook Login for Business говорит, что перед переходом в Live приложению нужен расширенный
доступ к `public_profile`. Обычно он одобряется автоматически. Проверить статус в «App Review → Permissions and Features»;
если там «Request advanced access», запросить в той же заявке, отдельный текст не нужен.

### 1.8. Рекомендуемый порядок заявок

1. **Заявка 1 (как только М1 на проде и вызовы учтены):** `business_management`, `pages_show_list`,
   `pages_read_engagement` (после правки §1.5), `ads_read` (CAPI-часть, тестовая покупка в Test events).
2. **Заявка 2 (через 15 дней работы М2):** Marketing API Access Tier, дополнение к `ads_read` с экраном расходов.
3. **Заявка 3 (когда есть кнопка паузы):** `ads_management`.

Если владелец хочет одну заявку — её можно подать не раньше, чем через 15 дней после выката М2.

## 2. Сценарий записи экрана

Требования Meta ([Submission Guide](https://developers.facebook.com/docs/app-review/submission-guide)):
интерфейс на английском; показать **весь вход Facebook**, начиная с выхода; для каждого разрешения — выдачу прав,
функцию и показанные данные; разрешение 1080p или выше, ширина экрана не больше 1440; крупный курсор; мышь, а не клавиатура;
без звука; подписи на экране для неочевидных шагов. Ограничения длины Meta не называет. Каждое разрешение должно
иметь свою запись: можно загрузить один и тот же ролик к каждому разрешению и в примечании указать время нужного куска.

### 2.1. Подготовка (за 15 минут до записи)

1. Стенд или прод с выкаченным М1 (и М2 для `ads_read`). Решите, где записываете, и пишите там же, где будет тестировать рецензент (§4.5).
2. Ивент-центр на английском: переключатель «Language» в меню → English. Браузер в английской локали, окно 1440×900 или 1280×800, масштаб 100–125 %.
3. Выйдите из Facebook в этом браузере (Meta требует показать вход с нуля). Лучше отдельный профиль Chrome без расширений.
4. Подключение Meta у тестовой организации ABH TEAM **отключено** (кнопка «Disconnect»), и в Meta Business Suite
   приложение «Arena Sold Out» удалено из «Интеграции», чтобы окно согласия показалось полностью.
5. В Events Manager у пикселя ABH TEAM открыта вкладка **Test events**, код теста скопирован.
   Код теста задаётся только через API `PATCH /v1/organizations/{org_id}/conversion-settings`
   (поле `meta_test_event_code`), в ивент-центре поля для него нет. Вставьте его до записи.
6. Тестовое событие ABH TEAM на продаже, оплата в тестовом режиме провайдера.
7. Для экрана расходов: в кабинете есть кампания с расходами за прошлые дни (иначе экран будет пустым — Meta откажет).
8. Включите подписи: удобно в OBS или в редакторе после записи. Курсор крупный (Windows: «Указатель мыши» → размер 3).

### 2.2. Шаги и время

| Время | Что на экране | Подпись на экране (по-английски) | Что доказывает |
|---|---|---|---|
| 0:00–0:15 | Заставка: название приложения, App ID | `Arena Sold Out — Meta connection for event organizers` | — |
| 0:15–0:45 | Страница входа ивент-центра, вход тестовым владельцем ABH TEAM | `The organizer signs in to the Arena Sold Out Event Center` | доступ к приложению |
| 0:45–1:00 | Меню → «Advertising» → «Meta». Экран «Meta advertising», статус «Meta is not connected» | `Advertising → Meta` | — |
| 1:00–1:10 | Кнопка «Connect Meta», переход на facebook.com (адрес `dialog/oauth?...config_id=1110037748538625`) | `Facebook Login for Business opens` | вход Facebook |
| 1:10–1:40 | Вход в Facebook (логин и пароль замазать в редакторе) | `The organizer logs in to Facebook` | весь вход с выхода |
| 1:40–2:30 | Окно согласия: название «Arena Sold Out», выбор бизнеса, пикселя, рекламного кабинета, страницы; список прав; «Continue»/«Save» | `The organizer chooses the business, the dataset, the ad account and the Page` | выдача `business_management`, `ads_read`, `pages_*` |
| 2:30–2:45 | Возврат в ивент-центр: «Finishing the connection with Meta…» → «Meta is connected.» | `Back in the Event Center` | — |
| 2:45–3:30 | Блоки экрана: Business ID, Permissions, Datasets (pixels), Ad accounts, Pages; выбор «Dataset for purchases» и «Ad account» → «Saved.» | `Assets read with business_management and pages_show_list` | использование `business_management`, `pages_show_list`, `pages_read_engagement` (если §1.5 сделан) |
| 3:30–4:30 | Новая вкладка: hosted-страница события, покупка 1 билета тестовой картой | `A buyer buys a ticket` | — |
| 4:30–5:15 | Events Manager → пиксель → Test events: событие Purchase, источник Server, partner agent | `The Purchase event arrives through the Conversions API` | `ads_read` (CAPI) |
| 5:15–6:15 | Ивент-центр → событие → экран расходов (М2): потрачено, показы, клики, покупки, выручка, MER по кампаниям и объявлениям; кнопка «Refresh» | `Ad spend read with Ads Insights (ads_read)` | `ads_read` (Insights), Access Tier |
| 6:15–7:00 | Только при `ads_management`: список кампаний, «Pause» → подтверждение → статус Paused в ивент-центре и в Ads Manager | `The owner pauses a campaign` | `ads_management` |
| 7:00–7:30 | «Disconnect» → подтверждение → «Meta is not connected» | `The owner can disconnect Meta at any time` | удаление доступа |

Итого 6–7,5 минут. Если М2 ещё нет, строки 5:15–6:15 и 6:15–7:00 не записывать и `ads_read` подавать только с CAPI.

## 3. Ответы на вопросы об обработке данных

Вопросы Meta ([Data Handling Questions](https://developers.facebook.com/docs/development/release/data-handling-questions/questions-preview)):
обработчики и их страны; кто контролирует данные и его страна; запросы госорганов по национальной безопасности за
12 месяцев; правила работы с такими запросами. Ответы Meta проверяет сразу. Meta советует свериться с юристом —
пусть владелец проверит ответы 3.2 и 3.4.

### 3.1. Обработчики (data processors / service providers)

Ответ на «Do you have data processors or service providers that will access Platform Data?» — **Yes**.

| Обработчик | Категория в анкете Meta | Страны | Что видит |
|---|---|---|---|
| Hetzner Online GmbH | IT solutions and services, including cloud storage and processing | Germany, Finland (**уточнить**, в каких центрах стоят наши серверы) | база данных и резервные копии |
| Cloudflare, Inc. | IT solutions and services | United States и сеть Cloudflare по миру | трафик API и ивент-центра проходит через их прокси (HTTPS прерывается на Cloudflare) |

Brevo и Telegram данные Meta сейчас не получают. Если М2 будет слать сводку расходов в Telegram (спека §5),
Telegram придётся добавить сюда (страны — по их политике) до выката этой сводки.

Описание для поля «Other / details», если есть:

```text
Hetzner Online GmbH hosts our servers and encrypted backups in data centres in the European Union. Cloudflare, Inc. provides the content delivery network and DDoS protection for our website and API, so requests and responses pass through its network. Both work under data processing agreements. No other service provider accesses Platform Data.
```

### 3.2. Кто контролирует данные (data controller)

Название: `ABH TEAM OÜ`. Страна: **Estonia**.

```text
ABH TEAM OÜ, Narva mnt 5a, 10117 Tallinn, Estonia, Estonian Commercial Register code 14162982. Contact: support@arenasoldout.com.
```

### 3.3. Запросы госорганов по национальной безопасности за 12 месяцев

Ответ: **No** (если владелец подтверждает, что таких запросов не было).

### 3.4. Правила работы с запросами госорганов

Отметить: **Required review of the legality of these requests**, **Provisions for challenging these requests if they are considered unlawful**,
**Data minimization policy — the ability to disclose the minimum information necessary**,
**Documentation of these requests, including your responses to the requests and the legal reasoning and actors involved**.
Отмечать только то, что владелец готов выполнять. Политика на сайте говорит лишь «если требует закон» (раздел 2.4) —
см. расхождение 6 ниже.

### 3.5. Описание работы с данными (для поля описания, Data Use Checkup и инструкций)

Каждый блок — отдельное поле или отдельный абзац, если Meta спросит.

Хранение и шифрование:

```text
Platform Data is stored in a PostgreSQL database on servers of Hetzner Online GmbH in the European Union, with nightly encrypted backups. The Meta access token of each organization is encrypted at rest with AES-256-GCM; the key is kept only in the server environment, never in the code repository, and the encrypted value is bound to the organization, so it cannot be used for another organization. The token is never returned by our API, shown in the user interface, or written to logs or the audit log. All traffic uses HTTPS.
```

Доступ сотрудников:

```text
Inside an organization, only the owner can connect, change or disconnect Meta; other team members cannot. At ABH TEAM OÜ, only the engineers who operate the platform have access to the production database and servers, through personal accounts. They cannot read Meta access tokens because the tokens are encrypted and the key is held only by the server process.
```

Что храним и сколько:

```text
For each connected organization we store: the encrypted access token, the app-scoped Meta user ID, the business ID, the granted permissions, the IDs and names of the shared pixels, ad accounts and Pages, and the selected pixel and ad account. For ad spend reports we store daily ad statistics (spend, impressions, clicks, purchases and purchase value by campaign, ad set and ad). We keep this data while the connection is active. When the owner disconnects, or the organization removes the app in Meta, we delete the token immediately and the remaining Meta data within one month.
```

Удаление по запросу и через callback:

```text
An owner can disconnect Meta in the Event Center at any time: we ask Meta to revoke the access and delete the token at once. We also handle Meta's deauthorize callback (https://api.arenasoldout.com/v1/integrations/meta/deauthorize), which deletes the token, and the data deletion callback (https://api.arenasoldout.com/v1/integrations/meta/data-deletion), which deletes the connection and all stored assets and returns a confirmation code and a status URL. Instructions for a manual request are at https://arenasoldout.com/legal/data-deletion; requests sent to support@arenasoldout.com are answered within one month.
```

Передача третьим лицам:

```text
We do not sell Platform Data and we do not share it with third parties. Purchase events are sent only to the organizer's own Meta dataset, on the organizer's instruction. Ad statistics are shown only to the organization they belong to.
```

### 3.6. Расхождения политики, кода и этих ответов — для владельца

1. **Hetzner не назван.** Политика (2.1) говорит «серверы в ЕС у хостинг-провайдеров», имени нет. Meta спрашивает
   конкретных обработчиков. Добавить в политику Hetzner Online GmbH (Германия) и Cloudflare, Inc. (США) поимённо.
2. **Передача за пределы ЕС не описана.** Cloudflare, Inc. — компания США, политика не называет основание передачи
   (стандартные договорные условия или EU-US Data Privacy Framework). Добавить.
3. **Шифрование AES-256-GCM.** Политика (1.10) говорит только «stored encrypted». Не ошибка, но можно уточнить.
4. **Срок удаления.** Политика (3.2(g)): токен, id и статистика удаляются «within one month». Код делает быстрее:
   отключение и deauthorize стирают токен сразу, но **deauthorize оставляет строку подключения и объекты**
   (`Revoke(..., deleteAssets=false)`), data-deletion удаляет всё сразу. Нужна задача, которая удаляет оставшееся в течение месяца,
   или deauthorize тоже должен удалять объекты. Иначе обещание «within one month» не выполняется. **Нужен код.**
5. **Статистика расходов (1.10) уже описана в политике, а кода М2 нет.** Не страшно (политика может опережать код),
   но в ответах Meta про `ad_spend_daily` писать только после выката М2 — или оставить абзац, если подача после М2.
6. **Запросы госорганов.** Политика (2.4) и условия (3, 7) говорят только «раскрываем, если требует закон». Если в анкете
   отмечаются проверка законности, оспаривание, минимизация и журнал, добавить это в политику одним абзацем.
7. **Страница удаления не знает про код подтверждения.** Callback отдаёт `url = https://arenasoldout.com/legal/data-deletion?code=<код>`,
   но страница с `?code=TEST123` ничем не отличается от обычной: нет ни кода, ни статуса. Meta требует, чтобы по ссылке человек
   видел статус удаления. Этим занимается сессия М0 / data-deletion-code; проверить до подачи.
8. **Страница удаления не называет кнопку «Disconnect» в ивент-центре.** Добавить путь: Event Center → Advertising → Meta → Disconnect.
9. **Нет поимённого списка обработчиков** (почта, аналитика, поддержка без имён в 2.2). Для Meta достаточно 3.1, но для GDPR лучше список.
10. **Доступ сотрудников.** Политика (4.2) — «staff who need it». Ответ 3.5 точнее; согласовать формулировку.
11. **Telegram и сводка расходов М2.** Если сводка будет, политика уже называет Telegram, но анкету Meta нужно дополнить (3.1).

## 4. Чек-лист перед подачей

### 4.1. Код и выкат

- [ ] PR #467 (М1) слит, выкачен на стенд и прод; переменные `META_*` заданы на arena-api и arena-worker; ключ `META_TOKEN_ENC_KEY` разный для стенда и прода.
- [ ] Живой вход на стенде прошёл: в экране есть пиксели, кабинеты и страницы (проверка рёбер `META_ASSET_EDGES`).
- [ ] Правка §1.5 (`pages_read_engagement`) — если подаём это разрешение.
- [ ] М2 выкачен — для экрана расходов и Access Tier.
- [ ] Кнопка паузы — только если подаём `ads_management`.
- [ ] Страница `/legal/data-deletion?code=…` показывает код и статус (расхождение 7).
- [ ] Удаление после deauthorize в течение месяца (расхождение 4).

### 4.2. Панель приложения

- [ ] «Основные настройки»: политика, условия, URL инструкции по удалению данных, значок 1024×1024, категория, контактный e-mail — заполнены.
- [ ] **Deauthorize callback URL** = `https://api.arenasoldout.com/v1/integrations/meta/deauthorize` — вписать **после выката М1 на прод**.
- [ ] **Data deletion callback URL** = `https://api.arenasoldout.com/v1/integrations/meta/data-deletion` — после выката; проверить кнопкой проверки в панели, если она есть.
- [ ] Facebook Login for Business → Settings: адреса возврата `https://api.arenasoldout.com/v1/integrations/meta/callback` и `https://api-staging.arenasoldout.com/v1/integrations/meta/callback`; Client OAuth и Web OAuth включены, Enforce HTTPS включён. JS SDK не нужен: код М1 его не использует (решение 23.11), заметка М1 из общей папки про «включить JS SDK» устарела.
- [ ] Конфигурация `1110037748538625`: разрешения `ads_read`, `business_management`, `pages_show_list`, `pages_read_engagement` (и `ads_management`, если подаём); объекты: пиксели, рекламные аккаунты, страницы.
- [ ] Вебхук `business_integration_uninstall` — пока долг М1, на подачу не влияет.
- [ ] **Data Use Checkup:** в режиме разработки не обязателен, но нужен до перехода в Live; включает те же вопросы об обработке данных (§3). Заполнить в «App Review → Data Use Checkup», если панель предлагает.
- [ ] **Режим Live.** Спека §6 говорит «перевести в Live перед подачей». Руководство Meta по подаче ставит переход в Live **последним шагом, после одобрения**, а в Live приложение просит только одобренные разрешения — даже у людей с ролью. Значит, переход до одобрения сломает вход у нас самих. Рекомендация: **не переключать до одобрения**, если панель сама не требует. Сверить в панели перед нажатием.

### 4.3. 500 вызовов Marketing API за 15 дней

Что считается вызовом Marketing API, Meta точно не пишет; надёжно — вызовы рекламных объектов: `act_*`, `/insights`,
кампании, группы, объявления, пиксели. Вызовы CAPI (`/{pixel}/events`) могут считаться, но не рассчитывайте на них.

Способ 1 (лучший, после М2): синхронизация Insights по часам для ABH TEAM и, по согласию, Vino&Co. 2 кабинета × 24 часа ×
(insights + campaigns) ≈ 96 вызовов в сутки, 500 — за 5–6 дней. Ошибки держать около нуля: не звать кабинет без доступа.

Способ 2 (сразу, вручную, кабинеты владельца): Graph API Explorer, приложение «Arena Sold Out», токен пользователя
владельца с правами `ads_read`, `business_management`. Токен вводит владелец сам, в чат его не копировать.
Команда ниже делает 40 успешных чтений; запускать раз в день 15 дней (или 13 раз подряд в один день — Meta смотрит
сумму за 15 дней, но равномерность выглядит естественнее). `act_<ID>` — ваш рекламный кабинет.

```powershell
$token = Read-Host "Token from Graph API Explorer"
```

```powershell
$acct = "act_<ID>"; $base = "https://graph.facebook.com/v25.0"; 1..10 | ForEach-Object { foreach ($p in @("$acct`?fields=name,currency,timezone_name", "$acct/campaigns?fields=id,name,status&limit=25", "$acct/insights?fields=spend,impressions,clicks&date_preset=last_30d", "$acct/adsets?fields=id,name,status&limit=25")) { try { Invoke-RestMethod -Uri "$base/$p&access_token=$token" | Out-Null; "ok $p" } catch { "ERROR $p $($_.Exception.Message)" } ; Start-Sleep -Seconds 2 } }
```

Строк `ERROR` должно быть 0. Если они есть — остановитесь: ошибки сверх 15 % среди последних 500 закрывают доступ.
Прогресс смотреть: панель → App Review → Permissions and Features → Marketing API Access Tier.

### 4.4. Успешные вызовы каждого разрешения (не меньше 1 за 30 дней до подачи)

| Разрешение | Чем сделать вызов | Готово |
|---|---|---|
| `business_management` | вход через «Connect Meta» на стенде (рёбра бизнеса) | [ ] |
| `pages_show_list` | тот же вход (`owned_pages`, `me/accounts`) | [ ] |
| `pages_read_engagement` | после правки §1.5 — тот же вход; до неё — Graph API Explorer `GET /{page_id}?fields=name,link,followers_count` | [ ] |
| `ads_read` | тестовая покупка (CAPI) и/или синхронизация М2 | [ ] |
| `ads_management` | кнопка паузы или Graph API Explorer `POST /{campaign_id}` `status=PAUSED` на своей кампании | [ ] |

Учёт в панели появляется до 2 суток; кнопка «Request advanced access» серая, пока вызова нет.

### 4.5. Доступ рецензента

- Meta входит **своими тестовыми аккаунтами Facebook**. Свои личные данные Facebook и пароль владельца не давать: Meta это прямо запрещает.
- Наш ивент-центр требует своего входа, поэтому создать **отдельную учётку**: организация «Meta Review» (или ABH TEAM на стенде),
  владелец `meta-review@arenasoldout.com` (или другой ящик, который читает команда), пароль сгенерировать, хранить в менеджере паролей,
  в репозиторий и в чат не копировать. После одобрения учётку заблокировать.
- У организации рецензента — тестовое событие на продаже и тестовый режим оплаты, чтобы он мог повторить покупку.
- Где тестирует рецензент: адрес должен совпадать с записью. Callbacks в панели настроены на прод, поэтому **лучше прод** с отдельной тестовой организацией.
  Если прод ещё без М1 — стенд `events-staging.arenasoldout.com`, но тогда учётка и инструкции — для стенда.
- В «Complete App Verification» на вопрос, входят ли пользователи через Meta, ответ: да, через Facebook Login for Business из ивент-центра.

Текст инструкции для рецензента (поле «Testing instructions»; пароль вставить вручную в поле, не в этот файл):

```text
1. Open https://events.arenasoldout.com and sign in with the test Event Center account below. This account is the owner of the test organization "Meta Review".
2. Make sure the interface language is English (use the "Language" switch in the menu).
3. Open Menu → Advertising → Meta and press "Connect Meta". Facebook Login for Business opens. Log in with your Facebook test account, choose a business, a dataset (pixel), an ad account and a Page, and confirm.
4. You return to the Event Center. The screen shows the business ID, the granted permissions and the shared datasets, ad accounts and Pages. Select the dataset for purchases and the ad account.
5. To see the Conversions API: open the public page of the test event "Meta Review Test Event" (link on the event card), buy one ticket with the test card 4242 4242 4242 4242, any future date, any CVC. The Purchase event goes to the selected dataset; it is visible in Events Manager → Test events if you set a test event code.
6. To see ad spend: open the event in the Event Center → Advertising. It shows spend, impressions, clicks and purchases read from Ads Insights of the selected ad account.
7. To disconnect, press "Disconnect" on the Meta screen.
Event Center test account: e-mail and password are given in the fields below.
```

Шаги 5 и 6 проверить и поправить под реальный экран перед вставкой: тестовая карта зависит от провайдера оплаты тестовой организации
(4242… — тестовая карта Stripe), а шаг 6 — только после М2.

## 5. Что Meta обычно отклоняет и как этого избежать

| Причина отказа | Источник | Как избежать |
|---|---|---|
| Приложение недоступно рецензенту — отклоняется вся заявка | [Common Mistakes](https://developers.facebook.com/docs/app-review/submission-guide/common-mistakes) | рабочая тестовая учётка §4.5, проверить вход в окне инкогнито за час до подачи |
| Нет записи для разрешения — оно не одобряется | там же | ролик к каждому разрешению, с временем куска в примечании |
| Приложение ещё в разработке | там же | подавать только выкаченные функции; не показывать экраны-заглушки |
| Разрешения «на потом» | там же; [Permissions](https://developers.facebook.com/docs/permissions) | `ads_management` — только с кнопкой паузы (§1.3) |
| Facebook Login не найден или не работает — отклоняется вся заявка | Common Mistakes | кнопка «Connect Meta» на виду, инструкция §4.5 шаг 3 |
| Фальшивый аккаунт в инструкциях | Common Mistakes | никаких «тестовых» Facebook-аккаунтов от нас; рецензент входит своим |
| Рецензент открыл приложение, но не смог проверить разрешение — оно не одобряется | [App Review](https://developers.facebook.com/docs/app-review) | данные в кабинете есть (расходы, страница, пиксель); тестовое событие на продаже |
| Использование не совпадает с «Allowed Usage» | App Review; Permissions | тексты §1 описывают ровно разрешённое использование |
| Скопированные одинаковые тексты для разных разрешений | [Submission Guide](https://developers.facebook.com/docs/app-review/submission-guide) | тексты §1 разные, каждый про своё разрешение |
| Запись без английского интерфейса и без пояснений | Submission Guide | английский язык ивент-центра, подписи §2.2 |
| Нет успешного вызова за 30 дней — кнопка подачи серая | Submission Guide | §4.4 |
| Расходятся политика конфиденциальности и анкета | Data Handling Questions; опыт сообщества (непроверенный источник, форум разработчиков Meta) | исправить расхождения §3.6 до подачи |
| Ссылка статуса удаления данных не работает | [Data Deletion Callback](https://developers.facebook.com/docs/development/create-an-app/app-dashboard/data-deletion-callback) | расхождение 7 |

После отказа Meta пишет причину по каждому разрешению; одобренные остаются. Переподача — с исправленной записью и текстом,
сроки проверки — от нескольких дней до 2–4 недель (спека §8).

## 6. Утром владельцу

1. Проверить §0: выкачен ли М1, есть ли вызовы в панели (§4.4). Если нет — подача сегодня невозможна, только запись ролика на стенде.
2. Решить: подаём `ads_management` сейчас или позже (§1.3); одна заявка или три (§1.8).
3. Исправить политику по §3.6 (пункты 1, 2, 6, 8) — правка сайта.
4. Создать учётку рецензента (§4.5).
5. Записать ролик по §2, вставить тексты §1 и §3, нажать «Отправить».
