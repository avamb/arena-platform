package eventbot

import (
	"testing"
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
