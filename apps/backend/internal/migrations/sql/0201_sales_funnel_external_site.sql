-- 0201_sales_funnel_external_site.sql — a customer's own site on its own domain
-- (the first is vinoandco.events) sends the same three funnel steps as the
-- widget and the hosted pages (spec 50, P-21). It is a new surface of
-- POST /v1/public/funnel: external_site. Only the surface CHECK of
-- sales_funnel_daily (migration 0193) is widened, no data changes.

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE sales_funnel_daily DROP CONSTRAINT sales_funnel_daily_surface_check;
ALTER TABLE sales_funnel_daily ADD CONSTRAINT sales_funnel_daily_surface_check
    CHECK (surface IN ('widget', 'hosted_page', 'tickets_page', 'external_site'));

-- +goose Down
DELETE FROM sales_funnel_daily WHERE surface = 'external_site';
ALTER TABLE sales_funnel_daily DROP CONSTRAINT sales_funnel_daily_surface_check;
ALTER TABLE sales_funnel_daily ADD CONSTRAINT sales_funnel_daily_surface_check
    CHECK (surface IN ('widget', 'hosted_page', 'tickets_page'));
