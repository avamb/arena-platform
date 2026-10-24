package himports

import (
	"context"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/egress"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/mediastore"
)

// A poster on a non-public address is skipped with one fixed sentence, so the
// import answer reveals nothing about the platform's network.
func TestPosterPrecheck_RefusesNonPublicAddresses(t *testing.T) {
	h := New(nil, nil, nil, nil).WithMedia(&mediastore.Repo{})
	h.posterEgress = egress.Policy{AllowHTTP: true, Private: egress.PrivateDeny}

	for _, raw := range []string{
		"http://127.0.0.1:8080/poster.png",
		"http://169.254.169.254/latest/meta-data/",
		"https://10.0.0.5/p.jpg",
		"http://localhost/p.jpg",
		"http://[::1]/p.jpg",
	} {
		if got := h.posterPrecheck(context.Background(), raw); got != posterNotPublic {
			t.Errorf("posterPrecheck(%q) = %q, want %q", raw, got, posterNotPublic)
		}
	}
	if got := h.posterPrecheck(context.Background(), "https://8.8.8.8/poster.png"); got != "" {
		t.Errorf("a public address must be tried, got %q", got)
	}
	if got := h.posterPrecheck(context.Background(), "ftp://8.8.8.8/p.png"); got == "" {
		t.Error("a non-http scheme must be refused")
	}
}
