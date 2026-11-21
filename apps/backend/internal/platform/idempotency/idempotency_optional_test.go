package idempotency

// Options.Optional / ScopeFunc / MaxBodyBytes (П-19: the widget's public
// checkout/start adopts the key without breaking clients that never send one).

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type signallingStore struct {
	*inMemoryStore
	lookups chan struct{}
}

func (s *signallingStore) Lookup(ctx context.Context, key, scope string) (StoredResponse, bool, error) {
	resp, hit, err := s.inMemoryStore.Lookup(ctx, key, scope)
	s.lookups <- struct{}{}
	return resp, hit, err
}

func countingHandler(calls *int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(calls, 1)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"call":` + string(rune('0'+n)) + `,"len":` + itoa(len(body)) + `}`))
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestOptional_NoKeyPassesThroughEveryTime(t *testing.T) {
	store := newInMemoryStore()
	var calls int32
	h := Middleware(store, Options{Scope: "s", Optional: true})(countingHandler(&calls))
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`)))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status %d, want 201", rec.Code)
		}
		if rec.Header().Get("Idempotent-Replay") != "" {
			t.Fatalf("a request without a key must never be a replay")
		}
	}
	if calls != 2 {
		t.Fatalf("handler ran %d times, want 2", calls)
	}
	if len(store.records) != 0 {
		t.Fatalf("a request without a key must store nothing, stored %d", len(store.records))
	}
}

func TestOptional_KeyStillEnforced(t *testing.T) {
	store := newInMemoryStore()
	var calls int32
	h := Middleware(store, Options{Scope: "s", Optional: true})(countingHandler(&calls))
	send := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		req.Header.Set(HeaderName, "k1")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	first := send(`{"a":1}`)
	second := send(`{"a":1}`)
	if first.Body.String() != second.Body.String() || second.Header().Get("Idempotent-Replay") != "true" {
		t.Fatalf("same key and body must replay: %q vs %q", first.Body.String(), second.Body.String())
	}
	if third := send(`{"a":2}`); third.Code != http.StatusConflict {
		t.Fatalf("same key, other body: status %d, want 409", third.Code)
	}
	if calls != 1 {
		t.Fatalf("handler ran %d times, want 1", calls)
	}
}

func TestScopeFunc_SeparatesScopes(t *testing.T) {
	store := newInMemoryStore()
	var calls int32
	h := Middleware(store, Options{
		Scope:     "fallback",
		ScopeFunc: func(r *http.Request) string { return "POST checkout/start:" + r.URL.Query().Get("t") },
	})(countingHandler(&calls))
	for _, tok := range []string{"a", "b"} {
		req := httptest.NewRequest(http.MethodPost, "/x?t="+tok, strings.NewReader(`{}`))
		req.Header.Set(HeaderName, "same-key")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	if calls != 2 {
		t.Fatalf("one key in two scopes must run twice, ran %d", calls)
	}
	for _, want := range []string{"POST checkout/start:a", "POST checkout/start:b"} {
		if _, ok := store.records[store.storeKey("same-key", want)]; !ok {
			t.Fatalf("no row stored under scope %q", want)
		}
	}
}

func TestMaxBodyBytes_CutsTheBody(t *testing.T) {
	store := newInMemoryStore()
	var calls int32
	h := Middleware(store, Options{Scope: "s", MaxBodyBytes: 4})(countingHandler(&calls))
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`0123456789`))
	req.Header.Set(HeaderName, "k")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), `"len":4`) {
		t.Fatalf("handler saw %s, want a body cut to 4 bytes", rec.Body.String())
	}
}

func TestConcurrentWaiter_DifferentBodyIs409(t *testing.T) {
	mem := newInMemoryStore()
	lookups := make(chan struct{}, 4)
	store := &signallingStore{inMemoryStore: mem, lookups: lookups}
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		once.Do(func() { close(started) })
		<-release
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})
	h := Middleware(store, Options{Scope: "s"})(inner)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":1}`))
		req.Header.Set(HeaderName, "k")
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-started

	waiter := httptest.NewRecorder()
	wg.Add(1)
	go func() {
		defer wg.Done()
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"a":2}`))
		req.Header.Set(HeaderName, "k")
		h.ServeHTTP(waiter, req)
	}()
	// Let the waiter pass its lookup (a miss: nothing is stored yet) and
	// queue on the in-flight call before the winner finishes. Were it slower,
	// it would hit the stored row instead: that answers 409 too.
	<-lookups // winner
	<-lookups // waiter
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if waiter.Code != http.StatusConflict {
		t.Fatalf("waiter with another body: status %d, want 409", waiter.Code)
	}
}
