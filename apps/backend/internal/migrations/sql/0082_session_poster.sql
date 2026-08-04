-- +goose Up
ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg',
            'session_poster'
        ));

ALTER TABLE sessions
    ADD COLUMN poster_media_id uuid NULL
        REFERENCES media_objects(id) ON DELETE SET NULL;

COMMENT ON COLUMN sessions.poster_media_id IS
    'FK to media_objects holding the session-level poster artwork. '
    'Overrides events.poster_media_id for this specific session. '
    'Resolution order: sessions.poster_media_id ?? events.poster_media_id ?? none. '
    'AB-47.';

-- +goose Down
ALTER TABLE sessions
    DROP COLUMN IF EXISTS poster_media_id;

ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg'
        ));
