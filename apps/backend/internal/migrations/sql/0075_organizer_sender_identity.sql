-- +goose Up
-- AB-10: Brevo-managed sender identities. SMTP credentials remain platform-only.
ALTER TABLE organizations
  ADD COLUMN sender_email text,
  ADD COLUMN sender_verification_status text NOT NULL DEFAULT 'not_configured',
  ADD COLUMN sender_verified_at timestamptz;

ALTER TABLE organizations ADD CONSTRAINT organizations_sender_verification_status_check
  CHECK (sender_verification_status IN ('not_configured', 'pending', 'verified', 'failed'));

-- A verified state is meaningful only when it names the sender Brevo verified.
ALTER TABLE organizations ADD CONSTRAINT organizations_verified_sender_email_check
  CHECK (sender_verification_status <> 'verified' OR sender_email IS NOT NULL);

-- +goose Down
ALTER TABLE organizations DROP CONSTRAINT IF EXISTS organizations_verified_sender_email_check;
ALTER TABLE organizations DROP CONSTRAINT IF EXISTS organizations_sender_verification_status_check;
ALTER TABLE organizations DROP COLUMN IF EXISTS sender_verified_at;
ALTER TABLE organizations DROP COLUMN IF EXISTS sender_verification_status;
ALTER TABLE organizations DROP COLUMN IF EXISTS sender_email;
