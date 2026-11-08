package delivery

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery/templates"
)

// contactFakeDB answers the three reads the buyer contact makes.
type contactFakeDB struct {
	promoterEmail string // "" with havePromoter: a promoter without an address
	havePromoter  bool
	orgEmail      string
	eventID       uuid.UUID
	orgID         uuid.UUID
}

type contactRow struct{ vals []any }

func (r contactRow) Scan(dest ...any) error {
	if r.vals == nil {
		return pgx.ErrNoRows
	}
	for i, d := range dest {
		switch p := d.(type) {
		case *uuid.UUID:
			*p = r.vals[i].(uuid.UUID)
		case *string:
			*p = r.vals[i].(string)
		case *bool:
			*p = r.vals[i].(bool)
		}
	}
	return nil
}

func (f contactFakeDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	switch {
	case strings.Contains(sql, "FROM   event_promoters"):
		if !f.havePromoter {
			return contactRow{}
		}
		return contactRow{[]any{uuid.New(), "Promoter", f.promoterEmail, "", false}}
	case strings.Contains(sql, "FROM   organizations"):
		return contactRow{[]any{f.orgID, "Org", f.orgEmail, "", false}}
	case strings.Contains(sql, "FROM tickets t"):
		return contactRow{[]any{f.eventID, f.orgID}}
	}
	return contactRow{}
}
func (contactFakeDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (contactFakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }

func TestBuyerContactEmail_PromoterThenOrganizationThenNothing(t *testing.T) {
	ctx := context.Background()
	org, ev := uuid.New(), uuid.New()

	// A promoter with an address wins.
	db := contactFakeDB{havePromoter: true, promoterEmail: "promoter@x.example", orgEmail: "org@x.example", orgID: org, eventID: ev}
	if got := buyerContactEmail(ctx, db, ev, org, nil); got != "promoter@x.example" {
		t.Errorf("promoter: got %q", got)
	}
	// A promoter without an address falls back to the organization.
	db.promoterEmail = ""
	if got := buyerContactEmail(ctx, db, ev, org, nil); got != "org@x.example" {
		t.Errorf("promoter without address: got %q", got)
	}
	// No promoter: the organization.
	db.havePromoter = false
	if got := buyerContactEmail(ctx, db, ev, org, nil); got != "org@x.example" {
		t.Errorf("no promoter: got %q", got)
	}
	// A letter without an event: the organization.
	if got := buyerContactEmail(ctx, db, uuid.Nil, org, nil); got != "org@x.example" {
		t.Errorf("no event: got %q", got)
	}
	// Nobody has an address.
	db.orgEmail = ""
	if got := buyerContactEmail(ctx, db, ev, org, nil); got != "" {
		t.Errorf("no address: got %q", got)
	}
}

// The ticket letter's payload takes the organizer's address, drops the
// contact-form stand-in, and the sent message's Reply-To is that address. With
// no address the Reply-To of the older rule is untouched.
func TestApplyBuyerContact_SetsPayloadAndReplyTo(t *testing.T) {
	ctx := context.Background()
	org, ev, tk := uuid.New(), uuid.New(), uuid.New()
	db := contactFakeDB{orgEmail: "marketing@einatwinery.com", orgID: org, eventID: ev}

	p := Payload{ContactFormURL: "https://tickets.example/page#contact"}
	applyBuyerContact(ctx, db, tk, &p, nil)
	if p.OrgContactEmail != "marketing@einatwinery.com" || p.ContactFormURL != "" {
		t.Fatalf("payload = %+v", p)
	}
	msg := ticketEmailMessage(p, "buyer@example.test", "s", "<p>h</p>", "t")
	if msg.ReplyTo != "marketing@einatwinery.com" {
		t.Errorf("Reply-To = %q", msg.ReplyTo)
	}

	db.orgEmail = ""
	p = Payload{ContactFormURL: "https://tickets.example/page#contact"}
	applyBuyerContact(ctx, db, tk, &p, nil)
	if p.OrgContactEmail != "" || p.ContactFormURL == "" {
		t.Errorf("payload changed without an address: %+v", p)
	}
	if msg := ticketEmailMessage(p, "buyer@example.test", "s", "<p>h</p>", "t"); msg.ReplyTo != "" {
		t.Errorf("Reply-To = %q, want none", msg.ReplyTo)
	}
}

// The org-level letters (my tickets, marketing, reminders, subscribers) set the
// Reply-To through withReplyTo; an empty address leaves none.
func TestWithReplyTo(t *testing.T) {
	m := email.Message{To: "b@example.test"}
	withReplyTo(&m, " marketing@einatwinery.com ")
	if m.ReplyTo != "marketing@einatwinery.com" {
		t.Errorf("Reply-To = %q", m.ReplyTo)
	}
	withReplyTo(&m, "")
	if m.ReplyTo != "" {
		t.Errorf("Reply-To = %q, want none", m.ReplyTo)
	}
}

// A ticket letter rendered from a payload that carries the organizer's address
// shows the block and links the order number, end to end through Render.
func TestTicketLetterShowsTheBlockWithTheOrderNumber(t *testing.T) {
	r, err := templates.New()
	if err != nil {
		t.Fatal(err)
	}
	p := Payload{OrgContactEmail: "marketing@einatwinery.com", OrderNumber: "9096"}
	out, err := r.Render(templates.TemplateKindTicket, "en", templates.Data{
		EventName: "Wine night",
		Branding:  templates.Branding{OrgName: "Einat", ContactEmail: p.OrgContactEmail},
		Contact:   templates.BuyerContact{Email: p.OrgContactEmail, OrderNumber: p.OrderNumber},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Please do not reply to this e-mail.", "mailto:marketing@einatwinery.com?subject=Order%209096", "Contact the organizer"} {
		if !strings.Contains(out.HTMLBody, want) || !strings.Contains(out.TextBody, want) {
			t.Errorf("letter lacks %q", want)
		}
	}
}
