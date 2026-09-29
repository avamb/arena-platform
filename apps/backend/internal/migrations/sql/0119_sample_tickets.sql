-- 0119_sample_tickets.sql — a sample e-ticket per session, with a real code.
--
-- After the Telegram event-center bot publishes an event, the organizer gets
-- a PDF of "their" ticket right in the chat (owner decision 2026-09-29): the
-- exact layout a buyer will receive, printed with SAMPLE across it, so they
-- can check the name, the date, the venue and the price before the first
-- sale — and even scan it. The barcode is a REAL platform EAN-13, minted
-- through the same uniqueness guard as a sold ticket's, so no sold ticket
-- can ever collide with a sample; it lives in its own barcode authority so
-- the gate recognises it as a sample and never admits anyone on it.
--
-- One code per session: the link table below keeps it, so a repeated
-- request (the bot's "Sample ticket" button) reuses the same PDF and the
-- barcodes table does not grow with every click.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE barcode_authorities DROP CONSTRAINT barcode_authorities_type_check;
ALTER TABLE barcode_authorities
    ADD CONSTRAINT barcode_authorities_type_check
    CHECK (type IN ('platform', 'legacy_bil24', 'external_platform', 'guest_list', 'sample'));

INSERT INTO barcode_authorities (type, label)
SELECT 'sample', 'Sample tickets'
WHERE  NOT EXISTS (SELECT 1 FROM barcode_authorities WHERE type = 'sample');

CREATE TABLE session_sample_barcodes (
    session_id uuid        NOT NULL PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
    barcode_id uuid        NOT NULL REFERENCES barcodes(id),
    ean13      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE session_sample_barcodes IS
    'The EAN-13 a session''s sample e-ticket carries (barcode authority ''sample''); one per session, reused on every request.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS session_sample_barcodes;
DELETE FROM barcodes WHERE authority_id IN (SELECT id FROM barcode_authorities WHERE type = 'sample');
DELETE FROM barcode_authorities WHERE type = 'sample';
ALTER TABLE barcode_authorities DROP CONSTRAINT barcode_authorities_type_check;
ALTER TABLE barcode_authorities
    ADD CONSTRAINT barcode_authorities_type_check
    CHECK (type IN ('platform', 'legacy_bil24', 'external_platform', 'guest_list'));

-- +goose StatementEnd
