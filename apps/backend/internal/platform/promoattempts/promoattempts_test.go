package promoattempts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestReason_MapsEveryRuleCode(t *testing.T) {
	cases := map[string]string{
		"":                             "",
		"promo.not_found":              ReasonUnknownCode,
		"promo.not_active":             ReasonPaused,
		"promo.not_yet_valid":          ReasonOutsideWindow,
		"promo.expired":                ReasonExpired,
		"promo.session_not_applicable": ReasonWrongSession,
		"promo.tier_not_applicable":    ReasonWrongTier,
		"promo.invalid_order_amount":   ReasonMinOrder,
		"promo.currency_mismatch":      ReasonCurrency,
		"promo.exhausted":              ReasonMaxUses,
		"promo.per_customer_limit":     ReasonMaxUsesPerCustomer,
		"something.new":                ReasonOther,
	}
	for in, want := range cases {
		if got := Reason(in); got != want {
			t.Errorf("Reason(%q) = %q, want %q", in, got, want)
		}
	}
}

func recorderCapturing(rows *[]Row, mu *sync.Mutex, err error) *Recorder {
	return New(nil, "salt", nil).WithSyncWrites().WithInsert(func(_ context.Context, rw Row) error {
		mu.Lock()
		*rows = append(*rows, rw)
		mu.Unlock()
		return err
	})
}

func TestRecord_BuildsTheRow(t *testing.T) {
	var rows []Row
	var mu sync.Mutex
	r := recorderCapturing(&rows, &mu, nil)
	org, sess := uuid.New(), uuid.New()
	long := strings.Repeat("я", 100)

	r.Record(context.Background(), Attempt{
		OrgID: org, Surface: SurfaceGatewayAddPromoCodes, Code: "  " + long + "  ",
		SessionIDs: []string{sess.String(), sess.String(), "not-a-uuid"},
		ErrCode:    "promo.session_not_applicable", ClientIP: "203.0.113.7",
	})
	r.Record(context.Background(), Attempt{OrgID: org, Surface: SurfaceWidgetQuote, Code: "RED30"})

	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	a := rows[0]
	if a.Result != ResultRejected || a.Reason != ReasonWrongSession {
		t.Errorf("verdict = %s/%s, want rejected/wrong_session", a.Result, a.Reason)
	}
	if n := len([]rune(a.Code)); n != 64 {
		t.Errorf("code cut to %d runes, want 64", n)
	}
	if len(a.Sessions) != 1 || a.Sessions[0] != sess {
		t.Errorf("sessions = %v, want just %s", a.Sessions, sess)
	}
	if a.IPHash == "" || strings.Contains(a.IPHash, "203.0.113.7") {
		t.Errorf("ip hash = %q, want a hash that does not contain the address", a.IPHash)
	}
	if a.DedupKey != "ip:"+a.IPHash {
		t.Errorf("dedup key = %q, want the address hash as fallback", a.DedupKey)
	}
	b := rows[1]
	if b.Result != ResultApplied || b.Reason != "" || b.IPHash != "" {
		t.Errorf("applied row = %+v", b)
	}
}

func TestRecord_IgnoresEmptyCodeAndUnknownOrgAndNil(t *testing.T) {
	var rows []Row
	var mu sync.Mutex
	r := recorderCapturing(&rows, &mu, nil)
	r.Record(context.Background(), Attempt{OrgID: uuid.New(), Code: "   "})
	r.Record(context.Background(), Attempt{Code: "X"})
	var nilRec *Recorder
	nilRec.Record(context.Background(), Attempt{OrgID: uuid.New(), Code: "X"})
	nilRec.Wait()
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
}

// A journal that fails — an error or a panic — must never reach the caller.
func TestRecord_FailureNeverReachesTheCaller(t *testing.T) {
	var rows []Row
	var mu sync.Mutex
	failing := recorderCapturing(&rows, &mu, errors.New("database is down"))
	failing.Record(context.Background(), Attempt{OrgID: uuid.New(), Surface: SurfaceOrgValidate, Code: "A"})

	panicking := New(nil, "s", nil).WithSyncWrites().WithInsert(func(context.Context, Row) error { panic("boom") })
	panicking.Record(context.Background(), Attempt{OrgID: uuid.New(), Surface: SurfaceOrgValidate, Code: "A"})

	async := New(nil, "s", nil).WithInsert(func(context.Context, Row) error { return errors.New("down") })
	async.Record(context.Background(), Attempt{OrgID: uuid.New(), Surface: SurfaceOrgValidate, Code: "A"})
	async.Wait()
}

func TestRecord_BusyJournalDropsInsteadOfBlocking(t *testing.T) {
	release := make(chan struct{})
	r := New(nil, "s", nil).WithInsert(func(context.Context, Row) error { <-release; return nil })
	for i := 0; i < maxInFlight+10; i++ {
		r.Record(context.Background(), Attempt{OrgID: uuid.New(), Surface: SurfaceOrgValidate, Code: "A"})
	}
	close(release)
	r.Wait()
}
