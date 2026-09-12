# Суперадмин на org-scoped поверхностях — паритет прав и байпас членства (W1-S0)

Статус: design authority для мини-волны **W1-S0** (AutoForge, фичи #531–#533). Дата: 2026-09-12.
Найдено при живой проверке стенда (head 72527a3) под реальным суперадмином `andreev@abhteam.com`
сразу после создания организации `lampyris-staging` через админку.

## 1. Симптомы на стенде

1. Вкладка «Channels» новой организации: `GET /v1/organizations/{org}/channels` → **403
   `org.access_denied` «caller is not a member of this organization»**, с заголовком `X-Admin-Reason` и без него.
2. Вкладка «API keys»: `GET …/api-keys` → **403 `permissions.denied`**.
3. `/v1/me` при этом показывает `roles: [platform_superadmin]`, 122 права, `superadmin.read: true`.

## 2. Причины (по коду)

- **Байпас членства завязан на claim `roles` в JWT.** `httpserver/mount_v1.go` `markSuperadminOrgAccess`
  требует `hasRole(actor.Roles, "platform_superadmin")`, а `hauth/login.go` и `/refresh` выдают JWT через
  `auth.IssueJWT(..., nil /*roles*/)` — payload содержит только `sub` и `exp` (проверено на живом токене).
  Значит `auth.WithSuperadminOrgAccess` не выставляется никогда для реального логина, и `requireOrgMembership`
  (`hcatalog/orgauth.go` и все двойники) падает в проверку членства. Это тот же корень, что и запись в
  `AGENTS.md` «org-scoped роли из `user_roles` не доходят до проверок прав». `permissions.DBChecker.Check`
  при пустом `actor.Roles` делает DB-fallback через `GetActiveRolesForUser` — байпас должен использовать тот
  же источник, а не claim.
- **Права, добавленные после миграции 0071, суперадмину не выданы.** 0071 выдала `platform_superadmin` все
  122 права на тот момент; дальше 0092–0098 добавили `order.read`, `order.write`, `customer.read`,
  `customer.import`, `api_key.manage`, `import.bil24_session` и выдали их только `org_admin`/другим ролям.
  На локальной базе (0099): `permissions` = 128, у `platform_superadmin` = 122, разница ровно эти 6.

## 3. Решение

### 3.1 Байпас членства не зависит от JWT (фича #531)

- `markSuperadminOrgAccess`: роли актора разрешать серверно: если `actor.Roles` пуст, брать
  `GetActiveRolesForUser(actor.ID)` (тот же интерфейс `memberships`, что у `DBChecker`, с тем же кэшем 60 с),
  и только потом `hasRole(..., "platform_superadmin")` и `perms.Check("superadmin.read")`. Никакого
  доверия caller-supplied claim (правило из комментария к `WithSuperadminOrgAccess` сохраняется).
- Сервисные акторы (API-ключ) — без изменений (у них ролей нет и байпаса быть не должно).
- `X-Admin-Reason` остаётся обязательным для байпаса: без него — 400 `superadmin.missing_reason` (SPA
  умеет повторить запрос с причиной, `lib/api/client.ts` «missing-reason retry»).
- Тесты: юнит на middleware с актором без ролей в claims, но с `platform_superadmin` из DB-источника;
  интеграционный (`//go:build integration`): реальный `POST /v1/auth/login` суперадмина → `GET
  /v1/organizations/{чужая org}/channels` с `X-Admin-Reason` → 200, без заголовка → 400
  `superadmin.missing_reason`; обычный пользователь-не-член → 403 как раньше; запись
  `superadmin.organization_access` в аудите.
- **Не делать** в этой фиче: класть роли в JWT (отдельное решение, см. §5).

### 3.2 Паритет прав суперадмина (фича #532)

- Миграция `0100_superadmin_permission_parity.sql`: идемпотентно выдать `platform_superadmin` **все**
  строки `permissions` (`INSERT … SELECT … ON CONFLICT DO NOTHING`), Down — удалить только шесть
  перечисленных выше. Пин головы → 0100.
- Гардрейл: интеграционный тест после миграций — `count(permissions) == count(role_permissions
  платформенного суперадмина)`; плюс статический тест по embedded-FS миграций: каждая миграция, которая
  делает `INSERT INTO permissions`, либо сама выдаёт их `platform_superadmin`, либо её номер ≤ 0100
  (покрыта паритетной миграцией). Запись в `AGENTS.md`: «новая permission → выдать platform_superadmin в
  той же миграции, иначе `TestSuperadminPermissionParity` красный».
- `/v1/me` суперадмина после раскатки: 128 прав, вкладки «API keys», «Customers», «Orders» доступны.

### 3.3 Проверка (фича #533)

Сквозной интеграционный тест на полном httpserver: логин суперадмина → создать организацию → в ней
`POST …/channels` → `PUT …/channels/{id}/gateway-credential` → `PUT …/channels/{id}/wp-webhook` (wpstub)
→ `POST …/api-keys` со скоупом `import.bil24_session` → этим ключом `POST …/imports/event-bundle`
(фикстура `arena_ga_lampyris.json`) → `GET_ALL_ACTIONS` с fid канала видит мероприятие. Всё с
`X-Admin-Reason`, без единой строки `memberships` для суперадмина. Полный холодный гейт.

## 4. Не в этой волне

- Роли в JWT и org-scoped `user_roles` для обычных пользователей (организаторов) — отдельная волна
  «организаторская админка»; текущая волна чинит только суперадмина.
- SPA: если после фикса какая-то страница всё ещё не шлёт `X-Admin-Reason` и не умеет retry — отдельная
  правка admin-web, фиксируется по результатам живой проверки.

## 5. Разбиение

| # | Код | Суть | Сложность |
|---|---|---|---|
| 531 | W1-S0a | `markSuperadminOrgAccess` разрешает роли серверно; юнит + интеграция через реальный логин | 2 |
| 532 | W1-S0b | Миграция 0100 паритета прав + пин головы + гардрейлы + AGENTS.md | 2 |
| 533 | W1-S0 [EPIC-VERIFY] | Сквозной тест провижининга под суперадмином + полный холодный гейт + прогресс-нота | 2 |

Зависимости: 533 → 531, 532.

## 6. Дополнение 2026-09-12 после редеплоя (фича #534, admin-web)

Бэкенд с #531/#532 на стенде отвечает 200 на `GET …/channels` и `…/api-keys` при заголовке
`X-Admin-Reason`. Но SPA (`apps/admin-web/src/lib/api/reason.ts`) прикладывает заголовок на
`/v1/organizations/{id}/channels|venues|payment-configs|members|bank-accounts` **только для мутаций**
(`REASON_REQUIRED_MUTATION_REGEX`), поэтому вкладки организации (Channels, Venues, Payments, Users) и
страница `/channels?org=…` показывают «superadmin.missing_reason … Retry», а кнопка Retry повторяет запрос
снова без заголовка. До #531 байпас никогда не срабатывал (403), и пробел был не виден.

Решение (#534):
1. `lib/api/client.ts`: при ответе 400 `superadmin.missing_reason` — **один автоматический повтор** с
   сохранённой причиной (`arena.admin.adminReason` из sessionStorage) для любого пути и метода; без
   сохранённой причины — существующий промпт.
2. `reason.ts`: перевести org-scoped регэкспы из «только мутации» в «все методы» для
   `channels|venues|payment-configs|members|bank-accounts|events|sessions|customers|orders|api-keys|
   imports` под `/v1/organizations/{id}/…` — заголовок на чтение безвреден для члена организации и
   обязателен для суперадмина.
3. Кнопка Retry в состояниях ошибки передаёт `adminReason` явно.
4. Тесты: `reason.test.ts` (GET на org-scoped путь требует причину), тест клиента на авто-повтор
   (первый ответ 400 missing_reason → второй запрос с заголовком → 200), vitest без регрессий,
   `npm run type-check`.
