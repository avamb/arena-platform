package eventmaterials

import "testing"

func TestEmbedURL(t *testing.T) {
	cases := []struct{ provider, id, want string }{
		{ProviderYouTube, "dQw4w9WgXcQ", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ"},
		{ProviderVimeo, "76979871", "https://player.vimeo.com/video/76979871"},
		{ProviderRutube, "0123456789abcdef0123456789abcdef", "https://rutube.ru/play/embed/0123456789abcdef0123456789abcdef"},
		{ProviderHTTPS, "", ""},
		{ProviderYouTube, "../x", ""},
		{ProviderVimeo, "abc", ""},
		{"other", "dQw4w9WgXcQ", ""},
	}
	for _, c := range cases {
		if got := EmbedURL(c.provider, c.id); got != c.want {
			t.Errorf("EmbedURL(%q,%q) = %q, want %q", c.provider, c.id, got, c.want)
		}
	}
}
