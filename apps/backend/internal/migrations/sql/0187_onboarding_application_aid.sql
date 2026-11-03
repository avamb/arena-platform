-- 0187_onboarding_application_aid.sql — the visitor id a website application
-- was started with (spec 08_architecture/50_product_analytics_ru.md, stage A5).
--
-- arenasoldout.com sends an optional `aid` (the analytics visitor id, only
-- after the visitor accepted the "Analytics" consent) in the body of the
-- onboarding start request. The hourly analytics.rollup job joins it with the
-- e-mail of the application, so a lead who read the blog and then applied is
-- ONE person. The value is a random id, not personal data by itself, but it
-- is cleared with the rest of the personal fields when onboarding.sweep or an
-- operator purges the application.

-- +goose Up

ALTER TABLE onboarding_applications
    ADD COLUMN aid text CHECK (aid IS NULL OR aid ~ '^[A-Za-z0-9_-]{8,40}$');

COMMENT ON COLUMN onboarding_applications.aid IS
    'Analytics visitor id (cookie aso_aid) of the browser that started the application, NULL when the visitor gave no consent. Cleared on purge.';

-- +goose Down

ALTER TABLE onboarding_applications DROP COLUMN IF EXISTS aid;
