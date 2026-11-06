-- 0201_test_sales_channel.sql — a test sales channel and test orders (П-13).
--
-- An organization may keep a second, test channel next to its live one. The
-- test event is published only into it, payments still go through the live
-- provider, and every order born in such a channel is marked as a test order
-- so that reports, exports and notifications do not mix it with real sales.
--
-- sales_channels.is_test  the channel is a test one. Organizer-editable.
-- orders.is_test          copied from the channel when the order is created
--                         and never changed afterwards (no update path).

-- The flag is carried by a trigger, not by every order writer: the REST
-- checkout, the Bil24 gateway, invitations and the shop all insert into orders
-- through different code, and none of them may forget it. On INSERT the order
-- takes the flag of its channel, on UPDATE the stored value is kept whatever
-- the statement says.

-- +goose Up
ALTER TABLE sales_channels ADD COLUMN is_test boolean NOT NULL DEFAULT false;
ALTER TABLE orders         ADD COLUMN is_test boolean NOT NULL DEFAULT false;

-- +goose StatementBegin
CREATE FUNCTION orders_is_test_from_channel() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        NEW.is_test := NEW.is_test OR COALESCE(
            (SELECT sc.is_test FROM sales_channels sc WHERE sc.id = NEW.channel_id), false);
    ELSE
        NEW.is_test := OLD.is_test;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER orders_is_test_insert BEFORE INSERT ON orders
    FOR EACH ROW EXECUTE FUNCTION orders_is_test_from_channel();
CREATE TRIGGER orders_is_test_update BEFORE UPDATE OF is_test ON orders
    FOR EACH ROW EXECUTE FUNCTION orders_is_test_from_channel();

-- +goose Down
DROP TRIGGER orders_is_test_update ON orders;
DROP TRIGGER orders_is_test_insert ON orders;
DROP FUNCTION orders_is_test_from_channel();
ALTER TABLE orders         DROP COLUMN is_test;
ALTER TABLE sales_channels DROP COLUMN is_test;
