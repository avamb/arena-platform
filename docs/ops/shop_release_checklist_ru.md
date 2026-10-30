# Магазин организатора: выпуск на своём домене

Для кого: оператор платформы. Спека модуля: `08_architecture/47_merch_shop_module_ru.md`, AllPay:
`08_architecture/39_allpay_payment_provider_ru.md`, деплой: `deploy/DOKPLOY.md` (раздел «Магазин»),
витрина: `apps/shop-page/README.md`, таблица 301: `apps/shop-page/docs/redirects_301.md`.
Основание: аудит `docs/reviews/shop_module_audit_2026-10-30_ru.md` (SA-06, SA-07, SA-08, SA-23).

Пример ниже: организация Einat Winery, витрина `shop.einatwinery.com`, стенд
`shop-staging.arenasoldout.com`, API стенда `api-staging.arenasoldout.com`, прод `api.arenasoldout.com`.
Каждый шаг сначала на стенде, потом на проде. Магазин остаётся закрытым (`enabled=false`), пока не пройден шаг 8.

Сначала на стенде нужно пройти шаги 1-9 с тестовыми ключами AllPay и тестовой картой, и только
потом повторить на проде с боевыми ключами.

## 0. Что нужно до начала

- Импорт из Shopify сделан (`docs/ops/shop_import.md`), картинки и баннер перенесены в медиа платформы
  (аудит SA-02: адреса `www.einatwinery.com/cdn/...` после переключения DNS отдают HTML), витрина на стенде
  сверена с живым сайтом.
- У организации есть учётная запись владельца и конфигурация AllPay (шаг 5).
- JWT платформенного суперадмина (`POST /v1/auth/login`). Пароль в команды и в чат не вставляется.
- Решено, на каком адресе живёт витрина. Дальше `SHOP_HOST` = `shop.einatwinery.com`.

```bash
API=https://api-staging.arenasoldout.com       # прод: https://api.arenasoldout.com
ORG=<uuid организации>
TOKEN=<JWT суперадмина>
SHOP_HOST=shop.einatwinery.com
```

## 1. DNS и Cloudflare у клиента

1. В зоне клиента (`einatwinery.com`) создать запись `CNAME shop` на хост сервера, где стоит сервис
   `shop-page` в Dokploy (тот же хост, что у остальных витрин платформы; адрес берёт оператор из панели).
   Если Cloudflare у клиента, облако оранжевое, режим SSL/TLS **Full (strict)**.
2. Пока сертификата нет, режим Full (strict) отвечает 526. Это нормально: Traefik заказывает сертификат
   Let's Encrypt только после того, как DNS указывает на сервер (шаг 2, пункт 4).
3. Если зона на другом аккаунте Cloudflare, запись создаёт сам клиент по инструкции выше. У платформы
   токена на чужую зону нет и быть не должно.
4. Apex-домен `einatwinery.com` и `www` остаются на Shopify до шага 10.

## 2. Сервис `shop-page` в Dokploy

1. Приложение из `apps/shop-page/Dockerfile`, контекст сборки: корень репозитория. Порт контейнера 8080.
2. Build-аргумент `VITE_API_BASE_URL` = origin API без завершающего слеша
   (стенд `https://api-staging.arenasoldout.com`, прод `https://api.arenasoldout.com`).
   Значение запекается в JS и в CSP (`connect-src`, `img-src`). Секретов в `VITE_*` не бывает.
3. Домен в Dokploy: `shop.einatwinery.com`, HTTPS через Let's Encrypt, порт 8080.
4. После переключения DNS (шаг 1) сертификат заказывается сразу. Если домен был добавлен до DNS, Traefik
   сам не повторит попытку: `docker restart` контейнера `shop-page` (см. AGENTS.md, «WordPress site moved»).
5. Проверка: `curl -sI https://shop.einatwinery.com/health` отвечает 200, `curl -sI https://shop.einatwinery.com/robots.txt`
   отдаёт текст, не HTML.

## 3. CORS и адрес возврата в arena-api (без этого оформление не работает)

Витрина ходит в API с другого origin, и адрес возврата с платёжной страницы допускается, только если его
origin есть в `CORS_ALLOWED_ORIGINS` или равен `PUBLIC_TICKETS_BASE_URL` (`checkoutReturnURLPolicy`).

