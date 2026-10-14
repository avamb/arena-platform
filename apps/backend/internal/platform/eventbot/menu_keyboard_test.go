package eventbot

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The menu button is recognised in every language, whatever language the
// person has now, and nothing else is taken for it.
func TestIsMenuButton(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, text := range []string{"☰ Menu", "☰ Меню", "☰ Menú", "  ☰ Меню  "} {
		if !b.isMenuButton(text) {
			t.Errorf("%q must open the menu", text)
		}
	}
	for _, text := range []string{"", "Меню", "menu", "☰", "/menu"} {
		if b.isMenuButton(text) {
			t.Errorf("%q must not be taken for the menu button", text)
		}
	}
}

// The keyboard stays under the message field and is one button.
func TestMenuReplyKeyboard(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, loc := range SupportedLocales {
		kb := b.menuReplyKeyboard(loc)
		if !kb.IsPersistent || !kb.ResizeKeyboard || kb.OneTimeKeyboard {
			t.Errorf("%s: keyboard must be persistent and resized: %+v", loc, kb)
		}
		if len(kb.Keyboard) != 1 || len(kb.Keyboard[0]) != 1 || !b.isMenuButton(kb.Keyboard[0][0].Text) {
			t.Errorf("%s: one menu button expected: %+v", loc, kb.Keyboard)
		}
	}
}

func TestMenuShown(t *testing.T) {
	t.Parallel()
	m := newMenuShown()
	if m.has(1) {
		t.Fatal("a new chat has no keyboard")
	}
	m.set(1, true)
	if !m.has(1) || m.has(2) {
		t.Fatal("set marks one chat")
	}
	m.set(1, false)
	if m.has(1) {
		t.Fatal("a removed keyboard is forgotten")
	}
	var none *menuShown
	none.set(1, true)
	if none.has(1) {
		t.Fatal("a nil memory remembers nothing")
	}
}

// dialogKindsInSource lists every bot_dialogs kind the package declares
// (constants named *DialogKind), read from the source so a new dialog cannot
// be forgotten.
func dialogKindsInSource(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(\w+DialogKind)\s*=\s*"([a-z_]+)"`)
	kinds := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			kinds[m[2]] = m[1]
		}
	}
	return kinds
}

// The menu ends every dialog that may wait for a typed answer, so no text
// typed after it (a session's cancel word, an e-mail, a promo name) is taken
// as one. The refund is kept only when the menu asks about it.
func TestLeaveAllDialogs_EndsEveryDialogKind(t *testing.T) {
	t.Parallel()
	// The application dialog belongs to a stranger, who never gets the menu.
	exempt := map[string]bool{onbDialogKind: true}
	kinds := dialogKindsInSource(t)
	if len(kinds) < 12 {
		t.Fatalf("found only %d dialog kinds in the source: %v", len(kinds), kinds)
	}
	for _, keepRefund := range []bool{false, true} {
		clock := &testClock{t: time.Now()}
		b := &Bot{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), dialogs: newMemDialogStore(clock)}
		ctx := context.Background()
		org := uuid.New()
		for kind := range kinds {
			if err := b.dialogs.Save(ctx, 42, &org, kind, "pick", map[string]any{"org_id": org}, dialogTTL); err != nil {
				t.Fatal(err)
			}
		}
		b.leaveAllDialogs(ctx, 42, nil, "", keepRefund)
		for kind, name := range kinds {
			var state map[string]any
			_, found, _, err := b.dialogs.Load(ctx, 42, kind, &state)
			if err != nil {
				t.Fatal(err)
			}
			kept := exempt[kind] || (keepRefund && kind == refundDialogKind)
			if found != kept {
				t.Errorf("keepRefund=%v: dialog %s (%q) open=%v, want open=%v", keepRefund, name, kind, found, kept)
			}
		}
	}
}

// A refund that went to the server without an answer keeps its key when the
// person leaves: going back sends the same operation again.
func TestLeaveAllDialogs_KeepsTheKeyOfARefundThatMayHaveStarted(t *testing.T) {
	t.Parallel()
	clock := &testClock{t: time.Now()}
	b := &Bot{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), dialogs: newMemDialogStore(clock)}
	ctx := context.Background()
	st := refundDialog{OrgID: uuid.New(), Key: "tg-same", Attempted: true}
	if err := b.dialogs.Save(ctx, 42, &st.OrgID, refundDialogKind, refStepFinal, st, dialogTTL); err != nil {
		t.Fatal(err)
	}
	b.leaveAllDialogs(ctx, 42, nil, "", false)
	var got refundDialog
	step, found, _, err := b.dialogs.Load(ctx, 42, refundDialogKind, &got)
	if err != nil || !found || step != refStepFinal || got.Key != "tg-same" || !got.Parked {
		t.Fatalf("the refund that may have started lost its key: found=%v step=%q key=%q parked=%v err=%v", found, step, got.Key, got.Parked, err)
	}
	// Without an attempt nothing was sent: leaving ends the dialog.
	st = refundDialog{OrgID: uuid.New(), Key: "tg-new"}
	if err := b.dialogs.Save(ctx, 43, &st.OrgID, refundDialogKind, refStepFinal, st, dialogTTL); err != nil {
		t.Fatal(err)
	}
	b.leaveAllDialogs(ctx, 43, nil, "", false)
	if _, found, _, _ := b.dialogs.Load(ctx, 43, refundDialogKind, &got); found {
		t.Fatal("a refund that was never sent must end with the menu")
	}
}

// Only an operation that went to the server without an answer may have
// started; a refund still being confirmed has not.
func TestRefundDialog_MayHaveStarted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		attempted bool
		step      string
		want      bool
	}{
		{false, refStepWord, false},
		{false, refStepFinal, false},
		{true, refStepFinal, true},
		{false, refStepSending, true},
		{true, refStepPick, false},
	}
	for _, c := range cases {
		if got := (refundDialog{Attempted: c.attempted}).mayHaveStarted(c.step); got != c.want {
			t.Errorf("attempted=%v step=%s: %v, want %v", c.attempted, c.step, got, c.want)
		}
	}
}
