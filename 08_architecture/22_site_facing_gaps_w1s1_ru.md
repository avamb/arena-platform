# Пробелы, найденные при живом тесте стенда с сайтом — спецификация W1-S1

Статус: design authority для мини-волны **W1-S1** (AutoForge, фичи #535–#538). Дата: 2026-09-12.
Источник: живой сквозной тест на стенде c8fa60f (org `lampyris-staging`, канал fid=2, бандл
`arena_ga_lampyris`, `GET_ALL_ACTIONS`, лог воркера) — см. §1.

## 1. Что увидели

1. **Афиша недоступна сайту.** `GET_ALL_ACTIONS` отдаёт `bigPosterUrl: "/v1/media-files/{uuid}"` —
   относительный путь без подписи. `hmedia.DownloadMedia` требует `?expires=&sig=` (`hmedia/media.go:275-306`),
   подписанный URL строит `mediastore.Repo.localSignedURL` (`mediastore.go:288-297`). `posterURL()`
   (`hbil24/cmd_catalog.go:367-379`) и `hfeed/public_feed.go:80-82` не используют ни базу, ни подпись.
   Сайт (`bil24-acf-sync` качает афишу по URL) получит 401.
2. **Публичная база — хост админки.** `APP_PUBLIC_URL` (= origin SPA, `https://app.arenasoldout.com`)
   используется и как `PUBLIC_BASE_URL` шлюза: `pdfUrl/downloadUrl` в `GET_TICKETS_BY_ORDER`
   (`cmd_tickets_by_order.go:209-212`) и `base_url/image_url` в ответе `PUT gateway-credential`
   (`hcatalog/gateway_credential.go:287-292`). На стенде API живёт на `api.arenasoldout.com`, ссылки на
   PDF билетов через app-хост не откроются; `base_url` без `/compat/bil24` вводит оператора в заблуждение.
3. **Бандл не публикует событие в канал.** `applyPublish` только меняет `events.status`; outbox
   `v1.event.published` ушёл в 14:56 без подписчиков (лог воркера), потому что
   `ListWPSubscribersForEvent` (`gen/wp_webhook_subscribers.sql.go:54-64`) требует цепочку
   `event_publications → agent_feed_tokens(active) → webhook_subscribers(kind=bil24_wp, active)` по
   `channel_id`. Ручная публикация в канал через админку новых outbox-событий не породила; только PATCH
   события дал `v1.event.updated` → доставка на сайт (2 с). Сайт, создавший мероприятие, никогда не
   получит `event.created`.
4. **Город в нижнем регистре.** `resolveGeography` (`himports/import_exec.go:246-283`) создаёт город
   только со `slug` (`slugify("Praha")` → `praha`), а `ListActionVenuesByOrg` отдаёт
   `COALESCE(i18n_loc, i18n_en, ci.slug)` (`gen/venues.sql.go:279`) — перевода нет, уходит slug.
5. Сервисный актор не знает свой канал: `apikeys.APIKey.ChannelID` есть (`apikeys.go:111`), но
   `authenticateAPIKey` (`server_apikey_auth.go:133-139`) не копирует его в `auth.Actor`.

## 2. Решения

### 2.1 Публичная база API и подписанные ссылки (фича #535)

- Новая переменная `API_PUBLIC_URL` (`config.Config.APIPublicURL`): публичный origin API
  (`https://api.arenasoldout.com`). Пустая → падение назад на `APP_PUBLIC_URL` (совместимость с
  однохостовыми стендами). В production при `BIL24_COMPAT_ENABLED=true` — обязательна, `https://`.
  `.env.example`, `deploy/DOKPLOY.md` (таблица переменных, отдельная строка), `docs/ops/bil24_gateway.md` §1.
- `PublicBaseURL` шлюза (`bil24_tickets_shim.go`, `hcatalog/handler.go:137`) = `API_PUBLIC_URL`.
  `PUT gateway-credential` отвечает `base_url = <API_PUBLIC_URL>/compat/bil24`,
  `image_url = <API_PUBLIC_URL>/compat/bil24/image` (как в runbook §1, а не голый origin).
- Афиши: `posterURL()` строит **абсолютный подписанный** URL через `mediastore` (тот же механизм, что
  `localSignedURL`, TTL 24 ч — синк сайта ходит каждые 15 минут и всегда получит живую ссылку) на базе
  `API_PUBLIC_URL`. Для `MEDIA_BACKEND` с внешним хранилищем — его подписанный URL как есть. Тот же
  хелпер — в `hfeed/public_feed.go` `mediaFileURL`. Легаси `events.image_url` — как есть.
- Тесты: юнит `posterURL` (абсолютность, `expires`/`sig`, база); интеграция: `GET_ALL_ACTIONS` →
  `bigPosterUrl` → `GET` по нему через тестовый httpserver → 200 и байты афиши; `gateway-credential` →
  `base_url` оканчивается на `/compat/bil24`; `config` тест на fallback и production-валидацию.

### 2.2 Авто-публикация в канал API-ключа (фича #536)

- `auth.Actor` получает `ChannelID *uuid.UUID` (только сервисные акторы); `authenticateAPIKey` копирует
  `key.ChannelID`.
- В импорте (`bil24-session` и `event-bundle`, оба `source`): если `publish:true` **и** актор — сервисный
  с `ChannelID`, то **внутри той же транзакции** до коммита: (а) взять активный неотозванный
  `agent_feed_tokens` этого канала, если нет — создать с label `auto:event-bundle`; (б) `PublishEvent`
  (`event_publications`, `ON CONFLICT DO NOTHING` семантика уже есть) с `city_id` города площадки сеанса.
  Идемпотентно при повторном пакете. Если ключ без канала — ничего, предупреждение
  `import.channel_publication_skipped`.
- Ответ импорта: `publication: {channel_id, feed_token_id, publication_id} | null`.
- Порядок гарантирует, что к моменту диспетча `v1.event.published` подписчик уже виден.
- Тесты: интеграционный на полном httpserver: канал + wp-webhook (wpstub) + ключ с `channel_id` →
  бандл `publish:true` → реальный outbox-диспетчер → wpstub получил `event.created` с
  `actionEventId` из `compat_ids` **без ручной публикации**; ключ без `channel_id` → предупреждение и
  нет строки `event_publications`; повтор бандла не создаёт второй feed-token/публикацию.
- `docs/ops/bil24_gateway.md` §8: абзац «ключ должен быть привязан к каналу сайта, иначе вебхуки не идут».

### 2.3 Имя города при импорте (фича #537)

- `resolveGeography`: при создании города писать перевод имени в `i18n_text` для локали организации
  (`organizations.default_locale`) **и** для `en` (если отличается), значение — исходный `cityName`
  с нормализацией пробелов, без изменения регистра. Существующий город без перевода — дописать перевод
  (не трогать, если уже есть). Использовать существующие запросы `geo` (тот же путь, что `POST
  /v1/geo/cities` в `hgeo`), не изобретать новые таблицы.
- Тесты: бандл с `cityName: "Praha"` → `GET_ALL_ACTIONS.cityList[].cityName == "Praha"`; повтор не
  дублирует переводы; город из Bil24-импорта (`source=bil24`) — то же поведение.

### 2.4 Проверка (фича #538)

Сквозной интеграционный тест (расширить `TestSuperadminOrgProvisioning533_Integration` или рядом):
провижининг → бандл `publish:true` под ключом с каналом → wpstub получил `event.created` без ручной
публикации → `GET_ALL_ACTIONS`: `cityName` в исходном регистре, `bigPosterUrl` абсолютный и `GET` по нему
200 → `GET_TICKETS_BY_ORDER`-подобный `pdfUrl` начинается с `API_PUBLIC_URL`. Полный холодный гейт,
прогресс-нота с командами и кодами выхода.

## 3. Раскатка стенда после волны

В compose добавить `API_PUBLIC_URL: "https://api.arenasoldout.com"`. Для уже созданной организации
`lampyris-staging` ничего пересоздавать не нужно; тестовое мероприw1 можно перевыпустить повторным
бандлом (`externalRef` тот же) — публикация в канал появится автоматически.

## 4. Разбиение

| # | Код | Суть | Сложность |
|---|---|---|---|
| 535 | W1-S1a | `API_PUBLIC_URL`, подписанные абсолютные афиши, `base_url/image_url`, `pdfUrl` | 2 |
| 536 | W1-S1b | `Actor.ChannelID`; авто-публикация в канал ключа из импорта; ответ `publication`; интеграция через outbox → wpstub | 3 |
| 537 | W1-S1c | Переводы имени города при создании в импорте | 2 |
| 538 | W1-S1 [EPIC-VERIFY] | Сквозной тест + полный холодный гейт + прогресс-нота | 2 |

Зависимости: 538 → 535, 536, 537.
