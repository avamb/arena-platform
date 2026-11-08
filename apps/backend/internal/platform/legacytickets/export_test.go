package legacytickets

import (
	"os"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T) Export {
	t.Helper()
	f, err := os.Open("testdata/legacy-tickets.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestParse_Fixture(t *testing.T) {
	doc := loadFixture(t)
	if len(doc.Orders) != 6 || len(doc.Actions) != 2 || doc.Actions[0] != 267465 {
		t.Fatalf("orders=%d actions=%v", len(doc.Orders), doc.Actions)
	}
	o := doc.Orders[0]
	if o.WCOrderID != "3461" || o.Bil24OrderID != "4518801" || o.LiveIDs == nil || len(*o.LiveIDs) != 2 {
		t.Fatalf("first order = %+v", o)
	}
	if got := o.Tickets[0].CategoryName(); got != "Входной билет" {
		t.Fatalf("category = %q", got)
	}
	// The second order spells numbers as strings and the category as an object.
	o2 := doc.Orders[1]
	if o2.Bil24OrderID != "4518877" || o2.LiveIDs != nil || o2.Tickets[0].ID != 80000010 || o2.Items[0].Bil24ActionID != 267466 {
		t.Fatalf("second order = %+v", o2)
	}
	if got := o2.Tickets[0].CategoryName(); got != "Входной билет" {
		t.Fatalf("object category = %q", got)
	}
}

func TestPrepare_LiveTicketsOnly(t *testing.T) {
	doc := loadFixture(t)
	p, prob := Prepare(doc.Orders[0])
	if prob != nil {
		t.Fatalf("problem: %v", prob)
	}
	if p.Bil24OrderID != 4518801 || p.WCOrderID != "3461" || p.Status != "completed" {
		t.Fatalf("prepared = %+v", p)
	}
	if p.BuyerName != "Dana Example" || p.Email != "dana.buyer@example.test" || p.Phone != "+972 50 000 0001" || p.Locale != "ru" {
		t.Fatalf("buyer = %q %q %q %q", p.BuyerName, p.Email, p.Phone, p.Locale)
	}
	if len(p.Tickets) != 2 || p.Refunded != 1 || p.Tickets[0].Bil24TicketID != 80000001 || p.Tickets[1].Bil24TicketID != 80000003 {
		t.Fatalf("tickets = %+v refunded=%d", p.Tickets, p.Refunded)
	}
	if p.Tickets[0].ActionID != 267465 || p.Tickets[0].ActionEventID != 1300001 || p.Tickets[0].Barcode != "2100000000017" {
		t.Fatalf("ticket = %+v", p.Tickets[0])
	}
	if !p.PaidAt.Equal(time.Date(2026, 8, 30, 17, 25, 0, 0, time.UTC)) || p.LiveIDsMissing {
		t.Fatalf("paid=%s missing=%v", p.PaidAt, p.LiveIDsMissing)
	}
}

func TestPrepare_NoLiveIDsTakesAllAndFallsBackToOrderDate(t *testing.T) {
	doc := loadFixture(t)
	p, prob := Prepare(doc.Orders[1])
	if prob != nil {
		t.Fatalf("problem: %v", prob)
	}
	if !p.LiveIDsMissing || len(p.Tickets) != 1 || len(p.Warnings) != 2 {
		t.Fatalf("prepared = %+v", p)
	}
	if !p.PaidAt.Equal(p.CreatedAt) || p.Locale != "he" || p.Status != "processing" {
		t.Fatalf("paid=%s created=%s locale=%q", p.PaidAt, p.CreatedAt, p.Locale)
	}
	// The ticket's Bil24 session is on no line: the order's only action counts.
	if p.Tickets[0].ActionID != 267466 || p.Tickets[0].ActionEventID != 1300002 {
		t.Fatalf("ticket = %+v", p.Tickets[0])
	}
}

func TestPrepare_SkipsAndRefusals(t *testing.T) {
	doc := loadFixture(t)
	cases := []struct {
		order int
		code  string
	}{
		{2, SkipStatus},
		{3, SkipNotBil24Era},
		{4, SkipNoLiveTickets},
		{5, RefuseMissingData},
	}
	for _, c := range cases {
		_, prob := Prepare(doc.Orders[c.order])
		if prob == nil || prob.Code != c.code {
			t.Errorf("order %d: problem %v, want %s", c.order, prob, c.code)
		}
	}
	if !(&Problem{Code: SkipPastSession}).Skipped() || (&Problem{Code: RefuseBarcodeTaken}).Skipped() {
		t.Fatal("Skipped() mixes skips and refusals")
	}

	o := doc.Orders[0]
	o.Billing.Email = " "
	if _, prob := Prepare(o); prob == nil || prob.Code != RefuseMissingData || !strings.Contains(prob.Detail, "e-mail") {
		t.Fatalf("no e-mail: %v", prob)
	}
	o = doc.Orders[0]
	o.Tickets = append(o.Tickets, o.Tickets[0])
	if _, prob := Prepare(o); prob == nil || prob.Code != RefuseDuplicateInFile {
		t.Fatalf("duplicate ticket: %v", prob)
	}
	o = doc.Orders[0]
	o.Status = "wc-refunded"
	if _, prob := Prepare(o); prob == nil || prob.Code != SkipStatus {
		t.Fatalf("refunded: %v", prob)
	}
}

func TestParseMajor(t *testing.T) {
	cases := []struct {
		in    string
		scale int64
		want  int64
		bad   bool
	}{
		{"250", 100, 25000, false},
		{"18.9", 100, 1890, false},
		{"12.50", 100, 1250, false},
		{"0.05", 100, 5, false},
		{"", 100, 0, false},
		{"250.000", 100, 25000, false},
		{"18.905", 100, 0, true},
		{"abc", 100, 0, true},
		{"1500", 1, 1500, false},
	}
	for _, c := range cases {
		got, err := ParseMajor(c.in, c.scale)
		if (err != nil) != c.bad || (!c.bad && got != c.want) {
			t.Errorf("ParseMajor(%q,%d) = %d, %v", c.in, c.scale, got, err)
		}
	}
}

func TestTicketMoney(t *testing.T) {
	m, err := TicketMoney(PreparedTicket{Bil24TicketID: 1, Price: "250.00", Discount: "25", Charge: "12.5", Total: "237.5"}, 100)
	if err != nil || m != (Money{Price: 25000, Discount: 2500, Charge: 1250, Total: 23750}) {
		t.Fatalf("money = %+v %v", m, err)
	}
	m, err = TicketMoney(PreparedTicket{Bil24TicketID: 2, Price: "100"}, 100)
	if err != nil || m.Total != 10000 {
		t.Fatalf("no total = %+v %v", m, err)
	}
	if _, err := TicketMoney(PreparedTicket{Bil24TicketID: 3, Price: "100", Total: "90"}, 100); err == nil {
		t.Fatal("a total that does not add up was accepted")
	}
}

func TestNormalizeLocale(t *testing.T) {
	for in, want := range map[string]string{"ru": "ru", "EN": "en", "he_IL": "he", "iw": "he", "ar": "", "": ""} {
		if got := NormalizeLocale(in); got != want {
			t.Errorf("NormalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsBil24OrderNumber(t *testing.T) {
	for n, want := range map[int64]bool{4518801: true, 1000000: true, 9999999: true, 999999: false, 1000000731: false} {
		if IsBil24OrderNumber(n) != want {
			t.Errorf("IsBil24OrderNumber(%d) != %v", n, want)
		}
	}
}

func TestPickTier(t *testing.T) {
	a := tierInfo{ID: [16]byte{1}, Name: "Входной билет"}
	b := tierInfo{ID: [16]byte{2}, Name: "VIP"}
	if id, prob := pickTier([]tierInfo{a}, "anything"); prob != nil || id != a.ID {
		t.Fatal("the only category is not taken")
	}
	if id, prob := pickTier([]tierInfo{a, b}, " vip "); prob != nil || id != b.ID {
		t.Fatalf("by name: %v %v", id, prob)
	}
	if _, prob := pickTier([]tierInfo{a, b}, "Balcony"); prob == nil || prob.Code != RefuseAmbiguousCategory {
		t.Fatalf("unknown name: %v", prob)
	}
	if _, prob := pickTier(nil, ""); prob == nil || prob.Code != RefuseNoGACategory {
		t.Fatalf("none: %v", prob)
	}
}
