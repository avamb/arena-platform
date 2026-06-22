# Platform Management API и права на площадки

Обновлено: 2026-06-21

## Решение

Платформа должна позволять внешним приложениям полностью управлять доступными им ресурсами: через web apps, mobile apps, Telegram bots, partner dashboards и будущие профессиональные интерфейсы. Управление не должно быть возможно только через внутренний operator console.

При этом права должны быть тонкими. Организатор, которому разрешено создавать мероприятия и площадки, может создать новую площадку или использовать существующую. Но он не может изменять чужой seating plan. Если ему нужен другой план зала для той же физической площадки, он создает новый seating plan или делает fork/copy существующего, если это разрешено.

## Главный принцип

```text
Venue = общая физическая сущность: адрес, координаты, город, страна, характеристики.
SeatingPlan = схема размещения/зала, привязанная к Venue, но имеющая владельца, версию и права доступа.
EventSession = использует Venue и конкретную версию SeatingPlan или режим without assigned seats.
```

Площадка может быть общей. Схема зала не обязана быть общей.

## Что было в Bil24 и что меняем

В текущей логике Bil24 новая площадка обычно заводится оператором. Это снижает риск дублей и ошибок, но ограничивает self-service для организаторов и внешних приложений.

В новой архитектуре нужно сохранить контроль качества, но открыть API:

- оператор может создавать и верифицировать любые справочники
- организатор с правом `venue.create` может предложить или создать площадку
- API перед созданием должен искать похожие существующие площадки
- если площадка уже есть, организатор может использовать ее для своего события
- если seating plan уже есть и доступен только read-only, организатор может использовать его без изменений
- если seating plan чужой и нужно изменить схему, создается новая версия/fork под организацию организатора

## Сущности

### Country

Справочник стран.

Поля:

- `id`
- `iso2`
- `iso3`
- `name`
- `localized_names`
- `status`
- `created_by`
- `verified_by`

Создание стран обычно operator-level, но API должен поддерживать создание/предложение страны, если бизнес-роль это разрешает.

### City

Справочник городов.

Поля:

- `id`
- `country_id`
- `region_id` optional
- `name`
- `localized_names`
- `timezone`
- `geo_lat`
- `geo_lon`
- `status`
- `created_by`
- `verified_by`

Город должен иметь timezone. Это важно для событий, продаж, expiration времени резерва, отчетов и webhooks.

### Venue

Каноническая физическая площадка.

Поля:

- `id`
- `country_id`
- `city_id`
- `name`
- `localized_names`
- `address_line`
- `postal_code`
- `geo_lat`
- `geo_lon`
- `timezone` optional override, обычно наследуется от city
- `characteristics`
- `accessibility`
- `contacts`
- `website`
- `media`
- `status`: `draft`, `pending_review`, `verified`, `merged`, `archived`
- `visibility`: `public`, `restricted`, `private`
- `created_by_user_id`
- `created_by_organization_id`
- `verified_by_user_id`

Venue должен быть переиспользуемым. Если несколько организаторов проводят события в одном адресе, они должны ссылаться на один `venue.id`, а не плодить дубли.

### VenueAlias

Нужен для альтернативных названий и локализаций.

Примеры:

- официальное название
- название на русском/иврите/английском
- старое название
- название из импортированной Bil24/TixGear системы

### SeatingPlan

Схема размещения, привязанная к venue.

Поля:

- `id`
- `venue_id`
- `owner_organization_id`
- `name`
- `type`: `assigned_seats`, `general_admission`, `tables`, `mixed`
- `visibility`: `private`, `shared_read`, `public_template`, `operator_verified`
- `status`: `draft`, `active`, `archived`
- `created_by_user_id`
- `source_seating_plan_id` optional, если это fork/copy
- `current_version_id`

Правило: seating plan нельзя считать просто частью venue. У одной площадки может быть несколько seating plans: разные конфигурации зала, разные типы мероприятий, столы вместо рядов, партер без мест, VIP-схемы и т.д.

### SeatingPlanVersion

Версия схемы.

Поля:

- `id`
- `seating_plan_id`
- `version_number`
- `geometry`
- `svg_asset_id` optional
- `sections`
- `rows`
- `seats`
- `standing_zones`
- `tables`
- `capacity`
- `checksum`
- `created_by_user_id`
- `created_at`
- `immutable_after_use`

Критическое правило: версия seating plan, которая уже используется в event/session с продажами или выпущенными билетами, не редактируется in-place. Создается новая версия.

