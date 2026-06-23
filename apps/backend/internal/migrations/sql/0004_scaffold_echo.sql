-- +goose Up
-- =====================================================================
-- arena_new — scaffold_echo example table (Wave 7, feature #105)
--
-- Creates the scaffold_echo table used by the POST /v1/scaffold/echo
-- example endpoint. This table demonstrates the full cross-cutting
-- boundary stack (auth → permission → idempotency → audit → outbox)
-- in a single transaction without any real domain logic.
--
-- NOTE: This table (and its endpoint) are scaffolding examples. They
-- will be removed when real domain command tables arrive in later
-- milestones.
-- =====================================================================

CREATE TABLE scaffold_echo (
    id         uuid        PRIMARY KEY DEFAULT uuidv7(),
    actor_id   uuid        NOT NULL,
    message    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE scaffold_echo IS
    'Scaffolding example table for POST /v1/scaffold/echo. '
    'Remove when real domain tables exist.';

-- +goose Down
DROP TABLE IF EXISTS scaffold_echo;