1. В окружение `arena-api` добавить origin витрины к существующему списку, через запятую, без `*` и без слеша:
   `CORS_ALLOWED_ORIGINS=<прежний список>,https://shop.einatwinery.com`.
2. Передеплоить `arena-api` (в Dokploy: Redeploy сервиса, окружение подхватывается при создании контейнера).
3. Проверка CORS (до включения магазина ответ 404, важен сам заголовок: он должен содержать `access-control-allow-origin` с origin витрины):

   ```bash
   curl -sI -H "Origin: https://$SHOP_HOST" "$API/v1/public/shop/by-host?host=$SHOP_HOST" | grep -i access-control
   ```
4. Без этого шага браузер не прочитает каталог, а оформление ответит 400 `checkout.invalid_return_url`.
5. Каждый новый организатор со своим доменом повторяет этот шаг, пока допустимые origin не берутся из
   `organizations.shop_domain` (идея в аудите SA-06, пока не сделано).

## 4. Переменные окружения магазина

| Переменная | Сервис | Значение |
| --- | --- | --- |
| `VITE_API_BASE_URL` | build `shop-page` | origin API, шаг 2 |
| `CORS_ALLOWED_ORIGINS` | `arena-api` | со всеми origin витрин, шаг 3 |
| `SHOP_PUBLIC_BASE_URL` | `arena-worker` | origin витрины платформы, например `https://shop.arenasoldout.com`. Нужна письму «заказ получен» для ссылки на страницу заказа у организации БЕЗ своего `shop_domain`. У организации с доменом ссылка строится из `shop_domain`. Пусто: письмо без ссылки |
| `SHOP_TURNSTILE_SITE_KEY` | `arena-api` | публичный ключ Cloudflare Turnstile на оформлении. Витрина получает его из каталога (`settings.captcha_site_key`), в сборку `shop-page` он не входит |
| `SHOP_TURNSTILE_SECRET` | `arena-api` | секретный ключ Turnstile. **Пустое значение выключает капчу в любом окружении, в том числе в production** (в отличие от онбординга, который в production отвечает 503): прод без секрета остаётся без защиты от бота. Секрет без site key отказывает каждому оформлению |
| `SALES_TELEGRAM_BOT_TOKEN` | `arena-worker` | бот продаж, нужен для Telegram логиста (шаг 7) |

Turnstile на оформлении ввела отдельная доработка модуля. Для **стенда** годятся тестовые ключи Cloudflare,
они всегда пропускают: site key `1x00000000000000000000AA`, secret `1x0000000000000000000000000000000AA`.
Для **прода** ключи создаются в Cloudflare (Turnstile, виджет на домен `shop.einatwinery.com`), пара хранится
только в окружении Dokploy. Тестовые ключи на проде не ставить: они ничего не проверяют.

## 5. Платёжный провайдер AllPay (SA-23)

Без выбранного канала магазин берёт старший живой канал организации, у которого провайдер настроен.
Если у организации есть билетные каналы на разных провайдерах, деньги магазина могут уйти не в AllPay.
Поэтому канал выбирается явно.

1. Конфигурация AllPay организации (владелец или оператор в ивент-центре, «Платёжные настройки»):
   `provider=allpay`, режим `test` на стенде и `live` на проде, `merchant_id` = API login, `secret_key` = API key
   из кабинета AllPay (Settings, API Integrations). Спека 39, раздел 13.
2. Проверка записи (в БД, значения секретов не выводить):

   ```sql
   SELECT id, provider, mode, is_active, verification_status
     FROM payment_provider_configs
    WHERE org_id = '<ORG>' AND provider = 'allpay';
   ```

   Нужна одна строка: `is_active = true`, `verification_status = 'ok'`. Статус обновляется при сохранении
   ключей и по `POST /v1/organizations/{org_id}/payment-configs/{id}/verify`.
3. Канал продаж с провайдером AllPay. Список каналов: `GET $API/v1/organizations/$ORG/channels`. Если подходящий
   канал уже есть, взять его `id`. Иначе создать канал (тип прямой продавец, `payment_mode=direct_merchant`)
   или переключить существующий: `PATCH $API/v1/organizations/$ORG/channels/<id>` с `{"provider":"allpay"}`
   (токен суперадмина и `X-Admin-Reason`). Режим `merchant_of_record` ставит только суперадмин и только по решению владельца.
