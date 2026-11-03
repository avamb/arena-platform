-- 0185_provider_receipts.sql — the payment provider's own receipt for the
-- buyer (spec 51 section 2, owner decisions R-6 and 2026-11-01).
--
-- organizations.provider_receipt_enabled is the owner's choice: ask the
-- provider to e-mail its receipt to the buyer (Stripe receipt_email, PayPlus
-- sendEmailApproval). Default ON (owner decision 2026-11-01).
-- organizations.provider_receipt_forced is set ONLY by a platform superadmin:
-- the receipt is then always requested, whatever the owner chose. Turning the
-- setting off cannot promise that no receipt is sent: the organizer's own
-- provider account may send one by itself.
--
-- payment_intents keeps the receipt of a paid payment: the provider's link
-- (Stripe charge.receipt_url, valid 30 days), its number when the provider
-- printed one, and a COPY in arena's media storage, because a dispute may
-- come 120 days after the event. The copy is a media_objects row of the new
-- owner type 'payment_receipt', which no upload route accepts
-- (mediastore.AllowedOwnerTypes leaves it out); only the archive job writes
-- it. The job is payment.receipt_archive (internal/platform/paymentreceipts),
-- queued by the payment webhook when a payment succeeds.
-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE organizations
    ADD COLUMN provider_receipt_enabled boolean NOT NULL DEFAULT true,
    ADD COLUMN provider_receipt_forced  boolean NOT NULL DEFAULT false;

ALTER TABLE payment_intents
    ADD COLUMN provider_receipt_url         text,
    ADD COLUMN provider_receipt_number      text,
    ADD COLUMN provider_receipt_media_id    uuid REFERENCES media_objects(id) ON DELETE SET NULL,
    ADD COLUMN provider_receipt_archived_at timestamptz;

ALTER TABLE payment_intents
    ADD CONSTRAINT payment_intents_provider_receipt_url_check
        CHECK (provider_receipt_url IS NULL OR length(provider_receipt_url) BETWEEN 1 AND 2048),
    ADD CONSTRAINT payment_intents_provider_receipt_number_check
        CHECK (provider_receipt_number IS NULL OR length(provider_receipt_number) BETWEEN 1 AND 100);

ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg',
            'session_poster',
            'event_gallery',
            'event_report',
            'shop_product',
            'shop_digital',
            'payment_receipt'
        ));

-- +goose Down
SET LOCAL lock_timeout = '5s';

ALTER TABLE payment_intents
    DROP CONSTRAINT IF EXISTS payment_intents_provider_receipt_number_check,
    DROP CONSTRAINT IF EXISTS payment_intents_provider_receipt_url_check,
    DROP COLUMN IF EXISTS provider_receipt_archived_at,
    DROP COLUMN IF EXISTS provider_receipt_media_id,
    DROP COLUMN IF EXISTS provider_receipt_number,
    DROP COLUMN IF EXISTS provider_receipt_url;

DELETE FROM media_objects WHERE owner_type = 'payment_receipt';

ALTER TABLE media_objects
    DROP CONSTRAINT IF EXISTS media_objects_owner_type_check;

ALTER TABLE media_objects
    ADD CONSTRAINT media_objects_owner_type_check
        CHECK (owner_type IN (
            'org_logo',
            'event_poster',
            'artist_photo',
            'seating_plan_svg',
            'session_poster',
            'event_gallery',
            'event_report',
            'shop_product',
            'shop_digital'
        ));

ALTER TABLE organizations
    DROP COLUMN IF EXISTS provider_receipt_forced,
    DROP COLUMN IF EXISTS provider_receipt_enabled;
