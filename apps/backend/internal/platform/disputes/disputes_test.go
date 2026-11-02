package disputes

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestReasonGroupOf(t *testing.T) {
	cases := map[string]string{
		"fraudulent": ReasonFraud, "debit_not_authorized": ReasonFraud, " Fraudulent ": ReasonFraud,
		"product_not_received": ReasonNotReceived, "unrecognized": ReasonNotRecognized,
		"credit_not_processed": ReasonRefund, "subscription_canceled": ReasonRefund,
		"product_unacceptable": ReasonQuality, "duplicate": ReasonDuplicate,
		"general": ReasonOther, "": ReasonOther, "bank_cannot_process": ReasonOther,
	}
	for in, want := range cases {
		if got := ReasonGroupOf(in); got != want {
			t.Errorf("ReasonGroupOf(%q) = %q, want %q", in, got, want)
		}
	}
	for _, r := range ManualReasons {
		if ReasonGroupOf(r) == "" {
			t.Errorf("manual reason %q has no group", r)
		}
	}
}

func TestProviderTransition(t *testing.T) {
	cases := []struct {
		cur, status          string
		submitted            bool
		wantState, wantOutcm string
	}{
		{"", "needs_response", false, StateNeedsResponse, ""},
		{"", "warning_needs_response", false, StateNeedsResponse, ""},
		{StateDraftReady, "needs_response", false, StateDraftReady, ""},
		{StateSubmitted, "under_review", true, StateUnderReview, ""},
		{StateAccepted, "under_review", false, StateAccepted, ""},
		{StateUnderReview, "won", true, StateClosed, OutcomeWon},
		{StateUnderReview, "lost", true, StateClosed, OutcomeLost},
		{StateAccepted, "lost", false, StateAccepted, OutcomeAccepted},
		{StateExpired, "lost", false, StateExpired, OutcomeExpired},
		{StateNeedsResponse, "warning_closed", false, StateClosed, OutcomeWithdrawn},
		{StateNeedsResponse, "charge_refunded", false, StateClosed, OutcomeWithdrawn},
		{StateSubmitted, "chargeback_revert", true, StateClosed, OutcomeWon},
		{StateNeedsResponse, "chargeback_revert", false, StateClosed, OutcomeWithdrawn},
		{"", "chargeback", false, StateNeedsResponse, ""},
		{StateDraftReady, "something_new", false, StateDraftReady, ""},
		// An expired dispute the provider still waits for is open again.
		{StateExpired, "needs_response", false, StateNeedsResponse, ""},
		{StateExpired, "warning_needs_response", false, StateNeedsResponse, ""},
	}
	for _, c := range cases {
		st, out := providerTransition(c.cur, c.status, c.submitted)
		if st != c.wantState || out != c.wantOutcm {
			t.Errorf("providerTransition(%q,%q,%v) = %q/%q, want %q/%q", c.cur, c.status, c.submitted, st, out, c.wantState, c.wantOutcm)
		}
	}
	if !isInquiry("warning_needs_response") || isInquiry("needs_response") {
		t.Error("isInquiry is wrong")
	}
}

func TestNoticesDue(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	due := func(d time.Duration) *time.Time { t := now.Add(d); return &t }
	keys := func(d Dispute) string { return strings.Join(noticesDue(d, now), ",") }

	if got := keys(Dispute{State: StateNeedsResponse, DueBy: due(10 * 24 * time.Hour)}); got != "opened" {
		t.Errorf("10 days left: %s", got)
	}
	if got := keys(Dispute{State: StateDraftReady, DueBy: due(5 * 24 * time.Hour)}); got != "opened,due_7" {
		t.Errorf("5 days left: %s", got)
	}
	if got := keys(Dispute{State: StateDraftReady, DueBy: due(20 * time.Hour)}); got != "opened,due_7,due_3,due_1" {
		t.Errorf("20 hours left: %s", got)
	}
	// An answered dispute is not reminded of its deadline.
	if got := keys(Dispute{State: StateSubmitted, DueBy: due(20 * time.Hour)}); got != "opened" {
		t.Errorf("submitted: %s", got)
	}
	if got := keys(Dispute{State: StateExpired}); got != "opened,expired" {
		t.Errorf("expired: %s", got)
	}
	if got := keys(Dispute{State: StateClosed}); got != "opened,closed" {
		t.Errorf("closed: %s", got)
	}
}

