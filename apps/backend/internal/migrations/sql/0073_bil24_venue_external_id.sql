-- +goose Up
-- Bil24 venue imports need a durable source identity, independent of a
-- mutable venue name or address. NULL remains the native-Arena default.
ALTER TABLE venues ADD COLUMN external_bil24_id text NULL;

CREATE UNIQUE INDEX venues_external_bil24_id_unique
    ON venues (external_bil24_id)
    WHERE external_bil24_id IS NOT NULL;

COMMENT ON COLUMN venues.external_bil24_id IS
    'Source venue ID from the Bil24 live-import tool. Unique when populated.';

-- +goose Down
DROP INDEX IF EXISTS venues_external_bil24_id_unique;
ALTER TABLE venues DROP COLUMN IF EXISTS external_bil24_id;
