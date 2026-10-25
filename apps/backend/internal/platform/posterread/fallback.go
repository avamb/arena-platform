package posterread

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const (
	// minFallbackRemaining is the time that must be left on the caller's
	// deadline for the second reader to be worth trying.
	minFallbackRemaining = 8 * time.Second
	// primaryShareNum / primaryShareDen: with a fallback present the first
	// reader gets this share of the time left, so a hung provider cannot eat
	// the whole deadline and leave nothing for the second one.
	primaryShareNum = 3
	primaryShareDen = 5
)

// WithFallback returns a reader that asks primary first and, when it fails
// or runs out of its share of the time, asks secondary once if enough time
// remains. A nil secondary returns primary unchanged. ErrNotConfigured of the
// primary is not an outage and is passed on without trying the secondary.
// The log names the providers and the kind of failure, never the error text.
func WithFallback(primary, secondary Reader, primaryName, secondaryName string, l *slog.Logger) Reader {
	if secondary == nil {
		return primary
	}
	return &fallbackReader{primary: primary, secondary: secondary, pName: primaryName, sName: secondaryName, log: l}
}

type fallbackReader struct {
	primary, secondary Reader
	pName, sName       string
	log                *slog.Logger
}

// Read implements Reader.
func (f *fallbackReader) Read(ctx context.Context, image []byte, mediaType string) (Facts, error) {
	primaryCtx, cancel := f.primaryContext(ctx)
	facts, err := f.primary.Read(primaryCtx, image, mediaType)
	cancel()
	if err == nil || errors.Is(err, ErrNotConfigured) {
		return facts, err
	}
	// The caller gave up or its own deadline passed: nothing left to spend.
	if ctx.Err() != nil || !hasTime(ctx) {
		return Facts{}, err
	}
	if f.log != nil {
		f.log.Warn("posterread: primary reader failed, trying the fallback",
			slog.String("primary", f.pName), slog.String("fallback", f.sName),
			slog.String("failure", failureKind(err)))
	}
	facts2, err2 := f.secondary.Read(ctx, image, mediaType)
	if err2 != nil {
		if f.log != nil {
			f.log.Warn("posterread: fallback reader failed too",
				slog.String("fallback", f.sName), slog.String("failure", failureKind(err2)))
		}
		// The first error is the more telling one for the operator.
		return Facts{}, fmt.Errorf("%w (fallback reader failed too: %v)", err, err2)
	}
	return facts2, nil
}

// primaryContext limits the first attempt to its share of the remaining time.
func (f *fallbackReader) primaryContext(ctx context.Context) (context.Context, context.CancelFunc) {
	dl, ok := ctx.Deadline()
	if !ok {
		return ctx, func() {}
	}
	share := time.Until(dl) * primaryShareNum / primaryShareDen
	if share <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, share)
}

func hasTime(ctx context.Context) bool {
	dl, ok := ctx.Deadline()
	return !ok || time.Until(dl) >= minFallbackRemaining
}

// failureKind is a short, text-free word for a failed read.
func failureKind(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, ErrNotConfigured):
		return "not_configured"
	}
	return "error"
}
