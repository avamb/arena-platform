-- +goose Up
-- AB-25: seating plan versions carry the original authoring SVG as a binary
-- media object so the admin drawer can preview the artwork without
-- re-rendering canonical geometry. The media pipeline discriminates uploads
-- by `owner_type`, whose accepted values are pinned by a CHECK constraint
-- (migration 0052). Widen that constraint with the new `seating_plan_svg`
-- kind; `mediastore.AllowedOwnerTypes` mirrors this list in Go and the
-- OpenAPI `owner_type` enums mirror it on the wire.
--
-- Existing rows are unaffected: the new value only widens the accepted set,
-- so the constraint can be swapped without a table rewrite of the data.
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

-- +goose Down
-- Reverting drops any seating-plan SVG assets uploaded while 0078 was
-- applied, otherwise the narrowed CHECK would fail to validate. The rows are
-- soft-deletable media metadata; the byte reclamation is handled by the
-- existing media-gc worker sweep.
DELETE FROM media_objects WHERE owner_type = 'seating_plan_svg';

ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN ('org_logo', 'event_poster', 'artist_photo'));
