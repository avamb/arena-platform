package hmacsscan

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/barcodes/ean13"
)

const testSecret = "whsec_macs_test_secret"

type fakeSubs struct {
	row gen.WebhookSubscriberRow
	err error
}

func (f fakeSubs) GetMACSSubscriberByID(_ context.Context, id uuid.UUID) (gen.WebhookSubscriberRow, error) {
	if f.err != nil {
		return gen.WebhookSubscriberRow{}, f.err
	}
	if id != f.row.ID {
		return gen.WebhookSubscriberRow{}, pgx.ErrNoRows
	}
	return f.row, nil
}

// downPool fails every transaction, as a database that is down.
type downPool struct{}

func (downPool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errors.New("database down")
}

type countingLimiter struct{ left int }

func (l *countingLimiter) Take(string) bool {
	if l.left <= 0 {
		return false
	}
	l.left--
	return true
}

func activeSub() gen.WebhookSubscriberRow {
	org := uuid.New()
	return gen.WebhookSubscriberRow{ID: uuid.New(), Kind: "macs", Active: true, OrgID: &org, SigningSecret: testSecret}
}

func newTestHandler(sub gen.WebhookSubscriberRow, lim Limiter) *Handler {
	h := New(downPool{}, fakeSubs{row: sub}, nil, nil, lim, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.now = func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) }
	return h
}

