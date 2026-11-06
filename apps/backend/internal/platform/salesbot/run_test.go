package salesbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// flakyTG answers getMe with failStatus for the first `fails` calls (a 0
// status drops the connection, like the reset seen on prod), then succeeds;
// every other method answers an empty ok so the poll loop idles.
type flakyTG struct {
	fails      int32
	failStatus int
	getMe      atomic.Int32
	polls      atomic.Int32
}

func (f *flakyTG) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !strings.HasSuffix(r.URL.Path, "/getMe") {
		if strings.HasSuffix(r.URL.Path, "/getUpdates") {
			f.polls.Add(1)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
		return
	}
	if n := f.getMe.Add(1); n <= f.fails {
		if f.failStatus == 0 {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		w.WriteHeader(f.failStatus)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":` + strconv.Itoa(f.failStatus) + `,"description":"failed"}`))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"Sales","username":"SalesBot"}}`))
}

func newRunBot(t *testing.T, tg *flakyTG) *Bot {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(tg.handle))
	t.Cleanup(srv.Close)
	b, err := New(Options{Token: "tok", API: &fakeAPI{}, Prompts: &memPrompts{m: map[string]Prompt{}},
		TelegramServerURL: srv.URL, RetryBase: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runAsync(ctx context.Context, b *Bot) <-chan error {
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()
	return done
}

// A dropped connection and a 502 at start are retried until getMe answers;
// the bot then polls instead of giving up for good.
func TestRun_RetriesGetMeAfterNetworkFailures(t *testing.T) {
	for _, status := range []int{0, http.StatusBadGateway} {
		tg := &flakyTG{fails: 3, failStatus: status}
		b := newRunBot(t, tg)
		ctx, cancel := context.WithCancel(context.Background())
		done := runAsync(ctx, b)
		deadline := time.Now().Add(5 * time.Second)
		for tg.polls.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if tg.polls.Load() == 0 {
			t.Fatalf("status %d: no getUpdates after %d getMe calls", status, tg.getMe.Load())
		}
		if n := tg.getMe.Load(); n != 4 {
			t.Fatalf("status %d: getMe called %d times, want 4", status, n)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("status %d: Run returned %v, want nil after cancel", status, err)
			}
			if b.username != "SalesBot" {
				t.Fatalf("status %d: username %q, want SalesBot", status, b.username)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("status %d: Run did not return after cancel", status)
		}
	}
}

// A token Telegram rejects stops at once: no retry can fix it.
func TestRun_RejectedTokenStopsWithoutRetry(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound} {
		tg := &flakyTG{fails: 100, failStatus: status}
		b := newRunBot(t, tg)
		select {
		case err := <-runAsync(context.Background(), b):
			if err == nil || !strings.Contains(err.Error(), "getMe") {
				t.Fatalf("status %d: Run returned %v, want a getMe error", status, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("status %d: Run kept retrying a rejected token", status)
		}
		if n := tg.getMe.Load(); n != 1 {
			t.Fatalf("status %d: getMe called %d times, want 1", status, n)
		}
	}
}

// Shutdown during the pause between attempts ends Run cleanly.
func TestRun_CancelDuringRetryPause(t *testing.T) {
	tg := &flakyTG{fails: 1 << 30, failStatus: http.StatusBadGateway}
	b := newRunBot(t, tg)
	b.retry = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := runAsync(ctx, b)
	deadline := time.Now().Add(5 * time.Second)
	for tg.getMe.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel during the pause")
	}
}
