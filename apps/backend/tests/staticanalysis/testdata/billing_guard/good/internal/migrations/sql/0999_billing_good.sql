-- +goose Up
CREATE TABLE charges (id uuid PRIMARY KEY, amount_minor bigint NOT NULL, currency text NOT NULL);
INSERT INTO charges (id, amount_minor, currency) VALUES (gen_random_uuid(), 0, 'EUR');
-- UPDATE charges SET amount_minor = 1 would be forbidden here, this is only a comment.

-- +goose Down
DELETE FROM charges;
UPDATE credit_ledger SET note = 'down';
TRUNCATE TABLE campaign_grants;
DROP TABLE charges;