func post(t *testing.T, h *Handler, subID, sig, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/macs/scan-events", strings.NewReader(body))
	if subID != "" {
		req.Header.Set(HeaderSubscriberID, subID)
	}
	if sig != "" {
		req.Header.Set(HeaderSignature, sig)
	}
	rec := httptest.NewRecorder()
	h.HandleScanEvents(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"scans":[]}`)
	good := SignatureHeader(testSecret, body)
	cases := []struct {
		name   string
		secret string
		header string
		want   bool
	}{
		{"good", testSecret, good, true},
		{"upper-case prefix and hex", testSecret, "SHA256=" + strings.ToUpper(good[len("sha256="):]), true},
		{"other secret", "another", good, false},
		{"no prefix", testSecret, good[len("sha256="):], false},
		{"not hex", testSecret, "sha256=zz", false},
		{"empty secret", "", good, false},
		{"empty header", testSecret, "", false},
		{"tampered", testSecret, good[:len(good)-1] + "0", good[len(good)-1] == '0'},
	}
	for _, c := range cases {
		if got := VerifySignature(c.secret, body, c.header); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	if VerifySignature(testSecret, []byte(`{"scans":[1]}`), good) {
		t.Error("a signature of another body must not verify")
	}
}

func TestValidate(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	id := int64(1000000123)
	zero := int64(0)
	code := ean13.PlatformCode(42)
	base := ScanInput{ScanID: "s-1", SystemTicketID: &id, Result: "admitted", ScannedAt: "2026-10-15T10:00:00+02:00"}
	cases := []struct {
		name string
		mut  func(*ScanInput)
		code string
	}{
		{"valid", func(*ScanInput) {}, ""},
		{"no scan id", func(s *ScanInput) { s.ScanID = "  " }, CodeInvalidScanID},
		{"scan id too long", func(s *ScanInput) { s.ScanID = strings.Repeat("x", 129) }, CodeInvalidScanID},
		{"bad time", func(s *ScanInput) { s.ScannedAt = "2026-10-15 10:00" }, CodeInvalidScannedAt},
		{"no zone", func(s *ScanInput) { s.ScannedAt = "2026-10-15T10:00:00" }, CodeInvalidScannedAt},
		{"future", func(s *ScanInput) { s.ScannedAt = "2026-10-15T12:06:00Z" }, CodeScannedInFuture},
		{"small skew", func(s *ScanInput) { s.ScannedAt = "2026-10-15T12:04:00Z" }, ""},
		{"long ago", func(s *ScanInput) { s.ScannedAt = "2025-01-01T00:00:00Z" }, ""},
		{"bad result", func(s *ScanInput) { s.Result = "denied" }, CodeInvalidResult},
		{"rejected", func(s *ScanInput) { s.Result = "rejected" }, ""},
		{"duplicate", func(s *ScanInput) { s.Result = "duplicate" }, ""},
		{"no ticket", func(s *ScanInput) { s.SystemTicketID = nil }, CodeTicketRequired},
		{"zero id", func(s *ScanInput) { s.SystemTicketID = &zero }, CodeInvalidTicketID},
		{"bad barcode", func(s *ScanInput) { s.Barcode = "1234567890123" }, CodeInvalidBarcode},
		{"barcode only", func(s *ScanInput) { s.SystemTicketID = nil; s.Barcode = code }, ""},
		{"long gate", func(s *ScanInput) { s.Gate = strings.Repeat("g", 201) }, CodeFieldTooLong},
		{"long scanner name", func(s *ScanInput) { s.ScannerName = strings.Repeat("n", 201) }, CodeFieldTooLong},
	}
	for _, c := range cases {
		in := base
		c.mut(&in)
		_, res, ok := validate(in, now)
		if c.code == "" && !ok {
			t.Errorf("%s: rejected %+v", c.name, res)
		}
		if c.code != "" && (ok || res.ErrorCode != c.code || res.Status != StatusRejected) {
			t.Errorf("%s: got ok=%v %+v, want %s", c.name, ok, res, c.code)
		}
	}
	s, _, _ := validate(ScanInput{ScanID: "a", Result: "duplicate", Barcode: code, ScannedAt: "2026-10-15T10:00:00+02:00"}, now)
	if s.storedResult != "denied" || s.credentialCode() != code || !s.scannedAt.Equal(time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("duplicate verdict: %+v", s)
	}
	s, _, _ = validate(base, now)
	if s.storedResult != "admitted" || s.credentialCode() != "system_ticket_id:1000000123" {
		t.Fatalf("admitted by id: %+v", s)
	}
}

func TestLegacySystemID(t *testing.T) {
	if id, ok := legacySystemID(ean13.PlatformCode(1000000123)); !ok || id != 1000000123 {
		t.Fatalf("legacy code: %d %v", id, ok)
	}
	if _, ok := legacySystemID("4006381333931"); ok {
		t.Fatal("a code outside the platform prefix decoded")
	}
}

func TestHandle_RequestLevelRefusals(t *testing.T) {
	sub := activeSub()
	body := `{"scans":[{"scan_id":"a","system_ticket_id":1,"result":"admitted","scanned_at":"2026-10-15T10:00:00Z"}]}`
	sig := SignatureHeader(testSecret, []byte(body))

	if st, out := post(t, newTestHandler(sub, nil), "", sig, body); st != 401 || errCode(out) != "macs.missing_signature" {
		t.Fatalf("no subscriber header: %d %v", st, out)
	}
	if st, out := post(t, newTestHandler(sub, nil), sub.ID.String(), "", body); st != 401 || errCode(out) != "macs.missing_signature" {
		t.Fatalf("no signature: %d %v", st, out)
	}
	if st, out := post(t, newTestHandler(sub, nil), uuid.NewString(), sig, body); st != 401 || errCode(out) != "macs.invalid_signature" {
		t.Fatalf("unknown subscriber: %d %v", st, out)
	}
	if st, out := post(t, newTestHandler(sub, nil), sub.ID.String(), SignatureHeader("wrong", []byte(body)), body); st != 401 || errCode(out) != "macs.invalid_signature" {
		t.Fatalf("bad signature: %d %v", st, out)
	}
	inactive := sub
	inactive.Active = false
	if st, out := post(t, newTestHandler(inactive, nil), sub.ID.String(), sig, body); st != 401 || errCode(out) != "macs.invalid_signature" {
		t.Fatalf("inactive subscriber: %d %v", st, out)
	}
	noSecret := sub
	noSecret.SigningSecret = ""
	if st, _ := post(t, newTestHandler(noSecret, nil), sub.ID.String(), SignatureHeader("", []byte(body)), body); st != 401 {
		t.Fatalf("subscriber without secret: %d", st)
	}

	signed := func(b string) (string, string) { return b, SignatureHeader(testSecret, []byte(b)) }
	b, s := signed(`not json`)
	if st, out := post(t, newTestHandler(sub, nil), sub.ID.String(), s, b); st != 400 || errCode(out) != "macs.invalid_body" {
		t.Fatalf("not json: %d %v", st, out)
	}
	b, s = signed(`{"scans":[]}`)
	if st, out := post(t, newTestHandler(sub, nil), sub.ID.String(), s, b); st != 400 || errCode(out) != "macs.empty_batch" {
		t.Fatalf("empty: %d %v", st, out)
	}
	many := make([]ScanInput, MaxBatchSize+1)
	for i := range many {
		many[i] = ScanInput{ScanID: "x"}
	}
	raw, _ := json.Marshal(map[string]any{"scans": many})
	if st, out := post(t, newTestHandler(sub, nil), sub.ID.String(), SignatureHeader(testSecret, raw), string(raw)); st != 413 || errCode(out) != "macs.batch_too_large" {
		t.Fatalf("too many: %d %v", st, out)
	}
	huge := `{"scans":[{"scan_id":"` + strings.Repeat("x", MaxBodyBytes) + `"}]}`
	if st, out := post(t, newTestHandler(sub, nil), sub.ID.String(), SignatureHeader(testSecret, []byte(huge)), huge); st != 413 || errCode(out) != "macs.body_too_large" {
		t.Fatalf("too big: %d %v", st, out)
	}

	lim := &countingLimiter{left: 1}
	h := newTestHandler(sub, lim)
	b, s = signed(`{"scans":[{"scan_id":""}]}`)
	if st, _ := post(t, h, sub.ID.String(), s, b); st != 202 {
		t.Fatalf("first batch under the limit: %d", st)
	}
	if st, out := post(t, h, sub.ID.String(), s, b); st != 429 || errCode(out) != "macs.rate_limited" {
		t.Fatalf("over the limit: %d %v", st, out)
	}

	if st, out := post(t, New(nil, nil, nil, nil, nil, nil), sub.ID.String(), sig, body); st != 503 || errCode(out) != "dependency.database_unavailable" {
		t.Fatalf("no database: %d %v", st, out)
	}
	if st, out := post(t, New(downPool{}, fakeSubs{err: errors.New("boom")}, nil, nil, nil, nil), sub.ID.String(), sig, body); st != 503 || errCode(out) != "macs.temporarily_unavailable" {
		t.Fatalf("subscriber lookup failed: %d %v", st, out)
	}
}

// A batch the database cannot record at all is answered 503, so MACS sends
// it again; one with a scan judged without the database is answered 202,
// the failed scans marked retryable.
func TestHandle_PartialSuccessAndRetryable(t *testing.T) {
	sub := activeSub()
	h := newTestHandler(sub, nil)
	allDown := `{"scans":[{"scan_id":"a","system_ticket_id":1,"result":"admitted","scanned_at":"2026-10-15T10:00:00Z"}]}`
	if st, out := post(t, h, sub.ID.String(), SignatureHeader(testSecret, []byte(allDown)), allDown); st != 503 || errCode(out) != "macs.temporarily_unavailable" {
		t.Fatalf("database down: %d %v", st, out)
	}
	mixed := `{"scans":[` +
		`{"scan_id":"late","system_ticket_id":1,"result":"admitted","scanned_at":"2026-10-15T10:05:00Z"},` +
		`{"scan_id":"bad","system_ticket_id":1,"result":"maybe","scanned_at":"2026-10-15T10:00:00Z","unknown_key":1},` +
		`{"scan_id":"early","barcode":"` + ean13.PlatformCode(7) + `","result":"rejected","scanned_at":"2026-10-15T09:00:00Z"}]}`
	st, out := post(t, h, sub.ID.String(), SignatureHeader(testSecret, []byte(mixed)), mixed)
	if st != 202 {
		t.Fatalf("mixed batch: %d %v", st, out)
	}
	results, _ := out["results"].([]any)
	if len(results) != 3 || out["rejected"].(float64) != 3 || out["accepted"].(float64) != 0 {
		t.Fatalf("mixed batch body: %v", out)
	}
	want := []struct {
		id, code  string
		retryable bool
	}{{"late", CodeUnavailable, true}, {"bad", CodeInvalidResult, false}, {"early", CodeUnavailable, true}}
	for i, w := range want {
		r := results[i].(map[string]any)
		if r["scan_id"] != w.id || r["error_code"] != w.code || (r["retryable"] == true) != w.retryable || r["status"] != StatusRejected {
			t.Errorf("result %d: %v, want %+v", i, r, w)
		}
		if _, ok := r["ticket_id"]; !ok {
			t.Errorf("result %d has no ticket_id key", i)
		}
	}
}
