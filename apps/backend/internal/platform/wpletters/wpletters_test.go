package wpletters

import (
	"os"
	"testing"
	"time"
)

func loadFixture(t *testing.T) Export {
	t.Helper()
	f, err := os.Open("testdata/wp-letters.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	e, err := Parse(f)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestParse_FixtureShape(t *testing.T) {
	e := loadFixture(t)
	if len(e.Letters) != 5 || e.OrdersScanned != 5 {
		t.Fatalf("letters=%d scanned=%d", len(e.Letters), e.OrdersScanned)
	}
	if got := e.Letters[3].SystemID(); got != 1000000504 {
		t.Fatalf("numeric system id: %d", got)
	}
	if got := e.Letters[4].SystemID(); got != 0 {
		t.Fatalf("empty system id: %d", got)
	}
}

func TestMap_Opened(t *testing.T) {
	l, ok := Map(loadFixture(t).Letters[0])
	if !ok || l.Status != StatusOpened {
		t.Fatalf("%+v ok=%v", l, ok)
	}
	if l.OpenedAt == nil || l.OpenedAt.Unix() != 1790850900 || l.DeliveredAt == nil || l.DeliveredAt.Unix() != 1790848810 {
		t.Fatalf("times: %+v", l)
	}
	if l.SentAt != time.Date(2026, 10, 1, 10, 0, 5, 0, time.UTC) {
		t.Fatalf("sent_at %v", l.SentAt)
	}
	if l.MessageID != "<202610011000.111@smtp-relay.example.test>" {
		t.Fatalf("message id %q", l.MessageID)
	}
}

func TestMap_ProxyOpenIsDeliveredNotOpened(t *testing.T) {
	l, _ := Map(loadFixture(t).Letters[1])
	if l.Status != StatusDelivered || l.OpenedAt != nil || l.ProxyOpenedAt == nil || l.ProxyOpenedAt.Unix() != 1790939000 {
		t.Fatalf("%+v", l)
	}
}

func TestMap_Failure(t *testing.T) {
	l, _ := Map(loadFixture(t).Letters[2])
	if l.Status != StatusFailed || l.FailureEvent == nil || *l.FailureEvent != "hard_bounce" ||
		l.FailureReason == nil || *l.FailureReason != "mailbox does not exist" || l.FailedAt == nil || l.FailedAt.Unix() != 1791029100 {
		t.Fatalf("%+v", l)
	}
}

func TestMap_NoStatusIsSent(t *testing.T) {
	l, _ := Map(loadFixture(t).Letters[3])
	if l.Status != StatusSent || l.DeliveredAt != nil || l.OpenedAt != nil {
		t.Fatalf("%+v", l)
	}
}

func TestMap_FailedFlagWithoutFailureStateIsError(t *testing.T) {
	r := loadFixture(t).Letters[3]
	r.Failed = true
	l, _ := Map(r)
	if l.Status != StatusFailed || l.FailureEvent == nil || *l.FailureEvent != "error" {
		t.Fatalf("%+v", l)
	}
}

func TestMap_FallsBackToTimesThenOrderDate(t *testing.T) {
	r := Record{Recipient: "a@example.test", Times: Times{Sent: 1790848805}, OrderDate: "2026-10-01T09:59:00Z"}
	l, ok := Map(r)
	if !ok || l.SentAt.Unix() != 1790848805 {
		t.Fatalf("%+v", l)
	}
	r.Times = Times{}
	l, ok = Map(r)
	if !ok || l.SentAt != time.Date(2026, 10, 1, 9, 59, 0, 0, time.UTC) {
		t.Fatalf("%+v", l)
	}
	if _, ok := Map(Record{Recipient: "a@example.test"}); ok {
		t.Fatal("a record with no time at all must be refused")
	}
	if _, ok := Map(Record{SentAt: "2026-10-01T09:59:00Z"}); ok {
		t.Fatal("a record with no recipient must be refused")
	}
}

func TestRank_OrdersTheStatuses(t *testing.T) {
	if !(Rank(StatusSent) < Rank(StatusDelivered) && Rank(StatusDelivered) < Rank(StatusOpened)) {
		t.Fatal("rank order")
	}
}

// Every status Map can produce must be one the email_letters CHECK allows.
func TestMap_StatusesAreAllowedByTheCheckConstraint(t *testing.T) {
	allowed := map[string]bool{"sending": true, "sent": true, "delayed": true, "delivered": true, "opened": true, "failed": true}
	for _, r := range loadFixture(t).Letters {
		if l, ok := Map(r); ok && !allowed[l.Status] {
			t.Fatalf("status %q", l.Status)
		}
	}
}
