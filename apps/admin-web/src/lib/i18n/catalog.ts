/**
 * SuperAdmin UI i18n catalog (Feature #251).
 *
 * Catalog of human-readable strings used across SuperAdmin UI shell,
 * navigation, and common UX surfaces. Keys are flat dotted strings;
 * resolution is done by `t(key)` from useTranslation().
 *
 * Locale policy (per spec / Feature #251):
 *
 *   - Default locale: ru (Russian) — operators are Russian-speaking.
 *   - Switchable to: en (English).
 *   - Entity names (Organization, Venue, Network Operator, Sales Channel,
 *     Payment Provider, User, Role) and technical identifiers (slug, code,
 *     enum values, API field names, role names like organizer/agent/
 *     platform_superadmin) stay English in both locales — only
 *     descriptive / instructional text is translated.
 *
 * Catalogs are inlined here (rather than separate JSON files) to keep
 * the bundle synchronous, avoid a runtime fetch for locale boot, and
 * keep TypeScript narrowing of keys. Real locale JSON files
 * (locales/{ru,en}/superadmin.json) can be exported from these objects
 * for translator handoff later if needed.
 */

export type LocaleCode = "ru" | "en";

export const SUPPORTED_LOCALES: readonly LocaleCode[] = ["ru", "en"];

export const DEFAULT_LOCALE: LocaleCode = "ru";

export const LOCALE_LABELS: Readonly<Record<LocaleCode, string>> = {
  ru: "Русский",
  en: "English",
};

/** Catalog shape — flat key/string map per locale. */
export type Catalog = Readonly<Record<string, string>>;

/** Russian catalog (default for SuperAdmin UI). */
export const ruCatalog: Catalog = {
  // Brand / shell
  "shell.brand": "Arena Admin",
  "shell.signIn": "Войти",
  "shell.signOut": "Выйти",
  "shell.noRoles": "(нет ролей)",
  "shell.devSuffix": "разработка",
  "shell.nav.aria": "Основная навигация",

  // Navigation labels (entity names stay English; labels here are explicit
  // human-readable descriptions paired with the English noun in parens).
  "nav.workspace": "Рабочее место",
  "nav.networks": "Сети операторов (Operator Networks)",
  "nav.organizations": "Организации (Organizations)",
  "nav.events_sessions": "События и сеансы (Events and Sessions)",
  "nav.venues": "Площадки (Venues)",
  "nav.orders": "Заказы (Orders)",
  "nav.tickets": "Билеты (Tickets)",
  "nav.refunds": "Возвраты (Refunds)",
  "nav.channels": "Каналы продаж (Sales Channels)",
  "nav.payments": "Платежные провайдеры (Payment Configs)",
  "nav.reports": "Отчёты (Reports)",
  "nav.notifications_content": "Уведомления и контент",
  "nav.pos": "Касса (POS Mode)",
  "nav.audit": "Журнал аудита (Audit Log)",
  "nav.observability": "Наблюдаемость (Observability)",
  "nav.geo": "Геосправочник (Geo)",

  // Empty/scope states
  "shell.scopeNone":
    "Активный контекст не выбран — разделы, требующие контекста, скрыты.",
  "shell.scopeActive": "Активный контекст: {label}",
  "shell.nav.empty":
    "Для текущих прав и контекста нет доступных разделов. Запросите у платформенного администратора расширение прав или переключите контекст.",

  // Locale switcher
  "locale.label": "Язык интерфейса",
  "locale.switch.aria":
    "Переключатель языка интерфейса администратора. По умолчанию — русский.",
  "locale.option.ru": "Русский",
  "locale.option.en": "Английский (English)",

  // Common actions (English action names retained where they are
  // domain-stable, with Russian helper text where useful).
  "action.save": "Сохранить",
  "action.cancel": "Отмена",
  "action.close": "Закрыть",
  "action.delete": "Удалить",
  "action.archive": "Архивировать (Archive)",
  "action.edit": "Редактировать",
  "action.add": "Добавить",
  "action.invite": "Пригласить (Invite)",
  "action.assignRole": "Назначить роль (Assign Role)",
  "action.refresh": "Обновить",

  // Empty states (entity names left English in quotes per spec).
  "empty.venues":
    "Площадки ещё не добавлены. Нажмите «Add Venue», чтобы создать первую запись \"Venue\".",
  "empty.organizations":
    "Организации ещё не созданы. Нажмите «Create Organization», чтобы добавить первую запись \"Organization\".",
  "empty.channels":
    "Каналы продаж ещё не настроены. Нажмите «Add Channel», чтобы создать первую запись \"Sales Channel\".",
  "empty.payments":
    "Конфигурации платежных провайдеров ещё не созданы. Нажмите «Add Payment Config», чтобы добавить первую запись \"Payment Provider Config\".",
  "empty.members":
    "В этой организации ещё нет участников. Нажмите «Invite», чтобы добавить пользователя \"User\" с нужной ролью.",
  "empty.networks":
    "Сети операторов ещё не созданы. Нажмите «Create Network», чтобы добавить первую запись \"Operator Network\".",

  // Validation / errors
  "error.required": "Поле обязательно для заполнения.",
  "error.invalid": "Значение некорректно.",
  "error.network": "Сетевая ошибка. Попробуйте повторить запрос.",
  "error.permissionDenied":
    "Недостаточно прав для этой операции. Обратитесь к платформенному администратору.",
  "error.reasonRequired":
    "Для действия в чужом тенанте требуется указать причину (X-Admin-Reason).",
  "error.unknown": "Произошла неизвестная ошибка.",

  // Reason / cross-tenant
  "reason.title": "Укажите причину действия",
  "reason.help":
    "Действие затрагивает чужой тенант. Причина будет записана в журнал аудита и привязана к каждому запросу до конца сессии.",
  "reason.placeholder": "Например: запрос поддержки клиента CRM-12345",

  // Loading
  "loading.generic": "Загрузка…",
  "loading.list": "Загрузка списка…",

  // Forbidden
  "forbidden.title": "Доступ запрещён",
  "forbidden.body":
    "У вас нет необходимых прав для просмотра этого раздела. Если это ошибка, обратитесь к платформенному администратору.",
};

