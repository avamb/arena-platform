# Контракт WordPress-интеграции

Обновлено: 2026-06-21

## Решение

Для наших WordPress-сайтов основной путь миграции - новый нативный WordPress-плагин под новую платформу. Текущая интеграция Vino&Co на WordPress/WooCommerce/Bil24 используется как рабочий эталон поведения, edge cases и операционного опыта, но не как целевая архитектура.

Bil24-compatible API остается полезным слоем для партнерской совместимости, rollback и внешних клиентов, которых нельзя быстро перевести. Для двух текущих сайтов, которые мы контролируем, основной путь - чистый новый плагин.

## Цели

1. Оставить WordPress внешним презентационным и интеграционным слоем, а не частью ticketing core.
2. Убрать обязательную зависимость от WooCommerce для покупки билетов.
3. Поддержать быстрые страницы мероприятий, SEO, мультиязычность и удобный редакторский workflow.
4. Оставить в ядре платформы мероприятия, сеансы, места, резервы, заказы, платежи, билеты, возвраты и webhooks.
5. Сделать один backend пригодным для WordPress, embeddable widgets, кастомных сайтов, Telegram Mini Apps, мобильных приложений и будущих партнерских интеграций.
6. Сохранить возможность WooCommerce-режима позже, но не делать WooCommerce базовой зависимостью.

## Не цели

WordPress-плагин не должен становиться:

- источником истины по билетным остаткам
- источником истины по заказам
- платежным шлюзом
- сервисом выпуска билетов
- сервисом сканирования
- заменой платформенного admin/backoffice
- местом, где дублируются бизнес-правила ядра

## Граница ответственности

```text
WordPress site
  - страницы мероприятий
  - blocks / shortcodes / widgets
  - локальный кеш каталога
  - прием webhook-ов
  - опциональное локальное зеркало заказов
        |
        | Platform API + webhooks
        v
New platform backend
  - ticketing core
  - inventory / reservations
  - orders / tickets
  - payment provider layer
  - promo engine
  - event bus / outbox
```

WordPress вызывает платформу. Платформа не зависит от WordPress.

## Авторизация и привязка канала

Каждый WordPress-сайт регистрируется в платформе как отдельный sales channel.

Обязательные данные канала:

- `organization_id`
- `sales_channel_id`
- публичный URL сайта
- разрешенный callback/webhook URL
- окружение: `production`, `staging`, `sandbox`
- API credential с ограниченными правами
- webhook secret для HMAC-подписи
- язык по умолчанию и включенные языки

Плагин не должен хранить глобальный admin token платформы. Нужен rotatable channel credential только с теми правами, которые нужны конкретному сайту.

Рекомендуемые scopes:

- `catalog:read`
- `availability:read`
- `reservation:write`
- `checkout:write`
- `order:read`
- `webhook:receive`

## Владение данными

### Платформа владеет

- организациями и sales channels
- площадками
- мероприятиями
- сеансами
- схемами залов
- билетными категориями и тарифами
- inventory и доступностью
- резервами и сроком их жизни
- промокодами, скидками и перерасчетом цены
- заказами
- payment intents, captures, refunds и платежным статусом
- выпущенными билетами и barcode-ами
- статусом доставки билетов
- журналом webhook delivery
- audit log

### WordPress владеет

- публичной версткой страниц
- редакторским контентом вокруг мероприятия
- SEO metadata
- локальным кешем каталога
- mapping между WordPress post и platform IDs
- настройками отображения
- локальным журналом обработки webhook-ов
- опциональным shadow order record для удобства админа сайта

### WordPress не владеет

- финальными остатками билетов
- состоянием резерва после ответа API
- данными банковских карт
- private keys платежных провайдеров
- barcode-ами как источником истины
- решениями по refund
- scan state

## Локальная модель WordPress

Рекомендуемая модель плагина:

- Custom Post Type `arena_event` для SEO-видимых страниц мероприятий.
- Опциональный Custom Post Type `arena_venue`, если нужны страницы площадок.
- Custom tables или структурированные post meta для кеша сеансов, цен, категорий и availability snapshots.
- Settings page для channel credentials, языков, page mappings, cache policy и feature flags.
- Опциональная локальная таблица order mirror для поддержки и админских просмотров.

По умолчанию плагин не должен создавать WooCommerce products. WooCommerce mode можно добавить отдельным adapter module позже.

## ID Mapping

Все platform IDs в WordPress лучше хранить строками, даже если они выглядят как числа.

