// Package worker — placeholder job handler (feature #102, step 3).
//
// ShouldRunPlaceholderJob and PlaceholderJobHandler are development-time
// stubs that let the worker boot with at least one registered handler
// before any real business-domain jobs are wired up.
//
// Both identifiers are intentionally exported so the cmd/arena-worker
// binary can reference them directly and so the worker_boot_test can
// exercise them without importing an internal test-only package.
package worker

import (
	"context"
	"log/slog"
)

// ShouldRunPlaceholderJob is a scheduling stub that always returns true.
//
// In a real scheduler the function would inspect the current time, the
// last-run timestamp, and rate-limit rules. For the foundation milestone
// it acts as an unconditional "yes, there is always something to do"
// signal so the poll loop stays visibly active in development.
func ShouldRunPlaceholderJob() bool {
	return true
}

// PlaceholderJobHandler returns a HandlerFunc that logs its invocation and
// returns nil (success). It is registered in the arena-worker binary as the
// "placeholder.log" job type so the queue machinery can be exercised
// end-to-end without any business logic in place.
func PlaceholderJobHandler(logger *slog.Logger) HandlerFunc {
	return func(_ context.Context, payload []byte) error {
		logger.Info("placeholder job handler invoked",
			"payload_bytes", len(payload),
		)
		return nil
	}
}
