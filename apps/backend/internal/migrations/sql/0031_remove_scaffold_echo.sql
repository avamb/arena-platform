-- +goose Up
-- =====================================================================
-- arena_new — Remove scaffold_echo table (Wave 20 cleanup, feature #171)
--
-- The scaffold_echo table was a scaffolding example used by POST /v1/scaffold/echo
-- to demonstrate the full cross-cutting boundary stack (auth → permission →
-- idempotency → audit → outbox) before real domain tables existed.
--
-- Now that ticket issuance (Wave 8) and later waves have shipped, the
-- scaffolding example is no longer needed. This migration drops the table.
--
-- The handler (scaffold_echo.go) and route (/v1/scaffold/echo) have been
-- removed from the server as part of the same cleanup (feature #171).
-- =====================================================================

DROP TABLE IF EXISTS scaffold_echo;

-- +goose Down
-- Restore the scaffold_echo table for rollback if needed.
CREATE TABLE IF NOT EXISTS scaffold_echo (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    actor_id   uuid        NOT NULL,
    message    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE scaffold_echo IS
    'Scaffold echo table — restored by down-migration 0031. '
    'Normally absent in production after migration 0031 is applied.';
