package eventbot

// typed_words.go — the phrases a person types to confirm an action that
// cannot be undone. Each action has its own phrase (owner decision
// 2026-10-14: canceling an order and canceling a session once took the same
// word, so a word typed for one could cancel the other). Case, extra spaces,
// surrounding quotes or a final full stop and Spanish accents do not matter;
// the English phrase is accepted in every language.

import "strings"

const (
	sesCancelWordKey = "bot.ses.cancel_word"
	ordCancelWordKey = "bot.ord.cancel_word"
	// The two phrases of the Disputes screens (spec 51.3 section 9.2): each
	// answer to a dispute has its own, so a word typed for one never confirms
	// the other.
	disputeSubmitWordKey = "bot.dp.submit_word"
	disputeAcceptWordKey = "bot.dp.accept_word"
	// reissueWordKey confirms "Replace tickets" (spec 63, decision R-5).
	reissueWordKey = "bot.reissue.word"
)

// reissueWord is the phrase the organizer types to replace an order's tickets.
func (b *Bot) reissueWord(loc string) string {
	return b.texts.T(loc, reissueWordKey, nil)
}

// isReissueWord reports whether the typed text confirms a ticket reissue.
func (b *Bot) isReissueWord(loc, text string) bool {
	return b.typedPhrase(loc, reissueWordKey, text)
}

// ordCancelWord is the phrase the organizer types to cancel an unpaid order.
func (b *Bot) ordCancelWord(loc string) string {
	return b.texts.T(loc, ordCancelWordKey, nil)
}

// ordIsCancelWord reports whether the typed text cancels an order.
func (b *Bot) ordIsCancelWord(loc, text string) bool {
	return b.typedPhrase(loc, ordCancelWordKey, text)
}

// typedPhrase reports whether text is the phrase of key in the person's
// language or in English.
func (b *Bot) typedPhrase(loc, key, text string) bool {
	got := normalizePhrase(text)
	if got == "" {
		return false
	}
	return got == normalizePhrase(b.texts.T(loc, key, nil)) || got == normalizePhrase(b.texts.T("en", key, nil))
}

// accentFold maps the accented letters of the bot's languages to plain ones,
// so "CANCELAR SESION" typed without the accent still counts.
var accentFold = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u", "ü", "u", "ñ", "n", "ё", "е",
)

// normalizePhrase folds case, accents and spacing, and drops the quotes and
// the closing punctuation a person may add around the phrase.
func normalizePhrase(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "\"'«»“”.!")
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return accentFold.Replace(s)
}
