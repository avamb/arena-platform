package eventbot

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCSVRows(t *testing.T) {
	t.Parallel()
	bom := "\xef\xbb\xbf"
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"header only", bom + "\"A\";\"B\"\r\n", 0},
		{"two rows", bom + "\"A\";\"B\"\r\n\"1\";\"2\"\r\n\"3\";\"4\"\r\n", 2},
		{"no final newline", "A;B\r\n1;2\r\n3;4", 2},
		{"a quoted line break is one record", "\"A\";\"B\"\r\n\"multi\nline name\";\"2\"\r\n\"3\";\"4\"\r\n", 2},
		{"blank lines are not rows", "A;B\r\n\r\n1;2\r\n\r\n", 1},
		{"escaped quotes", "A;B\r\n\"he said \"\"hi\nthere\"\"\";\"2\"\r\n", 1},
	}
	for _, c := range cases {
		if got := CSVRows([]byte(c.in)); got != c.want {
			t.Errorf("%s: CSVRows = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestSafeFileName(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		`attachment; filename="sales_swan-lake_2026-10-10.csv"`: "sales_swan-lake_2026-10-10.csv",
		`attachment; filename="../../etc/passwd"`:               "etc_passwd.csv",
		`attachment; filename="продажи.csv"`:                    "export.csv",
		`attachment; filename="report"`:                         "report.csv",
		``:                                                      "export.csv",
		`inline`:                                                "export.csv",
	} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDownloadCSV_SendsTheBotsHeadersAndCountsRows(t *testing.T) {
	t.Parallel()
	orgID, sessionID := uuid.New(), uuid.New()
	var gotPath, gotAccept, gotAuth, gotChannel, gotReason string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept, gotAuth = r.URL.RequestURI(), r.Header.Get("Accept"), r.Header.Get("Authorization")
		gotChannel, gotReason = r.Header.Get("X-Client-Channel"), r.Header.Get("X-Admin-Reason")
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="sales_x_2026-10-10.csv"`)
		_, _ = w.Write([]byte("\xef\xbb\xbf\"Order\";\"Barcode\"\r\n\"1\";=\"4600051000001\"\r\n\"2\";=\"0123456789012\"\r\n"))
	}))
	defer srv.Close()
	c := NewArenaClient(srv.URL, "svc", srv.Client())
	f, err := c.SessionSalesCSV(context.Background(), "jwt", orgID, sessionID, "ru")
	if err != nil {
		t.Fatalf("SessionSalesCSV: %v", err)
	}
	if gotPath != "/v1/organizations/"+orgID.String()+"/sessions/"+sessionID.String()+"/sales.csv?locale=ru" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAccept != "text/csv" || gotAuth != "Bearer jwt" || gotChannel != ClientChannel || gotReason != AdminReason {
		t.Errorf("headers: accept=%q auth=%q channel=%q reason=%q", gotAccept, gotAuth, gotChannel, gotReason)
	}
	if f.Name != "sales_x_2026-10-10.csv" || f.Rows != 2 || !strings.HasPrefix(string(f.Body), "\xef\xbb\xbf") {
		t.Errorf("file = %q rows=%d", f.Name, f.Rows)
	}
}

func TestDownloadCSV_TooManyRowsIs413(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_, _ = w.Write([]byte(`{"error":{"code":"export.too_many_rows","message":"narrow it"}}`))
	}))
	defer srv.Close()
	c := NewArenaClient(srv.URL, "svc", srv.Client())
	_, err := c.EventSalesCSV(context.Background(), "jwt", uuid.New(), uuid.New(), "en")
	if !IsAPIError(err, http.StatusRequestEntityTooLarge) || APIErrorCode(err) != "export.too_many_rows" {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadCSV_ForeignRowIs404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"session.not_found","message":"x"}}`))
	}))
	defer srv.Close()
	c := NewArenaClient(srv.URL, "svc", srv.Client())
	_, err := c.SessionSummaryCSV(context.Background(), "jwt", uuid.New(), uuid.New(), "")
	if !IsAPIError(err, http.StatusNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadCSV_OverTheByteCapIsRefused(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := []byte(strings.Repeat("x", 1<<20))
		for i := 0; i < 21; i++ {
			_, _ = w.Write(chunk)
		}
	}))
	defer srv.Close()
	c := NewArenaClient(srv.URL, "svc", srv.Client())
	_, err := c.SessionSalesCSV(context.Background(), "jwt", uuid.New(), uuid.New(), "")
	if !errors.Is(err, errCSVTooLarge) {
		t.Fatalf("err = %v, want errCSVTooLarge", err)
	}
}