### EventSessionVenueAssignment

Связь сеанса с площадкой и схемой.

Поля:

- `event_session_id`
- `venue_id`
- `seating_plan_version_id` nullable
- `admission_mode`: `assigned_seats`, `general_admission`, `hybrid`
- `capacity_override` optional

## Права и роли

Нужна комбинация RBAC и ABAC:

- RBAC отвечает на вопрос "какое действие роль вообще может делать".
- ABAC отвечает на вопрос "может ли этот пользователь делать это действие именно с этим ресурсом".

### Bil24-style role taxonomy

В master specification нужно явно сохранить понятную бизнес-таксономию ролей, близкую к Bil24/TixGear, но без копирования старой технической модели.

Базовые бизнес-роли:

- `agent` - продает билеты через разрешенные sales channels, agent web/mobile/POS интерфейсы или партнерские каналы. Агент не владеет мероприятием, если это отдельно не задано, но его продажи, комиссии, квоты, отчеты и scan/reconciliation data должны быть атрибутированы.
- `organizer` - владеет или управляет мероприятиями, сеансами, тарифами, квотами, пригласительными, отчетами и внешними распределениями в рамках своей организации.
- `operator` / `platform_operator` - внутренний оператор платформы для модерации, поддержки, верификации площадок, merge/deduplication, контроля справочников, поддержки заказов и операционных исключений.
- `superoperator` / `platform_superadmin` - суперадминистратор платформы с cross-tenant обзором, operations console, logs/errors/load/health, поддержкой всех сущностей и break-glass возможностями при строгом MFA, permission gating и audit.

Важно: Bil24-термин "билетный оператор" для интерфейса "Билетная система" не равен внутреннему `platform_operator`. В новой архитектуре это отдельный тип участника:

- `external_ticketing_operator` - внешняя билетная система/оператор со своим процессингом, клиентской базой, каналом продаж, квотами, barcode batches, external reports and settlement flow.

Один пользователь может иметь несколько ролей через memberships. Например, человек может быть organizer и agent одновременно; в таком случае отчеты и уведомления должны дедуплицироваться по verified identity/organization relationship.

### Примеры ролей

- `platform_superadmin`
- `superoperator`
- `platform_operator`
- `platform_admin`
- `operator`
- `organizer`
- `agent`
- `external_ticketing_operator`
- `organization_owner`
- `organization_admin`
- `event_manager`
- `venue_manager`
- `sales_manager`
- `readonly_reporter`
- `external_app`

### Примеры permissions

```text
geo.country.read
geo.country.create
geo.country.update

geo.city.read
geo.city.create
geo.city.update

venue.read
venue.create
venue.update.own
venue.update.verified
venue.merge
venue.archive

seating_plan.read
seating_plan.create
seating_plan.update.own
seating_plan.fork
seating_plan.share
seating_plan.verify
seating_plan.archive.own

event.create
event.update.own
event_session.create
event_session.assign_venue
event_session.assign_seating_plan

platform.superadmin.read_all
platform.superadmin.write_all
platform.superadmin.manage_roles
platform.superadmin.manage_credentials
platform.superadmin.manage_feature_flags
platform.superadmin.view_logs
platform.superadmin.view_sensitive_logs
platform.superadmin.view_audit
platform.superadmin.impersonate_readonly
platform.superadmin.impersonate_support
platform.superadmin.break_glass

observability.metrics.read
observability.logs.read
observability.errors.read
observability.traces.read
observability.queues.read
observability.alerts.manage
```

### Ключевые authorization rules

1. Организатор с `venue.create` может создать venue draft/pending.
2. Организатор может использовать существующий public/verified venue.
3. Организатор не может изменить verified venue напрямую, если у него нет `venue.update.verified`.
4. Организатор может предложить изменение venue через moderation/change request.
5. Организатор с `seating_plan.create` может создать seating plan для venue.
6. Организатор может редактировать только seating plans, где `owner_organization_id` совпадает с его организацией.
7. Чужой `shared_read` или `public_template` seating plan можно использовать read-only.
8. Чтобы изменить чужой seating plan, нужно сделать fork/copy и получить новый `owner_organization_id`.
9. Operator может merge venues, verify venues, verify public seating plan templates.
10. Сервисный API credential внешнего приложения получает те же ограничения, что и связанный user/organization/sales_channel.
11. `platform_superadmin` может видеть все организации, организаторов, агентов и сущности платформы, но cross-tenant доступ должен попадать в audit log.
12. `platform_superadmin` write/support actions должны быть разделены на отдельные permissions; full read access не должен автоматически означать destructive write access.
13. Support impersonation должен быть временным, аудитируемым и по умолчанию read-only, если не выдано отдельное elevated permission.
14. Доступ к raw logs, sensitive logs, secrets, payment data, personal data и raw barcode values должен быть permission-gated и masked by default.

