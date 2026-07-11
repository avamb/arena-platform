-- +goose Up
-- =====================================================================
-- arena_new — SEAT-C4: human-readable credential code + organizer
-- ticket-PDF format flag (feature #314).
--
-- 1. ticket_credentials.human_code
--
--    The manual-entry fallback printed under the QR code on the PDF
--    e-ticket and shown in the delivery email body. Generated for every
--    static_qr credential alongside the existing 64-hex QR token.
--
--    Stored form is CANONICAL: 8 characters from the Crockford Base32
--    alphabet (0-9 A-Z minus the look-alikes I L O U), first character
--    always a LETTER, no hyphen (e.g. 'M7KT2QV9'). Display surfaces
--    insert a hyphen after the fourth character ('M7KT-2QV9'); lookups
--    normalize input back to canonical (uppercase, strip hyphens and
--    spaces, map Crockford aliases I→1 L→1 O→0) — see
--    internal/platform/humancode.
--
--    The always-a-letter rule makes the code Excel-safe by
--    construction: it can never be parsed as a plain number nor as
--    scientific notation ('1234E567' → 1.23E+12) by spreadsheet
--    tooling, which has silently corrupted numeric (EAN-13-style)
--    codes in agent/organizer exports before. The same corruption risk
--    applies to external barcode-import batches.
--
--    NULLable: pdf-type credentials and pre-SEAT-C4 static_qr rows
--    carry NULL until (re)issued; generation backfills lazily on the
--    next credential access / delivery enqueue.
--
-- 2. organizations.ticket_pdf_format
--
--    Organizer-level flag choosing which PDF layout(s) the ticket
--    delivery email attaches: 'mobile' (default; phone-aspect layout),
--    'a4' (printable A4 portrait) or 'both'. Lives on the organizations
--    row — the same organizer configuration surface as the buyer-field
--    flags — and is threaded onto the ticket.deliver worker payload at
--    enqueue time (see delivery.Payload.TicketPDFFormat).
-- =====================================================================

ALTER TABLE ticket_credentials
    ADD COLUMN human_code text NULL;

-- Canonical-form guard: 8 Crockford Base32 characters, leading letter.
-- Guarantees Excel-safety and alias-free storage at the schema level.
ALTER TABLE ticket_credentials
    ADD CONSTRAINT ticket_credentials_human_code_shape_check
        CHECK (human_code IS NULL
            OR human_code ~ '^[ABCDEFGHJKMNPQRSTVWXYZ][0-9ABCDEFGHJKMNPQRSTVWXYZ]{7}$');

-- One code per credential across the platform. The application retries
-- generation (bounded) on a 23505 unique violation of this constraint.
ALTER TABLE ticket_credentials
    ADD CONSTRAINT ticket_credentials_human_code_unique UNIQUE (human_code);

COMMENT ON COLUMN ticket_credentials.human_code IS
    'Human-readable manual-entry fallback for the QR credential (SEAT-C4). '
    'Canonical form: 8 Crockford Base32 chars (no I/L/O/U), first char always '
    'a letter, stored WITHOUT hyphen; displayed grouped XXXX-XXXX. NULL for '
    'pdf-type credentials and legacy static_qr rows.';

ALTER TABLE organizations
    ADD COLUMN ticket_pdf_format text NOT NULL DEFAULT 'mobile';

ALTER TABLE organizations
    ADD CONSTRAINT organizations_ticket_pdf_format_check
        CHECK (ticket_pdf_format IN ('mobile', 'a4', 'both'));

COMMENT ON COLUMN organizations.ticket_pdf_format IS
    'Which PDF layout(s) the ticket delivery email attaches (SEAT-C4): '
    'mobile (default, phone-aspect ticket), a4 (printable A4 portrait), '
    'or both.';

-- +goose Down
ALTER TABLE organizations
    DROP CONSTRAINT IF EXISTS organizations_ticket_pdf_format_check;
ALTER TABLE organizations
    DROP COLUMN IF EXISTS ticket_pdf_format;

ALTER TABLE ticket_credentials
    DROP CONSTRAINT IF EXISTS ticket_credentials_human_code_unique,
    DROP CONSTRAINT IF EXISTS ticket_credentials_human_code_shape_check;
ALTER TABLE ticket_credentials
    DROP COLUMN IF EXISTS human_code;
