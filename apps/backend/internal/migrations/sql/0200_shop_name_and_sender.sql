-- 0200_shop_name_and_sender.sql — a shop's own name and its own sender address.
--
-- A shop may belong to an organization under another name: the Einat Winery
-- storefront is a shop of the organization Vino&Co (one legal entity, owner
-- decision 2026-11-06), and its buyers must get their letters from the shop,
-- not from the organization.
--
-- shop_settings.shop_name     the name the storefront and the shop letters
--                             print (the letter header and the From display
--                             name). NULL: the organization's name, as before.
--                             The owner or a manager sets it.
-- shop_settings.sender_email  the From address of the shop letters (order
--                             received, shipped, delivered, refunded, the
--                             referral letters of a shop order). NULL: the
--                             organization's verified sender, else the
--                             platform address, as before. Only a platform
--                             superadmin sets it, after the domain is
--                             authenticated in the platform's Brevo account,
--                             like shop_domain: a From address is not something
--                             an organizer may pick freely.
-- =====================================================================

-- +goose Up

SET LOCAL lock_timeout = '5s';

ALTER TABLE shop_settings
    ADD COLUMN shop_name text NULL
        CONSTRAINT shop_settings_shop_name_check
        CHECK (shop_name IS NULL OR char_length(btrim(shop_name)) BETWEEN 1 AND 100),
    ADD COLUMN sender_email text NULL
        CONSTRAINT shop_settings_sender_email_check
        CHECK (sender_email IS NULL OR (char_length(sender_email) BETWEEN 3 AND 254
                                       AND sender_email = lower(btrim(sender_email))
                                       AND position('@' IN sender_email) > 1));

-- +goose Down

SET LOCAL lock_timeout = '5s';

ALTER TABLE shop_settings
    DROP COLUMN IF EXISTS sender_email,
    DROP COLUMN IF EXISTS shop_name;