## Deduplication и создание площадки

Перед созданием venue API должен помочь избежать дублей.

Рекомендуемый flow:

```text
1. External app отправляет черновик venue: страна, город, адрес, название, координаты.
2. API нормализует адрес и координаты.
3. API ищет кандидатов-дублей.
4. Если найдено сильное совпадение, API предлагает использовать существующий venue.
5. Если совпадение слабое, API может создать pending venue или вернуть candidates для выбора.
6. Operator/authorized venue manager может позже verify/merge.
```

Рекомендуемые endpoints:

```text
GET  /v1/geo/countries
POST /v1/geo/countries

GET  /v1/geo/cities?country_id=...
POST /v1/geo/cities

GET  /v1/venues/search?query=...&city_id=...
POST /v1/venues/resolve
POST /v1/venues
GET  /v1/venues/{venue_id}
PATCH /v1/venues/{venue_id}

GET  /v1/venues/{venue_id}/seating-plans
POST /v1/venues/{venue_id}/seating-plans
POST /v1/seating-plans/{seating_plan_id}/fork
GET  /v1/seating-plans/{seating_plan_id}
POST /v1/seating-plans/{seating_plan_id}/versions
```

`POST /v1/venues/resolve` должен быть безопасным методом для внешних приложений: он возвращает `existing_venue`, `candidates` или `can_create`, не создавая дубль без явного подтверждения.

## Seating plan sharing model

Режимы видимости:

- `private`: видит и использует только owner organization.
- `shared_read`: другие разрешенные организации могут использовать, но не редактировать.
- `public_template`: можно использовать как шаблон/fork source.
- `operator_verified`: проверенный оператором план, может быть рекомендован как canonical для venue.

Редактирование:

```text
own draft plan                -> можно редактировать
own active unused plan         -> можно редактировать с audit log
own plan used in sales         -> только новая version
foreign shared/public plan     -> только use или fork
operator_verified foreign plan -> только use или fork, если нет operator permission
```

## Event/session assignment rules

При создании сеанса внешний app может:

- выбрать существующий `venue_id`
- выбрать существующий `seating_plan_version_id`, если он доступен read/use
- создать новый seating plan для этого venue
- сделать fork существующего seating plan и затем изменить свою копию
- выбрать `general_admission`, если мероприятие без закрепленных мест

API должен проверять:

- venue существует и доступен организации
- seating plan принадлежит этому venue
- seating plan version активна
- организация имеет право использовать эту version
- seating plan capacity совместима с ticket inventory
- после начала продаж нельзя незаметно заменить seating plan на несовместимый

## Audit и безопасность

Все изменения справочников и схем должны попадать в audit log:

- кто сделал изменение
- от имени какой организации
- через какое приложение/API credential
- старое и новое значение
- IP/request id
- idempotency key

Особенно важно логировать:

- создание/merge venue
- изменение координат/address
- создание/fork seating plan
- новую seating plan version
- assignment seating plan к event session
- изменение capacity после публикации события
- cross-tenant superadmin view/action
- support impersonation session
- изменение ролей и credentials
- просмотр sensitive logs/raw operational data
- break-glass access

## External apps

Внешнее приложение может быть:

- web admin app
- mobile organizer app
- Telegram bot
- WordPress plugin
- partner dashboard
- automation/no-code connector

Принцип один: приложение не получает сверхправ само по себе. Оно действует в контексте user, organization, sales channel или service account с ограниченными scopes.

## Открытые вопросы

1. Разрешаем ли организаторам создавать country/city напрямую или только отправлять pending request?
2. Нужна ли ручная operator verification для каждой новой venue перед продажами?
3. Какие seating plans можно делать `public_template`: только operator-verified или любые shared organizer plans?
4. Нужен ли marketplace/library схем залов для повторного использования?
5. Должны ли координаты venue проходить geocoding verification автоматически?
6. Какой уровень moderation нужен для изменения verified venue address/name?
