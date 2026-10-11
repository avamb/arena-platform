package eventbot

import (
	"context"
	"testing"
	"time"
)

func TestOnbFlagOf(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"ES": "🇪🇸", "cz": "🇨🇿", "": "", "E": "", "E1": "", "ESP": ""} {
		if got := flagOf(in); got != want {
			t.Errorf("flagOf(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestOnbDisplay(t *testing.T) {
	t.Parallel()
	f := OnbSchemaField{Key: "seating", Options: []OnbSchemaOption{{Value: "ga", Label: "General admission"}, {Value: "seated", Label: "Seated"}}}
	if got := onbDisplay(f, "ga"); got != "General admission" {
		t.Errorf("select label = %q", got)
	}
	if got := onbDisplay(f, []any{"seated", "ga", 7}); got != "Seated, General admission" {
		t.Errorf("multiselect labels = %q", got)
	}
	if got := onbDisplay(f, "free text"); got != "free text" {
		t.Errorf("unknown value should print as is, got %q", got)
	}
	if got := onbDisplay(f, 12.5); got != "12.5" {
		t.Errorf("number = %q", got)
	}
	if got := onbDisplay(f, true); got != "" {
		t.Errorf("a bool prints nothing, got %q", got)
	}
}

func TestOnbDialogsSurviveARestartExpireAndClear(t *testing.T) {
	ctx := context.Background()
	clock := &testClock{t: time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	store := newMemDialogStore(clock)

	d := newOnbDialogs(store)
	first := d.get(ctx, 1)
	first.step, first.first, first.last, first.locale = onbEmail, "Ana", "Perez", "es"
	first.phone = "+34600111222"
	first.sel["countries"] = map[string]bool{"ES": true}
	first.infoDone["website"] = true
	d.save(ctx, 1, first)

	// A new process: only the store is shared.
	got := newOnbDialogs(store).get(ctx, 1)
	if got.step != onbEmail || got.first != "Ana" || got.last != "Perez" || got.locale != "es" ||
		got.phone != "+34600111222" || !got.sel["countries"]["ES"] || !got.infoDone["website"] {
		t.Fatalf("state lost: %+v", got)
	}

	// An expired dialog starts empty, without an error.
	clock.advance(dialogTTL + time.Minute)
	if after := d.get(ctx, 1); after.step != onbIdle || after.first != "" || after.sel == nil || after.infoDone == nil {
		t.Fatalf("an expired dialog must start empty: %+v", after)
	}

	// A cleared dialog stays cleared even when the entry point saves it.
	again := d.get(ctx, 2)
	again.step = onbFirst
	d.save(ctx, 2, again)
	d.clear(ctx, 2, again)
	d.save(ctx, 2, again) // the deferred write-back
	if d.get(ctx, 2).step != onbIdle {
		t.Fatal("a cleared dialog starts empty")
	}
}
