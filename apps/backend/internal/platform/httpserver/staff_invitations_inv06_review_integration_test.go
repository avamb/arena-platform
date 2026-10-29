//go:build integration

// INV-06 review fixes (spec 46, section 3.5): the file caps, the dry run
// checks that mirror the transaction, the idempotency key, a request_id on
// two sessions, the Excel quirks of the file.
package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// addSeats gives the session n more free seats B|r|s (10 seats a row) and
// room for them in the ledger.
func (f *inv02Fx) addSeats(n int) {
	f.t.Helper()
	ctx := context.Background()
	for _, sql := range []string{
		`UPDATE inventory_ledger SET capacity_total = 5000 WHERE session_id = $1 AND tier_id IS NULL`,
	} {
		if _, err := f.pool.Exec(ctx, sql, f.session); err != nil {
			f.t.Fatalf("capacity: %v", err)
		}
	}
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		SELECT $1, 'B|' || ((i - 1) / 10 + 1) || '|' || ((i - 1) % 10 + 1), 'B', ((i - 1) / 10 + 1)::text, ((i - 1) % 10 + 1)::text,
		       $2, 'available', 'seat'
		FROM generate_series(1, $3::int) i`, f.session, f.seatTier, n); err != nil {
		f.t.Fatalf("seats: %v", err)
	}
}

// TestINV06_FileCapIsOneHundredGuestsAndTwoHundredFiftyPlaces issues a file
// exactly at the cap in one transaction and logs how long it takes.
func TestINV06_FileCapIsOneHundredGuestsAndTwoHundredFiftyPlaces(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	f.addSeats(251)
	run := uuid.NewString()[:8]
	var b strings.Builder
	b.WriteString("guest,name,email,sector,row,seat\n")
	seat := 0
	for g := 1; g <= 100; g++ {
		per := 2
		if g <= 50 {
			per = 3
		}
		for k := 0; k < per; k++ {
			seat++
			fmt.Fprintf(&b, "%d,Guest %d,g%d.%s@example.test,B,%d,%d\n", g, g, g, run, (seat-1)/10+1, (seat-1)%10+1)
		}
	}
	if seat != 250 {
		t.Fatalf("fixture builds %d seats", seat)
	}

	// One guest and one place over the cap: refused with advice to split.
	over := b.String() + fmt.Sprintf("101,Guest 101,,B,%d,%d\n", 250/10+1, 1)
	code, rep := f.upload(f.o.owner, "?dry_run=true", over, nil)
	if code != http.StatusOK || rep["valid"] != false || !strings.Contains(rep["_raw"].(string), "too_many_places") ||
		!strings.Contains(rep["_raw"].(string), "too_many_guests") || !strings.Contains(rep["_raw"].(string), "split") {
		t.Fatalf("over the cap: %d %.500s", code, rep["_raw"])
	}
	if code, body := f.upload(f.o.owner, "", over, map[string]string{"request_id": uuid.NewString()}); code != http.StatusUnprocessableEntity || f.batchOrders() != 0 {
		t.Fatalf("real upload over the cap: %d %.300s", code, body["_raw"])
	}

	start := time.Now()
	code, body := f.upload(f.o.owner, "", b.String(), map[string]string{"request_id": uuid.NewString()})
	took := time.Since(start)
	if code != http.StatusCreated {
		t.Fatalf("file at the cap: %d %.400s", code, body["_raw"])
	}
	t.Logf("100 guests, 250 places: %s end to end", took)
	if body["places"].(float64) != 250 || body["guests"].(float64) != 100 {
		t.Fatalf("batch: %.300s", body["_raw"])
	}
	if took > 30*time.Second {
		t.Fatalf("250 places took %s, past the router's request timeout", took)
	}
	if got := f.countRows(`SELECT count(*) FROM session_seats WHERE session_id = $1 AND status = 'sold'`, f.session); got != 250 {
		t.Fatalf("sold places = %d, want 250", got)
	}
}

// TestINV06_DryRunMirrorsTheTransaction: a seat without a category and a
// session without capacity are reported by the dry run, not first found by
// the real upload.
func TestINV06_DryRunMirrorsTheTransaction(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE session_seats SET tier_id = NULL WHERE session_id = $1 AND seat_key = 'A|1|7'`, f.session); err != nil {
		t.Fatalf("clear tier: %v", err)
	}
	code, rep := f.upload(f.manager, "?dry_run=true", "name,seat_key\nAna,A|1|7\n", nil)
	if code != http.StatusOK || rep["valid"] != false || !strings.Contains(rep["_raw"].(string), "seat_without_category") {
		t.Fatalf("seat without category: %d %s", code, rep["_raw"])
	}
	// The real upload agrees: 422 and nothing written.
	code, body := f.upload(f.manager, "", "name,seat_key\nAna,A|1|7\n", map[string]string{"request_id": uuid.NewString()})
	if code != http.StatusUnprocessableEntity || f.batchOrders() != 0 {
		t.Fatalf("real upload: %d %s", code, body["_raw"])
	}

	// Capacity: three places left, four asked.
	if _, err := f.pool.Exec(ctx, `UPDATE inventory_ledger SET capacity_total = 3 WHERE session_id = $1 AND tier_id IS NULL`, f.session); err != nil {
		t.Fatalf("capacity: %v", err)
	}
	csv := "name,seat_key\nAna,A|1|4\nBea,A|1|5\nCy,A|1|6\nDi,A|1|8\n"
	code, rep = f.upload(f.manager, "?dry_run=true", csv, nil)
	if code != http.StatusOK || rep["valid"] != false || !strings.Contains(rep["_raw"].(string), "capacity_exceeded") {
		t.Fatalf("capacity: %d %s", code, rep["_raw"])
	}
}