/** English catalog (fallback / switchable). */
export const enCatalog: Catalog = {
  // Brand / shell
  "shell.brand": "Arena Admin",
  "shell.signIn": "Sign in",
  "shell.signOut": "Sign out",
  "shell.noRoles": "(no roles)",
  "shell.devSuffix": "dev",
  "shell.nav.aria": "Primary navigation",

  // Navigation labels
  "nav.workspace": "Workspace",
  "nav.networks": "Operator Networks",
  "nav.organizations": "Organizations",
  "nav.events_sessions": "Events and Sessions",
  "nav.venues": "Venues",
  "nav.orders": "Orders",
  "nav.tickets": "Tickets",
  "nav.refunds": "Refunds",
  "nav.channels": "Sales Channels",
  "nav.payments": "Payment Configs",
  "nav.reports": "Reports",
  "nav.notifications_content": "Notifications and Content",
  "nav.pos": "POS Mode",
  "nav.audit": "Audit Log",
  "nav.observability": "Observability",
  "nav.geo": "Geo Registry",

  // Empty/scope states
  "shell.scopeNone":
    "No scope active — surfaces requiring a scope are hidden.",
  "shell.scopeActive": "Active scope: {label}",
  "shell.nav.empty":
    "No surfaces available for your current permissions and scope. Ask a platform administrator to grant access, or switch to a different scope.",

  // Locale switcher
  "locale.label": "UI language",
  "locale.switch.aria":
    "Admin UI language switcher. Default is Russian.",
  "locale.option.ru": "Russian (Русский)",
  "locale.option.en": "English",

  // Common actions
  "action.save": "Save",
  "action.cancel": "Cancel",
  "action.close": "Close",
  "action.delete": "Delete",
  "action.archive": "Archive",
  "action.edit": "Edit",
  "action.add": "Add",
  "action.invite": "Invite",
  "action.assignRole": "Assign Role",
  "action.refresh": "Refresh",

  // Empty states
  "empty.venues":
    "No venues yet. Press \"Add Venue\" to create the first \"Venue\" record.",
  "empty.organizations":
    "No organizations yet. Press \"Create Organization\" to add the first \"Organization\" record.",
  "empty.channels":
    "No sales channels yet. Press \"Add Channel\" to create the first \"Sales Channel\" record.",
  "empty.payments":
    "No payment provider configs yet. Press \"Add Payment Config\" to add the first \"Payment Provider Config\" record.",
  "empty.members":
    "No members in this organization yet. Press \"Invite\" to add a \"User\" with a role.",
  "empty.networks":
    "No operator networks yet. Press \"Create Network\" to add the first \"Operator Network\" record.",

  // Validation / errors
  "error.required": "This field is required.",
  "error.invalid": "Invalid value.",
  "error.network": "Network error. Please retry.",
  "error.permissionDenied":
    "You do not have permission for this operation. Contact a platform administrator.",
  "error.reasonRequired":
    "Acting on another tenant requires an audit reason (X-Admin-Reason).",
  "error.unknown": "An unknown error occurred.",

  // Reason / cross-tenant
  "reason.title": "Provide a reason",
  "reason.help":
    "This action targets another tenant. The reason is recorded in the audit log and applied to every request until the session ends.",
  "reason.placeholder": "e.g.: customer support ticket CRM-12345",

  // Loading
  "loading.generic": "Loading…",
  "loading.list": "Loading list…",

  // Forbidden
  "forbidden.title": "Access denied",
  "forbidden.body":
    "You do not have permission to view this section. If this is unexpected, contact a platform administrator.",
};

export const CATALOGS: Readonly<Record<LocaleCode, Catalog>> = {
  ru: ruCatalog,
  en: enCatalog,
};

/**
 * Resolve a key against a catalog with simple {param} interpolation.
 *
 * - Falls back to the English catalog if the key is missing from the
 *   active locale (covers translator gaps).
 * - Falls back to the raw key string if missing from both — visible in
 *   the UI so missing translations are caught in QA rather than silently
 *   rendering empty.
 * - Interpolation: `{name}` placeholders are replaced from `params`.
 *   Unmatched placeholders are left as-is.
 */
export function translate(
  locale: LocaleCode,
  key: string,
  params?: Readonly<Record<string, string | number>>,
): string {
  const cat = CATALOGS[locale] ?? CATALOGS[DEFAULT_LOCALE];
  let raw = cat[key];
  if (raw === undefined) {
    raw = CATALOGS.en[key];
  }
  if (raw === undefined) {
    return key;
  }
  if (params === undefined) {
    return raw;
  }
  return raw.replace(/\{(\w+)\}/g, (match, name: string) => {
    const value = params[name];
    return value === undefined ? match : String(value);
  });
}

/** Type guard: is `value` a supported locale code? */
export function isLocaleCode(value: unknown): value is LocaleCode {
  return (
    typeof value === "string" &&
    (SUPPORTED_LOCALES as readonly string[]).includes(value)
  );
}
