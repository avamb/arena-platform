package opsalert

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestNewNoop_IsSilentUntilItSends(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	n := NewNoop(logger)
	if _, ok := n.(*noopNotifier); !ok {
		t.Fatalf("NewNoop = %T, want *noopNotifier", n)
	}
	if buf.Len() != 0 {
		t.Fatalf("NewNoop logged on construction: %q", buf.String())
	}

	if err := n.Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("Send did not log the message: %q", buf.String())
	}
}

func TestNew_EmptyCredentials_StillAnnouncesOnce(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	_ = New("", "", "", logger)
	if got := strings.Count(buf.String(), "Telegram not configured"); got != 1 {
		t.Fatalf("New logged %d not-configured lines, want 1: %q", got, buf.String())
	}
}

func TestNewNoop_NilLogger(t *testing.T) {
	if err := NewNoop(nil).Send(context.Background(), "x"); err != nil {
		t.Fatalf("Send: %v", err)
	}
}
