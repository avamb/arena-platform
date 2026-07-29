-- +goose Up
-- Human-readable, per-entity presentation identifiers (AB-22 / feature #397).
-- UUIDs remain authoritative primary keys; these values are presentation only.
ALTER TABLE organizations ADD COLUMN display_number bigint;
ALTER TABLE venues ADD COLUMN display_number bigint;
ALTER TABLE events ADD COLUMN display_number bigint;
ALTER TABLE sales_channels ADD COLUMN display_number bigint;
ALTER TABLE users ADD COLUMN display_number bigint;

WITH numbered AS (SELECT id, row_number() OVER (ORDER BY created_at, id) AS n FROM organizations) UPDATE organizations t SET display_number = numbered.n FROM numbered WHERE t.id = numbered.id;
WITH numbered AS (SELECT id, row_number() OVER (ORDER BY created_at, id) AS n FROM venues) UPDATE venues t SET display_number = numbered.n FROM numbered WHERE t.id = numbered.id;
WITH numbered AS (SELECT id, row_number() OVER (ORDER BY created_at, id) AS n FROM events) UPDATE events t SET display_number = numbered.n FROM numbered WHERE t.id = numbered.id;
WITH numbered AS (SELECT id, row_number() OVER (ORDER BY created_at, id) AS n FROM sales_channels) UPDATE sales_channels t SET display_number = numbered.n FROM numbered WHERE t.id = numbered.id;
WITH numbered AS (SELECT id, row_number() OVER (ORDER BY created_at, id) AS n FROM users) UPDATE users t SET display_number = numbered.n FROM numbered WHERE t.id = numbered.id;

CREATE SEQUENCE organizations_display_number_seq OWNED BY organizations.display_number;
CREATE SEQUENCE venues_display_number_seq OWNED BY venues.display_number;
CREATE SEQUENCE events_display_number_seq OWNED BY events.display_number;
CREATE SEQUENCE sales_channels_display_number_seq OWNED BY sales_channels.display_number;
CREATE SEQUENCE users_display_number_seq OWNED BY users.display_number;
SELECT setval('organizations_display_number_seq', COALESCE((SELECT MAX(display_number) FROM organizations), 0) + 1, false);
SELECT setval('venues_display_number_seq', COALESCE((SELECT MAX(display_number) FROM venues), 0) + 1, false);
SELECT setval('events_display_number_seq', COALESCE((SELECT MAX(display_number) FROM events), 0) + 1, false);
SELECT setval('sales_channels_display_number_seq', COALESCE((SELECT MAX(display_number) FROM sales_channels), 0) + 1, false);
SELECT setval('users_display_number_seq', COALESCE((SELECT MAX(display_number) FROM users), 0) + 1, false);
ALTER TABLE organizations ALTER COLUMN display_number SET DEFAULT nextval('organizations_display_number_seq'), ALTER COLUMN display_number SET NOT NULL;
ALTER TABLE venues ALTER COLUMN display_number SET DEFAULT nextval('venues_display_number_seq'), ALTER COLUMN display_number SET NOT NULL;
ALTER TABLE events ALTER COLUMN display_number SET DEFAULT nextval('events_display_number_seq'), ALTER COLUMN display_number SET NOT NULL;
ALTER TABLE sales_channels ALTER COLUMN display_number SET DEFAULT nextval('sales_channels_display_number_seq'), ALTER COLUMN display_number SET NOT NULL;
ALTER TABLE users ALTER COLUMN display_number SET DEFAULT nextval('users_display_number_seq'), ALTER COLUMN display_number SET NOT NULL;
ALTER TABLE organizations ADD CONSTRAINT organizations_display_number_key UNIQUE (display_number);
ALTER TABLE venues ADD CONSTRAINT venues_display_number_key UNIQUE (display_number);
ALTER TABLE events ADD CONSTRAINT events_display_number_key UNIQUE (display_number);
ALTER TABLE sales_channels ADD CONSTRAINT sales_channels_display_number_key UNIQUE (display_number);
ALTER TABLE users ADD CONSTRAINT users_display_number_key UNIQUE (display_number);

-- +goose Down
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_display_number_key;
ALTER TABLE sales_channels DROP CONSTRAINT IF EXISTS sales_channels_display_number_key;
ALTER TABLE events DROP CONSTRAINT IF EXISTS events_display_number_key;
ALTER TABLE venues DROP CONSTRAINT IF EXISTS venues_display_number_key;
ALTER TABLE organizations DROP CONSTRAINT IF EXISTS organizations_display_number_key;
ALTER TABLE users DROP COLUMN IF EXISTS display_number;
ALTER TABLE sales_channels DROP COLUMN IF EXISTS display_number;
ALTER TABLE events DROP COLUMN IF EXISTS display_number;
ALTER TABLE venues DROP COLUMN IF EXISTS display_number;
ALTER TABLE organizations DROP COLUMN IF EXISTS display_number;
