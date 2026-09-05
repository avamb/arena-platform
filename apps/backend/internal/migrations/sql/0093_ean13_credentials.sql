-- 0093_ean13_credentials.sql — EAN-13 ticket credentials (feature #502, W1-B6a).
--
-- Spec: 08_architecture/18_bil24_compat_wave1_specification_ru.md §3.4, §11.
--
-- Widens ticket_credentials.type to accept 'ean13' alongside the existing
-- 'static_qr' / 'pdf' types (migration 0027) and adds a shape CHECK that
-- pins the payload to exactly 13 digits (mirrored in Go by
-- internal/platform/barcodes/ean13.Valid, which additionally verifies the
-- checksum — the DB-level CHECK only guards the shape, not the check
-- digit, so a corrupt payload cannot even be inserted with the wrong
-- number of characters).
--
-- barcodes (migration 0029) gets one row per ean13-credentialed ticket:
-- authority = 'platform' (seeded by 0029), external_ref = <the 13-digit
-- code>, ticket_id = the ticket. This makes the EAN-13 visible to
-- SCAN_TICKET / /v1/scanner/* / barcode_batches without introducing a new
-- barcode authority type — 'platform' already covers arena-issued codes.

-- +goose Up

ALTER TABLE ticket_credentials DROP CONSTRAINT ticket_credentials_type_check;
ALTER TABLE ticket_credentials ADD CONSTRAINT ticket_credentials_type_check
    CHECK (type IN ('static_qr', 'pdf', 'ean13'));

ALTER TABLE ticket_credentials ADD CONSTRAINT ticket_credentials_ean13_shape
    CHECK (type <> 'ean13' OR payload ~ '^[0-9]{13}$');

COMMENT ON CONSTRAINT ticket_credentials_ean13_shape ON ticket_credentials IS
    'W1-B6a: ean13 payload must be exactly 13 digits. Checksum validity '
    '(weights 1/3) is verified in Go by internal/platform/barcodes/ean13.Valid '
    'at issuance time; this CHECK only guards the shape so a malformed row '
    'can never be inserted even if the Go-side check is bypassed.';

-- +goose Down

ALTER TABLE ticket_credentials DROP CONSTRAINT IF EXISTS ticket_credentials_ean13_shape;
ALTER TABLE ticket_credentials DROP CONSTRAINT IF EXISTS ticket_credentials_type_check;
ALTER TABLE ticket_credentials ADD CONSTRAINT ticket_credentials_type_check
    CHECK (type IN ('static_qr', 'pdf'));
