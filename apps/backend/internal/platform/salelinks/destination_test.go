package salelinks

import (
	"net/url"
	"testing"
)

func TestBuildDestination(t *testing.T) {
	t.Run("ours win and the rest is kept", func(t *testing.T) {
		got := BuildDestination("https://tickets.example/acme/show?x=1&utm_source=old&ref=zzzzzzz#tickets",
			map[string]string{"utm_source": "qr", "utm_medium": "offline", "utm_campaign": "c1", "utm_content": "x7k2qpm", "utm_term": "", "other": "no"},
			"x7k2qpm")
		u, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		want := map[string]string{"x": "1", "utm_source": "qr", "utm_medium": "offline", "utm_campaign": "c1", "utm_content": "x7k2qpm", "ref": "x7k2qpm"}
		for k, v := range want {
			if q.Get(k) != v {
				t.Errorf("%s = %q, want %q (url %s)", k, q.Get(k), v, got)
			}
		}
		if _, has := q["utm_term"]; has {
			t.Error("an empty utm value must be skipped")
		}
		if _, has := q["other"]; has {
			t.Error("only utm_* keys are taken from the map")
		}
		if u.Fragment != "tickets" || u.Host != "tickets.example" || u.Path != "/acme/show" {
			t.Errorf("url shape changed: %s", got)
		}
	})
	t.Run("a destination with a session parameter keeps it", func(t *testing.T) {
		got := BuildDestination("https://t.example/a/b?session=11111111-1111-1111-1111-111111111111", nil, "x7k2qpm")
		u, _ := url.Parse(got)
		if u.Query().Get("session") != "11111111-1111-1111-1111-111111111111" || u.Query().Get("ref") != "x7k2qpm" {
			t.Errorf("got %s", got)
		}
	})
	t.Run("nothing to add leaves a clean address", func(t *testing.T) {
		if got := BuildDestination("https://t.example/a", nil, ""); got != "https://t.example/a" {
			t.Errorf("got %s", got)
		}
	})
	t.Run("empty and unparseable", func(t *testing.T) {
		if BuildDestination("", map[string]string{"utm_source": "qr"}, "x7k2qpm") != "" {
			t.Error("empty destination must stay empty")
		}
		bad := "http://[::1"
		if BuildDestination(bad, nil, "x7k2qpm") != bad {
			t.Error("an unparseable destination is returned unchanged")
		}
	})
	t.Run("values are escaped", func(t *testing.T) {
		got := BuildDestination("https://t.example/a", map[string]string{"utm_campaign": "a b&c"}, "")
		u, _ := url.Parse(got)
		if u.Query().Get("utm_campaign") != "a b&c" {
			t.Errorf("got %s", got)
		}
	})
}

func TestBuildDestination_KeepsOtherPairsVerbatim(t *testing.T) {
	got := BuildDestination("https://t.example/a?q=a%20b;c=1&z=2&utm_source=old&z=3&Utm_Source=keep#frag",
		map[string]string{"utm_source": "qr", "utm_campaign": "c 1"}, "x7k2qpm")
	want := "https://t.example/a?q=a%20b;c=1&z=2&z=3&Utm_Source=keep&utm_source=qr&utm_campaign=c+1&ref=x7k2qpm#frag"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestBuildDestination_UnsuppliedKeysStay(t *testing.T) {
	got := BuildDestination("https://t.example/a?utm_medium=mail&ref=zzzzzzz", map[string]string{"utm_source": "qr"}, "")
	if got != "https://t.example/a?utm_medium=mail&ref=zzzzzzz&utm_source=qr" {
		t.Errorf("got %s", got)
	}
}

func TestDefaultUTM_CampaignGoesThroughCleanUTM(t *testing.T) {
	if _, has := DefaultUTM("", "Spring Sale!", "x7k2qpm")["utm_campaign"]; has {
		t.Error("a slug with characters outside the allowlist must leave utm_campaign out")
	}
	if got := DefaultUTM("", " spring-2026 ", "x7k2qpm")["utm_campaign"]; got != "spring-2026" {
		t.Errorf("utm_campaign = %q", got)
	}
}

func TestDefaultUTM(t *testing.T) {
	for _, c := range []struct {
		placement string
		source    string
		medium    string
	}{
		{"", "qr", "offline"},
		{"poster", "qr", "offline"},
		{"flyer", "qr", "offline"},
		{"social", "social", "social"},
		{"EMAIL", "email", "email"},
		{"messenger", "messenger", "social"},
	} {
		got := DefaultUTM(c.placement, "concert-2026", "x7k2qpm")
		if got["utm_source"] != c.source || got["utm_medium"] != c.medium ||
			got["utm_campaign"] != "concert-2026" || got["utm_content"] != "x7k2qpm" {
			t.Errorf("DefaultUTM(%q) = %v", c.placement, got)
		}
	}
	empty := DefaultUTM("", "  ", "x7k2qpm")
	if _, has := empty["utm_campaign"]; has {
		t.Errorf("an empty campaign slug must leave the key out: %v", empty)
	}
	if _, has := DefaultUTM("", "c", "")["utm_content"]; has {
		t.Error("an empty code must leave utm_content out")
	}
}

func TestShortURL(t *testing.T) {
	for _, base := range []string{"https://go.arenasoldout.com", "https://go.arenasoldout.com/"} {
		if got := ShortURL(base, "x7k2qpm"); got != "https://go.arenasoldout.com/x7k2qpm" {
			t.Errorf("ShortURL(%q) = %s", base, got)
		}
	}
}
