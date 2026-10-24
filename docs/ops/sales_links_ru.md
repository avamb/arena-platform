# Ссылки продаж и QR: как завести хост и читать цифры

Спека: `08_architecture/43_sales_links_qr_ru.md`. Код: `GET /go/{code}` на arena-api (`httpserver/hgo`).

**Результат.** Организатор печатает на афише QR с адресом `https://go.arenasoldout.com/<код>`. Сканирование ведёт на страницу мероприятия с метками `utm_*` и `ref=<код>`. Платформа считает сканы и заказы по ссылке.

## 1. Что это

- Ссылка продаж — строка в `sales_links`: организация, цель, метка (`label`), `utm`, короткий код из 7 символов.
- Цель: страница организации, страница промоутера, мероприятие, сеанс или чужой `https`-адрес.
- QR кодирует только короткий адрес `<SHORT_LINK_BASE_URL>/<код>`. Адрес цели в QR не зашит, поэтому цель можно сменить после печати.
- Редирект один и не зависит от хоста: код разрешается одинаково с любого хоста. Traefik только добавляет к пути префикс `/go`.
- Короткий адрес собирает один код: `salelinks.ShortURL(base, code)`. Статическая проверка `short_link_url_test.go` не пускает ручную сборку.

## 2. Шаги владельца (QL-15)

> Предупреждение. Агенты DNS и Traefik не трогают. Делает владелец или ops. Хост с редиректом на fallback безвреден, его можно заводить до выката QL-4.

1. Cloudflare, зона `arenasoldout.com`: добавить запись `go` типа A (или CNAME на хост arena-prod), режим **Proxied**.
2. Cloudflare, Rules, Cache Rules: правило «Bypass cache» для `Hostname equals go.arenasoldout.com`.
   Зачем два слоя: приложение уже отвечает `Cache-Control: no-store`, а правило не даёт будущей настройке зоны закэшировать 302 и потерять счёт.
3. Traefik (роутер Dokploy для arena-api): добавить роутер по хосту и middleware с префиксом. Форма меток, как в `ops/macs/docker-compose.yml`. Имя сервиса и порт возьмите у роутера `api.arenasoldout.com`.

```yaml
- traefik.http.middlewares.go-prefix.addprefix.prefix=/go
- traefik.http.routers.arena-go-websecure.entrypoints=websecure
- traefik.http.routers.arena-go-websecure.rule=Host(`go.arenasoldout.com`)
- traefik.http.routers.arena-go-websecure.tls.certresolver=letsencrypt
- traefik.http.routers.arena-go-websecure.middlewares=go-prefix
- traefik.http.routers.arena-go-websecure.service=<сервис arena-api>
```

4. Стенд: те же три шага для `go-staging.arenasoldout.com` → стенд (`api-staging`). В окружении стенда задать `SHORT_LINK_BASE_URL=https://go-staging.arenasoldout.com`.
5. Проверка неизвестного кода. Ждём `302` и `Location` на fallback.

```powershell
curl.exe -I https://go.arenasoldout.com/zzzzzzz
```

6. Проверка настоящего кода. Ждём `302`, `Location` на страницу мероприятия с `utm_*` и `ref=<код>`, заголовок `Cache-Control: no-store`.

```powershell
curl.exe -I https://go.arenasoldout.com/<код>
```

7. Если ответ `404`: проверьте middleware `go-prefix`, без него путь `/<код>` не доходит до маршрута `/go/{code}`. Если сертификат не выпускается: DNS должен смотреть на origin до запроса Let's Encrypt, затем перезапустите контейнер arena-api.

## 3. Окружение (только arena-api)

| Переменная | Что делает |
|---|---|
| `SHORT_LINK_BASE_URL` | Хост под QR. По умолчанию `https://go.arenasoldout.com`. В production: `https://`, только хост, без пути и параметров. |
| `SHORT_LINK_FALLBACK_URL` | Куда ведёт неизвестный или битый код. В production: `https://`. |
| `TRUSTED_PROXY_COUNT` | Должен быть больше 0 и равен числу прокси перед api (проверьте на стенде). Иначе заголовок `CF-IPCountry` не читается, страна пишется пустой. |
| `PUBLIC_API_IP_RATE_LIMIT` | Лимит по IP действует и на редирект. Сверх лимита: `429` с `Retry-After`. |

Цепочка fallback: `SHORT_LINK_FALLBACK_URL`, затем `PUBLIC_TICKETS_BASE_URL`, затем `APP_PUBLIC_URL`. Таблица переменных по ролям: `deploy/DOKPLOY.md` §2.6.

## 4. Перенацелить или убрать напечатанный код

Маршруты организации: по спеке 43 §4.2 (права `sales_link.read` и `sales_link.manage`).

1. Перенацелить: `PATCH /v1/organizations/{org_id}/sales-links/{id}` с телом `{"target": {...}}`. В аудит пишется `v1.sales_link.retarget` с прежней и новой целью. Напечатанный QR остаётся прежним.
2. Убрать: `PATCH` с `{"status": "archived"}`. Редирект ведёт на страницу организации. 404 на бумаге не бывает. `DELETE` нет.
3. Если мероприятие снято с продажи или сеанс отменён, цепочка падает на уровень выше: сеанс, мероприятие, промоутер, организация.

## 5. Как читать цифры

- `sales_links.scan_count` — сканы, `suspect_scan_count` — похожие на ботов, `last_scanned_at` — последний скан.
- «Подозрительный» скан: User-Agent как у бота или пустой (правило `pagestats`). Такой скан идёт только в `suspect`.
- `sales_link_hits` — по дню и стране (`day`, `country`, `hits`, `suspect_hits`).
- Заказы: `orders.sales_link_id`. В сводке сеанса — блок `sources` (по ссылкам, по `utm`, прямые).

```sql
SELECT day, country, hits, suspect_hits
FROM sales_link_hits
WHERE sales_link_id = '<id ссылки>'
ORDER BY day DESC, country;
```

```sql
SELECT count(*) AS orders, sum(total) AS minor_units
FROM orders
WHERE sales_link_id = '<id ссылки>'
  AND status IN ('paid', 'partially_refunded', 'refunded');
```

- IP и User-Agent нигде не хранятся и не пишутся в лог редиректа (ST-QL1, Р-QL8). Хранятся агрегаты по дню и стране.

## 6. Ограничения

- Цифры — ориентир организатору, а не доказательство для рекламного кабинета.
- Повторный скан одного человека за сутки считается один раз. Память дедупликации живёт в процессе: после рестарта повтор посчитается ещё раз.
- У каждой реплики arena-api своя память дедупликации. При N репликах один человек может попасть в счёт до N раз.
- Лимит по IP тоже в памяти процесса: потолок равен лимиту, умноженному на число реплик.
- Страна есть только при `TRUSTED_PROXY_COUNT` больше 0 и включённом Cloudflare.