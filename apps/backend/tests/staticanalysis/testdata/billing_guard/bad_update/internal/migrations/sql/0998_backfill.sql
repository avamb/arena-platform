-- +goose Up
UPDATE campaign_grants SET note = 'backfill';

-- +goose Down
UPDATE campaign_grants SET note = NULL;