func sampleFacts(tickets int) Facts {
	paid := time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)
	f := Facts{
		Org:            OrgFacts{Name: "Teatro", LegalName: "Teatro S.L.", TaxID: "B12345678", Address: "Calle 1, Madrid", Country: "ES", ContactEmail: "info@teatro.es"},
		Order:          &OrderFacts{ID: uuid.New(), Number: 1000123, Status: "paid", Currency: "EUR", Total: 5900, BuyerName: "Ana García", BuyerEmail: "ana@example.com", CreatedAt: paid, PaidAt: &paid},
		Payment:        &PaymentFacts{Provider: "stripe", ProviderPaymentID: "cs_test", ChargeRef: "pi_test", Amount: 5900, Currency: "EUR", State: "succeeded", SucceededAt: &paid},
		Session:        &SessionFacts{EventName: "Hamlet", Status: "scheduled", StartAt: paid.Add(72 * time.Hour), VenueName: "Sala 1", City: "Madrid", Timezone: "Europe/Madrid"},
		Consent:        &ConsentFacts{AcceptedAt: paid, IP: "203.0.113.7", UserAgent: "Mozilla/5.0", Origin: "https://teatro.es", TermsVersion: "v1", TermsHTML: "<p>Refunds only if the event is cancelled.</p>", TermsSHA256: "abc"},
		AccessLogKnown: true,
	}
	for i := 0; i < tickets; i++ {
		n := fmt.Sprintf("%d", 900+i)
		f.Items = append(f.Items, ItemFacts{Ordinal: i + 1, Category: "Stalls", UnitPrice: 5900, Total: 5900, TicketNumber: n})
		f.Tickets = append(f.Tickets, TicketFacts{Number: n, Category: "Stalls", Status: "active", Barcode: "4600051000001"})
		f.Access = append(f.Access, AccessFacts{Action: "download", Source: "email", IP: "203.0.113.7", UserAgent: "Mozilla/5.0", TicketNumber: n, At: paid.Add(time.Duration(i) * time.Minute)})
		f.Letters = append(f.Letters, LetterFacts{Kind: "ticket", Recipient: "ana@example.com", Subject: "Your tickets", Status: "delivered", SentAt: paid, TicketNumbers: []string{n}})
	}
	return f
}

func TestBuildPackage_WithinBudgetAndEnglishForProvider(t *testing.T) {
	d := Dispute{ID: uuid.New(), Channel: ChannelAPI, AmountMinor: 5900, Currency: "EUR", Reason: "fraudulent", ReasonGroup: ReasonFraud, State: StateNeedsResponse}
	b, err := BuildPackage(d, sampleFacts(3), "ru", StripeBudget, "")
	if err != nil {
		t.Fatal(err)
	}
	if b.TotalBytes == 0 || b.TotalBytes > StripeBudget.MaxBytes || b.TotalPages > StripeBudget.MaxPages {
		t.Fatalf("budget: %d bytes, %d pages", b.TotalBytes, b.TotalPages)
	}
	var full *RenderedFile
	providers := 0
	for i := range b.Files {
		f := b.Files[i]
		if !bytes.HasPrefix(f.Content, []byte("%PDF-")) {
			t.Errorf("%s is not a PDF", f.Kind)
		}
		if f.Kind == FileFull {
			full = &b.Files[i]
			if f.ForProvider {
				t.Error("the organizer's copy must not go to the provider")
			}
		} else {
			providers++
		}
	}
	if full == nil || providers == 0 {
		t.Fatalf("files: full=%v providers=%d", full != nil, providers)
	}
	if b.Locale != "ru" {
		t.Errorf("full copy locale = %q", b.Locale)
	}
	if b.EmailDraft != nil {
		t.Error("an API dispute has no e-mail draft")
	}
	for _, k := range []string{"customer_name", "customer_email_address", "customer_purchase_ip", "service_date", "product_description", "uncategorized_text", "refund_policy_disclosure", "access_activity_log"} {
		if b.Fields[k] == "" {
			t.Errorf("field %s is empty", k)
		}
	}
	if b.Fields["customer_purchase_ip"] != "203.0.113.7" {
		t.Errorf("purchase ip = %q", b.Fields["customer_purchase_ip"])
	}
	if b.Fields["service_date"] != "2026-10-04" {
		t.Errorf("service date = %q", b.Fields["service_date"])
	}
}

