package onboarding

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// arena-api builds a Service on every request (the bot polls the notice
// claim every 45 s), so building one without a notifier must not log the
// "Telegram not configured" line each time.
func TestNew_WithoutNotifier_DoesNotLogPerBuild(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	for i := 0; i < 3; i++ {
		s := New(Options{Logger: logger})
		if s.notifier == nil {
			t.Fatal("New left the notifier nil")
		}
	}
	if strings.Contains(buf.String(), "not configured") {
		t.Fatalf("New logged on build: %q", buf.String())
	}
}
