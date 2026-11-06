# Переключение einatwinery.com с Shopify на витрину arena

Дата: 2026-11-06. Время в плане указано по Мадриду.

Решение владельца от 06.11: витрина запускается сразу на основном домене, без временного поддомена.
Откатом служит возврат DNS на Shopify. Магазин принадлежит организации **Vino&Co** (одно юрлицо с Einat Winery).
Покупатель видит «Einat Winery» и получает письма с `shop@einatwinery.com`.

Общий порядок выпуска магазина описан в `docs/ops/shop_release_checklist_ru.md`. Этот план описывает только einatwinery.com.

## 1. Итоговая картина

| Что | Значение |
|---|---|
| Основной адрес | `https://www.einatwinery.com` (так же, как сейчас у Shopify; Google знает страницы по этому адресу) |
| Корень `einatwinery.com` | 301 на `https://www.einatwinery.com/<тот же путь>` (правило Cloudflare) |
| Витрина | приложение `shop-prod` в Dokploy на сервере прода (178.105.217.195), `apps/shop-page`, порт 8080 |
| API | `https://api.arenasoldout.com`, организация Vino&Co, `shop_domain` = `einatwinery.com` (вариант с `www.` сервер понимает сам) |
| Оплата | AllPay, боевой аккаунт Vino&Co, отдельный канал продаж «Einat Winery shop» |
| Письма | «Einat Winery <shop@einatwinery.com>» через Brevo платформы (аккаунт ABH TEAM OU, домен подтверждён) |
| Ответы покупателей | `contact_email` магазина (ящик Google на einatwinery.com) |
| Почта домена | MX Google, DKIM Google и Brevo: не трогаем |
| Старые ссылки Shopify | 301 на новые страницы делает nginx витрины (`apps/shop-page/docs/redirects_301.md`) |

## 2. Что уже готово (06.11)

- Прод на `41c84a5`. В нём:
  - модуль «Магазин»;
  - магазин в боте;
  - шаги доставки;
  - своё название и отправитель магазина (миграция 0200);
  - повторное подключение бота продаж.
- Brevo:
  - einatwinery.com подтверждён в аккаунте платформы;
  - отправитель `shop@einatwinery.com` активен;
  - `BREVO_API_KEY` стоит на проде;
  - в Authorized IPs есть адреса прода, стенда и владельца.
- DNS einatwinery.com: записи Brevo стоят (brevo-code, `brevo1`/`brevo2._domainkey`, `_dmarc`). Токен Cloudflare `CLOUDFLARE_EINAT_TOKEN` даёт права на DNS, настройки зоны, редиректы, кэш и Turnstile.
- Витрина: есть фавикон из логотипа. Адреса в `og:image`, `robots.txt` и `sitemap.xml` указывают на `www.einatwinery.com` (PR этого плана).

## 3. Что нужно от владельца или Анны

1. **Боевые ключи AllPay** аккаунта Vino&Co (API login и API key из Settings → API Integrations). Их вводят в ивент-центре в «Платёжных настройках», в чат ключи не присылают.
   - Подтвердить, что к боевому AllPay подключён боевой Morning: чеки выпускает он.
   - Код НДС `vat=1`.
2. **Контактный адрес магазина** для ответов покупателей, например `info@einatwinery.com` или `sales@einatwinery.com`.
3. **Группа доставки в Telegram**: кто в ней, кто добавит в неё бота продаж.
4. **Цена модуля «Магазин»** для Vino&Co: бесплатно или своя.
5. **Окно переключения**: около 30 минут в тихое время.
   - Кто ставит Shopify на паузу.
   - Кто до переключения закрывает в Shopify неотправленные заказы `#1869`–`#1874`.
6. **Аналитика**: что из Shopify нужно сохранить (пиксель Meta, Google Analytics, Search Console). В витрине arena их пока нет.

## 4. Подготовка (текущий сайт не затрагивается)

**П-1. Код.** Влить PR с адресами витрины.

**П-2. Приложение `shop-prod` в Dokploy** (проект arena → production, сервер прода):
- сборка из `apps/shop-page/Dockerfile`, контекст сборки — корень репозитория, ветка `master`;
- build-аргумент `VITE_API_BASE_URL=https://api.arenasoldout.com`;
- порт контейнера 8080;
- домены `www.einatwinery.com` и `einatwinery.com`, HTTPS через Let's Encrypt.

Пока DNS указывает на Shopify, сертификат не выпустится. Это нормально, см. шаг Т-5.

**П-3. Turnstile.** Создать виджет на `einatwinery.com` и `www.einatwinery.com`. Ключи положить в окружение `backend-prod` в панели:
```
SHOP_TURNSTILE_SITE_KEY=...
SHOP_TURNSTILE_SECRET=...
```
В блок `x-api-env` compose добавить ссылки `${...}`.

**П-4. API.**
- В `CORS_ALLOWED_ORIGINS` добавить `https://www.einatwinery.com,https://einatwinery.com`.
- Лимит памяти api поднять с 512 до 768 МБ, как на стенде после падения по памяти на больших фото.
- Deploy на том же образе.

