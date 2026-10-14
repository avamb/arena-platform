package eventbot

import "testing"

// Canceling an order and canceling a session take different phrases, and a
// phrase typed for one never confirms the other (owner decision 2026-10-14).
// The refund word stays its own too.
func TestTypedWords_OrderAndSessionCancelDiffer(t *testing.T) {
	b := ecTestBot(t)
	for _, loc := range SupportedLocales {
		ses, ord := b.sesCancelWord(loc), b.ordCancelWord(loc)
		ref := b.texts.T(loc, "bot.ref.word", nil)
		if ses == "" || ord == "" || ses == sesCancelWordKey || ord == ordCancelWordKey {
			t.Fatalf("%s: a cancel phrase is missing from the catalog: %q / %q", loc, ses, ord)
		}
		seen := map[string]string{}
		for name, w := range map[string]string{"session": ses, "order": ord, "refund": ref} {
			n := normalizePhrase(w)
			if other, dup := seen[n]; dup {
				t.Fatalf("%s: the %s and %s phrases are the same: %q", loc, name, other, w)
			}
			seen[n] = name
		}
		if !b.sesIsCancelWord(loc, ses) || b.ordIsCancelWord(loc, ses) {
			t.Errorf("%s: %q must cancel a session and only a session", loc, ses)
		}
		if !b.ordIsCancelWord(loc, ord) || b.sesIsCancelWord(loc, ord) {
			t.Errorf("%s: %q must cancel an order and only an order", loc, ord)
		}
		if b.sesIsCancelWord(loc, ref) || b.ordIsCancelWord(loc, ref) {
			t.Errorf("%s: the refund word %q cancels something", loc, ref)
		}
		// The English phrase works in every language, the other language's
		// does not.
		if !b.sesIsCancelWord(loc, "cancel session") || !b.ordIsCancelWord(loc, "Cancel Order") {
			t.Errorf("%s: the English phrases must be accepted", loc)
		}
	}
}

// The single word that used to cancel both is no longer enough for either.
func TestTypedWords_TheOldSingleWordCancelsNothing(t *testing.T) {
	b := ecTestBot(t)
	for _, loc := range SupportedLocales {
		for _, typed := range []string{"", "cancel", "CANCEL", "ОТМЕНИТЬ", "отменить", "CANCELAR", "да", "yes", "сеанс", "заказ", "order", "session"} {
			if b.sesIsCancelWord(loc, typed) || b.ordIsCancelWord(loc, typed) {
				t.Errorf("%s: %q cancels something", loc, typed)
			}
		}
	}
}

// Case, extra spaces, quotes, a full stop and Spanish accents do not matter.
func TestTypedWords_FormsThatStillCount(t *testing.T) {
	b := ecTestBot(t)
	cases := []struct {
		loc, typed string
		session    bool
	}{
		{"ru", "отменить сеанс", true},
		{"ru", "  ОТМЕНИТЬ   СЕАНС  ", true},
		{"ru", "«Отменить заказ».", false},
		{"es", "cancelar sesion", true},
		{"es", "CANCELAR SESIÓN", true},
		{"es", "cancelar pedido!", false},
		{"en", "\"cancel order\"", false},
	}
	for _, c := range cases {
		ses, ord := b.sesIsCancelWord(c.loc, c.typed), b.ordIsCancelWord(c.loc, c.typed)
		if ses != c.session || ord == c.session {
			t.Errorf("%s %q: session=%v order=%v, want session=%v", c.loc, c.typed, ses, ord, c.session)
		}
	}
}

// Each action takes its own phrase and nothing else: of every confirmation
// word the bot knows, in every language, an order cancel accepts only the
// order's phrase (the person's language or English) and a session cancel
// only the session's.
func TestTypedWords_EachActionAcceptsOnlyItsOwnPhrase(t *testing.T) {
	b := ecTestBot(t)
	keys := []string{ordCancelWordKey, sesCancelWordKey, "bot.ref.word", "bot.evs.delete_word", "bot.inv.revoke_word", "bot.promo.delete_word"}
	var all []string
	for _, loc := range SupportedLocales {
		for _, k := range keys {
			all = append(all, b.texts.T(loc, k, nil))
		}
	}
	for _, loc := range SupportedLocales {
		ownOrd := map[string]bool{b.ordCancelWord(loc): true, b.ordCancelWord("en"): true}
		ownSes := map[string]bool{b.sesCancelWord(loc): true, b.sesCancelWord("en"): true}
		for _, w := range all {
			if got := b.ordIsCancelWord(loc, w); got != ownOrd[w] {
				t.Errorf("%s: order cancel accepts %q = %v, want %v", loc, w, got, ownOrd[w])
			}
			if got := b.sesIsCancelWord(loc, w); got != ownSes[w] {
				t.Errorf("%s: session cancel accepts %q = %v, want %v", loc, w, got, ownSes[w])
			}
		}
	}
}
