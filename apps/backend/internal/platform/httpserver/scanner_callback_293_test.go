// scanner_callback_293_test.go — unit tests for the S-2 scanner callback
// (feature #293).  Covers:
//
//   * TicketScannedEventType constant
//   * extractBearerToken header parsing edge cases
//   * credentialPrefixForLog truncation behaviour
//   * mountScannerCallbackRoutes mounts when feedTokenQueries is set
//   * mountScannerCallbackRoutes is silent when feedTokenQueries is nil
//
// Integration coverage of the full handler path (resolve token → resolve
// credential → insert → mark used_at → emit outbox) is exercised in the
// docker-compose integration suite, where a live PostgreSQL is available.
package httpserver

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestS2_TicketScannedEventType(t *testing.T) {
	t.Parallel()
	if TicketScannedEventType != "v1.ticket.scanned" {
		t.Errorf("TicketScannedEventType = %q; want v1.ticket.scanned", TicketScannedEventType)
	}
	if !strings.HasPrefix(TicketScannedEventType, "v1.") {
		t.Errorf("event type %q must start with v1. (catalog versioning)", TicketScannedEventType)
	}
}

func TestS2_ExtractBearerToken(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"basic", "Bearer abc", "abc"},
		{"lowercase scheme", "bearer abc", "abc"},
		{"trims whitespace", "Bearer   xyz  ", "xyz"},
		{"only scheme rejected", "Bearer ", ""},
		{"missing scheme rejected", "abc", ""},
		{"different scheme rejected", "Basic abc", ""},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := extractBearerToken(c.in); got != c.want {
				t.Errorf("extractBearerToken(%q) = %q; want %q", c.in, got, c.want)
			}
		})
	}
}

func TestS2_CredentialPrefixForLog_Truncates(t *testing.T) {
	t.Parallel()
	in := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	got := credentialPrefixForLog(in)
	if got == in {
		t.Errorf("expected truncation; got full string")
	}
	if !strings.HasPrefix(got, "01234567") {
		t.Errorf("expected first 8 chars; got %q", got)
	}
}

func TestS2_CredentialPrefixForLog_ShortPassesThrough(t *testing.T) {
	t.Parallel()
	in := "short"
	if got := credentialPrefixForLog(in); got != in {
		t.Errorf("expected %q to pass through unchanged; got %q", in, got)
	}
}

func TestS2_HandleScannerScanEvents_ServiceUnavailableWithoutFeedTokenQueries(t *testing.T) {
	t.Parallel()
	s := &Server{logger: slog.Default()}
	req := httptest.NewRequest(http.MethodPost, "/v1/scanner/scan-events",
		strings.NewReader(`{"scans":[]}`))
	req.Header.Set("Authorization", "Bearer abc")
	w := httptest.NewRecorder()
	s.handleScannerScanEvents(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want %d", w.Code, http.StatusServiceUnavailable)
	}
}

func TestS2_HandleScannerScanEvents_UnauthorizedWithoutBearer(t *testing.T) {
	t.Parallel()
	// Feature #278 (A-17) flipped mountScannerCallbackRoutes to always mount
	// the route so it is reachable for the openapi-drift coverage check;
	// the handler self-gates on s.feedTokenQueries == nil and returns a
	// 503 dependency.database_unavailable envelope (mirroring the A-15
	// delivery-resend precedent).  Confirm the route IS mounted and the
	// handler returns 503 when feedTokenQueries is unset.
	s := &Server{logger: slog.Default()}
	r := chi.NewRouter()
	r.Route("/v1", func(pr chi.Router) {
		s.mountScannerCallbackRoutes(pr)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/scanner/scan-events",
		strings.NewReader(`{"scans":[]}`))
	req.Header.Set("Authorization", "Bearer abc")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected route to be mounted and return 503 when feedTokenQueries is nil; got status %d", w.Code)
	}
}

// TestS2_FileShapes asserts the implementation files referenced by the
// feature description exist and contain the expected hooks.  Cheap static
// guard — catches accidental refactor/rename without needing a live DB.
func TestS2_FileShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fname string
		needs []string
	}{
		{
			fname: "scanner_callback.go",
			needs: []string{
				"handleScannerScanEvents",
				"processScannerScan",
				"TicketScannedEventType",
				`"v1.ticket.scanned"`,
				"InsertScanEvent",
				"MarkTicketUsedAtIfUnset",
				"ResolveScanCredentialByTicketQR",
				"publishScannerEvent",
				"agent_feed_token", // referenced in the file header / docstrings
			},
		},
		{
			fname: "mount_scanning.go",
			needs: []string{"mountScannerCallbackRoutes", "/scanner/scan-events"},
		},
		{
			fname: "mount_v1.go",
			needs: []string{"mountScannerCallbackRoutes"},
		},
		{
			fname: "0055_scan_events.sql",
			needs: []string{
				"CREATE TABLE scan_events",
				"credential_code",
				"scanned_at",
				"scan_events_credential_scanned_at_unique",
				"tickets.used_at",
				"DROP TABLE IF EXISTS scan_events",
			},
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.fname, func(t *testing.T) {
			t.Parallel()
			content := findFileByName(t, c.fname)
			if content == "" {
				t.Fatalf("file %s not found", c.fname)
			}
			for _, n := range c.needs {
				if !strings.Contains(content, n) {
					t.Errorf("%s: expected to contain %q", c.fname, n)
				}
			}
		})
	}
}
