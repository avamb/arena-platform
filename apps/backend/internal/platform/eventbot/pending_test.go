package eventbot

import (
	"testing"
	"time"
)

func TestPendingInvites(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	p := newPendingInvites(30 * time.Minute)
	p.now = func() time.Time { return now }

	if _, ok := p.take(1); ok {
		t.Fatal("nothing pending yet")
	}
	p.put(1, "code-1")
	// A mistyped e-mail must not cost the person the invitation: take keeps it.
	for i := 0; i < 2; i++ {
		if code, ok := p.take(1); !ok || code != "code-1" {
			t.Fatalf("take #%d = %q, %v", i, code, ok)
		}
	}
	p.clear(1)
	if _, ok := p.take(1); ok {
		t.Fatal("cleared entry must be gone")
	}
	p.put(2, "code-2")
	now = now.Add(31 * time.Minute)
	if _, ok := p.take(2); ok {
		t.Fatal("expired entry must be gone")
	}
	// A later put sweeps the expired ones without touching live ones.
	p.put(3, "code-3")
	now = now.Add(10 * time.Minute)
	p.put(4, "code-4")
	if code, ok := p.take(3); !ok || code != "code-3" {
		t.Fatalf("live entry lost by the sweep: %q %v", code, ok)
	}
}
