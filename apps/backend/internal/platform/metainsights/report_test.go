package metainsights

import (
	"testing"
	"time"
)

func TestParsePeriod(t *testing.T) {
	madrid, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		t.Fatal(err)
	}
	// 23:30 UTC on 2026-11-22 is already 2026-11-23 in Madrid.
	now := time.Date(2026, 11, 22, 23, 30, 0, 0, time.UTC)
	cases := []struct {
		name, from, to string
		loc            *time.Location
		wantFrom       string
		wantTo         string
		wantCode       string
	}{
		{"default UTC", "", "", nil, "2026-10-24", "2026-11-22", ""},
		{"default in the account timezone", "", "", madrid, "2026-10-25", "2026-11-23", ""},
		{"both given", "2026-01-01", "2026-01-31", madrid, "2026-01-01", "2026-01-31", ""},
		{"only to", "", "2026-03-30", nil, "2026-03-01", "2026-03-30", ""},
		{"only from", "2026-11-01", "", nil, "2026-11-01", "2026-11-22", ""},
		{"one day", "2026-05-05", "2026-05-05", nil, "2026-05-05", "2026-05-05", ""},
		{"400 days is the limit", "2025-01-01", "2026-02-04", nil, "2025-01-01", "2026-02-04", ""},
		{"401 days", "2025-01-01", "2026-02-05", nil, "", "", ErrCodeRangeTooLong},
		{"from after to", "2026-02-02", "2026-02-01", nil, "", "", ErrCodeInvalidRange},
		{"bad from", "2026-02-30", "", nil, "", "", ErrCodeInvalidDate},
		{"bad to", "", "yesterday", nil, "", "", ErrCodeInvalidDate},
		{"date with time", "2026-02-01T00:00:00Z", "", nil, "", "", ErrCodeInvalidDate},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			from, to, perr := ParsePeriod(c.from, c.to, c.loc, now)
			if c.wantCode != "" {
				if perr == nil || perr.Code != c.wantCode {
					t.Fatalf("got %v, want code %s", perr, c.wantCode)
				}
				return
			}
			if perr != nil {
				t.Fatalf("unexpected error %v", perr)
			}
			if got := from.Format(dateLayout); got != c.wantFrom {
				t.Errorf("from = %s, want %s", got, c.wantFrom)
			}
			if got := to.Format(dateLayout); got != c.wantTo {
				t.Errorf("to = %s, want %s", got, c.wantTo)
			}
		})
	}
}

func TestPickMoney(t *testing.T) {
	if m := pickMoney(nil, "EUR"); m.Currency != "" || m.PaidOrders != 0 {
		t.Fatalf("empty: %+v", m)
	}
	by := map[string]*Money{}
	addMoney(by, "USD", 3, 900)
	addMoney(by, "EUR", 1, 100)
	addMoney(by, "EUR", 1, 50)
	if m := pickMoney(by, "EUR"); m.Currency != "EUR" || m.PaidOrders != 2 || m.NetMinor != 150 {
		t.Errorf("prefer EUR: %+v", m)
	}
	if m := pickMoney(by, "GBP"); m.Currency != "USD" || m.PaidOrders != 3 {
		t.Errorf("most orders: %+v", m)
	}
	if m := pickMoney(by, ""); m.Currency != "USD" {
		t.Errorf("no preference: %+v", m)
	}
}
