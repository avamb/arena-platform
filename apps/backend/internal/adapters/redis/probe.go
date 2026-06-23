// Package redis provides Redis connectivity helpers for arena_new.
//
// This package is intentionally thin: it does not depend on any third-party
// Redis client library. The only consumer of the package for the foundation
// milestone is the readiness probe registry (feature #112).
//
// Key types:
//
//   - RedisPinger      — minimal interface: Ping(ctx) error. Any Redis client
//                        that exposes a Ping method satisfies it automatically.
//   - RedisPingProbe   — wraps a RedisPinger and adapts it to the
//                        httpserver.ReadinessProbe contract (structurally,
//                        without importing the httpserver package).
//   - DialRedisPinger  — implements RedisPinger using a raw TCP connection and
//                        the Redis inline PING command. No third-party Redis
//                        client library is required.
package redis

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"time"
)

// RedisPinger is the minimal interface required to ping a Redis server.
// Any Redis client that exposes a Ping method with this signature satisfies
// it automatically (e.g. (*redis.Client).Ping from go-redis/v9 returns a
// *Cmd whose Err() matches this shape when wrapped in a helper function).
// The DialRedisPinger in this package also satisfies it without any external
// Redis client dependency.
type RedisPinger interface {
	// Ping returns nil when the Redis server is reachable, or an error
	// describing the failure (connection refused, authentication error, etc.).
	Ping(ctx context.Context) error
}

// RedisPingProbe wraps a RedisPinger and adapts it into a ReadinessProbe.
// It satisfies the httpserver.ReadinessProbe interface structurally (both
// ProbeName() string and Ping(context.Context) error are defined) without
// importing the httpserver package — Go's structural typing handles the rest.
type RedisPingProbe struct {
	pinger    RedisPinger
	probeName string
}

// NewRedisPingProbe constructs a RedisPingProbe that delegates to pinger.
// probeName is the stable key published in the /readyz checks map
// (e.g. "redis", "session-cache"). Defaults to "redis" when empty.
func NewRedisPingProbe(pinger RedisPinger, probeName string) *RedisPingProbe {
	if probeName == "" {
		probeName = "redis"
	}
	return &RedisPingProbe{pinger: pinger, probeName: probeName}
}

// ProbeName returns the stable identifier used in the /readyz checks map.
func (p *RedisPingProbe) ProbeName() string { return p.probeName }

// Ping delegates to the underlying RedisPinger with a 2-second timeout
// applied independently of any deadline already on ctx, so the readiness
// check completes quickly and never holds up the /readyz response.
func (p *RedisPingProbe) Ping(ctx context.Context) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := p.pinger.Ping(pingCtx); err != nil {
		return fmt.Errorf("redis ping: %w", err)
	}
	return nil
}

// DialRedisPinger implements RedisPinger by opening a raw TCP connection to
// the Redis server and sending the inline PING command.
//
// It does not import any third-party Redis client library: connectivity is
// established via net.DialContext and the minimal Redis inline protocol
// (PING\r\n → +PONG\r\n). This keeps the foundation-milestone dependency
// footprint small while still exercising the real network path.
//
// For production usage where authentication or TLS is required, wire a
// go-redis/v9 *redis.Client (which satisfies RedisPinger via a thin wrapper)
// instead of DialRedisPinger.
type DialRedisPinger struct {
	addr string // host:port resolved from the Redis URL at construction time
}

// NewDialRedisPinger parses redisURL (e.g. "redis://localhost:6379" or
// "redis://:password@cache.internal:6379/0") and returns a DialRedisPinger
// that dials the resolved host:port on each Ping call.
//
// If redisURL is empty the pinger defaults to "localhost:6379".
// Returns an error when the URL cannot be parsed or contains no host.
func NewDialRedisPinger(redisURL string) (*DialRedisPinger, error) {
	addr, err := parseRedisAddr(redisURL)
	if err != nil {
		return nil, err
	}
	return &DialRedisPinger{addr: addr}, nil
}

// Ping opens a TCP connection to the Redis server, sends the inline PING
// command, reads the +PONG\r\n response, and returns nil on success.
// Any network failure, read failure, or non-PONG response returns an error.
func (d *DialRedisPinger) Ping(ctx context.Context) error {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return fmt.Errorf("redis: dial %s: %w", d.addr, err)
	}
	defer conn.Close()

	// Apply the context deadline to read/write operations so the TCP calls
	// honour the caller's timeout without a separate goroutine.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			return fmt.Errorf("redis: set deadline: %w", err)
		}
	}

	// Send the Redis inline PING command.
	// Redis inline protocol: a line of text terminated by \r\n.
	if _, err := fmt.Fprintf(conn, "PING\r\n"); err != nil {
		return fmt.Errorf("redis: write PING: %w", err)
	}

	// Read exactly 7 bytes: "+PONG\r\n".
	buf := make([]byte, 7)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("redis: read PONG: %w", err)
	}
	if string(buf) != "+PONG\r\n" {
		return fmt.Errorf("redis: unexpected response: %q (want \"+PONG\\r\\n\")", buf)
	}
	return nil
}

// parseRedisAddr extracts the "host:port" string from a Redis URL.
//
// Supported URL schemes: redis:// and rediss:// (Redis over TLS, though
// DialRedisPinger uses plain TCP; the scheme is parsed for compatibility).
// Credentials (user:password) and database number (path) are ignored —
// only the host and port are needed for a connectivity probe.
//
// Falls back to "localhost:6379" when redisURL is empty.
func parseRedisAddr(rawURL string) (string, error) {
	if rawURL == "" {
		return "localhost:6379", nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("redis: parse URL %q: %w", rawURL, err)
	}
	host := u.Host
	if host == "" {
		return "", fmt.Errorf("redis: URL %q has no host", rawURL)
	}
	// If the URL already encodes host:port (e.g. "redis://cache:6380"), use
	// it directly. If only a hostname is present (no colon), append the
	// default Redis port 6379.
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "6379")
	}
	return host, nil
}

// -----------------------------------------------------------------------------
// Compile-time interface guards
// -----------------------------------------------------------------------------

// Verify that DialRedisPinger satisfies RedisPinger.
var _ RedisPinger = (*DialRedisPinger)(nil)
