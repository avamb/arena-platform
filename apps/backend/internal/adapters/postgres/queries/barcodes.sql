-- barcodes.sql — sqlc source queries for barcode authority federation (feature #142).
--
-- This file is the sqlc input; the generated output lives in
-- ../gen/barcodes.sql.go. Regenerate with: make sqlc-generate.

-- name: InsertBarcodeAuthority :one
INSERT INTO barcode_authorities (type, label)
VALUES ($1, $2)
RETURNING id, type, label, created_at;

-- name: GetBarcodeAuthorityByID :one
SELECT id, type, label, created_at
FROM   barcode_authorities
WHERE  id = $1;

-- name: GetBarcodeAuthorityByType :one
SELECT id, type, label, created_at
FROM   barcode_authorities
WHERE  type = $1
LIMIT  1;

-- name: ListBarcodeAuthorities :many
SELECT id, type, label, created_at
FROM   barcode_authorities
ORDER BY created_at ASC;

-- name: InsertBarcode :one
INSERT INTO barcodes (authority_id, external_ref, ticket_id)
VALUES ($1, $2, $3)
RETURNING id, authority_id, external_ref, ticket_id, status, scanned_at, created_at, updated_at;

-- name: GetBarcodeByRef :one
-- Lookup a barcode by its authority + external reference. Used by the scan flow
-- to resolve the barcode record before updating its status.
SELECT id, authority_id, external_ref, ticket_id, status, scanned_at, created_at, updated_at
FROM   barcodes
WHERE  authority_id = $1
  AND  external_ref = $2;

-- name: GetBarcodeByID :one
SELECT id, authority_id, external_ref, ticket_id, status, scanned_at, created_at, updated_at
FROM   barcodes
WHERE  id = $1;

-- name: MarkBarcodeScanned :one
-- Atomically transitions an 'active' barcode to 'scanned'. Returns the updated
-- row. Returns pgx.ErrNoRows when the barcode is already scanned or revoked
-- (status != 'active'), enabling the caller to detect double-scan without a
-- separate read-then-write.
UPDATE barcodes
SET    status     = 'scanned',
       scanned_at = now(),
       updated_at = now()
WHERE  id     = $1
  AND  status = 'active'
RETURNING id, authority_id, external_ref, ticket_id, status, scanned_at, created_at, updated_at;

-- name: RevokeBarcode :one
UPDATE barcodes
SET    status     = 'revoked',
       updated_at = now()
WHERE  id = $1
RETURNING id, authority_id, external_ref, ticket_id, status, scanned_at, created_at, updated_at;

-- name: ListBarcodesByTicketID :many
SELECT id, authority_id, external_ref, ticket_id, status, scanned_at, created_at, updated_at
FROM   barcodes
WHERE  ticket_id = $1
ORDER BY created_at ASC;