// TestINV06_DeliveryIsPartOfTheIdempotencyKey: the same request_id and file
// with another delivery would make other letters, so it is refused.
func TestINV06_DeliveryIsPartOfTheIdempotencyKey(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	csv := "name,seat_key\nAna,A|1|5\n"
	reqID := uuid.NewString()
	if code, body := f.upload(f.manager, "", csv, map[string]string{"request_id": reqID, "delivery": "pdf_only"}); code != http.StatusCreated {
		t.Fatalf("first: %d %s", code, body["_raw"])
	}
	code, body := f.upload(f.manager, "", csv, map[string]string{"request_id": reqID, "delivery": "my_email"})
	if code != http.StatusConflict || !strings.Contains(body["_raw"].(string), "invitation_batch.request_id_reused") {
		t.Fatalf("other delivery: %d %s", code, body["_raw"])
	}
	if code, body := f.upload(f.manager, "", csv, map[string]string{"request_id": reqID, "delivery": "pdf_only"}); code != http.StatusOK || body["replayed"] != true {
		t.Fatalf("same delivery: %d %s", code, body["_raw"])
	}
}

// TestINV06_OneRequestIDOnTwoSessions_ConflictNotServerError: the unique
// index on (org, request_id) is checked at the statement, so a twin on
// ANOTHER session (not serialized by the sessions row lock) loses with 409.
func TestINV06_OneRequestIDOnTwoSessions_ConflictNotServerError(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	second := uuid.New()
	t.Cleanup(func() {
		for _, sql := range inv02SessionCleanupSQL {
			if _, err := f.pool.Exec(context.Background(), sql, second); err != nil {
				t.Logf("second session cleanup: %v", err)
			}
		}
	})
	for i, s := range []string{
		`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, admission_mode, currency, currency_source, seating_plan_version_id)
		 VALUES ($1, $2, $3, now() + interval '30 days', now() + interval '30 days 2 hours', 100, 'scheduled', 'hybrid', 'EUR', 'override', $4)`,
	} {
		if _, err := f.pool.Exec(ctx, s, second, f.event, f.venue, f.planVer); err != nil {
			t.Fatalf("second session step %d: %v", i, err)
		}
	}
	tier := uuid.New()
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ticket_tiers (id, session_id, name, pricing_mode, price_amount, currency, sort_order) VALUES ($1, $2, 'P2', 'fixed', 1000, 'EUR', 0)`, []any{tier, second}},
		{`INSERT INTO inventory_ledger (session_id, tier_id, capacity_total) VALUES ($1, NULL, 100)`, []any{second}},
		{`INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
		  SELECT $1, 'C|1|' || n, 'C', '1', n::text, $2, 'available', 'seat' FROM generate_series(1, 3) n`, []any{second, tier}},
	} {
		if _, err := f.pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatalf("second session fixture: %v", err)
		}
	}
	reqID := uuid.NewString()
	path2 := "/v1/organizations/" + f.o.org.String() + "/sessions/" + second.String() + "/invitation-batches"

	// Sequentially: the second is a clean 409.
	if code, body := f.upload(f.manager, "", "name,seat_key\nAna,A|1|5\n", map[string]string{"request_id": reqID}); code != http.StatusCreated {
		t.Fatalf("first: %d %s", code, body["_raw"])
	}
	if code, body := f.uploadTo(f.manager, path2, "name,seat_key\nAna,C|1|1\n", map[string]string{"request_id": reqID}); code != http.StatusConflict {
		t.Fatalf("second session, same request_id: %d %s", code, body["_raw"])
	}

	// Concurrently, with a fresh id: one wins, the other is 409, never 500.
	reqID = uuid.NewString()
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i == 0 {
				codes[i], _ = f.upload(f.manager, "", "name,seat_key\nBea,A|1|6\n", map[string]string{"request_id": reqID})
			} else {
				codes[i], _ = f.uploadTo(f.manager, path2, "name,seat_key\nBea,C|1|2\n", map[string]string{"request_id": reqID})
			}
		}()
	}
	wg.Wait()
	created, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflict++
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatalf("codes = %v, want one 201 and one 409", codes)
	}
}

// TestINV06_ExcelQuirks: a "sep=;" first line, a BOM and guest numbers
// written "1" and "01" are one guest, and an unclosed quote is an error
// rather than rows silently swallowed.
func TestINV06_ExcelQuirks(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	csv := "\xEF\xBB\xBFsep=;\r\nguest;name;seat_key\r\n1;Ana;A|1|5\r\n01;Ana;A|1|6\r\n2;Bea;A|1|8\r\n"
	code, rep := f.upload(f.manager, "?dry_run=true", csv, nil)
	if code != http.StatusOK || rep["valid"] != true || rep["guests"].(float64) != 2 || rep["places"].(float64) != 3 {
		t.Fatalf("excel file: %d %s", code, rep["_raw"])
	}
	code, rep = f.upload(f.manager, "?dry_run=true", "name,seat_key\nAna,A|1|5\n\"Bea,A|1|6\nCy,A|1|8\n", nil)
	if code != http.StatusBadRequest || !strings.Contains(rep["_raw"].(string), "invitation_batch.invalid_csv") {
		t.Fatalf("unclosed quote: %d %s", code, rep["_raw"])
	}
}
