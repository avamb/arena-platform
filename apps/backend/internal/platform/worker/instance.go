// Package worker — instance ID helpers.
//
// The instance_id is written into the claimed_by column on every row
// this worker claims. Two competing requirements shape it:
//
//   - Unique enough to distinguish two replicas of the same image
//     running in the same Postgres cluster (so a stuck row can be
//     traced back to a specific container).
//   - Stable across the lifetime of a single process so a poll loop
//     can correlate its own writes in logs.
//
// defaultInstanceID combines hostname (container name in Docker /
// Dokploy) with the PID and a short random suffix. Hostname carries
// most of the signal; the random suffix prevents accidental collisions
// when two containers share a hostname (rare, but possible during a
// rolling deploy that briefly runs two pods with identical names).
package worker

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
)

func defaultInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "worker"
	}

	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand failing is exceptional; fall back to a static
		// suffix so we still emit a valid claimed_by value.
		return fmt.Sprintf("%s/pid-%d", host, os.Getpid())
	}
	return fmt.Sprintf("%s/pid-%d/%s", host, os.Getpid(), hex.EncodeToString(buf[:]))
}
