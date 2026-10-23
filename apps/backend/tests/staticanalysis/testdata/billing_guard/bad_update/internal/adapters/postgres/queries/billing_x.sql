-- name: BumpLedger :exec
DELETE FROM credit_ledger WHERE id = $1;
