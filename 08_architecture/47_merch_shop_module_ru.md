# 47. Магазин организатора (мерч): спецификация первой версии

Дата: 2026-10-28. Статус: черновик оркестратора для недельного прогона
(срок владельца 5 ноября). План работ:
`docs/plans/einatwinery_merch_shop_execution_plan_2026-10-28_ru.md`.
Решения владельца: arena только SaaS, деньги берёт организатор через свой
аккаунт провайдера; модуль платформенный, первый клиент Einat Winery (Vino&Co);
дизайн витрины 1:1 с нынешним Shopify (Dawn 11, русский).

## 1. Границы первой версии

Входит: каталог товаров организации, витрина на домене организатора,
корзина, оформление с адресом и плоской доставкой, оплата через hosted-страницу
провайдера организатора (PayPlus для Vino&Co; Stripe и Flitt через ту же
абстракцию), письмо «заказ принят», Telegram-сообщение логисту, список заказов
и статус «отправлен» в веб-ивент-центре, полный и частичный возврат, импорт из
Shopify. Не входит (вторая волна): самовывоз на мероприятии с QR, промокоды на
товары, бот, подписки, Invoice+/Green Invoice, иврит и английский, комиссия
платформы (0 до решения), привязка товара к событию.

## 1a. Многоарендность (требование владельца 28.10)

Модуль платформенный: Einat Winery первая организация, у которой он включён,
но ни одна таблица, маршрут, экран или шаблон не знает о ней. Проверки при
ревью: каждая сущность магазина с `org_id`; провайдер оплаты любой из
`payment_provider_configs` организации; покупатели, промокоды, уведомления и
права общие с билетами; витрина одна на всех по `{org_slug}` плюс свой домен
через `organizations.shop_domain`; включение магазина флагом организации
(по умолчанию выключен). Дизайн Dawn, импорт из Shopify, группа логиста и
правило 18+ это данные и настройки Einat Winery, не код. Заложено на потом:
привязка товара к мероприятию и самовывоз на событии, товар в корзине
билетов, ставка биллинга за заказ магазина, тема витрины платформы.

## 2. Данные (миграция от головы master, сейчас 0171 → 0172)

Все деньги в минорных единицах, `currency char(3)`. Все таблицы с `org_id`.

- `shop_settings` (org_id PK, currency, shipping_fee_minor, shipping_label,
  country_iso2, age_gate_min_age int null, announcement_text, contact_email,
  logistics_note, updated_at). Одна строка на организацию, создаётся при первом
  товаре.
- `shop_products` (id uuid, org_id, slug unique per org, title, body_html text,
  status draft|active|archived, product_type, vendor, sort_order, created_at,
  updated_at, deleted_at). `shop_product_images` (product_id, media_id →
  media_objects, position, alt). `shop_variants` (id, product_id, title, sku,
  price_minor, compare_at_minor null, stock int, track_stock bool, position,
  option1..3 text). Один товар всегда имеет хотя бы один вариант (у Dawn и
  Shopify так же; у 6 из 7 товаров Einat один вариант).
- `shop_carts` не храним: корзина живёт в браузере (localStorage), сервер
  получает её при оформлении.
- `shop_orders` (id, org_id, number bigint из `compatibility_system_id_seq`,
  customer_id → customers null, status pending_payment|paid|shipped|cancelled|
  expired|refunded|partially_refunded, buyer_name, buyer_email, buyer_phone,
  ship_name, ship_address1, ship_address2, ship_city, ship_postal, ship_country,
  delivery_kind shipping|pickup (pickup не используется в v1), delivery_note,
  currency, subtotal, discount 0, shipping_fee, total, payment_intent_id →
  payment_intents null, provider, buyer_locale, source shopify|web, external_ref
  (номер заказа Shopify при импорте), paid_at, shipped_at, tracking_note,
  expires_at, metadata jsonb, created_at, updated_at). CHECK total = subtotal -
  discount + shipping_fee.
- `shop_order_items` (id, order_id, variant_id, product_title, variant_title,
  sku, qty, unit_price_minor, total_minor, ordinal).
- `shop_stock_moves` (id, variant_id, order_id null, delta int, reason
  reserve|release|sale|refund|manual|import, created_at). Остаток варианта =
  `shop_variants.stock` минус активные резервы; резерв пишется при создании
  заказа и снимается sweep-ом по `expires_at` (воркер `shop.order.expire_sweep`,
  30 с) или оплатой (резерв превращается в sale).
- `organizations.shop_domain` text unique null: домен витрины
  (`einatwinery.com`), по нему `shop-page` находит организацию.
