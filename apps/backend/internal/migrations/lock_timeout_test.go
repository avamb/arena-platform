package migrations

import (
	"strings"
	"testing"
)

// The refund migrations that add foreign keys to busy tables (tickets,
// organizations, refunds) wait at most a few seconds for their locks, so a
// deploy that meets a long transaction fails fast instead of queueing every
// sale behind it.
func TestMigrations_ForeignKeysOnBusyTablesSetALockTimeout(t *testing.T) {
	for _, name := range []string{"sql/0143_refund_scan_blocks.sql", "sql/0145_refund_scan_exceptions.sql", "sql/0147_email_letters.sql", "sql/0149_email_letter_sources.sql", "sql/0150_refresh_token_sessions.sql", "sql/0151_web_login_codes.sql", "sql/0154_macs_scan_events.sql", "sql/0155_checkout_consents.sql", "sql/0156_letter_interaction_client.sql", "sql/0157_billing_foundation.sql", "sql/0158_billing_events_charges.sql", "sql/0162_session_seat_batches.sql", "sql/0163_legal_documents.sql", "sql/0164_ticket_vip.sql", "sql/0165_event_email_notes.sql", "sql/0166_promo_code_attempts.sql", "sql/0167_sales_links.sql", "sql/0168_rich_text.sql", "sql/0169_web_login_code_purpose.sql", "sql/0170_session_change_message_format.sql", "sql/0171_seat_layouts.sql", "sql/0172_shop.sql", "sql/0173_sales_notification_kinds.sql", "sql/0176_staff_invitations.sql", "sql/0177_shop_order_ip_and_download.sql", "sql/0178_shop_product_seo.sql", "sql/0179_shop_refund_idempotency_letters.sql"} {
		raw, err := FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		up := string(raw)
		i := strings.Index(up, "-- +goose Up")
		if i < 0 {
			t.Fatalf("%s: no Up section", name)
		}
		up = up[i:]
		lock := strings.Index(up, "SET LOCAL lock_timeout")
		create := firstStatement(up)
		if lock < 0 || create < 0 || lock > create {
			t.Errorf("%s: SET LOCAL lock_timeout must come before the first statement of the Up section", name)
		}
	}
}

// firstStatement is the offset of the first CREATE TABLE or ALTER TABLE.
func firstStatement(up string) int {
	first := -1
	for _, kw := range []string{"CREATE TABLE", "ALTER TABLE"} {
		if i := strings.Index(up, kw); i >= 0 && (first < 0 || i < first) {
			first = i
		}
	}
	return first
}
