-- 0218_barcode_authority_type_unique.sql — at most one barcode authority per
-- type.
--
-- Every reader resolves an authority by its type (GetBarcodeAuthorityByType),
-- so a type names exactly one row. POST /v1/barcodes/authorities answers 409
-- barcode_authority.type_exists for a type that already has a row.
--
-- No permission is seeded, so the superadmin parity guard has nothing to
-- check.

-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE UNIQUE INDEX barcode_authorities_type_unique ON barcode_authorities (type);

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS barcode_authorities_type_unique;