- События outbox: `v1.shop.order.paid`, `v1.shop.order.shipped`,
  `v1.shop.order.refunded` (aggregate_id = order id).
- Права: `shop.read`, `shop.manage` (миграция грантит org_admin, organizer,
  platform_superadmin в той же миграции).

## 3. Оплата

Используется существующий `hfeed.PaymentStarter` (`CheckPaymentConfigured`,
`StartHostedCheckout`) и `payment_intents` с `checkout_session_id = NULL`.
`StartHostedCheckout` получает сумму, валюту, описание, `return_url`,
`cancel_url`, наш идентификатор (shop order id) и e-mail; результат
`hosted_checkout_url` + `provider_payment_id`. Провайдер берётся из
`payment_provider_configs` организации (`ResolveProviderConfig`), как у
билетов. Вебхук: `POST /v1/public/payment-webhook/{config_id}` (существующий
маршрут по config_id), после `NormalizedEvent` диспетчер находит либо
checkout_session (билеты), либо `shop_orders.payment_intent_id` (товары) и
вызывает `shop.FulfillPaidOrderTx`: статус paid, резерв → sale, outbox
`shop.order.paid`, письмо, Telegram. Возврат: `POST
/v1/organizations/{org}/shop/orders/{id}/refund` {amount} → `refunds` на
`payment_intents` → `payments.RefundPayment` провайдера → статус заказа,
`shop_stock_moves.refund` не возвращает товар на склад автоматически (вино
вскрыто/отправлено), кнопка «вернуть на склад» отдельно.

**PayPlus** (новый провайдер, по образцу `adapters/flitt`): `module.go`
регистрирует дескриптор `payplus` (подпись: заголовок `hash` = base64
HMAC-SHA256(secret_key, сырое тело), `user-agent: PayPlus`; `provider_payment_id`
= `page_request_uid`, ссылка на заказ в `more_info`; событие успешно при
`transaction.status_code == "000"`, `provider_charge_ref` = `transaction.uid`);
`adapter.go`: `generateLink` (`refURL_success/cancel/callback`,
`send_failure_callback=true`, `expiry_datetime` = окно оплаты в минутах,
`customer{customer_name,email}`, `items[]`, `language_code`), `Transactions/View`,
`RefundByTransactionUID`, `VerifyCredentials` (вызов списка устройств с
ключами). Секреты конфига: `api_key`, `secret_key`, `payment_page_uid`.
`PAYPLUS_API_BASE_URL` только вне production; заглушка
`apps/widget/scripts/payplus-stub.cjs` для тестов и стенда. Доказательство
независимо от магазина: интеграционный тест покупает и возвращает БИЛЕТ через
PayPlus-заглушку.

## 4. API

Организация (`/v1/organizations/{org_id}/shop/...`, JWT или ключ API с
`shop.read`/`shop.manage`): `GET/PUT settings`; `GET/POST products`,
`GET/PATCH/DELETE products/{id}`, `POST products/{id}/images` (media id),
`PUT products/{id}/variants` (полная замена списка); `GET orders` (фильтры
status, q по номеру/e-mail/имени, период), `GET orders/{id}`, `POST
orders/{id}/ship` {tracking_note}, `POST orders/{id}/cancel`, `POST
orders/{id}/refund` {amount_minor, reason}, `GET orders/{id}/courier-text`
(реквизиты построчно в порядке полей курьера).

Публичное (`/v1/public/shop/...`, без логина, rate limit по IP как у feed):
`GET {org_slug}` (settings + products active с вариантами и картинками,
`Cache-Control: public, max-age=30`, `Vary: Origin`), `GET
{org_slug}/products/{slug}`, `GET pages/{org_slug}` (статические страницы:
о нас, партнёры, контакты, политики как `shop_pages` в settings jsonb или
отдельная таблица `shop_pages(org_id, slug, title, body_html)` — отдельная
таблица), `POST {org_slug}/checkout` {items[{variant_id, qty}], buyer, ship,
locale, return_url} → 201 {order_token, payment_url, total} (резерв, заказ,
hosted-страница; 409 `shop.out_of_stock` с перечнем), `GET
checkout/{order_token}` статус (pending|paid|expired|cancelled, payment_url
для возврата на оплату), `GET|POST /v1/public/payment-return` существующий.
По домену: витрина резолвит организацию через `GET /v1/public/shop/by-host?host=`
→ org_slug (или сразу выдача каталога).

## 5. Витрина `apps/shop-page`