Рекомендуемые соответствия:

```text
platform.event.id         -> arena_event post meta: platform_event_id
platform.session.id       -> cached session row/meta: platform_session_id
platform.venue.id         -> venue cache/meta: platform_venue_id
platform.ticket_type.id   -> cached ticket type/category id
platform.reservation.id   -> checkout session state only
platform.order.id         -> local order mirror/reference
platform.ticket.id        -> local reference only, never source of truth
```

При миграции можно хранить legacy Bil24 IDs:

```text
legacy.actionId
legacy.actionEventId
legacy.venueId
legacy.categoryPriceId
legacy.orderId
```

Это совместимые references, а не первичные platform IDs.

## Контракт синхронизации каталога

Плагин должен поддерживать полный sync и webhook-triggered incremental sync.

### Full Sync

WordPress вызывает catalog endpoints платформы:

```text
GET /v1/catalog/events
GET /v1/catalog/events/{event_id}
GET /v1/catalog/sessions?event_id={event_id}
GET /v1/catalog/venues/{venue_id}
```

Ожидаемое поведение:

- создавать или обновлять `arena_event` posts
- обновлять title, slug candidate, poster URLs, description, age limit, organizer display data
- обновлять кеш сеансов: date/time, venue, city, currency, ticket categories, tariffs, min/max price
- сохранять локальные редакторские поля, если sync policy явно не говорит перезаписывать
- помечать исчезнувшие сеансы inactive, а не удалять сразу
- делать sync идемпотентным по platform ID и `updated_at`/version

### Incremental Sync

Платформенные webhooks запускают точечное обновление:

```text
catalog.event.created
catalog.event.updated
catalog.event.deleted
catalog.session.created
catalog.session.updated
catalog.session.deleted
catalog.price.updated
inventory.changed
```

WordPress проверяет подпись webhook-а, сохраняет event ID для идемпотентности и затем подтягивает затронутый объект из Platform API. Payload webhook-а лучше считать уведомлением, а не полным источником истины.

## Контракт отображения

Плагин должен предоставить:

- event list block/shortcode
- event detail block/template integration
- calendar/list filters
- session selector
- ticket category/tariff selector
- quantity selector
- checkout button или embedded checkout container
- order status / thank-you block

Рекомендуемая политика render:

- server-render страниц мероприятий для SEO
- JavaScript hydration для availability и checkout controls
- cached price range на первом render
- live availability перед резервированием
- запрет покупки по устаревшему cached availability
- RU/HE/EN с самого начала, потому что текущим сайтам уже нужна мультиязычность

## Контракт checkout

Рекомендуемый default: platform-hosted checkout или platform-owned embedded checkout. WordPress начинает checkout, но платформа владеет резервом, заказом, оплатой и выпуском билетов.

### Основной flow

```text
1. Клиент открывает страницу мероприятия в WordPress.
2. Плагин показывает cached event/session data.
3. Widget запрашивает live availability из платформы.
4. Клиент выбирает session, ticket category, tariff, quantity.
5. Плагин/widget создает reservation или checkout session через Platform API.
6. Promo code применяется через Platform API после того, как выбран inventory.
7. Платформа считает sum, discount, charge, total.
8. Клиент оплачивает через платформенный payment flow.
9. Платформа выпускает билеты после успешной оплаты.
10. Платформа отправляет order/ticket webhook в WordPress.
11. WordPress обновляет local order mirror и thank-you/status page.
```

Важное правило из текущей Bil24-интеграции: payment total должен быть авторитетным значением из ticketing platform, а не пересчитываться WordPress-ом. Платформа должна отдавать отдельные финансовые поля:

```text
sum
discount
charge
total
currency
```

Старый Bil24-урок: `totalSum = sum - discount + charge`. В новом API можно назвать поле понятнее (`total`), но семантика должна сохраниться.

### Promo Flow

Новая платформа не должна наследовать хрупкое поведение Bil24 с промокодами, но плагин должен соблюдать безопасную последовательность:

```text
select inventory -> create/update reservation -> apply promo -> recalculate totals -> create/confirm checkout
```

Promo application должен быть идемпотентным. Double-clicks и retries не должны удалять или дублировать скидку.

### Idempotency

Каждый mutating checkout request от WordPress или browser widget должен иметь idempotency key:

- reservation create/update
- promo apply/remove
- checkout session create
- order create/confirm

Idempotency keys должны быть scoped к `sales_channel_id`, browser/session identity и operation type.

