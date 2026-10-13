package eventbot

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// waitIdle waits until no account has a lane any more.
func waitIdle(t *testing.T, s *userSerial) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.busy() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("lanes still busy: %d", s.busy())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestUserSerial_OneAccountRunsInOrderAndAlone(t *testing.T) {
	s := newUserSerial(nil)
	const n = 200
	s.maxQueued = n // this test is about order, not the cap
	var (
		mu      sync.Mutex
		order   []int
		running atomic.Int32
		maxSeen atomic.Int32
		wg      sync.WaitGroup
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		if err := s.submit(7, func() {
			defer wg.Done()
			cur := running.Add(1)
			defer running.Add(-1)
			if cur > maxSeen.Load() {
				maxSeen.Store(cur)
			}
			if i%10 == 0 {
				time.Sleep(time.Millisecond) // give an overtaking job its chance
			}
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
		}); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}
	wg.Wait()
	waitIdle(t, s)
	if maxSeen.Load() != 1 {
		t.Fatalf("%d jobs of one account ran at once, want 1", maxSeen.Load())
	}
	for i, v := range order {
		if v != i {
			t.Fatalf("job %d ran at position %d: %v", v, i, order[:i+1])
		}
	}
}

func TestUserSerial_AccountsDoNotWaitForEachOther(t *testing.T) {
	s := newUserSerial(nil)
	release := make(chan struct{})
	other := make(chan struct{})
	// Account 1 hangs until account 2 has run: if accounts shared one lock,
	// account 2 would never run and the test would time out.
	if err := s.submit(1, func() { <-release }); err != nil {
		t.Fatal(err)
	}
	if err := s.submit(2, func() { close(other) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-other:
	case <-time.After(5 * time.Second):
		t.Fatal("a hanging handler of one account blocked another account")
	}
	close(release)
	waitIdle(t, s)
}

func TestUserSerial_PanicReleasesTheLane(t *testing.T) {
	s := newUserSerial(nil)
	ran := make(chan struct{})
	if err := s.submit(5, func() { panic("boom") }); err != nil {
		t.Fatal(err)
	}
	if err := s.submit(5, func() { close(ran) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the update after a panic never ran")
	}
	waitIdle(t, s)
	// And the account can start a new lane afterwards.
	again := make(chan struct{})
	if err := s.submit(5, func() { close(again) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-again:
	case <-time.After(5 * time.Second):
		t.Fatal("the account is stuck after a panic")
	}
	waitIdle(t, s)
}

func TestUserSerial_QueueIsCapped(t *testing.T) {
	s := newUserSerial(nil)
	s.maxQueued = 3
	release := make(chan struct{})
	if err := s.submit(9, func() { <-release }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.submit(9, func() {}); err != nil {
			t.Fatalf("queued job %d refused: %v", i, err)
		}
	}
	if err := s.submit(9, func() {}); !errors.Is(err, errLaneFull) {
		t.Fatalf("over the cap: got %v, want errLaneFull", err)
	}
	close(release)
	waitIdle(t, s)
}

func TestUserSerial_DoWaitsForTheTurn(t *testing.T) {
	s := newUserSerial(nil)
	release := make(chan struct{})
	var handlerDone atomic.Bool
	if err := s.submit(3, func() {
		<-release
		handlerDone.Store(true)
	}); err != nil {
		t.Fatal(err)
	}
	result := make(chan bool, 1)
	go func() {
		_ = s.do(context.Background(), 3, func(context.Context) { result <- handlerDone.Load() })
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	select {
	case sawDone := <-result:
		if !sawDone {
			t.Fatal("do ran while the account's handler was still running")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("do never ran")
	}
	waitIdle(t, s)
}

func TestUserSerial_DoInsideTheTurnRunsAtOnce(t *testing.T) {
	s := newUserSerial(nil)
	done := make(chan struct{})
	if err := s.submit(4, func() {
		defer close(done)
		ran := false
		_ = s.do(withTurn(context.Background(), 4), 4, func(context.Context) { ran = true })
		if !ran {
			t.Error("do inside the account's own turn did not run")
		}
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("do inside the turn deadlocked")
	}
	waitIdle(t, s)
}

func TestUserSerial_AbandonedDoIsSkipped(t *testing.T) {
	s := newUserSerial(nil)
	release := make(chan struct{})
	if err := s.submit(6, func() { <-release }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	var ran atomic.Bool
	if err := s.do(ctx, 6, func(context.Context) { ran.Store(true) }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("do: got %v, want deadline exceeded", err)
	}
	close(release)
	waitIdle(t, s)
	if ran.Load() {
		t.Fatal("do ran after its caller gave up")
	}
}
