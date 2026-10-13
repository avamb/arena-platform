package httpserver

import (
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func TestContactFormHostnames_FollowTheTicketsBaseURL(t *testing.T) {
	for _, c := range []struct{ base, want string }{
		{"https://tickets.arenasoldout.com", "tickets.arenasoldout.com"},
		{"http://localhost:4174", "localhost"},
		{"", ""},
	} {
		s := &Server{cfg: &config.Config{PublicTicketsBaseURL: c.base}}
		got := s.contactFormHostnames()
		if c.want == "" {
			if got != nil {
				t.Errorf("%q: got %v, want no host check", c.base, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%q: got %v, want [%s]", c.base, got, c.want)
		}
	}
}
