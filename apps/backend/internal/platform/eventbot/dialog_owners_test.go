package eventbot

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/http/openapi"
)

// Every dialog a linked user can have open knows its buttons and its typed
// answers, so a press elsewhere ends it and a text reaches it only when it is
// the newest dialog that takes text.
func TestDialogOwners_EveryDialogKindIsRouted(t *testing.T) {
	t.Parallel()
	kinds := dialogKindsInSource(t)
	seen := map[string]bool{}
	for _, kind := range dialogOwnerOrder {
		if seen[kind] {
			t.Errorf("dialog %q is twice in dialogOwnerOrder", kind)
		}
		seen[kind] = true
	}
	for kind, name := range kinds {
		if kind == onbDialogKind {
			continue // a stranger's application: no menu, no other dialog
		}
		if _, ok := dialogOwners[kind]; !ok {
			t.Errorf("dialog %s (%q) has no entry in dialogOwners: a foreign press would not end it", name, kind)
		}
		if dialogTexts[kind] == nil {
			t.Errorf("dialog %s (%q) has no entry in dialogTexts: a typed answer would not reach it", name, kind)
		}
		if !seen[kind] {
			t.Errorf("dialog %s (%q) is missing from dialogOwnerOrder", name, kind)
		}
	}
	if len(dialogOwners) != len(dialogOwnerOrder) || len(dialogTexts) != len(dialogOwnerOrder) {
		t.Errorf("the tables disagree: owners=%d texts=%d order=%d", len(dialogOwners), len(dialogTexts), len(dialogOwnerOrder))
	}
}

func openAllDialogs(t *testing.T, b *Bot, tg int64) {
	t.Helper()
	org := uuid.New()
	for _, kind := range dialogOwnerOrder {
		if err := b.dialogs.Save(context.Background(), tg, &org, kind, "pick", map[string]any{"org_id": org}, dialogTTL); err != nil {
			t.Fatal(err)
		}
	}
}

func openDialogs(t *testing.T, b *Bot, tg int64) []string {
	t.Helper()
	var open []string
	for _, kind := range dialogOwnerOrder {
		var state map[string]any
		_, found, _, err := b.dialogs.Load(context.Background(), tg, kind, &state)
		if err != nil {
			t.Fatal(err)
		}
		if found {
			open = append(open, kind)
		}
	}
	return open
}

// A press ends every dialog it does not belong to; a list's page counter
// ends nothing.
func TestLeaveForeignDialogs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		data string
		keep []string
	}{
		{"noop", dialogOwnerOrder},
		{"or:v:" + uuid.NewString(), []string{ordersDialogKind}},
		{"or:f:ok:abc", []string{ordersDialogKind, refundDialogKind}},
		{"or:f:x:1", []string{ordersDialogKind, refundDialogKind, scanExcDialogKind}},
		{"or:r:mail", []string{ordersDialogKind, resendDialogKind}},
		{"ec:o:" + uuid.NewString(), []string{eventsDialogKind, evsDialogKind}},
		{"el:p:2", []string{eventsDialogKind}},
		{"ses:go", []string{sesDialogKind}},
		{"team:invite", []string{teamDialogKind}},
		{"team", []string{teamDialogKind}},
		{"ct:noop", []string{categoryDialogKind}},
		{"pm:o:1", []string{promoDialogKind}},
		{"iv:new", []string{inviteDialogKind}},
		{"pr:l", []string{promoterDialogKind}},
		{"og:noop", []string{orgDataDialogKind}},
		{"help", nil},
		{"home", nil},
		{"wz:resume", nil},
	}
	for i, c := range cases {
		clock := &testClock{t: time.Now()}
		b := &Bot{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), dialogs: newMemDialogStore(clock)}
		tg := int64(100 + i)
		openAllDialogs(t, b, tg)
		b.leaveForeignDialogs(context.Background(), tg, c.data)
		got := openDialogs(t, b, tg)
		if len(got) != len(c.keep) || (len(got) > 0 && !reflect.DeepEqual(got, c.keep)) {
			t.Errorf("press %q: open dialogs %v, want %v", c.data, got, c.keep)
		}
	}
}

// The store lists a person's dialogs newest first, and a text goes to the
// first of them that takes it: an older dialog never sees a word typed for a
// newer one.
func TestRouteDialogText_NewestDialogFirst(t *testing.T) {
	t.Parallel()
	clock := &testClock{t: time.Now()}
	s := newMemDialogStore(clock)
	ctx := context.Background()
	org := uuid.New()
	for _, kind := range []string{sesDialogKind, teamDialogKind, ordersDialogKind} {
		if err := s.Save(ctx, 7, &org, kind, "x", map[string]any{}, dialogTTL); err != nil {
			t.Fatal(err)
		}
	}
	// The sessions dialog was opened first; the orders dialog is newest.
	kinds, err := s.Recent(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{ordersDialogKind, teamDialogKind, sesDialogKind}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("recent = %v, want %v", kinds, want)
	}
	// Touching the sessions dialog again makes it the newest.
	if err := s.Save(ctx, 7, &org, sesDialogKind, "y", map[string]any{}, dialogTTL); err != nil {
		t.Fatal(err)
	}
	if kinds, _ = s.Recent(ctx, 7); kinds[0] != sesDialogKind {
		t.Fatalf("recent after a save = %v", kinds)
	}
	if other, _ := s.Recent(ctx, 8); len(other) != 0 {
		t.Fatalf("the dialogs of another person leaked: %v", other)
	}

	var offered []string
	took := offerText([]string{ordersDialogKind, sesDialogKind}, func(kind string) bool {
		offered = append(offered, kind)
		return kind == ordersDialogKind
	})
	if !took || !reflect.DeepEqual(offered, []string{ordersDialogKind}) {
		t.Fatalf("the newest dialog took the text, yet it was offered to %v", offered)
	}
	offered = nil
	took = offerText([]string{ordersDialogKind, sesDialogKind}, func(kind string) bool {
		offered = append(offered, kind)
		return false
	})
	if took || !reflect.DeepEqual(offered, []string{ordersDialogKind, sesDialogKind}) {
		t.Fatalf("nobody took the text: took=%v offered=%v", took, offered)
	}
}

