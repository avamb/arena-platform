# Vino&Co: переключение прода с Bil24 на Arena — ранбук (подготовлен 24.09)

Всё, что можно было сделать заранее, сделано. На проде Vino **ничего не менялось**: только чтение и пробный прогон скрипта настроек без записи (dry run).
Выполнять по команде владельца. Bil24 отключается 28.09.

Основа — план `vinoandco_arena_migration_plan_2026-09-23_ru.md`, §6 (уроки Lampyris) и §8 (дополнения 24.09).
Скрипты — в `ops/vino-cutover/`.

## 0. Что готово

| Что | Состояние |
|---|---|
| Код сайта | `dev` = `master` = `8c12df3` на GitHub. На проде `prod` = `f2ad9f9`, это предок `dev`: переход fast-forward, 97 файлов. Всё проверено на staging. |
| Пакеты импорта | Пробная сборка по выгрузке 23.09 прошла: 7 сеансов с `--tz Asia/Jerusalem`. Выгрузку и сборку повторим в момент переключения (п. 3). |
| Скрипт настроек сайта | `ops/vino-cutover/cutover-settings.php`: dry run / apply / rollback, сам делает бэкап. Dry run на проде дал ожидаемое (см. ниже). |
| Arena | Организации `vinoandco` ещё нет. Её создаём в день переключения (п. 2), чтобы не держать пустую организацию. |

Результат dry run на проде (24.09):
```
[bil24_acf_sync] base_url_prod: https://api.bil24.pro/json → https://api.arenasoldout.com/compat/bil24/json; fid: 2580 → (новый)
[lops_arena] base_url=https://api.arenasoldout.com organizer_name=Vino&Co (api_key: вписывает владелец)
[WPML] product 1→2, product_variation 1→2, product_cat 1→2, product_tag 1→2
[Elementor] page 495: grid 82139f0 hide_tablet — elements found: 1
```

Товары на проде и сеансы Bil24:

| Товар | Сеанс | Замечание |
|---|---|---|
| 2888 | 7040556 | GA. В MACS билетов нет. |
| 2892 | 7040580 | Распродан, в каталоге Bil24 его нет. Собирается из sidecar-файла с 1 местом; после импорта **закрыть категорию**. |
| 3000 | 7040584 | Проданные билеты в MACS лежат под событием **7040580** (два сеанса одного вечера слиты). |
| 3027 | 7040585 | Места и GA (квиз). Флажок «Стол целиком» = yes. |
| 3047 | 7040589 | |
| 3277 | 7040596 | |
| 3294 | 7040597 | |

## 1. Перед окном переключения

1. **Закрыть продажи в Bil24 на всех фронтендах** (кабинет Bil24, делает владелец). Иначе после выгрузки кто-то купит через другой фронтенд, и остаток в Arena разойдётся.

   Почему это важно: повторный импорт `source=bil24` **не меняет количество GA**. Поэтому импортируем один раз, после остановки продаж. «Дозагрузка» из-за покупок, прошедших за это время, — это и есть свежая выгрузка в момент переключения.
2. Дождаться, пока не останется неоплаченных заказов с живой бронью Bil24. Окно брони — 20 минут. Проверка:
   ```sql
   select id, status, date_created_gmt from x4gd_wc_orders
   where type='shop_order' and status in ('wc-pending','wc-on-hold') and date_created_gmt > utc_timestamp() - interval 1 hour;
   ```
   Если такой заказ оплатят после переключения, PAY_ORDER уйдёт в Arena, а Arena этого заказа не знает. Покупатель заплатит, а билета не получит.
3. Бэкап (агент, по команде):
   ```bash
   ssh lead-parser 'mkdir -p /root/vino-prod-cutover-$(date +%Y%m%d) && docker exec asoconnector-vinoandcotempvino-fsppdj-wp_db-1 sh -c "mysqldump -uroot -p\"\$MYSQL_ROOT_PASSWORD\" --single-transaction \"\$MYSQL_DATABASE\"" | gzip > /root/vino-prod-cutover-$(date +%Y%m%d)/wp_before.sql.gz && ls -la /root/vino-prod-cutover-*'
   ```
   Тег отката в репо Vino: `git tag pre-arena-cutover-20260924 f2ad9f9 && git push origin pre-arena-cutover-20260924`.

## 2. Arena: организация и канал

Делается во вкладке админки `app.arenasoldout.com` от суперадмина, заголовок `X-Admin-Reason`. Порядок такой же, как у `vinoandco-staging`:

1. Организация `vinoandco` «Vino&Co», страна IL, язык en.
2. Канал «Vino&Co WP»:
   - provider `allpay`, direct_merchant — деньги берёт сайт;
   - шлюз включён, выдаётся новый **fid**.
3. MACS-подписчик `https://macs.arenasoldout.com/api/_wh/tickets`, без подписи.
4. **Владелец:**
   - выпускает токен шлюза;
   - регистрирует WordPress webhook на `https://vinoandco.events/wp-json/bil24/v1/notification`;
   - выпускает API-ключ организации с этим каналом и правами `promo.*`.

   Сверяем по sha256-отпечатку, в файлы ничего не пишем.

## 3. Свежая выгрузка Bil24 и импорт (после п. 1.1)

1. Выгрузка кредами сайта. Токен не печатается и на диск не пишется:
   ```bash
   ssh lead-parser "docker exec asoconnector-vinoandcotempvino-fsppdj-wordpress-1 php -r 'require \"/var/www/html/wp-load.php\"; echo serialize(get_option(\"bil24_acf_sync\"));'" | node ops/vino-cutover/run-export-from-wp.mjs
   ```
   7040580 в каталоге нет. Скопировать его `sessions/7040580/` (вместе с `actionEvent.json`) из выгрузки 23.09 или снять заново.
