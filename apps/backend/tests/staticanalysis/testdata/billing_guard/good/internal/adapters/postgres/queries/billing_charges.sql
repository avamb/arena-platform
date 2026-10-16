-- name: InsertCharge :one
INSERT INTO charges (id, amount_minor, currency)
VALUES ($1, $2, $3)
RETURNING id;

-- name: InsertCreditLedger :exec
INSERT INTO credit_ledger (id, delta_minor, currency) VALUES ($1, $2, $3);