**П-5. Vino&Co** (суперадмин):
- Админка → Modules → подключить «Магазин» (цена из пункта 3.4).
- Канал продаж «Einat Winery shop» с `provider=allpay`. Канал сайта `Vino&Co WP` не использовать.
- Конфигурация AllPay `live`, проверка `ok`, `vat=1` (пункт 3.1).

**П-6. Данные.** Прямо перед переключением, не раньше: решение владельца, потому что продажи в Shopify идут.
- Финальная выгрузка из Shopify: товары, покупатели, страницы.
- Исключить 6 подписчиков-ботов от 30.10 (Jessica Morris, Samuel Sanchez, Grayson Cooper, Noah Bailey, Liam Davis, Jennifer Flores).
- `arena-shop-import --include-drafts` в организацию Vino&Co на проде.
- Картинки страниц перенести с CDN Shopify.
- Сверить количества: товары 42 (7 активных), покупатели около 934.

**П-7. Настройки магазина** (`PUT .../shop/settings` от суперадмина, с `X-Admin-Reason`):
- `currency` ILS, доставка 50 ₪ и её подпись, `country_iso2` IL, `age_gate_min_age` 18;
- `contact_email` (пункт 3.2), `sales_channel_id` (П-5);
- `shop_domain` = `einatwinery.com`;
- `shop_name` = `Einat Winery`, `sender_email` = `shop@einatwinery.com`;
- `enabled` = false.

**П-8. Telegram.** Админка → Telegram chats → Add chat (Delivery) → код → бот продаж в группу → `/connect <код>`. Чат продаж Vino&Co уже получает `shop_sale`.

**П-9. Проверка в обход DNS** (сайт по-прежнему на Shopify):
```bash
curl -sk --resolve www.einatwinery.com:443:178.105.217.195 https://www.einatwinery.com/health
curl -s "https://api.arenasoldout.com/v1/public/shop/by-host?host=www.einatwinery.com"   # 404 до открытия
```

## 5. Переключение (окно около 30 минут)

**Т-0. Записать текущие записи для отката** (06.11 они такие, все без облака):

| Тип | Имя | Значение |
|---|---|---|
| A | `einatwinery.com` | `23.227.38.65` |
| CNAME | `www` | `shops.myshopify.com` |

**Т-1.** Открыть магазин: `PUT .../shop/storefront {"enabled":true}`. Нужны активный товар и проверенный AllPay. Проверка: `by-host` отвечает 200.

**Т-2. DNS через токен Cloudflare:**
- `www` → A `178.105.217.195`, облако оранжевое;
- корень → A `178.105.217.195`, облако оранжевое.

Режим SSL остаётся `full` (не strict), пока Let's Encrypt не выдаст сертификат.

**Т-3. Правило Single Redirect:** `einatwinery.com/*` → `https://www.einatwinery.com/${1}`, 301, с сохранением query.

**Т-4. Ждём DNS.** Cloudflare с облаком переключается почти сразу.

**Т-5. Сертификат.** `docker restart` контейнера `shop-prod`: Traefik сам не повторяет заказ сертификата. В логе Traefik должен появиться сертификат на оба имени.

Затем в зоне:
- SSL → Full (strict);
- Always Use HTTPS включить;
- минимальная версия TLS 1.2.

**Т-6. Проверка:**
- главная, каталог, карточка, корзина, оформление до страницы AllPay;
- фавикон, `robots.txt`, `sitemap.xml`, 404 на несуществующую страницу;
- 10–15 старых адресов Shopify (`/collections/...`, `/products/...`, `/pages/...`) отвечают 301 или 200, не 404;
- письма Google на einatwinery.com по-прежнему приходят: MX не трогали.

**Т-7. Боевая покупка** самого дешёвого товара на настоящую карту:
- заказ оплачен;
- письмо пришло от «Einat Winery <shop@einatwinery.com>»;
- чек Morning с НДС 18 %;
- в Telegram: факт продажи в чате продаж и карточка в группе доставки с кнопками.

Затем полный возврат из ивент-центра: письмо о возврате пришло, деньги вернулись в AllPay.

**Т-8. Shopify на паузу** («Pause and build», не удаление). Делается после успешной покупки и проверенных 301. Подписку не отключать, пока владелец не подтвердит, что старые заказы не нужны.

## 6. Откат (1–5 минут)

1. DNS вернуть к значениям из Т-0 (без облака). Удалить правило редиректа из Т-3.
2. Закрыть магазин: `PUT .../shop/storefront {"enabled":false}`. Заказы и остатки не теряются.
3. Снять Shopify с паузы, если её уже включили.

Режим SSL `full` для записей без облака ни на что не влияет.

## 7. После переключения

- Первые сутки следить:
  - статусы писем (`email_letters`);
  - сторож ops;
  - 4xx и 5xx в аналитике Cloudflare;
  - заказы в ивент-центре.
- Google Search Console: отправить заново `https://www.einatwinery.com/sitemap.xml`.
- `einat.wine` и `eiantwinery.com` (DNS у Joker): перенаправить на `https://www.einatwinery.com`, сейчас они ведут на Shopify.
- Через несколько недель убрать домены из настроек Shopify и решить, отменять ли подписку.
