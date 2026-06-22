-- +goose Up
-- =====================================================================
-- arena_new — i18n_text baseline seed (Wave 5, feature #21)
--
-- Inserts well-known system message keys for both 'en' and 'ru' so
-- that the platform has at least one seeded translation for every
-- supported locale out of the box.
--
-- Keys are idempotent via ON CONFLICT DO NOTHING so that re-running
-- "arena-migrate up" against an already-seeded database is safe.
--
-- Namespaces:
--   platform.errors — generic API/system error messages
--   platform.health — health check human-readable labels
-- =====================================================================

INSERT INTO i18n_text (namespace, key, locale, value)
VALUES
    -- platform.errors
    ('platform.errors', 'internal_server_error', 'en', 'An unexpected error occurred. Please try again later.'),
    ('platform.errors', 'internal_server_error', 'ru', 'Произошла непредвиденная ошибка. Пожалуйста, повторите попытку позже.'),
    ('platform.errors', 'not_found',              'en', 'The requested resource was not found.'),
    ('platform.errors', 'not_found',              'ru', 'Запрашиваемый ресурс не найден.'),
    ('platform.errors', 'unauthorized',           'en', 'Authentication is required to access this resource.'),
    ('platform.errors', 'unauthorized',           'ru', 'Для доступа к этому ресурсу необходима аутентификация.'),
    ('platform.errors', 'forbidden',              'en', 'You do not have permission to perform this action.'),
    ('platform.errors', 'forbidden',              'ru', 'У вас нет прав для выполнения этого действия.'),
    ('platform.errors', 'bad_request',            'en', 'The request could not be understood due to malformed syntax.'),
    ('platform.errors', 'bad_request',            'ru', 'Запрос не может быть обработан из-за некорректного синтаксиса.'),

    -- platform.health
    ('platform.health', 'status_ok',              'en', 'Service is operating normally.'),
    ('platform.health', 'status_ok',              'ru', 'Сервис работает в штатном режиме.'),
    ('platform.health', 'status_degraded',        'en', 'Service is experiencing degraded performance.'),
    ('platform.health', 'status_degraded',        'ru', 'Сервис работает в ухудшенном режиме.'),
    ('platform.health', 'db_ping_ok',             'en', 'Database connection is healthy.'),
    ('platform.health', 'db_ping_ok',             'ru', 'Соединение с базой данных работает нормально.')
ON CONFLICT (namespace, key, locale) DO NOTHING;

-- +goose Down
-- =====================================================================
-- Remove only the rows inserted by this seed.  Because ON CONFLICT DO
-- NOTHING was used, we delete only the exact rows we know we inserted
-- rather than truncating the whole table.
-- =====================================================================

DELETE FROM i18n_text
WHERE (namespace, key, locale) IN (
    ('platform.errors', 'internal_server_error', 'en'),
    ('platform.errors', 'internal_server_error', 'ru'),
    ('platform.errors', 'not_found',              'en'),
    ('platform.errors', 'not_found',              'ru'),
    ('platform.errors', 'unauthorized',           'en'),
    ('platform.errors', 'unauthorized',           'ru'),
    ('platform.errors', 'forbidden',              'en'),
    ('platform.errors', 'forbidden',              'ru'),
    ('platform.errors', 'bad_request',            'en'),
    ('platform.errors', 'bad_request',            'ru'),
    ('platform.health', 'status_ok',              'en'),
    ('platform.health', 'status_ok',              'ru'),
    ('platform.health', 'status_degraded',        'en'),
    ('platform.health', 'status_degraded',        'ru'),
    ('platform.health', 'db_ping_ok',             'en'),
    ('platform.health', 'db_ping_ok',             'ru')
);