func TestBuildPackage_BigOrderShrinksToBudget(t *testing.T) {
	d := Dispute{ID: uuid.New(), Channel: ChannelEmail, Provider: "allpay", AmountMinor: 590000, Currency: "EUR", Reason: "product_not_received", ReasonGroup: ReasonNotReceived, State: StateNeedsResponse}
	tight := Budget{MaxBytes: 2_000_000, MaxPages: 6}
	b, err := BuildPackage(d, sampleFacts(120), "en", tight, "info@allpay.co.il")
	if err != nil {
		t.Fatal(err)
	}
	if b.TotalPages > tight.MaxPages || b.TotalBytes > tight.MaxBytes {
		t.Fatalf("over budget: %d pages, %d bytes", b.TotalPages, b.TotalBytes)
	}
	if b.EmailDraft == nil || b.EmailDraft.To != "info@allpay.co.il" || b.EmailDraft.Subject == "" || b.EmailDraft.Body == "" {
		t.Fatalf("e-mail draft = %+v", b.EmailDraft)
	}
}

func TestBuildPackage_GapsAreNamed(t *testing.T) {
	f := sampleFacts(1)
	f.Consent = nil
	f.Access = nil
	f.AccessLogKnown = false
	f.Letters = nil
	got := strings.Join(gapsOf(f), ",")
	for _, want := range []string{"gap_no_letters", "gap_access_off", "gap_no_scans", "gap_no_consent"} {
		if !strings.Contains(got, want) {
			t.Errorf("gaps %q miss %s", got, want)
		}
	}
	if g := gapsOf(Facts{}); len(g) != 1 || g[0] != "gap_no_order" {
		t.Errorf("no order: %v", g)
	}
}

