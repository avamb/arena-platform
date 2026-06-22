// Package main is the arena-healthcheck binary: a tiny HTTP probe used by
// the Dockerfile HEALTHCHECK directive.
//
// The binary performs a single GET request to the arena-api /healthz endpoint
// and exits 0 on HTTP 200 or 1 on any error / non-200 response. It is
// statically linked (CGO_ENABLED=0) so it can run inside the
// gcr.io/distroless/static-debian12 image, which has no shell or curl.
//
// Configuration (environment variables):
//
//	HEALTH_ADDR — full base URL including scheme and port, e.g.
//	              "http://localhost:8080" (default). Override when the
//	              arena-api HTTP_LISTEN_ADDR differs from :8080.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := os.Getenv("HEALTH_ADDR")
	if addr == "" {
		addr = "http://localhost:8080"
	}
	url := addr + "/healthz"

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url) //nolint:noctx // intentional: healthcheck binary has no parent ctx
	if err != nil {
		fmt.Fprintf(os.Stderr, "arena-healthcheck: GET %s: %v\n", url, err)
		os.Exit(1)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "arena-healthcheck: GET %s: status %d\n", url, resp.StatusCode)
		os.Exit(1)
	}
}
