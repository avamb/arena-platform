package eventbot

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
)

// The buyer-letter button says what WILL happen, as a checkbox, and the
// screens repeat the outcome in a sentence (owner decision 2026-10-13).
func TestRefundLetter_CheckboxSaysWhatHappens(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	for _, c := range []struct {
		noLetter       bool
		button, result string
	}{
		{false, "☑ Отправить письмо покупателю", "Покупатель получит письмо."},
		{true, "☐ Не отправлять письмо покупателю", "Письмо покупателю не отправляется."},
	} {
		key := "bot.ref.letter_on_btn"
		if c.noLetter {
			key = "bot.ref.letter_off_btn"
		}
		if got := b.texts.T("ru", key, nil); got != c.button {
			t.Errorf("button = %q; want %q", got, c.button)
		}
		if got := b.texts.T("ru", refundLetterOutcome(refundDialog{NoLetter: c.noLetter}), nil); got != c.result {
			t.Errorf("outcome = %q; want %q", got, c.result)
		}
	}
	final := b.texts.T("ru", "bot.ref.final", map[string]any{"Letter": "Покупатель получит письмо."})
	if !strings.Contains(final, "\nПокупатель получит письмо.\n") {
		t.Errorf("the final check does not state the letter on its own line:\n%s", final)
	}
}

// Only a platform superadmin is offered the scan exception; the offer is
// stored so the press can name its ticket by index.
func TestScanExcRows_OnlyForThePlatformSuperadmin(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	b.logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	b.dialogs = newMemDialogStore(&testClock{t: time.Now()})
	org, order := uuid.New(), uuid.New()
	tickets := []scanExcTicket{{ID: uuid.New(), Label: "№349"}}
	back := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "«", CallbackData: "or:v:x"}}}}
	owner := &Identity{Link: gen.BotTelegramLinkRow{TelegramUserID: 42, Locale: "ru"}}
	if kb := b.withScanExcRows(context.Background(), back, owner, org, order, 1, tickets); len(kb.InlineKeyboard) != 1 {
		t.Fatalf("an owner was offered the exception: %+v", kb)
	}
	if _, found, _, _ := b.dialogs.Load(context.Background(), 42, scanExcDialogKind, nil); found {
		t.Fatal("an offer was stored for an owner")
	}
	op := &Identity{Link: gen.BotTelegramLinkRow{TelegramUserID: 43, Locale: "ru"}, Superadmin: true}
	kb := b.withScanExcRows(context.Background(), back, op, org, order, 1, tickets)
	if len(kb.InlineKeyboard) != 2 || kb.InlineKeyboard[0][0].CallbackData != "or:f:x:0" {
		t.Fatalf("the operator's keyboard = %+v", kb)
	}
	if got := kb.InlineKeyboard[0][0].Text; got != "🔓 Разрешить возврат билета №349 (24 ч)" {
		t.Errorf("button = %q", got)
	}
	var st scanExcDialog
	step, found, _, err := b.dialogs.Load(context.Background(), 43, scanExcDialogKind, &st)
	if err != nil || !found || step != scanExcStepOffer || st.OrderID != order || len(st.Tickets) != 1 {
		t.Fatalf("offer = %+v (step %q, found %v, err %v)", st, step, found, err)
	}
}

// The refusal for a scanned ticket names the ticket and what to do next.
func TestRefundScannedRefusal_NamesTheTicketAndTheNextStep(t *testing.T) {
	t.Parallel()
	b := ecTestBot(t)
	got := b.texts.T("ru", "bot.ref.err_scanned", map[string]any{
		"Ticket": "№349", "Next": b.texts.T("ru", b.scannedNextKey(&Identity{}), nil),
	})
	for _, want := range []string{"Билет №349 уже отсканирован на входе.", "Деньги за него вернуть нельзя.", "поддержку"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusal lacks %q:\n%s", want, got)
		}
	}
	if b.scannedNextKey(&Identity{Superadmin: true}) != "bot.ref.scanned_next_operator" {
		t.Error("the operator is not told about the button")
	}
}

func TestScanExcErrorKey(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		err  error
		key  string
		keep bool
	}{
		{&APIError{Status: http.StatusForbidden}, "bot.ref.exc_forbidden", false},
		{&APIError{Status: http.StatusConflict, Code: codeTicketNotScanned}, "bot.ref.exc_not_scanned", false},
		{&APIError{Status: http.StatusConflict, Code: codeScanExcExists}, "bot.ref.exc_exists", false},
		{&APIError{Status: http.StatusNotFound}, "bot.ref.exc_not_found", false},
		{&APIError{Status: http.StatusServiceUnavailable}, "bot.ref.exc_failed", true},
		{io.ErrUnexpectedEOF, "bot.ref.exc_failed", true},
	} {
		key, keep := scanExcErrorKey(c.err)
		if key != c.key || keep != c.keep {
			t.Errorf("%v: got (%s, %v), want (%s, %v)", c.err, key, keep, c.key, c.keep)
		}
	}
}
