package horders

import (
	"strconv"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

func i64(v int64) *int64 { return &v }

func TestClassifyQuery_Precedence(t *testing.T) {
	legacy := ean13.PlatformCode(3000000001)
	random := "4600051000001" // checksum-valid, not a platform prefix
	cases := []struct {
		name string
		q    string
		want searchTerms
	}{
		{"empty", "   ", searchTerms{}},
		{"barcode random", random, searchTerms{Barcode: random, PhoneDigits: random}},
		{"barcode legacy decodes the ticket id", legacy, searchTerms{Barcode: legacy, PhoneDigits: legacy, LegacyTicketID: i64(3000000001)}},
		{"barcode with separators", "4600 0510 0000 1", searchTerms{Barcode: random, PhoneDigits: random}},
		{"barcode with a bad check digit is still an exact barcode", "4600051000002", searchTerms{Barcode: "4600051000002", PhoneDigits: "4600051000002"}},
		{"order number", "1000000500", searchTerms{SystemID: i64(1000000500), PhoneDigits: "1000000500"}},
		{"order number with hash", "#1000000500", searchTerms{SystemID: i64(1000000500)}},
		{"order number with hash and space", "# 1000000500", searchTerms{SystemID: i64(1000000500)}},
		{"short number is an order number only", "42", searchTerms{SystemID: i64(42)}},
		{"twelve digits still an order number", "123456789012", searchTerms{SystemID: i64(123456789012), PhoneDigits: "123456789012"}},
		{"fourteen digits falls to similarity", "12345678901234", searchTerms{Text: "12345678901234"}},
		{"email", "Buyer@Example.COM", searchTerms{Email: "buyer@example.com"}},
		{"phone plus", "+34 600 111 222", searchTerms{PhoneDigits: "34600111222"}},
		{"phone 00 prefix is 13 digits: a barcode AND a phone", "0034-600-111-222", searchTerms{Barcode: "0034600111222", PhoneDigits: "34600111222"}},
		{"phone 00 prefix short", "0034-600-111-22", searchTerms{SystemID: i64(3460011122), PhoneDigits: "3460011122"}},
		{"phone parentheses", "+7 (912) 345-67-89", searchTerms{PhoneDigits: "79123456789"}},
		{"phone dots", "+420.777.123.456", searchTerms{PhoneDigits: "420777123456"}},
		{"name", "Anna Nováková", searchTerms{Text: "Anna Nováková"}},
		{"hash with letters is text", "#abc", searchTerms{Text: "#abc"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classifyQuery(c.q)
			if got.Barcode != c.want.Barcode || got.Email != c.want.Email ||
				got.PhoneDigits != c.want.PhoneDigits || got.Text != c.want.Text ||
				!eqI64(got.SystemID, c.want.SystemID) || !eqI64(got.LegacyTicketID, c.want.LegacyTicketID) {
				t.Fatalf("classifyQuery(%q) = %s, want %s", c.q, fmtTerms(got), fmtTerms(c.want))
			}
			if got.empty() != (c.want == searchTerms{}) {
				t.Fatalf("empty() = %v for %q", got.empty(), c.q)
			}
		})
	}
}

func eqI64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func fmtTerms(s searchTerms) string {
	out := "{barcode=" + s.Barcode + " email=" + s.Email + " phone=" + s.PhoneDigits + " text=" + s.Text
	if s.SystemID != nil {
		out += " system_id=" + strconv.FormatInt(*s.SystemID, 10)
	}
	if s.LegacyTicketID != nil {
		out += " legacy_ticket=" + strconv.FormatInt(*s.LegacyTicketID, 10)
	}
	return out + "}"
}

func TestPhoneDigits(t *testing.T) {
	cases := map[string]string{
		"+34600111222":       "34600111222",
		"0034600111222":      "34600111222",
		"00 34 600 111 222":  "34600111222",
		"600 111 222":        "600111222",
		"+1 (212) 555-0100":  "12125550100",
		"123456":             "", // too short
		"+1234567890123456":  "", // 16 digits
		"abc":                "",
		"+34 600 111 222 x1": "", // letters are not a phone
		"":                   "",
	}
	for in, want := range cases {
		if got := phoneDigits(in); got != want {
			t.Errorf("phoneDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLegacyTicketSystemID(t *testing.T) {
	code := ean13.PlatformCode(42)
	if id, ok := legacyTicketSystemID(code); !ok || id != 42 {
		t.Fatalf("legacyTicketSystemID(%q) = %d,%v", code, id, ok)
	}
	if _, ok := legacyTicketSystemID("4600051000001"); ok {
		t.Fatal("a foreign prefix must not decode")
	}
	// Flip the check digit: the code is no longer checksum-valid.
	bad := code[:12] + string('0'+(code[12]-'0'+1)%10)
	if _, ok := legacyTicketSystemID(bad); ok {
		t.Fatal("a bad check digit must not decode")
	}
}

func TestTabFilter(t *testing.T) {
	for in, want := range map[string]string{"": "", "recent": "", "Paid": "paid", " unpaid ": "unpaid"} {
		got, ok := tabFilter(in)
		if !ok || got != want {
			t.Errorf("tabFilter(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	if _, ok := tabFilter("refunded"); ok {
		t.Error("tabFilter(refunded) must be refused")
	}
}

func TestUnpaidReasonAndPaymentPick(t *testing.T) {
	failed := gen.PaymentIntentRow{State: "failed"}
	created := gen.PaymentIntentRow{State: "created"}
	succeeded := gen.PaymentIntentRow{State: "succeeded"}

	if got := pickPaymentIntent([]gen.PaymentIntentRow{created, failed, succeeded}); got.State != "succeeded" {
		t.Errorf("succeeded must win, got %s", got.State)
	}
	if got := pickPaymentIntent([]gen.PaymentIntentRow{created, failed}); got.State != "failed" {
		t.Errorf("failed must win over an open retry, got %s", got.State)
	}
	if got := pickPaymentIntent(nil); got != nil {
		t.Error("no intents -> nil")
	}

	cases := []struct {
		status string
		pi     *gen.PaymentIntentRow
		want   string
	}{
		{"paid", &succeeded, ""},
		{"partially_refunded", &succeeded, ""},
		{"refunded", nil, ""},
		{"cancelled", &failed, reasonCancelled},
		{"manual_review", &succeeded, reasonManualReview},
		{"pending_payment", nil, reasonAwaitingPayment},
		{"pending_payment", &created, reasonAwaitingPayment},
		{"pending_payment", &failed, reasonPaymentFailed},
		{"expired", &failed, reasonPaymentFailed},
		{"expired", &created, reasonPaymentAbandoned},
		{"expired", nil, reasonHoldExpired},
		{"abandoned", nil, reasonHoldExpired},
	}
	for _, c := range cases {
		if got := unpaidReason(c.status, c.pi); got != c.want {
			t.Errorf("unpaidReason(%s, %v) = %q, want %q", c.status, c.pi, got, c.want)
		}
	}
}

func TestDeliveryStateAndChannelKind(t *testing.T) {
	row := func(s string) gen.OrderTicketDetailRow {
		if s == "" {
			return gen.OrderTicketDetailRow{}
		}
		return gen.OrderTicketDetailRow{DeliveryStatus: &s}
	}
	cases := []struct {
		rows []gen.OrderTicketDetailRow
		want string
	}{
		{nil, deliveryNone},
		{[]gen.OrderTicketDetailRow{row("sent"), row("sent")}, deliverySent},
		{[]gen.OrderTicketDetailRow{row("sent"), row("pending")}, deliveryPending},
		{[]gen.OrderTicketDetailRow{row("failed"), row("processing")}, deliveryPending},
		{[]gen.OrderTicketDetailRow{row("failed"), row("sent")}, deliveryFailed},
		{[]gen.OrderTicketDetailRow{row(""), row("sent")}, deliveryNone},
		{[]gen.OrderTicketDetailRow{row("skipped")}, deliveryNone},
	}
	for i, c := range cases {
		if got := deliveryState(c.rows); got != c.want {
			t.Errorf("case %d: deliveryState = %q, want %q", i, got, c.want)
		}
	}

	kinds := []struct {
		source   string
		settings string
		want     string
	}{
		{"bil24_gateway", `{}`, channelKindSite},
		{"public_feed", `{"gateway":{"token_hash":"$2a$x"}}`, channelKindSite},
		{"public_feed", `{"gateway_token_hash":"$2a$x"}`, channelKindSite},
		{"public_feed", `{"hosted_page":{"enabled":true}}`, channelKindHostedPage},
		{"complimentary", `{"hosted_page":{"enabled":true}}`, channelKindHostedPage},
		{"public_feed", `{"hosted_page":{"enabled":false}}`, channelKindWidget},
		{"checkout_api", ``, channelKindWidget},
		{"public_feed", `not json`, channelKindWidget},
	}
	for _, c := range kinds {
		if got := channelKind(c.source, []byte(c.settings)); got != c.want {
			t.Errorf("channelKind(%s, %s) = %q, want %q", c.source, c.settings, got, c.want)
		}
	}

	if l := seatLabel(ptr("A"), ptr("3"), ptr("12")); l == nil || *l != "A / 3 / 12" {
		t.Errorf("seatLabel = %v", l)
	}
	if l := seatLabel(nil, nil, ptr(" ")); l != nil {
		t.Errorf("blank seat parts must give nil, got %q", *l)
	}
}
