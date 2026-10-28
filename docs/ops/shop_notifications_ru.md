# Магазин: уведомления логисту и письма покупателю

Спека: `08_architecture/47_merch_shop_module_ru.md`, раздел 7. Миграция 0173.

## Что происходит при оплате

Оплаченный заказ магазина (`v1.shop.order.paid`) делает две вещи независимо друг от друга:

1. В той же транзакции, что помечает заказ оплаченным, кладётся задание `shop.order_email` (письмо `shop_order_received`). При «Отправлен» кладётся второе задание (`shop_order_shipped`), повторное нажатие «Отправлен» только меняет заметку и письма не шлёт. Письмо уходит из `arena-worker`, язык берётся из `shop_orders.buyer_locale` (нет такого шаблона — английский), Reply-To — `shop_settings.contact_email`. Адрес организации (`organizations.contact_email`) в письмо не попадает никогда.
2. Звено `salesnotify` в `arena-worker` шлёт сообщение в Telegram в чаты, у которых в `kinds` есть `shop` (заказ — строка по порядку формы курьера: имя, телефон, город, адрес 1, адрес 2, индекс, затем состав, доставка, заметка покупателя). Возврат (`v1.shop.order.refunded`) — отдельное короткое сообщение. Каждое сообщение записывается один раз в `sales_notification_deliveries` (`shop_paid:<order id>`, `shop_refund:<order id>:<возвращено всего>`).

Бот тот же, что у продаж билетов (`SALES_TELEGRAM_BOT_TOKEN`), бот должен быть участником группы.

## Переменные окружения

| Переменная | Где | Зачем |
| --- | --- | --- |
| `SHOP_PUBLIC_BASE_URL` | `arena-worker` | Адрес витрины платформы. Ссылка на статус заказа в письме: `<SHOP_PUBLIC_BASE_URL>/<slug организации>/checkout/<токен заказа>`. У организации с собственным `organizations.shop_domain` ссылка `https://<домен>/checkout/<токен>`. Пусто и домена нет — письмо уходит без ссылки. |
| `SALES_TELEGRAM_BOT_TOKEN` | `arena-worker` | Как для билетов. Пусто — Telegram-звено ничего не шлёт. |

Письма уходят только через настоящий почтовый sender (не `LogSender`), как и билеты.

## Группа логиста: строка в `sales_notification_subscriptions`

Подписок нет ни в API, ни в админке: это строки SQL. Один чат — одна строка (уникальность `org + chat_id`), поэтому «что слушает чат» задаёт массив `kinds`: `tickets` (продажи и возвраты билетов), `shop` (заказы магазина). Старые строки получили `{tickets}`.

Группа логиста организации (бот должен быть добавлен в группу заранее; id группы — число со знаком минус, супергруппа начинается с `-100`):

```sql
INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds)
VALUES ('<org uuid>', 'Логистика', '<chat id>', '{shop}');
```

Чат, которому нужны и билеты, и магазин:

```sql
INSERT INTO sales_notification_subscriptions (org_id, name, chat_id, kinds)
VALUES ('<org uuid>', 'Продажи', '<chat id>', '{tickets,shop}');
```

Изменить существующий чат:

```sql
UPDATE sales_notification_subscriptions
   SET kinds = '{tickets,shop}', updated_at = now()
 WHERE org_id = '<org uuid>' AND chat_id = '<chat id>';
```

Правила:

- `org_id NULL` — оператор: слышит магазины всех организаций (если в `kinds` есть `shop`).
- Переключатели `on_order_paid` и `on_ticket_refunded` работают для обоих видов: первый глушит сообщения об оплате, второй — о возвратах. По умолчанию оба включены.
- `allowed = false` отключает чат целиком.
- В `kinds` допустимы только `tickets` и `shop`, массив не пустой (иначе 23514).
- Чат только с `shop` не получает ни продажи билетов, ни сообщения контактной формы покупателя организатору (`AnnounceToOrg` идёт в чаты с `tickets`).
- Ошибка последней доставки (бота нет в группе и т.п.) видна в `last_error` строки.

## Проверка на стенде

1. Строка группы с `{shop}` (SQL выше), бот в группе.
2. Заказ через витрину, оплата в тестовом Stripe.
3. В группе одно сообщение «New shop order»; у покупателя письмо `shop_order_received`; повторная доставка вебхука второго сообщения и письма не даёт.
4. «Отправлен» в ивент-центре: письмо `shop_order_shipped` с заметкой отправления.
5. Если сообщения нет: `SELECT last_error FROM sales_notification_subscriptions ...`, затем `SELECT * FROM worker_jobs WHERE job_type = 'shop.order_email' ORDER BY created_at DESC` для письма.
