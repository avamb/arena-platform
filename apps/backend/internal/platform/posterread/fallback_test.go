package posterread

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type stubReader struct {
	facts Facts
	err   error
	calls int
	// block makes the reader wait for its context.
	block bool
}

func (s *stubReader) Read(ctx context.Context, _ []byte, _ string) (Facts, error) {
	s.calls++
	if s.block {
		<-ctx.Done()
		return Facts{}, ctx.Err()
	}
	return s.facts, s.err
}

func TestWithFallback_NilSecondaryReturnsPrimary(t *testing.T) {
	p := &stubReader{}
	if WithFallback(p, nil, "a", "b", nil) != Reader(p) {
		t.Fatal("without a fallback the primary must be returned as it is")
	}
}

func TestFallback_PrimaryAnswerWins(t *testing.T) {
	p, s := &stubReader{facts: Facts{Name: "A"}}, &stubReader{facts: Facts{Name: "B"}}
	got, err := WithFallback(p, s, "anthropic", "gemini", nil).Read(context.Background(), nil, "image/png")
	if err != nil || got.Name != "A" || s.calls != 0 {
		t.Fatalf("got %+v err %v secondary calls %d", got, err, s.calls)
	}
}

func TestFallback_PrimaryErrorTriesSecondaryOnce(t *testing.T) {
	p, s := &stubReader{err: errors.New("boom")}, &stubReader{facts: Facts{Name: "B"}}
	got, err := WithFallback(p, s, "anthropic", "gemini", nil).Read(context.Background(), nil, "image/png")
	if err != nil || got.Name != "B" || p.calls != 1 || s.calls != 1 {
		t.Fatalf("got %+v err %v calls %d/%d", got, err, p.calls, s.calls)
	}
}

func TestFallback_BothFailKeepsBothMessages(t *testing.T) {
	p, s := &stubReader{err: errors.New("first")}, &stubReader{err: errors.New("second")}
	_, err := WithFallback(p, s, "a", "b", nil).Read(context.Background(), nil, "image/png")
	if err == nil || !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") || !errors.Is(err, p.err) {
		t.Fatalf("err = %v", err)
	}
}

func TestFallback_NotConfiguredIsNotAnOutage(t *testing.T) {
	p, s := &stubReader{err: ErrNotConfigured}, &stubReader{facts: Facts{Name: "B"}}
	_, err := WithFallback(p, s, "a", "b", nil).Read(context.Background(), nil, "image/png")
	if !errors.Is(err, ErrNotConfigured) || s.calls != 0 {
		t.Fatalf("err = %v secondary calls %d", err, s.calls)
	}
}

// A primary that hangs gets only its share of the caller's time, so the
// secondary still has the rest. The deadline is 10 s: the primary share is
// 3/5 of what is left, the secondary runs on the caller's context.
func TestFallback_HungPrimaryLeavesTimeForSecondary(t *testing.T) {
	p, s := &stubReader{block: true}, &stubReader{facts: Facts{Name: "B"}}
	fb := &fallbackReader{primary: p, secondary: s, pName: "a", sName: "b"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pc, c := fb.primaryContext(ctx)
	defer c()
	dl, _ := pc.Deadline()
	cdl, _ := ctx.Deadline()
	if left := time.Until(dl); left > 7*time.Second || left < 4*time.Second || !dl.Before(cdl) {
		t.Errorf("primary budget = %v, want about 6 s of the caller's 10 s", left)
	}
}

// End to end with a short real deadline: the primary blocks until its
// share ends, then the secondary answers if the remaining time allows. With
// a deadline too short for that, the secondary is skipped.
func TestFallback_NoTimeLeftSkipsSecondary(t *testing.T) {
	p, s := &stubReader{err: errors.New("boom")}, &stubReader{facts: Facts{Name: "B"}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := WithFallback(p, s, "a", "b", nil).Read(ctx, nil, "image/png")
	if err == nil || s.calls != 0 {
		t.Fatalf("err = %v secondary calls %d", err, s.calls)
	}
}

func TestFallback_RoomLeftTriesSecondaryAfterFastFailure(t *testing.T) {
	p, s := &stubReader{err: errors.New("boom")}, &stubReader{facts: Facts{Name: "B"}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := WithFallback(p, s, "a", "b", nil).Read(ctx, nil, "image/png")
	if err != nil || got.Name != "B" {
		t.Fatalf("got %+v err %v", got, err)
	}
}
