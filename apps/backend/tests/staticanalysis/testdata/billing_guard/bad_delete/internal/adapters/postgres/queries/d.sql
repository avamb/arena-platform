-- name: PurgeEvents :exec
delete from billable_events where id = $1;
-- DELETE FROM charge_allocations and credit_lots are not append-only.
DELETE FROM charge_allocations WHERE charge_id = $1;
DELETE FROM credit_lots WHERE id = $1;
