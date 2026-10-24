package http_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpadapter "github.com/abhteam/arena_new/apps/backend/internal/adapters/http"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/logging"
)

// TestRequestLog_ShortLinkRedirectOmitsAddressAndAgent: the short-link
// redirect (spec 43, ST-QL1) never logs the scanner's address or browser,
// while any other route still does.
func TestRequestLog_ShortLinkRedirectOmitsAddressAndAgent(t *testing.T) {
	const ip, ua = "198.51.100.23", "ScannerAgent/9.9 DO-NOT-LOG"
	for _, c := range []struct {
		method, path string
		private      bool
	}{
		{http.MethodGet, "/go/x7k2qpm", true},
		{http.MethodHead, "/go/x7k2qpm", true},
		{http.MethodGet, "/probe", false},
	} {
		var buf bytes.Buffer
		r := httpadapter.NewRouter(httpadapter.Deps{Logger: logging.New(&buf, "json", "debug")})
		handler := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusFound) }
		r.Get("/go/{code}", handler)
		r.Head("/go/{code}", handler)
		r.Get("/probe", handler)

		req := httptest.NewRequest(c.method, c.path, nil)
		req.RemoteAddr = ip + ":4321"
		req.Header.Set("User-Agent", ua)
		r.ServeHTTP(httptest.NewRecorder(), req)

		logs := buf.String()
		if !strings.Contains(logs, "http.request.completed") {
			t.Fatalf("%s %s: no request record:\n%s", c.method, c.path, logs)
		}
		leaked := strings.Contains(logs, ip) || strings.Contains(logs, ua)
		if c.private && leaked {
			t.Errorf("%s %s: address or agent logged:\n%s", c.method, c.path, logs)
		}
		if !c.private && !leaked {
			t.Errorf("%s %s: an ordinary route lost its address and agent:\n%s", c.method, c.path, logs)
		}
	}
}