4. `CHANNEL_ID` из этого шага идёт в настройки магазина (шаг 6), поле `sales_channel_id`.
5. Вебхук в кабинете AllPay настраивать не нужно: arena присылает `webhook_url`
   (`<API_PUBLIC_URL>/v1/payment-intents/webhook/{config_id}`) с каждым заказом. Валюта магазина должна быть
   той, что разрешена аккаунту AllPay (ILS, USD, EUR).

## 6. Настройки магазина: канал, домен, включение (SA-07)

В ивент-центре и в admin-web нет кнопки, которая включает магазин и задаёт домен, а суперадмина ивент-центр
не пускает. Это делается вызовом API от суперадмина. `shop_domain` и `enabled` может менять только платформенный
суперадмин, любой другой получит 403 `shop.domain_superadmin_only` или `shop.enabled_superadmin_only`.

`PUT` **заменяет всю строку настроек**: все поля, кроме `shop_domain`, `enabled` и `max_line_qty`, берутся из тела,
а пропущенное становится пустым. Поэтому сначала читаем текущие, потом шлём их же с нужными изменениями:

```bash
curl -s -H "Authorization: Bearer $TOKEN" -H "X-Admin-Reason: shop release $SHOP_HOST" \
  "$API/v1/organizations/$ORG/shop/settings"
```

Тело (`ShopSettingsUpdateRequest`; обязательна только `currency`, но не оставляйте остальные пропущенными):

| Поле | Что |
| --- | --- |
| `currency` | ISO 4217, например `ILS` |
| `shipping_fee_minor` | плоская доставка в минорных единицах |
| `shipping_label` | подпись доставки на витрине, до 200 символов |
| `country_iso2` | страна магазина или `null` |
| `age_gate_min_age` | возраст для плашки 18+ или `null` |
| `announcement_text` | строка объявления, до 500 символов |
| `contact_email` | адрес для покупателей или `null` (Reply-To писем) |
| `logistics_note` | внутренняя заметка логисту, до 2000 символов |
| `sales_channel_id` | UUID канала из шага 5 |
| `shop_domain` | голый хост, `shop.einatwinery.com`. Нет поля или `null` оставляет, `""` очищает. Уникален на платформу (409 `shop.duplicate_domain`) |
| `max_line_qty` | 1-99, нет поля оставляет текущее |
| `enabled` | `true` открывает витрину и оформление, нет поля оставляет |

Пример (значения подставить из GET, ниже условные):

```bash
curl -s -X PUT -H "Authorization: Bearer $TOKEN" -H "X-Admin-Reason: shop release $SHOP_HOST" \
  -H "Content-Type: application/json" \
  "$API/v1/organizations/$ORG/shop/settings" -d '{
    "currency": "ILS",
    "shipping_fee_minor": 3000,
    "shipping_label": "Доставка курьером",
    "country_iso2": "IL",
    "age_gate_min_age": 18,
    "announcement_text": "",
    "contact_email": "shop@einatwinery.com",
    "logistics_note": "",
    "sales_channel_id": "<CHANNEL_ID>",
    "shop_domain": "shop.einatwinery.com",
    "max_line_qty": 24,
    "enabled": false
  }'
```

Домен и канал задаются при `enabled=false`, включение отдельным вызовом на шаге 8, после проверок. Ответ
повторяет настройки (`configured=true`). Проверка резолва домена (404 до включения ожидаем):

```bash
curl -s "$API/v1/public/shop/by-host?host=$SHOP_HOST"      # пока магазин закрыт: 404 shop.not_found, после шага 8: 200
```

## 7. Telegram логиста

1. Бот продаж (`SALES_TELEGRAM_BOT_TOKEN` на `arena-worker`) добавлен в группу логиста. id группы: число со знаком
   минус, у супергруппы начинается с `-100`.
2. Подписка (SQL, подробности `docs/ops/shop_notifications_ru.md`):

   ```sql
   INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds)
   VALUES ('<ORG>', 'Логистика', '<chat id>', '{shop}');
   ```
3. Если нужен и чат с билетами: `kinds = '{tickets,shop}'`. Ошибка последней доставки видна в `last_error` строки.

## 8. Включение и первый заказ

1. На стенде прогнать полный цикл: покупка 2 бутылок тестовой картой AllPay, сообщение в группе логиста,
   письмо «заказ получен», заказ в ивент-центре, «Отправлен» с письмом `shop_order_shipped`, частичный возврат
   и возврат остатка (письма `shop_order_refunded`). Приёмка 2-4 в спеке 47 §9.