// Only the "Yes, refund" button of the final screen open now sends: a button
// of an earlier final screen (another key) or a press on any other step only
// redraws.
func TestRefundYesVerdict(t *testing.T) {
	t.Parallel()
	st := refundDialog{Key: "tg-11111111-2222-3333-4444-555555555555"}
	tag := refundKeyTag(st.Key)
	old := refundKeyTag("tg-99999999-2222-3333-4444-555555555555")
	if tag == old {
		t.Fatal("two keys must have different tags")
	}
	cases := []struct {
		step string
		st   refundDialog
		tag  string
		want string
	}{
		{refStepFinal, st, tag, refundYesSend},
		{refStepFinal, st, old, refundYesStaleFinal},
		{refStepFinal, st, "", refundYesStaleFinal},
		{refStepWord, st, tag, refundYesStalePick},
		{refStepPick, st, tag, refundYesStalePick},
		{refStepFinal, refundDialog{}, "", refundYesStalePick},
	}
	for _, c := range cases {
		if got := refundYesVerdict(c.step, c.st, c.tag); got != c.want {
			t.Errorf("step=%s key=%q tag=%q: %s, want %s", c.step, c.st.Key, c.tag, got, c.want)
		}
	}
}

// A session cancellation is never carried out by a press: the confirm
// screen's button only asks for the typed word, and a second press (or an
// older screen's button) cancels nothing.
func TestSesGoVerdict(t *testing.T) {
	t.Parallel()
	imp := &sesImpact{}
	cases := []struct {
		dlg  sessionDialog
		want string
	}{
		{sessionDialog{Mode: sesModeCancel, Step: sesStepConfirm, Impact: imp}, sesGoAskWord},
		{sessionDialog{Mode: sesModeCancelAll, Step: sesStepConfirm, Impact: imp}, sesGoAskWord},
		{sessionDialog{Mode: sesModeCancel, Step: sesStepType, Impact: imp}, sesGoNothing},
		{sessionDialog{Mode: sesModeCancelAll, Step: sesStepType, Impact: imp}, sesGoNothing},
		{sessionDialog{Mode: sesModeCancel, Step: sesStepConfirm}, sesGoNothing},
		{sessionDialog{Mode: sesModeCancel, Step: sesStepCard, Impact: imp}, sesGoNothing},
		{sessionDialog{Mode: "", Step: sesStepConfirm, Impact: imp}, sesGoNothing},
		{sessionDialog{Mode: sesModeMove, Step: sesStepConfirm, Impact: imp}, sesGoRun},
	}
	for _, c := range cases {
		dlg := c.dlg
		if got := sesGoVerdict(&dlg); got != c.want {
			t.Errorf("mode=%q step=%q impact=%v: %s, want %s", c.dlg.Mode, c.dlg.Step, c.dlg.Impact != nil, got, c.want)
		}
	}
}

// A refused retry from the refunds queue names the order by its number, and
// never prints a number it does not have.
func TestQueueRetryErrorText(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	id := &Identity{}
	num, ticket := int64(1000000731), int64(349)
	it := &openapi.OrderRefundQueueItem{OrderNumber: &num, TicketNumber: &ticket}
	for _, loc := range SupportedLocales {
		key, vars := b.queueRetryErrorText(loc, "bot.ref.err_progress", id, it)
		text := b.texts.T(loc, key, vars)
		if key != "bot.ref.err_progress" || !strings.Contains(text, strconv.FormatInt(num, 10)) {
			t.Errorf("%s: the order number is missing: %q", loc, text)
		}
		if strings.Contains(text, "—") || strings.Contains(text, "<no value>") {
			t.Errorf("%s: a placeholder leaked: %q", loc, text)
		}
		// Without the numbers of the refund a text that names the order is
		// replaced by the general refusal.
		key, vars = b.queueRetryErrorText(loc, "bot.ref.err_progress", id, nil)
		if key != "bot.rq.err_refused" {
			t.Errorf("%s: without a number the key is %q", loc, key)
		}
		if text := b.texts.T(loc, key, vars); strings.Contains(text, "<no value>") || strings.Contains(text, "№—") {
			t.Errorf("%s: %q", loc, text)
		}
		// A text that names no order keeps its key.
		if key, _ := b.queueRetryErrorText(loc, "bot.rq.err_ticket", id, nil); key != "bot.rq.err_ticket" {
			t.Errorf("%s: a text without the order changed to %q", loc, key)
		}
	}
}

// While a refund may have started (it went to the server and no answer
// came), no press may change the operation: only "Yes" with the same key,
// "Cancel" (it parks) and "back to the refund" stay open.
func TestRefundPressLocked(t *testing.T) {
	t.Parallel()
	attempted := refundDialog{Key: "tg-same", Attempted: true}
	for _, kind := range []string{"t", "all", "nt", "p", "go", "r", "ro", "b"} {
		if !refundPressLocked(attempted, kind) {
			t.Errorf("%q must be locked while the refund may have started", kind)
		}
		if refundPressLocked(refundDialog{Key: "tg-new"}, kind) {
			t.Errorf("%q must stay open before anything was sent", kind)
		}
	}
	for _, kind := range []string{"ok", "no", "cur"} {
		if refundPressLocked(attempted, kind) {
			t.Errorf("%q must stay open while the refund may have started", kind)
		}
	}
}
