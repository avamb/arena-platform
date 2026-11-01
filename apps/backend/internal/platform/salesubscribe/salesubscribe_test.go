package salesubscribe

import "testing"

func TestNormalizeEmail(t *testing.T) {
	for in, want := range map[string]string{
		"Anna@Example.COM":     "anna@example.com",
		"  anna@example.com  ": "anna@example.com",
	} {
		got, err := NormalizeEmail(in)
		if err != nil || got != want {
			t.Errorf("NormalizeEmail(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "anna", "anna@localhost", "Anna <anna@example.com>", "a b@example.com"} {
		if _, err := NormalizeEmail(bad); err != ErrInvalidEmail {
			t.Errorf("NormalizeEmail(%q) accepted", bad)
		}
	}
}

func TestCleanPageURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://site.example/event/?a=1#tickets": "https://site.example/event/?a=1",
		"http://site.example/x":                   "http://site.example/x",
		"javascript:alert(1)":                     "",
		"/event/":                                 "",
		"https://user:pw@site.example/":           "",
		"ftp://site.example/":                     "",
	} {
		if got := CleanPageURL(in); got != want {
			t.Errorf("CleanPageURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanLocale(t *testing.T) {
	for in, want := range map[string]string{"es": "es", "pt-BR": "pt-BR", "e": "", "ru;drop": "", "": ""} {
		if got := CleanLocale(in); got != want {
			t.Errorf("CleanLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTokenHash(t *testing.T) {
	a, err := NewToken()
	if err != nil || len(a) != 64 {
		t.Fatalf("NewToken = %q, %v", a, err)
	}
	b, _ := NewToken()
	if a == b || HashToken(a) == a || HashToken(a) != HashToken(" "+a+" ") {
		t.Fatal("tokens must be random and hashed")
	}
}
