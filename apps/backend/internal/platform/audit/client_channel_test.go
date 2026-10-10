package audit

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestNormalizeClientChannel(t *testing.T) {
	tests := []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{"telegram_bot", ChannelTelegramBot, true},
		{"  Telegram_Bot ", ChannelTelegramBot, true},
		{"ADMIN_WEB", ChannelAdminWeb, true},
		{"site_plugin", ChannelSitePlugin, true},
		{"", "", false},
		{"   ", "", false},
		{"curl", "", false},
		{"telegram_bot; drop table", "", false},
		{"telegram-bot", "", false},
	}
	for _, tc := range tests {
		got, ok := NormalizeClientChannel(tc.raw)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("NormalizeClientChannel(%q) = (%q, %v), want (%q, %v)", tc.raw, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestClientChannelContext(t *testing.T) {
	if _, ok := ClientChannelFromContext(context.Background()); ok {
		t.Fatal("empty context must carry no channel")
	}
	if _, ok := ClientChannelFromContext(WithClientChannel(context.Background(), "")); ok {
		t.Fatal("an empty channel must not be stored")
	}
	got, ok := ClientChannelFromContext(WithClientChannel(context.Background(), ChannelAdminWeb))
	if !ok || got != ChannelAdminWeb {
		t.Fatalf("got (%q, %v), want %q", got, ok, ChannelAdminWeb)
	}
}

// recordingWriter remembers the last event handed to it, and through which
// of the two Writer methods.
type recordingWriter struct {
	ev     Event
	viaTx  bool
	called int
}

func (w *recordingWriter) Write(_ context.Context, ev Event) error {
	w.ev, w.viaTx = ev, false
	w.called++
	return nil
}

func (w *recordingWriter) WriteTx(_ context.Context, _ pgx.Tx, ev Event) error {
	w.ev, w.viaTx = ev, true
	w.called++
	return nil
}

func TestWithVia_StampsWriteAndWriteTx(t *testing.T) {
	ctx := WithClientChannel(context.Background(), ChannelTelegramBot)
	inner := &recordingWriter{}
	w := WithVia(inner)

	callerMeta := map[string]any{"k": "v"}
	if err := w.Write(ctx, Event{Action: "a", Metadata: callerMeta}); err != nil {
		t.Fatal(err)
	}
	if inner.viaTx || inner.ev.Metadata[MetadataVia] != ChannelTelegramBot || inner.ev.Metadata["k"] != "v" {
		t.Fatalf("Write: %+v (viaTx=%v)", inner.ev.Metadata, inner.viaTx)
	}
	if _, polluted := callerMeta[MetadataVia]; polluted || len(callerMeta) != 1 {
		t.Fatalf("caller's map was mutated: %v", callerMeta)
	}

	if err := w.WriteTx(ctx, nil, Event{Action: "b", Metadata: callerMeta}); err != nil {
		t.Fatal(err)
	}
	if !inner.viaTx || inner.ev.Metadata[MetadataVia] != ChannelTelegramBot {
		t.Fatalf("WriteTx: %+v (viaTx=%v)", inner.ev.Metadata, inner.viaTx)
	}
	if inner.ev.Action != "b" {
		t.Fatalf("decorator must not touch other fields, Action = %q", inner.ev.Action)
	}
}

func TestWithVia_NilMetadata(t *testing.T) {
	inner := &recordingWriter{}
	ctx := WithClientChannel(context.Background(), ChannelSitePlugin)
	if err := WithVia(inner).Write(ctx, Event{Action: "a"}); err != nil {
		t.Fatal(err)
	}
	if got := inner.ev.Metadata[MetadataVia]; got != ChannelSitePlugin {
		t.Fatalf("via = %v, want %q", got, ChannelSitePlugin)
	}
}

func TestWithVia_ExplicitViaWins(t *testing.T) {
	inner := &recordingWriter{}
	ctx := WithClientChannel(context.Background(), ChannelTelegramBot)
	ev := Event{Metadata: map[string]any{MetadataVia: "import_job"}}
	if err := WithVia(inner).Write(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := inner.ev.Metadata[MetadataVia]; got != "import_job" {
		t.Fatalf("via = %v, want the call site's own value", got)
	}
}

func TestWithVia_NoChannelLeavesEventUntouched(t *testing.T) {
	inner := &recordingWriter{}
	if err := WithVia(inner).Write(context.Background(), Event{Action: "a"}); err != nil {
		t.Fatal(err)
	}
	if inner.ev.Metadata != nil {
		t.Fatalf("Metadata = %v, want nil (no channel on the context)", inner.ev.Metadata)
	}
}

func TestWithVia_NilInner(t *testing.T) {
	if got := WithVia(nil); got != nil {
		t.Fatalf("WithVia(nil) = %v, want nil", got)
	}
}
