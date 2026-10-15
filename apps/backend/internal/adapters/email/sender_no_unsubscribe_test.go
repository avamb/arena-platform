package email

import (
	"bytes"
	"strings"
	"testing"
)

// TestBuildMIMEMessage_NoListUnsubscribeHeader pins that arena never writes a
// List-Unsubscribe (or List-Unsubscribe-Post) header on a buyer letter.
// Every letter is transactional, and Brevo turns an unsubscribe that comes
// through that header into a block on ALL later mail to the address, so the
// buyer would stop getting tickets and refund notices. Brevo adds its own
// header at the relay (see docs/ops/brevo_delivery_status_webhook_ru.md,
// section "Отписка в транзакционных письмах"), which this test cannot see.
func TestBuildMIMEMessage_NoListUnsubscribeHeader(t *testing.T) {
	cases := map[string]Message{
		"with attachments": trackedTicketLetter(),
		"html only": {
			To: "buyer@example.com", Subject: "Refund", HTMLBody: "<html><body>x</body></html>",
			TrackingID: trackedLetterID,
		},
		"reply-to": {
			To: "buyer@example.com", Subject: "Hi", HTMLBody: "<html><body>x</body></html>",
			TextBody: "x", ReplyTo: "organizer@example.com",
		},
	}
	for name, msg := range cases {
		raw, err := buildMIMEMessage("Arena <noreply@example.com>", msg)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bytes.Contains(bytes.ToLower(raw), []byte("list-unsubscribe")) {
			t.Errorf("%s: the message carries a List-Unsubscribe header", name)
		}
		low := strings.ToLower(string(raw))
		for _, w := range []string{"unsubscribe", "list-help"} {
			if strings.Contains(low, w) {
				t.Errorf("%s: the message mentions %q", name, w)
			}
		}
	}
}