func TestNoticeTexts(t *testing.T) {
	due := time.Date(2026, 11, 9, 23, 59, 0, 0, time.UTC)
	out := "won"
	d := Dispute{ID: uuid.New(), OrgID: uuid.New(), Provider: "stripe", Channel: ChannelAPI, AmountMinor: 450, Currency: "EUR",
		ReasonGroup: ReasonFraud, State: StateDraftReady, DueBy: &due, LowValue: true}
	nf := noticeFacts{OrgName: "Teatro <b>", Locale: "ru-RU", EventName: "Hamlet & Co", Timezone: "Atlantic/Canary", OrderNumber: 1000123, HasPackage: true, Threshold: 500}
	text := noticeText("opened", d, nf, "https://events.example.com/")
	for _, want := range []string{"Открыт спор", "Hamlet &amp; Co", "Заказ 1000123", "(Madrid)", "Atlantic/Canary", "Отправить", "Мы советуем принять спор", "https://events.example.com/o/"} {
		if !strings.Contains(text, want) {
			t.Errorf("opened text misses %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Hamlet & Co") {
		t.Error("event name not escaped")
	}
	d.State, d.Outcome = StateClosed, &out
	if got := noticeText("closed", d, noticeFacts{Locale: "es"}, ""); !strings.Contains(got, "Disputa ganada") {
		t.Errorf("closed es: %s", got)
	}
	if got := noticeText("due_3", Dispute{State: StateNeedsResponse, DueBy: &due, Channel: ChannelEmail, Currency: "ILS", AmountMinor: 10000}, noticeFacts{Locale: "xx"}, ""); !strings.Contains(got, "3 day(s) left") || !strings.Contains(got, "«Send»") {
		t.Errorf("due_3 fallback: %s", got)
	}
	subject, body := noticeEmail("opened", d, nf, "")
	if strings.ContainsAny(subject, "<>") || strings.Contains(body, "<b>") || !strings.Contains(subject, "1000123") {
		t.Errorf("e-mail: %q / %q", subject, body)
	}
	ops := opsText("opened", d, noticeFacts{OrgName: "Teatro", OrderNumber: 7})
	if !strings.Contains(ops, d.ID.String()) || strings.Contains(ops, "@") {
		t.Errorf("ops text: %s", ops)
	}
}

func TestSettingsThreshold(t *testing.T) {
	st := Settings{Enabled: true, Thresholds: map[string]int64{"EUR": 500, "ILS": 2000}}
	if !st.IsLowValue(499, "EUR") || st.IsLowValue(500, "EUR") || !st.IsLowValue(1999, "ils") {
		t.Error("threshold comparison is wrong")
	}
	if st.IsLowValue(100, "JPY") {
		t.Error("a currency without a threshold is never low value")
	}
	st.Enabled = false
	if st.IsLowValue(1, "EUR") {
		t.Error("a disabled rule advises nothing")
	}
}

// A label longer than its column wraps onto its own lines, so the row is as
// tall as the label and the value never sits under the label's text.
func TestReportKV_LongLabelWraps(t *testing.T) {
	r := newReport("t", labelsFor("en"), 0, "")
	y0 := r.doc.GetY()
	r.kv([][2]string{{"Short", "1"}})
	short := r.doc.GetY() - y0
	y1 := r.doc.GetY()
	r.kv([][2]string{{labelsFor("en").get("prior_orders"), "2"}})
	long := r.doc.GetY() - y1
	if long < 2*short {
		t.Fatalf("a long label should take at least two lines: short row %.1f, long row %.1f", short, long)
	}
}

// The provider's receipt (PR #359's archive) reaches the answer: its number
// is named in the case summary and the receipt file still renders.
func TestBuildPackage_NamesTheProviderReceipt(t *testing.T) {
	d := Dispute{ID: uuid.New(), Channel: ChannelAPI, AmountMinor: 5900, Currency: "EUR", Reason: "fraudulent", ReasonGroup: ReasonFraud, State: StateNeedsResponse}
	f := sampleFacts(1)
	at := time.Date(2026, 10, 1, 18, 1, 0, 0, time.UTC)
	f.Payment.Receipt = &ProviderReceiptFacts{Number: "1234-5678", URL: "https://pay.stripe.com/receipts/x", ArchivedAt: &at, CopyType: "application/pdf", CopySHA256: "deadbeef", CopyBytes: 34000}
	b, err := BuildPackage(d, f, "en", StripeBudget, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.Fields["uncategorized_text"], "receipt No. 1234-5678") {
		t.Errorf("summary does not name the receipt: %q", b.Fields["uncategorized_text"])
	}
	without, err := BuildPackage(d, sampleFacts(1), "en", StripeBudget, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(without.Fields["uncategorized_text"], "receipt No.") {
		t.Error("a payment without a provider receipt must not mention one")
	}
	for i, rf := range b.Files {
		if rf.Kind == FileReceipt && len(rf.Content) <= len(without.Files[i].Content) {
			t.Error("the receipt file should carry the provider's receipt rows")
		}
	}
}
