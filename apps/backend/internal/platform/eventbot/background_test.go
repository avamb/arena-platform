package eventbot

import (
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

// A panic in background work (a refund send, a queue retry) must not take the
// bot down for everyone, and the work's own clean-up still runs.
func TestGoBackground_RecoversAPanicAndRunsTheCleanUp(t *testing.T) {
	b := &Bot{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	var cleaned atomic.Bool
	b.goBackground("test", func() { panic("boom") }, func() { cleaned.Store(true) })
	done := make(chan struct{})
	go func() { b.bg.wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background work never finished")
	}
	if !cleaned.Load() {
		t.Fatal("the clean-up of the panicking work did not run")
	}
}

// Work started while the bot is shutting down is not started at all: the
// caller holds the person's lane, and work writing through that lane would
// wait for itself. The caller is told (false) and tells the person; adding
// cannot race the shutdown's wait.
func TestGoBackground_WorkStartedDuringShutdownIsRefused(t *testing.T) {
	b := &Bot{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	b.bg.close()
	var ran atomic.Bool
	if b.goBackground("test", func() { ran.Store(true) }, nil) {
		t.Fatal("work was accepted after the shutdown began")
	}
	b.bg.wait()
	if ran.Load() {
		t.Fatal("work started after the shutdown began was run")
	}
}