Vite + TS (как tickets-page), без фреймворка или с Svelte как виджет. Рендер
один в один с Dawn по снятым `theme_live/` (CSS Dawn подключается как есть,
HTML-структура секций повторяется, переменные `:root` из `index.html`).
Страницы: `/` (announcement bar, шапка с меню и иконками, hero-баннер,
«Featured collection» из 7 товаров, подвал), `/collections/all`,
`/products/{slug}` (галерея, цена, количество, «Добавить в корзину», описание),
`/cart` + cart drawer, `/checkout` (своя страница: контакты, адрес, доставка
50 ₪, согласие 18+, кнопка «Оплатить» → payment_url), `/checkout/{token}`
статус, `/pages/{slug}`, `/policies/{slug}`, 404. Заглушка 18+ при первом
визите (localStorage). Меню и тексты из `theme_live`. Сохранить адреса
Shopify, чтобы 301-таблица была тривиальной. Мобильная версия в приоритете.
Роутинг по домену: приложение читает `location.host`, спрашивает
`by-host`, кеширует org_slug.

## 6. Ивент-центр (web)

Раздел «Магазин»: Товары (список, создать/изменить: название, описание,
картинки, варианты с ценой и остатком, статус), Заказы (список со статусами и
суммами, карточка: покупатель, адрес, состав, оплата, кнопки «Отправлен»,
«Возврат» с суммой, «Скопировать для курьера»), Настройки (валюта, доставка,
домен только чтение). Права `shop.*`.

## 7. Уведомления и письма

`sales_notification_subscriptions.kinds text[]` (миграция той же волны):
`tickets`, `shop`; существующие строки получают `{tickets}`; строка группы
логиста Vino&Co `{shop}`. Сообщение `shop.order.paid`: номер, сумма, имя,
телефон, адрес построчно, состав, комментарий (порядок полей по списку
Анны, до его получения: имя, телефон, город, адрес, состав, сумма,
комментарий). Письмо покупателю `shop_order_received.{ru,en,he}.tmpl` в
дизайне заказного письма: состав, сумма, адрес, контакт организатора;
`shop_order_shipped` при «Отправлен».

## 8. Импорт из Shopify

`cmd/arena-shop-import --org <id> --products products.csv --customers
customers.csv [--orders orders.csv] --images-dir ...`: товары со статусом
active (черновики пропускаются, флаг `--include-drafts`), варианты, цены,
остатки, картинки side-load по URL (или из локальной папки), `body_html`;
покупатели в `customers` через `customers.Resolve` с согласием на рассылку из
`Accepts Email Marketing`; заказы (вторая волна) как `shop_orders` source
`shopify`. Отчёт: счётчики до/после, пропущенные строки с причиной.

## 9. Приёмка

1. Витрина на стенде пиксельно совпадает с einatwinery.com на десктопе и
   телефоне (скриншоты рядом).
2. Покупка 2 бутылок через PayPlus `restapidev`: заказ paid, остаток уменьшен,
   письмо получено, сообщение в тестовой группе, заказ виден в ивент-центре.
3. Частичный возврат 50 ₪ из карточки: refunds succeeded, статус
   partially_refunded, сумма в PayPlus.
4. Истечение неоплаченного заказа: резерв снят, статус expired.
5. Импорт: 7 товаров, 932 покупателя, сверка счётчиков.
6. Домен: einatwinery.com → витрина, `/products/<handle>` старого адреса
   открывает карточку.

Пошаговый выпуск магазина организатора на своём домене (DNS, Dokploy, CORS,
включение суперадмином, платёжный канал, Telegram логиста, 301, пауза
Shopify): `docs/ops/shop_release_checklist_ru.md`.

## 10. Бэклог ивент-центра после запуска (решения владельца 30.10)

Версия 5.11 даёт организатору товары, заказы и настройки (§6). Следующий
этап делает раздел «Магазин» полноценным рабочим местом по образцу того,
что уже сделано для WordPress-сайтов (плагин заказов и рассыльщика
билетов): менеджер видит не только заказ, но и путь товара и каждого письма.

- **SHOP-B1. Редактор страниц.** Экран «Страницы» в разделе «Магазин»:
  список (`shop_pages`: заголовок, слаг, порядок в меню, опубликована /
  черновик), форма с редактором форматированного текста по профилю страниц
  (`shop.SanitizePageBody`: картинки с загрузкой в медиа владельца
  `shop_page`, таблицы, ссылки), предпросмотр на витрине, перенос картинок
  со стороннего хоста (CDN Shopify) в медиа платформы одной кнопкой. API
  `PutPage` уже есть; нужен новый тип владельца медиа и список страниц в
  меню витрины, управляемый порядком.
