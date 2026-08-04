-- +goose Up
-- AB-40 A3: per-plan display-name overrides for price categories, so
-- renaming "Third" to "SEATING - SEZENI" does not require re-importing
-- the SVG (geometry stays immutable per version; the override lives on
-- the plan). Keys are category indexes as strings ("1".."15"), values
-- are the display names.
ALTER TABLE seating_plans
    ADD COLUMN category_name_overrides jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN seating_plans.category_name_overrides IS
    'JSON object mapping category index ("1".."15") to a display-name '
    'override applied over geometry.categories[].name. AB-40 A3: '
    'renaming a category must not require re-importing the SVG.';

-- +goose Down
ALTER TABLE seating_plans
    DROP COLUMN IF EXISTS category_name_overrides;
