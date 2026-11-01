package hfeed

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

type fakeAccessLog struct {
	pdf   []gen.InsertTicketPDFDownloadParams
	views []gen.InsertTicketsViewParams
	err   error
}

func (f *fakeAccessLog) InsertTicketPDFDownload(_ context.Context, arg gen.InsertTicketPDFDownloadParams) error {
	f.pdf = append(f.pdf, arg)
	return f.err
}

func (f *fakeAccessLog) InsertTicketsView(_ context.Context, arg gen.InsertTicketsViewParams) (bool, error) {
	f.views = append(f.views, arg)
	return f.err == nil, f.err
}

func TestTicketAccessSource(t *testing.T) {
	cases := map[string]string{
		"/x":              TicketAccessSourceWeb,
		"/x?src=email":    TicketAccessSourceEmail,
		"/x?src=EMAIL":    TicketAccessSourceWeb, // only the exact value the letter sends
		"/x?src=whatever": TicketAccessSourceWeb, // unknown is web, never an error
		"/x?src=":         TicketAccessSourceWeb,
	}
	for target, want := range cases {
		if got := ticketAccessSource(httptest.NewRequest("GET", target, nil)); got != want {
			t.Errorf("%s: source = %q, want %q", target, got, want)
		}
	}
}

// A PDF request is logged with the client address, the browser string, the
// source, the checkout, the ticket and the organization.
func TestRecordPDFDownload_WritesTheRow(t *testing.T) {
	rec := &fakeAccessLog{}
	h := (&Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).WithTicketAccessLog(rec)
	r := httptest.NewRequest("GET", "/v1/public/checkout/tok/tickets/x/pdf?src=email", nil)
	r.RemoteAddr = "203.0.113.7:51234"
	r.Header.Set("User-Agent", "Mozilla/5.0 Test")
	cs, org, ticket := uuid.New(), uuid.New(), uuid.New()

	h.recordPDFDownload(r, cs, org, ticket)

	if len(rec.pdf) != 1 {
		t.Fatalf("rows = %d, want 1", len(rec.pdf))
	}
	got := rec.pdf[0]
	want := gen.InsertTicketPDFDownloadParams{
		CheckoutSessionID: cs, TicketID: ticket, OrgID: org,
		Source: TicketAccessSourceEmail, IP: "203.0.113.7", UserAgent: "Mozilla/5.0 Test",
	}
	if got != want {
		t.Errorf("row = %+v, want %+v", got, want)
	}
}

// An address that does not parse is stored as unknown, and a failing insert
// is swallowed: the buyer still gets the tickets.
func TestRecordTicketsView_BadAddressAndFailureAreHarmless(t *testing.T) {
	rec := &fakeAccessLog{err: errors.New("db down")}
	h := (&Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}).WithTicketAccessLog(rec)
	r := httptest.NewRequest("GET", "/v1/public/checkout/tok", nil)
	r.RemoteAddr = "not-an-ip"

	h.recordTicketsView(r, uuid.New(), uuid.New())

	if len(rec.views) != 1 {
		t.Fatalf("rows = %d, want 1", len(rec.views))
	}
	if rec.views[0].IP != "" || rec.views[0].Source != TicketAccessSourceWeb {
		t.Errorf("row = %+v, want unknown IP and source web", rec.views[0])
	}
}

// Without a recorder nothing is attempted.
func TestRecord_NoRecorderIsANoOp(_ *testing.T) {
	h := &Handler{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	r := httptest.NewRequest("GET", "/x", nil)
	h.recordPDFDownload(r, uuid.New(), uuid.New(), uuid.New())
	h.recordTicketsView(r, uuid.New(), uuid.New())
}

// The Go constants and the CHECK constraints of migration 0184 are two lists
// in two languages; a value accepted here but not there would fail every
// insert with 23514 (and, being best effort, silently log nothing).
func TestTicketAccessValues_MatchMigration(t *testing.T) {
	raw, err := migrations.FS.ReadFile(migrations.Dir + "/0184_ticket_access_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	if i := strings.Index(up, "-- +goose Down"); i >= 0 {
		up = up[:i]
	}
	checks := map[string][]string{
		"ticket_access_events_source_check": {TicketAccessSourceWeb, TicketAccessSourceEmail},
		"ticket_access_events_action_check": {TicketAccessActionPDFDownload, TicketAccessActionTicketsView},
	}
	for name, values := range checks {
		i := strings.Index(up, name)
		if i < 0 {
			t.Fatalf("migration 0184 has no %s", name)
		}
		clause := up[i:]
		if end := strings.Index(clause, ")),"); end >= 0 {
			clause = clause[:end]
		}
		for _, v := range values {
			if !strings.Contains(clause, "'"+v+"'") {
				t.Errorf("%s does not accept %q", name, v)
			}
		}
	}
}
