// Package main is the arena-healthcheck binary: a tiny HTTP probe used by
// the Dockerfile HEALTHCHECK directive.
//
// The binary performs a single GET request to /healthz on the resolved target
// address and exits 0 on HTTP 200 or 1 on any error / non-200 response. It is
// statically linked (CGO_ENABLED=0) so it can run inside the
// gcr.io/distroless/static-debian12 image, which has no shell or curl.
//
// # Resolution order (deterministic)
//
// 1. HEALTH_ADDR — if set, use it verbatim (must include scheme, e.g.
// "http://localhost:8080"). This is the explicit override and always wins.
//
// 2. APP_NAME contains "worker" — derive the URL from WORKER_METRICS_ADDR
// (default ":9091"). The arena-worker sidecar HTTP server exposes /healthz
// there, separate from the public arena-api port.
//
// 3. Default (API and any other role) — derive the URL from HTTP_LISTEN_ADDR
// (default ":8080"). This is the arena-api main HTTP server.
//
// docker-compose.yml and Dokploy should always set HEALTH_ADDR explicitly for
// clarity and to avoid relying on the APP_NAME heuristic at runtime.
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// resolveTarget returns the base URL for the /healthz probe.
//
// Resolution order:
//  1. HEALTH_ADDR (explicit override, full URL including scheme).
//  2. APP_NAME contains "worker": derive from WORKER_METRICS_ADDR (default :9091).
//  3. Default (API): derive from HTTP_LISTEN_ADDR (default :8080).
func resolveTarget() string {
	// Explicit override always wins.
	if addr := os.Getenv("HEALTH_ADDR"); addr != "" {
		return addr
	}

	// Role-based fallback using APP_NAME.
	appName := os.Getenv("APP_NAME")
	if strings.Contains(appName, "worker") {
		listen := os.Getenv("WORKER_METRICS_ADDR")
		if listen == "" {
			listen = ":9091"
		}
		return listenToURL(listen)
	}

	// Default: API role, derive from HTTP listen address.
	listen := os.Getenv("HTTP_LISTEN_ADDR")
	if listen == "" {
		listen = ":8080"
	}
	return listenToURL(listen)
}

// listenToURL converts a listen address such as ":8080" or "0.0.0.0:8080"
// to a localhost-targeted URL "http://localhost:8080".
// The healthcheck always probes localhost because the /healthz server is
// co-located inside the same container.
func listenToURL(addr string) string {
	if strings.HasPrefix(addr, ":") {
		// Port-only form: ":8080" → "http://localhost:8080"
		return "http://localhost" + addr
	}
	// Host:port form — strip the host and replace with localhost so the probe
	// is always directed at the co-located process (e.g. "0.0.0.0:8080"
	// → "http://localhost:8080").
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		return "http://localhost" + addr[idx:]
	}
	// Fallback: assume the address is a bare host with no port; use as-is.
	return "http://" + addr
}

func main() {
	target := resolveTarget()
	url := target + "/healthz"

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url) //nolint:noctx,gosec // intentional: healthcheck binary; URL is operator-controlled
	if err != nil {
		fmt.Fprintf(os.Stderr, "arena-healthcheck: GET %s: %v\n", url, err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "arena-healthcheck: GET %s: status %d\n", url, resp.StatusCode)
		os.Exit(1)
	}
	// Success: exit 0 implicitly.
}