2. Включить магазин (то же тело, что в шаге 6, но `"enabled": true`).
3. Открыть `https://shop.einatwinery.com/`: каталог, карточка, корзина, оформление доходит до страницы AllPay.
4. На проде первый боевой заказ сделать самим на минимальную сумму, сверить сумму в кабинете AllPay и в arena,
   вернуть его целиком из ивент-центра.
5. Если что-то идёт не так, магазин закрывается тем же вызовом с `"enabled": false`. Заказы и остатки не теряются.

## 9. Проверка robots, sitemap и 404

```bash
curl -s https://$SHOP_HOST/robots.txt               # Disallow: /cart /checkout /order/ /search, Sitemap
curl -s https://$SHOP_HOST/sitemap.xml | head       # главные маршруты
curl -sI https://$SHOP_HOST/cart | grep -i x-robots # noindex, nofollow
curl -sI https://$SHOP_HOST/no-such-page | head -1  # HTTP 404
```

В `index.html` витрины зашит `og:image` `https://shop.einatwinery.com/theme/brand/logo.png`, файл кладёт доработка
витрины (аудит SA-02). Проверить, что он открывается.

## 10. 301 со старых адресов и пауза Shopify

Правила 301 для старых адресов Shopify уже в `apps/shop-page/nginx.conf.template` и действуют на любом домене организатора
(не на `arenasoldout.com`): коллекции, `/collections/*/products/*`, блоги, аккаунты, постоянные ссылки корзины.
Таблица и отличия: `apps/shop-page/docs/redirects_301.md`.

Когда основной домен `einatwinery.com` переводится на витрину (спека 47 §9, пункт 6):

1. Заменить домен: `shop_domain` = `einatwinery.com` (вызов шага 6), домен в Dokploy и DNS (apex на сервер,
   у Cloudflare можно CNAME flattening), origin в `CORS_ALLOWED_ORIGINS` (шаг 3), хост в `public/robots.txt` и `public/sitemap.xml`
   (там зашит `shop.einatwinery.com`), `og:image` в `index.html`. Передеплоить `arena-api` и пересобрать `shop-page`.
2. Пока основной домен остаётся на Shopify, а витрина на `shop.einatwinery.com`, 301 со старых адресов делается на стороне
   Shopify (Navigation, URL redirects) или Cloudflare Bulk Redirects зоны клиента: старый путь на тот же путь
   `https://shop.einatwinery.com/...`.
3. Проверить 10-15 старых адресов из `hrefs.txt` выгрузки: `curl -sI` отдаёт 301 или 200, не 404.
4. Шаг «пауза Shopify» (приостановить магазин, не удалять) делать последним, после первой успешной боевой продажи
   и подтверждённых 301. Подписку Shopify не отключать, пока владелец не подтвердит, что старые заказы не нужны (SA-29).
5. Если ломается, вернуть DNS на Shopify и закрыть магазин (`enabled=false`).

## Что проверить в конце

- [ ] `by-host` отвечает 200, CORS-заголовок есть, оформление не отвечает 400 `checkout.invalid_return_url`.
- [ ] `sales_channel_id` задан, канал на AllPay, конфигурация `ok`.
- [ ] Turnstile: на проде боевая пара, не тестовая. `GET .../shop/{slug}` в `settings` отдаёт `captcha_site_key`.
- [ ] `TRUSTED_PROXY_COUNT` у `arena-api` соответствует цепочке Cloudflare -> Traefik (второй аудит, SB-03): иначе все покупатели получают
      один адрес, и после трёх неоплаченных заказов магазин отвечает всем 409 `shop.too_many_pending`. Проверка: два оформления
      с разных сетей (мобильная и домашняя) без оплаты, затем `select buyer_ip_hash, count(*) from shop_orders where status='pending_payment'
      group by 1` показывает две разные строки, а не одну.
- [ ] Группа логиста получила сообщение, письмо покупателя пришло со ссылкой на страницу заказа.
- [ ] Возврат из ивент-центра дошёл до AllPay, письмо `shop_order_refunded` пришло.
- [ ] robots, sitemap, noindex на `/cart` и `/checkout`, 404 на неизвестные адреса.
- [ ] 301 старых адресов, Shopify приостановлен.