## Контракт платежей

Payment providers находятся в платформенном payment layer, а не в WordPress-плагине.

Плагин может:

- открыть platform checkout URL
- встроить platform checkout component
- показать payment status
- принять payment/order webhooks
- отрисовать thank-you page

Плагин не должен:

- напрямую вызывать AllPay/Stripe/YooKassa/PayPal как основной flow
- хранить private keys платежных провайдеров
- решать, что заказ оплачен, без подтверждения платформы
- выпускать билеты из локального WordPress state

## Контракт webhook-ов

Platform-to-WordPress webhooks должны использовать стабильный envelope:

```json
{
  "id": "evt_...",
  "type": "order.paid",
  "created_at": "2026-06-21T12:00:00Z",
  "organization_id": "org_...",
  "sales_channel_id": "ch_...",
  "data": {}
}
```

Обязательная безопасность:

- HMAC signature header
- timestamp header
- replay window validation
- event ID idempotency
- per-channel webhook secret

Обязательные event families:

```text
catalog.*
inventory.changed
reservation.expired
order.created
order.paid
order.cancelled
order.refunded
ticket.issued
ticket.refunded
payment.succeeded
payment.failed
```

WordPress должен возвращать 2xx только после того, как событие сохранено или обработано идемпотентно. Ошибки обработки должны быть retryable со стороны платформы.

## Контракт админки WordPress-плагина

Админка плагина должна включать:

- environment selector
- API base URL
- sales channel ID
- API credential management
- webhook endpoint display
- webhook secret management
- connection test
- full catalog sync action
- sync status и last error
- cache purge action
- page mapping: event list, event detail, checkout/status, thank-you
- locale mapping
- feature flags: hosted checkout, embedded checkout, WooCommerce mode

Secrets должны быть masked в админке и никогда не попадать во frontend HTML.

## Опциональный WooCommerce Mode

WooCommerce можно поддержать позже как compatibility module, но не как default architecture.

Возможные обязанности WooCommerce mode:

- mirror platform events as WooCommerce products, если клиенту это нужно
- create local WooCommerce order mirrors after platform checkout
- sync payment/order status back into WooCommerce admin

Даже в WooCommerce mode платформа остается source of truth для inventory, order totals, payments и tickets.

## Миграция текущей Vino&Co-интеграции

Текущий Vino&Co проект используется как reference behavior:

- catalog sync behavior
- отображение сеансов и ticket categories
- checkout UX
- promo edge cases
- payment total handling
- операционные уроки AllPay
- webhooks для order и catalog updates
- reporting expectations

Рекомендуемый migration path:

1. Зарегистрировать каждый текущий сайт как platform sales channel.
2. Построить mapping old Bil24 IDs -> new platform IDs.
3. Создать или синхронизировать `arena_event` posts из новой платформы.
4. Сохранить или перенести editorial content и SEO metadata, где нужно.
5. Добавить 301 redirects со старых event/product URLs на новые event pages.
6. Оставить исторические WooCommerce orders read-only для старых отчетов.
7. Прогнать test orders через новый plugin на staging.
8. Переключить production checkout сайта на platform checkout.
9. Держать rollback на уровне plugin configuration в течение cutover window.

## Критерии готовности для первой production-миграции

WordPress-интеграция готова к миграции первого сайта, когда:

- full catalog sync идемпотентен
- страницы мероприятий рендерятся без live API dependency
- live availability запрашивается перед reservation
- checkout не создает duplicate orders при double-click/retry
- promo codes дают корректные totals
- payment status приходит из платформы
- ticket issuance происходит только в платформе
- order/ticket webhooks обновляют WordPress идемпотентно
- catalog webhooks обновляют затронутые events/sessions
- cache не ломает checkout, nonce или session behavior
- RU/HE/EN content paths проверены
- старые URLs редиректятся на новые event pages

## Открытые вопросы

1. Первый вариант checkout: hosted checkout, embedded checkout или оба сразу?
2. Нужны ли двум нашим сайтам WooCommerce order mirrors после миграции, или хватит отчетности платформы?
3. Страницы мероприятий делаем через WordPress custom posts или некоторым сайтам достаточно embedded widget?
4. Какие поля из текущих Vino&Co event pages нужно сохранить как editorial content?
5. Какой второй подключенный сайт и совпадают ли у него checkout/reporting requirements с Vino&Co?
6. Какой payment provider включаем первым в платформенном payment layer?
