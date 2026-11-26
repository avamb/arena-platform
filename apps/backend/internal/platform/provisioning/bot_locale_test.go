package provisioning

import "testing"

func TestBotLocale(t *testing.T) {
	cases := map[string]string{
		"ru": "ru",
		"es": "es",
		"uk": "uk",
		"de": "en",
		"":   "en",
	}
	for in, want := range cases {
		if got := botLocale(in); got != want {
			t.Errorf("botLocale(%q) = %q, want %q", in, got, want)
		}
	}
}
