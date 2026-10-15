package eventmaterials

import (
	"errors"
	"strings"
	"testing"
)

func TestParseVideoURL(t *testing.T) {
	const rt = "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name, in, provider, id string
	}{
		{"youtube watch", "https://www.youtube.com/watch?v=dQw4w9WgXcQ", ProviderYouTube, "dQw4w9WgXcQ"},
		{"youtube watch extra params", "https://youtube.com/watch?feature=share&v=dQw4w9WgXcQ&t=10", ProviderYouTube, "dQw4w9WgXcQ"},
		{"youtube mobile", "https://m.youtube.com/watch?v=dQw4w9WgXcQ", ProviderYouTube, "dQw4w9WgXcQ"},
		{"youtu.be", "https://youtu.be/dQw4w9WgXcQ?si=abc", ProviderYouTube, "dQw4w9WgXcQ"},
		{"shorts", "https://www.youtube.com/shorts/dQw4w9WgXcQ", ProviderYouTube, "dQw4w9WgXcQ"},
		{"embed", "https://www.youtube.com/embed/dQw4w9WgXcQ", ProviderYouTube, "dQw4w9WgXcQ"},
		{"nocookie embed", "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ", ProviderYouTube, "dQw4w9WgXcQ"},
		{"upper case host", "HTTPS://WWW.YouTube.COM/watch?v=dQw4w9WgXcQ", ProviderYouTube, "dQw4w9WgXcQ"},
		{"surrounding blanks", "  https://youtu.be/dQw4w9WgXcQ \n", ProviderYouTube, "dQw4w9WgXcQ"},
		{"youtube channel is a plain link", "https://www.youtube.com/@arena", ProviderHTTPS, ""},
		{"youtube bad id is a plain link", "https://www.youtube.com/watch?v=short", ProviderHTTPS, ""},
		{"youtube no v is a plain link", "https://www.youtube.com/watch", ProviderHTTPS, ""},
		{"vimeo", "https://vimeo.com/123456789", ProviderVimeo, "123456789"},
		{"vimeo unlisted", "https://vimeo.com/123456789/abcdef0123", ProviderVimeo, "123456789"},
		{"vimeo player", "https://player.vimeo.com/video/123456789?h=abc", ProviderVimeo, "123456789"},
		{"vimeo channel", "https://vimeo.com/channels/staffpicks/123456789", ProviderVimeo, "123456789"},
		{"vimeo profile is a plain link", "https://vimeo.com/someuser", ProviderHTTPS, ""},
		{"rutube", "https://rutube.ru/video/" + rt + "/", ProviderRutube, rt},
		{"rutube no slash upper", "https://rutube.ru/video/" + strings.ToUpper(rt), ProviderRutube, rt},
		{"rutube embed", "https://rutube.ru/play/embed/" + rt, ProviderRutube, rt},
		{"rutube short id is a plain link", "https://rutube.ru/video/abc/", ProviderHTTPS, ""},
		{"other site", "https://example.com/promo.mp4", ProviderHTTPS, ""},
		{"lookalike host", "https://notyoutube.com/watch?v=dQw4w9WgXcQ", ProviderHTTPS, ""},
		{"lookalike suffix", "https://youtube.com.evil.example/watch?v=dQw4w9WgXcQ", ProviderHTTPS, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseVideoURL(c.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Provider != c.provider || got.VideoID != c.id {
				t.Fatalf("got provider=%q id=%q, want %q %q", got.Provider, got.VideoID, c.provider, c.id)
			}
			if got.URL != strings.TrimSpace(c.in) {
				t.Fatalf("stored url %q, want the trimmed input", got.URL)
			}
		})
	}
}

func TestParseVideoURL_Invalid(t *testing.T) {
	cases := map[string]string{
		"empty":             "",
		"blanks only":       "   ",
		"http":              "http://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"javascript":        "javascript:alert(1)",
		"data":              "data:text/html;base64,PHNjcmlwdD4=",
		"ftp":               "ftp://example.com/a",
		"no scheme":         "www.youtube.com/watch?v=dQw4w9WgXcQ",
		"protocol relative": "//example.com/a",
		"inner space":       "https://example.com/a b",
		"inner newline":     "https://example.com/a\nb",
		"no host":           "https:///path",
		"credentials":       "https://user:pass@example.com/a",
		"garbage":           "not a url at all",
		"too long":          "https://example.com/" + strings.Repeat("a", MaxURLLength),
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseVideoURL(in); !errors.Is(err, ErrInvalidVideoURL) {
				t.Fatalf("want ErrInvalidVideoURL, got %v", err)
			}
		})
	}
}

func TestParseVideoURL_MaxLengthAccepted(t *testing.T) {
	in := "https://example.com/" + strings.Repeat("a", MaxURLLength-len("https://example.com/"))
	if len(in) != MaxURLLength {
		t.Fatalf("fixture length %d", len(in))
	}
	if _, err := ParseVideoURL(in); err != nil {
		t.Fatalf("a link of exactly %d characters must pass: %v", MaxURLLength, err)
	}
}
