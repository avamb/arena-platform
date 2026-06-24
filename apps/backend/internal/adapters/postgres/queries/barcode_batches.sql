-- barcode_batches.sql — sqlc query source for barcode batch import (feature #146).

-- name: InsertBarcodeBatch :one
INSERT INTO barcode_batches (allocation_id, source, status, filename, row_count, authority_id, notes, uploaded_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id, allocation_id, source, status, filename, row_count, authority_id, notes, uploaded_by, created_at, updated_at;

-- name: GetBarcodeBatchByID :one
SELECT id, allocation_id, source, status, filename, row_count, authority_id, notes, uploaded_by, created_at, updated_at
FROM   barcode_batches
WHERE  id = $1;

-- name: ListBarcodeBatchesByAllocation :many
SELECT id, allocation_id, source, status, filename, row_count, authority_id, notes, uploaded_by, created_at, updated_at
FROM   barcode_batches
WHERE  allocation_id = $1
ORDER BY created_at DESC;

-- name: ListAllBarcodeBatches :many
SELECT id, allocation_id, source, status, filename, row_count, authority_id, notes, uploaded_by, created_at, updated_at
FROM   barcode_batches
ORDER BY created_at DESC;

-- name: UpdateBarcodeBatchStatus :one
UPDATE barcode_batches
SET    status     = $2,
       updated_at = now()
WHERE  id = $1
RETURNING id, allocation_id, source, status, filename, row_count, authority_id, notes, uploaded_by, created_at, updated_at;

-- name: UpdateBarcodeBatchAuthorityAndStatus :one
UPDATE barcode_batches
SET    authority_id = $2,
       status       = $3,
       updated_at   = now()
WHERE  id = $1
RETURNING id, allocation_id, source, status, filename, row_count, authority_id, notes, uploaded_by, created_at, updated_at;

-- name: InsertBarcodeBatchEntry :one
INSERT INTO barcode_batch_entries (batch_id, external_ref, status)
VALUES ($1, $2, $3)
RETURNING id, batch_id, external_ref, status, created_at;

-- name: ListBatchEntriesByBatchID :many
SELECT id, batch_id, external_ref, status, created_at
FROM   barcode_batch_entries
WHERE  batch_id = $1
ORDER BY created_at ASC;

-- name: UpdateBatchEntriesStatus :execrows
UPDATE barcode_batch_entries
SET    status = $2
WHERE  batch_id = $1;
