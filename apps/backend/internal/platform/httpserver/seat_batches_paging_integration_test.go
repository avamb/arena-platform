//go:build integration

package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
)

// TestSeatBatches_ListIsLightPagedAndTheCardIsFull: the list carries counts,
// never the per-seat arrays; limit/offset page it; the card of one batch
// carries the arrays; a broken body is a 400 with the decoder text in
// details; a revert that puts nothing back is 409 seat_batch.nothing_to_revert.
func TestSeatBatches_ListIsLightPagedAndTheCardIsFull(t *testing.T) {
	x := newSeatBatchFixture(t)
	f := x.f
	base := x.base(x.org)

	paint := fmt.Sprintf(`{"kind":"paint","changes":[{"seat_key":"S|A|1","tier_id":%q},{"seat_key":"S|A|4","tier_id":%q}]}`, x.tier2, x.tier2)
	code, resp := f.do(t, http.MethodPost, base+"/seat-batches", x.manager, paint)
	if code != http.StatusCreated {
		t.Fatalf("paint = %d %v", code, resp)
	}
	paintID := resp["id"].(string)
	closeBody := `{"kind":"close","changes":[{"seat_key":"S|A|1","status":"unavailable"}]}`
	if code, resp := f.do(t, http.MethodPost, base+"/seat-batches", x.manager, closeBody); code != http.StatusCreated {
		t.Fatalf("close = %d %v", code, resp)
	}

	code, resp = f.do(t, http.MethodGet, base+"/seat-batches?limit=1", x.manager, "")
	if code != http.StatusOK || resp["total"].(float64) != 2 || resp["has_more"] != true {
		t.Fatalf("page 1 = %d %v", code, resp)
	}
	item := resp["items"].([]any)[0].(map[string]any)
	if item["kind"] != "close" || item["changed_count"].(float64) != 1 || item["skipped_count"].(float64) != 0 {
		t.Fatalf("page 1 item = %v", item)
	}
	if _, heavy := item["changes"]; heavy {
		t.Fatal("the list must not carry changes")
	}
	if _, heavy := item["skipped"]; heavy {
		t.Fatal("the list must not carry skipped")
	}
	code, resp = f.do(t, http.MethodGet, base+"/seat-batches?limit=1&offset=1", x.manager, "")
	if code != http.StatusOK || resp["has_more"] != false {
		t.Fatalf("page 2 = %d %v", code, resp)
	}
	if it := resp["items"].([]any)[0].(map[string]any); it["id"] != paintID || it["skipped_count"].(float64) != 1 {
		t.Fatalf("page 2 item = %v", it)
	}
	for _, q := range []string{"limit=0", "limit=51", "limit=x", "offset=-1"} {
		if code, resp := f.do(t, http.MethodGet, base+"/seat-batches?"+q, x.manager, ""); code != http.StatusBadRequest || prom0113Code(resp) != "seat_batch.invalid_page" {
			t.Fatalf("%s = %d %v", q, code, resp)
		}
	}

	code, resp = f.do(t, http.MethodGet, base+"/seat-batches/"+paintID, x.manager, "")
	if code != http.StatusOK {
		t.Fatalf("card = %d %v", code, resp)
	}
	if ch, _ := resp["changes"].([]any); len(ch) != 1 {
		t.Fatalf("card changes = %v", resp["changes"])
	}
	if sk, _ := resp["skipped"].([]any); len(sk) != 1 {
		t.Fatalf("card skipped = %v", resp["skipped"])
	}
	if code, resp := f.do(t, http.MethodGet, base+"/seat-batches/"+uuid.NewString(), x.manager, ""); code != http.StatusNotFound || prom0113Code(resp) != "seat_batch.not_found" {
		t.Fatalf("unknown card = %d %v", code, resp)
	}
	if code, resp := f.do(t, http.MethodGet, x.base(x.other)+"/seat-batches/"+paintID, x.outsider, ""); code != http.StatusNotFound || prom0113Code(resp) != "session.not_found" {
		t.Fatalf("foreign card = %d %v", code, resp)
	}

	// Seat 1 was closed after the paint and seat 4 is sold: nothing to put back.
	code, resp = f.do(t, http.MethodPost, base+"/seat-batches/"+paintID+"/revert", x.manager, "")
	if code != http.StatusConflict || prom0113Code(resp) != "seat_batch.nothing_to_revert" {
		t.Fatalf("empty revert = %d %v", code, resp)
	}

	code, resp = f.do(t, http.MethodPost, base+"/seat-batches", x.manager, `{"kind":`)
	if code != http.StatusBadRequest || prom0113Code(resp) != "seat_batch.invalid_body" {
		t.Fatalf("broken body = %d %v", code, resp)
	}
	if e, _ := resp["error"].(map[string]any); e == nil || e["details"] == nil {
		t.Fatalf("broken body has no details: %v", resp)
	}
}

// TestSeatBlock_MoreThanTheBatchCapIs422: a sector selector that expands
// past 10 000 seats answers 422 seating.too_many_seat_keys, never a 500,
// and changes nothing.
func TestSeatBlock_MoreThanTheBatchCapIs422(t *testing.T) {
	x := newSeatBatchFixture(t)
	f := x.f
	if _, err := f.q.DB().Exec(context.Background(), `INSERT INTO session_seats (session_id, seat_key, sector_name, row_name, seat_number, tier_id, status, kind)
	    SELECT $1, 'Big|R|' || gs::text, 'Big', 'R', gs::text, $2, 'available', 'seat'
	    FROM generate_series(1, 10001) gs`, x.session, x.tier1); err != nil {
		t.Fatalf("seed big sector: %v", err)
	}
	path := "/v1/organizations/" + x.org.String() + "/events/" + x.event.String() + "/sessions/" + x.session.String() + "/seats"
	code, resp := f.do(t, http.MethodPatch, path, x.manager, `{"action":"block","sectors":["Big"]}`)
	if code != http.StatusUnprocessableEntity || prom0113Code(resp) != "seating.too_many_seat_keys" {
		t.Fatalf("big block = %d %v", code, resp)
	}
	var closed int
	if err := f.q.DB().QueryRow(context.Background(),
		`SELECT count(*) FROM session_seats WHERE session_id = $1 AND sector_name = 'Big' AND status <> 'available'`, x.session).Scan(&closed); err != nil || closed != 0 {
		t.Fatalf("closed = %d (%v)", closed, err)
	}
}