2. Сборка: `node ops/bil24-export/to-bundle.mjs <dump> --tz Asia/Jerusalem`. Venue offset **не нужен**: это прод, а не staging.

   Проверить сводку: 7 сеансов, остатки совпадают с Bil24 после закрытия продаж.
3. Импорт от суперадмина, без публикации: товары на сайте уже есть, дубли не нужны.

   После импорта:
   - вернуть короткие названия событий: `PATCH …/events/{id}`;
   - закрыть категорию 7040580;
   - сверить остатки через `GET_ALL_ACTIONS`.

## 4. Код сайта (владелец жмёт Deploy)

**Сделано 24.09 ~12:20:**
- `prod` на GitHub перемотан `f2ad9f9 → 8c12df3`;
- тег отката `pre-arena-cutover-20260924` запушен;
- бэкап БД до деплоя: `lead-parser:/root/vino-prod-cutover-20260924/wp_before_deploy.sql.gz`.

```bash
git -C C:\Projects\vinoandco-prod-rebuild\repo\vinoandco push origin dev:prod
```
Это fast-forward `f2ad9f9 → 8c12df3`. Затем в Dokploy (lead-parser, compose `asoconnector-vinoandcotempvino-fsppdj`) нажать **Deploy**.

Проверить:
- контейнер поднялся;
- в логе нет PHP fatal;
- `/wp-content/mu-plugins/tests/` отдаёт 404.

До смены настроек (п. 5) новый код продолжает ходить в Bil24. Поэтому деплой можно сделать заранее, отдельно от переключения.

## 5. Настройки сайта (агент, скрипт)

```bash
ssh lead-parser "docker exec -i -e VINO_ARENA_ORG_ID=<uuid> -e VINO_ARENA_FID=<fid> -w /var/www/html asoconnector-vinoandcotempvino-fsppdj-wordpress-1 php -- apply" < ops/vino-cutover/cutover-settings.php
```
Скрипт делает:
- шлюз: Bil24 → Arena (URL, fid, locale en);
- вкладка «Arena» ивент-центра (base_url, org_id, organizer_name=Vino&Co);
- WPML fallback = 2;
- Elementor: сетку 82139f0 на странице 495 скрыть на планшете.

Бэкап — в опции `vino_cutover_backup_20260924`.

Сразу после скрипта **владелец** вписывает:
- токен шлюза — Bil24 → Settings;
- секрет вебхука — Settings → Arena webhook;
- API-ключ — ивент-центр → Arena.

Проверка шлюза кредами сайта:
```bash
ssh lead-parser "docker exec asoconnector-vinoandcotempvino-fsppdj-wordpress-1 php -r 'require \"/var/www/html/wp-load.php\"; echo serialize(get_option(\"bil24_acf_sync\"));'" | node ops/vino-cutover/gw-check-from-wp.mjs
```
Ожидается resultCode 0 и 7 сеансов.

## 6. Синк и проверки

1. Синк каталога: `do_action('bil24_acf_sync_cron')` через `php -r` в контейнере. Число товаров и вариаций до и после должно совпасть (`prod-facts.sql`), дублей нет.
2. «Стол целиком» стоит на 3027 и на всех товарах, что продаются столами.
3. Страницы ru/he/en: карточки с кнопкой «Билеты», выбор мест, иврит (названия столов, промокод).
4. **Контрольная покупка владельца** — карту вводит владелец. Цепочка:
   - PAY_ORDER 0;
   - order.paid с подписью;
   - письмо и PDF на языке заказа;
   - билет в MACS.
5. Контрольный возврат одного билета:
   - AllPay;
   - REFUND_TICKET;
   - ticket.refunded;
   - письмо о возврате;
   - статус 3 в MACS.
6. **MACS на входе 29.10:** проданные билеты Bil24 для 7040584 лежат под событием 7040580, а новые билеты Arena для 7040584, скорее всего, придут под origin 7040584. Контроль на входе должен сканировать **оба** события. По 7040556 билетов Bil24 в MACS нет — проверить у владельца, продавалось ли там что-то вне сайта.
7. Очередь «Возвраты» покажет старые заказы Bil24 с возвращёнными деньгами без отмены билета. Их закрываем вручную.

8. Telegram-уведомления (с 24.09 на проде): добавить `@ArenaSoldOutSalesBot` в группу «Vino&Co» (-5042421279), затем
   `INSERT INTO sales_notification_subscriptions (org_id, name, chat_id) VALUES ('<id org vinoandco>', 'Vino&Co', '-5042421279');`
   на arena-prod. В Bil24 уведомление Vino&Co отключить после переключения.

## 7. Откат

1. `php -- rollback` тем же скриптом: вернёт опции и Elementor из бэкапа. Сайт снова ходит в Bil24, пока тот жив.
2. Код: в Dokploy задеплоить тег `pre-arena-cutover-20260924` (`git push -f origin pre-arena-cutover-20260924:prod`, только по команде владельца). Новый код с Bil24 совместим, поэтому откат кода нужен только при поломке самого кода.
3. Крайний случай — БД из `/root/vino-prod-cutover-*/wp_before.sql.gz`. Заказы, сделанные после бэкапа, при этом потеряются.

После 28.09 откатываться некуда. Поэтому переключение — с запасом до 28.09.
