package delivery

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

func TestTicketDownloadURL(t *testing.T) {
	id := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	got := ticketDownloadURL("https://api.example.com/", "deadbeef", id)
	want := "https://api.example.com/v1/public/checkout/deadbeef/tickets/11111111-1111-1111-1111-111111111111/pdf?src=email"
	if got != want {
		t.Errorf("ticketDownloadURL = %q, want %q", got, want)
	}
}

// Without an API address, for another kind of letter or without a ticket
// store the lines stay without links — and nothing is looked up (a nil store
// would panic otherwise).
func TestAttachDownloadLinks_SkipsWhenItCannotLink(t *testing.T) {
	members := []*letterMember{{ticketID: uuid.New()}}
	cases := []struct {
		name string
		kind string
		opts HandlerOptions
	}{
		{"no API address", templates.TemplateKindTicket, HandlerOptions{}},
		{"invitation", templates.TemplateKindInvitation, HandlerOptions{APIBaseURL: "https://api.example.com"}},
		{"no ticket store", templates.TemplateKindTicket, HandlerOptions{APIBaseURL: "https://api.example.com"}},
	}
	for _, c := range cases {
		lines := []templates.TicketLine{{Number: "1"}}
		attachDownloadLinks(context.Background(), c.opts, slog.Default(), c.kind, members, lines)
		if lines[0].DownloadURL != "" {
			t.Errorf("%s: DownloadURL = %q, want empty", c.name, lines[0].DownloadURL)
		}
	}
}
