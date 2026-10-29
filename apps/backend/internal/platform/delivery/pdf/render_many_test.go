package pdf

import (
	"bytes"
	"context"
	"regexp"
	"testing"
)

var pageObjRe = regexp.MustCompile(`/Type /Page[^s]`)

func manyTickets(t *testing.T, n int) []Ticket {
	t.Helper()
	out := make([]Ticket, 0, n)
	for i := 0; i < n; i++ {
		tk := invitationTicket(t)
		// Distinct tickets: their own id (image names) and seat.
		tk.TicketID = tk.TicketID + "-" + string(rune('a'+i))
		tk.SeatNumber = string(rune('1' + i))
		out = append(out, tk)
	}
	return out
}

func TestRenderMany_OnePagePerTicketInOrder(t *testing.T) {
	tickets := manyTickets(t, 3)
	out, err := RenderMany(context.Background(), tickets)
	if err != nil {
		t.Fatalf("RenderMany: %v", err)
	}
	if got := len(pageObjRe.FindAll(out, -1)); got != 3 {
		t.Fatalf("pages = %d, want 3", got)
	}
}

func TestRenderMany_SingleTicketIsTheSameBytesAsRender(t *testing.T) {
	tk := invitationTicket(t)
	one, err := Render(context.Background(), tk)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	many, err := RenderMany(context.Background(), []Ticket{tk})
	if err != nil {
		t.Fatalf("RenderMany: %v", err)
	}
	if !bytes.Equal(one, many) {
		t.Fatalf("a one-ticket RenderMany differs from Render (%d vs %d bytes)", len(many), len(one))
	}
}

func TestRenderMany_RefusesNothingAndInvalidTickets(t *testing.T) {
	if _, err := RenderMany(context.Background(), nil); err == nil {
		t.Fatal("no tickets must be refused")
	}
	bad := invitationTicket(t)
	bad.EventName = ""
	if _, err := RenderMany(context.Background(), []Ticket{invitationTicket(t), bad}); err == nil {
		t.Fatal("an invalid ticket must fail the whole document")
	}
}
