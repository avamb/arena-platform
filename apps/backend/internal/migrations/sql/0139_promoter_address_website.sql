-- 0139_promoter_address_website.sql — a promoter keeps a postal address and a
-- website next to its tax id, phone and e-mail (Telegram event-center bot,
-- spec 08_architecture/35 §6.5, EC-14).
--
-- Both are free text, optional, shown to the organizer only for now. The
-- length checks mirror the handler's own limit so a direct INSERT cannot slip
-- past it. No permission is seeded, so the superadmin parity guard has
-- nothing to check.

-- +goose Up
-- +goose StatementBegin

ALTER TABLE org_promoters
    ADD COLUMN address text,
    ADD COLUMN website text,
    ADD CONSTRAINT org_promoters_address_len CHECK (address IS NULL OR char_length(address) BETWEEN 1 AND 300),
    ADD CONSTRAINT org_promoters_website_len CHECK (website IS NULL OR char_length(website) BETWEEN 1 AND 300);

COMMENT ON COLUMN org_promoters.address IS
    'Postal address of the promoter, free text, at most 300 characters.';
COMMENT ON COLUMN org_promoters.website IS
    'Website of the promoter, an http(s) URL of at most 300 characters.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE org_promoters
    DROP CONSTRAINT IF EXISTS org_promoters_website_len,
    DROP CONSTRAINT IF EXISTS org_promoters_address_len,
    DROP COLUMN IF EXISTS website,
    DROP COLUMN IF EXISTS address;

-- +goose StatementEnd