- **SHOP-B2. Статусы доставки товара.** В карточке заказа и в списке —
  шаги доставки, как статусы заказа в WordPress: оплачен → собран →
  передан курьеру → доставлен (и «не доставлен / возвращён курьером»),
  с датой, курьером и трек-номером на каждом шаге и историей изменений
  (`shop_order_events`). Фильтр списка по шагу, бейдж «ждёт отправки
  больше N дней». Письмо покупателю на «передан курьеру» (уже есть
  `shop_order_shipped`) и, по настройке, на «доставлен». Уведомление
  логисту в Telegram о новом оплаченном заказе уже есть (§7).
- **SHOP-B3. Статусы доставки писем.** Журнал писем по заказу в карточке:
  какое письмо (получен / отправлен / изменение / отмена), когда поставлено
  в очередь, отправлено, доставлено или отклонено (по вебхуку Brevo:
  delivered / bounce / spam), текст ошибки и кнопка «Отправить повторно»,
  как в WordPress-рассыльщике билетов. Техническая основа общая с билетами
  (`worker_jobs` `shop.order_email`, события доставки Brevo), поэтому тот же
  журнал стоит показать и в карточке билетного заказа.
- **SHOP-B4. Порядок товаров в каталоге.** Ручная сортировка «Все товары»
  (перетаскивание в списке товаров, `sort_order`), чтобы повторить порядок
  витрины Shopify (первым — подарочный набор).

Порядок: B2 и B3 вместе (одна карточка заказа), затем B1, B4 — по ходу.

Пункты из независимого аудита 30.10 (`docs/reviews/shop_module_audit_2026-10-30_ru.md`),
которые не чинятся до выпуска:

- **SHOP-B5. Полная локализация витрины (SA-20).** Язык витрины зашит: `lang="ru"`,
  `locale: 'ru'` в оформлении, тексты в `lib/i18n.ts` и литералах `views/*.ts`.
  Нужны словарь иврита (с `dir="rtl"`) и английского, выбор языка по настройке
  магазина и языку браузера, `locale` покупателя в заказ, чтобы страница AllPay и
  письма шли на языке покупателя. До этого на плашке 18+ стоит только `lang="he" dir="rtl"`.
- **SHOP-B6. Тема организации в настройках магазина (SA-21).** Имя, логотип, соцсети,
  тексты подвала и ссылки сейчас в `src/lib/theme.ts` и `fixContentLinks` (`einatwinery.com`).
  Второй организатор получил бы бренд Einat. Перенести тему в `shop_settings`
  (JSON-поле `theme` с проверкой и загрузкой логотипа в медиа) и в экран «Настройки»
  ивент-центра; `theme.ts` оставить как значения по умолчанию.
- **SHOP-B7. Живые парные тесты экранов магазина (SA-24).** Строки «Магазин» в
  `apps/event-center/src/parity/matrix.ts` без `ready`, в `tests/smoke/live` нет файлов
  магазина, интеграционного теста «магазин + AllPay» нет (в тестах Stripe-заглушка и PayPlus).
  Нужны живые прогоны возврата, отмены, `expected_stock`, step-up и ошибок 409/429 и
  интеграционный тест денежного цикла на заглушке AllPay.
- **SHOP-B8. Перенос Shopify: SEO и история заказов (SA-29).** Импорт теряет SEO Title и
  SEO Description, теги, коллекции, вес и штрихкоды; заказы 932 покупателей не пишутся
  (`orders.go` только считает). Добавить колонки SEO в товар и показывать их на витрине
  (`<title>` и `description` карточки), решить с владельцем, нужны ли старые заказы
  (`shop_orders` source `shopify`).
- **SHOP-B9. Sitemap из API.** Сейчас `public/sitemap.xml` витрины статичный и называет
  только главные маршруты. Нужен динамический `GET /v1/public/shop/{org}/sitemap`
  (товары, страницы, политики, `lastmod`) и nginx, который отдаёт его как
  `/sitemap.xml` своего домена; статичный файл тогда убрать.
  Туда же: `<link rel="canonical">` и `og:title`/`og:description`/`og:image` для каждой
  страницы из SPA (`setMeta` в `main.ts`) и для превью без JavaScript (WhatsApp, Telegram
  читают только статичный HTML) отдача мета-тегов товара на стороне nginx или API.
  Статичного canonical в `index.html` нет специально: одно значение на все адреса
  указало бы все страницы на главную.
- **SHOP-B10. Лимиты в памяти процесса при двух репликах (SA-37).** Лимит «письмо ещё раз
  раз в минуту» и лимит оформлений живут в памяти `arena-api`: с двумя репликами лимит
  удваивается. Перенести счётчики в Postgres или Redis, как лимиты онбординга.
