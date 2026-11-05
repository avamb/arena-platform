-- 0197_addon_tiers_and_sales_alerts.sql — add-on categories and the sales
-- alerts the organizer did not get (owner request 2026-11-05, after
-- "Серебряное копытце": the main category of a session sold out on 26.10,
-- one "extra child" place kept the session "on sale", nobody was told, and a
-- buyer could take the extra-child ticket alone).
--
-- ticket_tiers.is_addon  an add-on category is sold only together with a
--                        ticket of a main (non-add-on) category of the same
--                        session, in the same purchase. Its free places do
--                        not keep a session "on sale": once every main
--                        category is gone the session is sold out
--                        (salesstate.Effective, gen.ListSessionSalesFacts).
--                        A session whose categories are ALL add-ons is
--                        treated as if none were.
--
-- sales_alert_notices    one row per thing the organization's sales chat was
--                        told about (package salesalert):
--                        category_sold_out — a category has no free place and
--                          no unpaid hold left (the row is deleted when places
--                          come back, so the next sell-out is told again);
--                        sales_ended — the session's sales end passed.
--
-- session_sales_state_changes.chat_announced_at  a manual change (or one a
--                        site made) was posted to the sales chat, so the rest
--                        of the team knows who stopped or reopened the sale.
--
-- Everything that already happened is marked as told here, so the first pass
-- after the deploy does not flood the chats with history.

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE ticket_tiers ADD COLUMN is_addon boolean NOT NULL DEFAULT false;

COMMENT ON COLUMN ticket_tiers.is_addon IS
    'Add-on category: sold only together with a main category ticket of the same session; its free places do not keep the session on sale.';

CREATE TABLE sales_alert_notices (
    kind        text        NOT NULL CHECK (kind IN ('category_sold_out', 'sales_ended')),
    subject_id  uuid        NOT NULL,
    org_id      uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    notified_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, subject_id)
);

COMMENT ON TABLE sales_alert_notices IS
    'Sales alerts already told to the organization: a sold-out category (tier id) or a session whose sales end passed (session id).';

ALTER TABLE session_sales_state_changes ADD COLUMN chat_announced_at timestamptz NULL;

UPDATE session_sales_state_changes SET chat_announced_at = now();

INSERT INTO sales_alert_notices (kind, subject_id, org_id)
SELECT 'category_sold_out', tt.id, e.org_id
FROM   ticket_tiers tt
JOIN   sessions s ON s.id = tt.session_id
JOIN   events   e ON e.id = s.event_id
WHERE  tt.deleted_at IS NULL
  AND  s.deleted_at IS NULL
  AND  EXISTS (SELECT 1 FROM session_seats ss WHERE ss.tier_id = tt.id)
  AND  NOT EXISTS (SELECT 1 FROM session_seats ss
                    WHERE ss.tier_id = tt.id AND ss.status IN ('available', 'held'))
ON CONFLICT DO NOTHING;

INSERT INTO sales_alert_notices (kind, subject_id, org_id)
SELECT 'sales_ended', s.id, e.org_id
FROM   sessions s
JOIN   events   e ON e.id = s.event_id
WHERE  s.sales_end_at <= now()
ON CONFLICT DO NOTHING;

-- +goose Down

DROP TABLE IF EXISTS sales_alert_notices;
ALTER TABLE session_sales_state_changes DROP COLUMN IF EXISTS chat_announced_at;
ALTER TABLE ticket_tiers DROP COLUMN IF EXISTS is_addon;
