# PayPlus — эксплуатация, заглушка, что проверить с настоящими ключами

PayPlus (Израиль, https://docs.payplus.co.il) — четвёртый провайдер hosted-checkout
после Stripe, Flitt и AllPay. Код: `apps/backend/internal/adapters/payplus`
(регистрация в `internal/app/payments/modules.go`), спецификация:
`08_architecture/47_merch_shop_module_ru.md` §3, исследование API:
`docs/plans/einatwinery_call_anna_2026-10-26_ru.md` §4. Миграция не нужна:
`sales_channels.provider` проверяется реестром модулей (миграция 0132).

## 1. Как это устроено

| Наше | PayPlus |
|---|---|
| `payment_provider_configs.secrets` | `api_key`, `secret_key` (заголовки `api-key`, `secret-key` каждого запроса), `payment_page_uid` (UUID платёжной страницы из дашборда, проверяется при сохранении) |
| режим конфига `test` / `live` | хост `https://restapidev.payplus.co.il/api/v1.0` / `https://restapi.payplus.co.il/api/v1.0`, ключи у них разные |
| создание страницы | `POST PaymentPages/generateLink`: `charge_method=1` (J4), сумма в **основных** единицах (`50.62`), `currency_code`, `more_info` = id нашей checkout-сессии, `refURL_success/failure/cancel` = наш `/v1/public/payment-return`, `refURL_callback` = `/v1/payment-intents/webhook/{config_id}`, `send_failure_callback=true`, `expiry_datetime` = окно оплаты в минутах (округление вниз), `language_code` he/ru/en (остальное en), одна строка `items` на всю сумму, письма PayPlus покупателю выключены |
| `provider_payment_id` | `data.page_request_uid` = `transaction.payment_request_uid` в колбэке |
| `provider_charge_ref` | `transaction.uid` из колбэка; если колбэк его не сохранил, модуль читает его через `PaymentPages/ipn` перед возвратом |
| подпись колбэка | заголовок `hash` = base64(HMAC-SHA256(secret_key, тело)), `user-agent: PayPlus` (не проверяется: доказательство — только HMAC) |
| успех | `transaction_type=Charge` и `status_code=000` |
| отказ карты | любой другой `status_code`: одна неудачная попытка, ссылка остаётся открытой, платёж **не** переводится в failed (правило Flitt/AllPay); брошенную ссылку снимает наша бронь по TTL |
| колбэк возврата | `transaction_type=Refund`: принимается без перехода состояния (наши возвраты синхронные) |
| возврат | `POST Transactions/RefundByTransactionUID` {`transaction_uid`, `amount` в основных единицах, `more_info=arena-refund:<id>`}; полный и частичный |
| повтор возврата | ключа идемпотентности нет: повторный вызов сначала ищет возврат по `more_info` через `Transactions/View`; ничего не нашёл в первые 10 минут — ничего не шлёт и ждёт |
| проверка ключей | `POST PaymentPages/ipn` с несуществующим uid: ответ про ключи (`MISSING API_KEY`, 401/403) = отказ, любой другой ответ = ключи приняты, 5xx/сеть = «недоступен» |
| маршрут колбэка | только per-config; на старом `/v1/payment-intents/webhook` тело PayPlus получает 400 `webhook.payplus_requires_config_route` |

## 2. Переменные окружения

- `PAYPLUS_API_BASE_URL` — тестовый шов, заменяет **оба** хоста, обязательно с
  `/api/v1.0`, например `http://localhost:12114/api/v1.0`. Нужен api и worker
  (воркер делает возвраты из `refund.sweep`). В `APP_ENV=production`
  `config.Validate` отказывается стартовать с ним. На стенде с настоящими
  тестовыми ключами PayPlus его **не** задают: конфиг в режиме `test` сам ходит
  в restapidev.
- `API_PUBLIC_URL` (иначе `APP_PUBLIC_URL`), как у Flitt: из него строятся
  `refURL_callback` и возврат покупателя.

## 3. Заглушка

`apps/widget/scripts/payplus-stub.cjs`, без зависимостей:

```
PORT=12114 node apps/widget/scripts/payplus-stub.cjs
# api / worker:
PAYPLUS_API_BASE_URL=http://localhost:12114/api/v1.0
```

- Конфиг PayPlus в админке: любые непустые `api_key`, `secret_key` и любой UUID
  в `payment_page_uid` (или задать `PAYPLUS_STUB_API_KEY` /
  `PAYPLUS_STUB_SECRET_KEY`, тогда чужая пара получает 422 `MISSING API_KEY`).
- Покупка: виджет уводит на `http://localhost:12114/pay/<page_request_uid>`,
  там кнопки «Pay with 5326-1402-8077-9844» и «Decline with
  5326-1402-0001-0120». Заглушка шлёт колбэк, подписанный тем `secret-key`, с
  которым создавалась ссылка, и возвращает покупателя на `refURL`.
- Без браузера: `curl -X POST "http://localhost:12114/__stub/pay/<uid>?outcome=approve"`
  (или `decline`), ответ JSON с `callback_status` — HTTP-код, который вернул
  наш вебхук.
- В контейнере: `HOST=0.0.0.0`, `PUBLIC_URL=http://<имя, видимое браузеру>:12114`.
  Callback-адрес должен быть доступен из контейнера заглушки.

## 4. Тесты

```
go.exe test ./apps/backend/internal/adapters/payplus/
DATABASE_URL=<чистая база по рецепту CI из AGENTS.md> \
  go.exe test -tags integration -run TestPayPlus ./apps/backend/internal/platform/httpserver/
```

Интеграционный тест покупает билет через checkout/start, проверяет отказ
карты, неверный и отсутствующий `hash` (401), старый маршрут (400), оплату,
повтор колбэка, недоплату (manual_review) и два частичных возврата билета.

## 5. Что проверить на restapidev с настоящими тестовыми ключами

Самообслуживаемой песочницы нет: ключи выдаёт PayPlus после регистрации как
software house (`tech@payplus.co.il`). Тестовые карты: 5326-1402-8077-9844
(успех), 5326-1402-0001-0120 (отказ), 05/30, CVV 000.

Ручной зонд (создаёт одну неоплаченную ссылку, печатает сырые ответы, ключи не
печатает):

```
PAYPLUS_TEST_API_KEY=... PAYPLUS_TEST_SECRET_KEY=... PAYPLUS_TEST_PAGE_UID=...   go.exe test -tags paypluslive -run TestPayPlusLive -v ./apps/backend/internal/adapters/payplus/
```

1. **Байты подписи.** Поймать настоящий колбэк и посмотреть, какой вариант
   совпал: raw, compact или js_stringify (`payplus.VerifyHash` возвращает
   вариант). Если всегда raw — fallback-и можно убрать. Отдельно проверить
   колбэк с ивритом в `items[].name` и с суммой вида `50.10`.
2. **Отказ карты.** Приходит ли колбэк при `send_failure_callback=true`, какой у
   него `status_code`, и действительно ли страница даёт ввести другую карту
   после отказа (на этом стоит решение «отказ не закрывает платёж»).
3. **Возврат J4.** `RefundByTransactionUID` по обычному списанию J4 (в доке
   описан для J5): полный, частичный, второй частичный; ответ при превышении
   остатка; `Transactions/Cancel` в тот же день до 23:00 не используется.
4. **Поиск возврата по `more_info`.** Находит ли `Transactions/View` с одним
   `more_info` транзакцию ВОЗВРАТА и что отвечает, когда ничего нет (успех с
   пустым массивом или ошибка «не найдено»). От этого зависит защита от
   двойного возврата при потерянном ответе.
5. **Проверка ключей.** Что отвечает `PaymentPages/ipn` на несуществующий uid
   при верных ключах и при верном `api_key` с неверным `secret_key`.
6. **`PaymentPages/ipn` по `payment_request_uid`** — форма ответа (в доке `{}`),
   где лежит `transaction.uid`.
6а. **Имя поля ссылки в колбэке.** В доке `transaction.payment_request_uid`,
   в ответе generateLink `page_request_uid`, в реальных колбэках встречается
   `transaction.payment_page_request_uid`. Адаптер принимает все три; записать,
   какое приходит на самом деле.
7. **Языки и валюты.** Какие `language_code` принимает страница (he, en, ru,
   ar?) и не ломает ли неизвестный; ILS обязательно, USD/EUR при необходимости.
8. **`items`.** Создаёт ли PayPlus продукт в каталоге мерчанта на каждое новое
   имя без `product_uid`; если да и Анне это мешает, убрать `items` или
   передавать `product_uid`. Нужны ли строки для Invoice+.
9. **Метод возврата покупателя** на `refURL_success`/`failure` (GET или POST) —
   работает в любом случае через `/v1/public/payment-return`.
10. **Повторы колбэка.** Повторяет ли PayPlus колбэк при не-200 и как долго.
