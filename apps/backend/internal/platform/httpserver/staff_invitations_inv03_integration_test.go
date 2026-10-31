//go:build integration

// Staff invitations, task INV-03 (spec 46, section 3.4): how the tickets of an
// invitation reach the guest. Through the real router and the real ticket
// issuance: the template and the recipient the first delivery gets by order
// source and delivery mode, the order-wide PDF, the resend, the letter that
// tells a guest the invitation was cancelled, and the session-change dry run.
package httpserver

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
)

// inv03Issue issues an invitation as the owner and mints its tickets the way
// the checkout.issue_tickets worker job does (which also queues the delivery).
func inv03Issue(t *testing.T, f *inv02Fx, seats []string, delivery, guestEmail string) (orderID, csID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	code, body := f.issue(f.o.owner, inv02Body(uuid.New(), seats, nil, 0, delivery, guestEmail))
	if code != http.StatusCreated {
		t.Fatalf("issue %s: %d %s", delivery, code, body["_raw"])
	}
	orderID = uuid.MustParse(body["order_id"].(string))
	if err := f.pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, orderID).Scan(&csID); err != nil {
		t.Fatalf("checkout session: %v", err)
	}
	cs, err := gen.New(f.pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("checkout session row: %v", err)
	}
	if tickets, err := f.srv.ticketsHandler().IssueTicketsForCheckout(ctx, cs); err != nil || len(tickets) != len(seats) {
		t.Fatalf("IssueTicketsForCheckout: %d tickets, %v", len(tickets), err)
	}
	return orderID, csID
}

type inv03Delivery struct {
	jobs      int
	recipient map[string]bool
	templates map[string]int
}

// inv03Deliveries reads what the first delivery queued for an order's tickets.
func inv03Deliveries(t *testing.T, f *inv02Fx, csID uuid.UUID) inv03Delivery {
	t.Helper()
	ctx := context.Background()
	out := inv03Delivery{recipient: map[string]bool{}, templates: map[string]int{}}
	rows, err := f.pool.Query(ctx, `
		SELECT COALESCE(dj.recipient_email, '') FROM delivery_jobs dj
		JOIN tickets t ON t.id = dj.ticket_id WHERE t.checkout_session_id = $1`, csID)
	if err != nil {
		t.Fatalf("delivery_jobs: %v", err)
	}
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out.jobs++
		out.recipient[r] = true
	}
	rows.Close()
	wrows, err := f.pool.Query(ctx, `
		SELECT COALESCE(wj.payload->>'template', '') FROM worker_jobs wj
		WHERE wj.job_type = 'ticket.deliver'
		  AND wj.payload->>'ticket_id' IN (SELECT id::text FROM tickets WHERE checkout_session_id = $1)`, csID)
	if err != nil {
		t.Fatalf("worker_jobs: %v", err)
	}
	for wrows.Next() {
		var tpl string
		if err := wrows.Scan(&tpl); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out.templates[tpl]++
	}
	wrows.Close()
	return out
}

func TestINV03_FirstDelivery_InvitationTemplateAndRecipientByMode(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	guest := "guest-" + uuid.NewString()[:8] + "@example.test"
	var ownerEmail string
	if err := f.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, f.ownerID).Scan(&ownerEmail); err != nil {
		t.Fatalf("owner email: %v", err)
	}

	// guest_email: the guest's address, the invitation template, every ticket.
	_, cs1 := inv03Issue(t, f, []string{"A|1|1", "A|1|5"}, "guest_email", guest)
	d := inv03Deliveries(t, f, cs1)
	if d.jobs != 2 || len(d.recipient) != 1 || !d.recipient[guest] || d.templates["invitation"] != 2 || len(d.templates) != 1 {
		t.Fatalf("guest_email delivery: %+v", d)
	}

	// my_email: the issuer's address, never written onto the order.
	order2, cs2 := inv03Issue(t, f, []string{"A|1|6", "A|1|7"}, "my_email", "")
	d = inv03Deliveries(t, f, cs2)
	if d.jobs != 2 || len(d.recipient) != 1 || !d.recipient[ownerEmail] || d.templates["invitation"] != 2 {
		t.Fatalf("my_email delivery: %+v (owner %s)", d, ownerEmail)
	}
	var buyerEmail *string
	if err := f.pool.QueryRow(ctx, `SELECT buyer_email FROM orders WHERE id = $1`, order2).Scan(&buyerEmail); err != nil || buyerEmail != nil {
		t.Fatalf("my_email order buyer_email = %v (%v), want NULL", buyerEmail, err)
	}

	// pdf_only: no letter at all.
	_, cs3 := inv03Issue(t, f, []string{"A|1|8"}, "pdf_only", "")
	d = inv03Deliveries(t, f, cs3)
	if d.jobs != 0 || len(d.templates) != 0 {
		t.Fatalf("pdf_only queued a letter: %+v", d)
	}
}

var pdfPageRe = regexp.MustCompile(`/Type /Page[^s]`)

func (f *inv02Fx) rawGet(token, path string) (int, http.Header, []byte) {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, f.ts.URL+path, nil)
	if err != nil {
		f.t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := f.ts.Client().Do(req)
	if err != nil {
		f.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	var buf strings.Builder
	b := make([]byte, 32<<10)
	for {
		n, rerr := resp.Body.Read(b)
		buf.Write(b[:n])
		if rerr != nil {
			break
		}
	}
	return resp.StatusCode, resp.Header, []byte(buf.String())
}

func TestINV03_TicketsPDF_HoldsEveryTicketOfTheOrder(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	code, body := f.issue(f.o.owner, inv02Body(uuid.New(), []string{"A|1|4", "A|1|5", "A|1|6"}, nil, 0, "pdf_only", ""))
	if code != http.StatusCreated {
		t.Fatalf("issue: %d %s", code, body["_raw"])
	}
	orderID := uuid.MustParse(body["order_id"].(string))
	systemID := int64(body["system_id"].(float64))
	pdfPath := "/v1/organizations/" + f.o.org.String() + "/invitations/" + orderID.String() + "/tickets.pdf"

	// Tickets are minted by the worker job: not yet.
	if code, _, raw := f.rawGet(f.manager, pdfPath); code != http.StatusConflict || !strings.Contains(string(raw), "invitation.tickets_not_issued") {
		t.Fatalf("pdf before issuance: %d %s", code, raw)
	}

	var csID uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT checkout_session_id FROM orders WHERE id = $1`, orderID).Scan(&csID); err != nil {
		t.Fatalf("cs: %v", err)
	}
	cs, err := gen.New(f.pool).GetCheckoutSessionByID(ctx, csID)
	if err != nil {
		t.Fatalf("cs row: %v", err)
	}
	tickets, err := f.srv.ticketsHandler().IssueTicketsForCheckout(ctx, cs)
	if err != nil || len(tickets) != 3 {
		t.Fatalf("issue tickets: %d %v", len(tickets), err)
	}

	code, hdr, raw := f.rawGet(f.manager, pdfPath)
	if code != http.StatusOK {
		t.Fatalf("pdf: %d %s", code, raw)
	}
	if hdr.Get("Content-Type") != "application/pdf" ||
		!strings.Contains(hdr.Get("Content-Disposition"), "attachment") ||
		!strings.Contains(hdr.Get("Content-Disposition"), `invitation-`+strconv.FormatInt(systemID, 10)+`.pdf`) {
		t.Fatalf("pdf headers: %v", hdr)
	}
	if !strings.HasPrefix(string(raw), "%PDF-") || len(pdfPageRe.FindAll(raw, -1)) != 3 {
		t.Fatalf("pdf has %d pages, want 3", len(pdfPageRe.FindAll(raw, -1)))
	}

	// A cancelled ticket leaves the file.
	cancelPath := "/v1/organizations/" + f.o.org.String() + "/invitations/" + orderID.String() + "/cancel"
	if code, body := f.do(f.o.owner, http.MethodPost, cancelPath, `{"ticket_ids":["`+tickets[0].ID.String()+`"],"notify_guest":false}`); code != http.StatusOK {
		t.Fatalf("cancel one: %d %s", code, body["_raw"])
	}
	if code, _, raw = f.rawGet(f.manager, pdfPath); code != http.StatusOK || len(pdfPageRe.FindAll(raw, -1)) != 2 {
		t.Fatalf("pdf after a cancel: %d, %d pages, want 2", code, len(pdfPageRe.FindAll(raw, -1)))
	}

	// Roles and isolation.
	if code, _, _ := f.rawGet(f.agent, pdfPath); code != http.StatusForbidden {
		t.Fatalf("agent pdf: %d, want 403", code)
	}
	foreign := newINV01Org(t, ctx, f.pool, f.secret)
	if code, _, _ := f.rawGet(foreign.owner, "/v1/organizations/"+foreign.org.String()+"/invitations/"+orderID.String()+"/tickets.pdf"); code != http.StatusNotFound {
		t.Fatalf("foreign organization pdf: %d, want 404", code)
	}
	if code, _, _ := f.rawGet(f.o.owner, "/v1/organizations/"+f.o.org.String()+"/invitations/"+uuid.NewString()+"/tickets.pdf"); code != http.StatusNotFound {
		t.Fatalf("unknown order pdf: %d, want 404", code)
	}
	// Every ticket cancelled: nothing to download.
	if code, body := f.do(f.o.owner, http.MethodPost, cancelPath, `{"notify_guest":false}`); code != http.StatusOK {
		t.Fatalf("cancel rest: %d %s", code, body["_raw"])
	}
	if code, _, raw = f.rawGet(f.manager, pdfPath); code != http.StatusConflict || !strings.Contains(string(raw), "invitation.no_active_tickets") {
		t.Fatalf("pdf of a cancelled invitation: %d %s", code, raw)
	}
}

func TestINV03_Resend_QueuesOneLetterToTheRightAddress(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	guest := "guest-" + uuid.NewString()[:8] + "@example.test"
	order1, cs1 := inv03Issue(t, f, []string{"A|1|4", "A|1|5"}, "guest_email", guest)
	resend := func(token string, order uuid.UUID, body string) (int, map[string]any) {
		return f.do(token, http.MethodPost, "/v1/organizations/"+f.o.org.String()+"/invitations/"+order.String()+"/resend", body)
	}

	// Mark the first delivery finished, as the worker would: a resend must
	// bring the jobs back to pending.
	if _, err := f.pool.Exec(ctx, `UPDATE delivery_jobs SET status = 'sent' WHERE ticket_id IN (SELECT id FROM tickets WHERE checkout_session_id = $1)`, cs1); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	code, body := resend(f.manager, order1, ``)
	if code != http.StatusAccepted || body["queued_tickets"].(float64) != 2 || body["different_address"] != false {
		t.Fatalf("resend to the guest: %d %s", code, body["_raw"])
	}
	var pending int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM delivery_jobs WHERE status = 'pending' AND recipient_email = $2
		  AND ticket_id IN (SELECT id FROM tickets WHERE checkout_session_id = $1)`, cs1, guest).Scan(&pending); err != nil || pending != 2 {
		t.Fatalf("pending jobs to the guest = %d (%v), want 2", pending, err)
	}
	var tplJobs int
	if err := f.pool.QueryRow(ctx, `
		SELECT count(*) FROM worker_jobs WHERE job_type = 'ticket.deliver' AND payload->>'template' = 'invitation'
		  AND payload->>'ticket_id' IN (SELECT id::text FROM tickets WHERE checkout_session_id = $1)`, cs1).Scan(&tplJobs); err != nil || tplJobs != 4 {
		t.Fatalf("invitation worker jobs = %d (%v), want 2 from the issue and 2 from the resend", tplJobs, err)
	}

	// A one-time address replaces the guest for this send only.
	other := "other-" + uuid.NewString()[:8] + "@example.test"
	code, body = resend(f.o.owner, order1, `{"email":"`+other+`"}`)
	if code != http.StatusAccepted || body["different_address"] != true || body["expires_at"] == nil {
		t.Fatalf("resend to another address: %d %s", code, body["_raw"])
	}
	if code, body = resend(f.o.owner, order1, `{"email":"not-an-address"}`); code != http.StatusBadRequest {
		t.Fatalf("bad address: %d %s", code, body["_raw"])
	}

	// A pdf_only invitation has no guest address: the caller's own is used.
	order2, _ := inv03Issue(t, f, []string{"A|1|6"}, "pdf_only", "")
	var ownerEmail string
	if err := f.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, f.ownerID).Scan(&ownerEmail); err != nil {
		t.Fatalf("owner email: %v", err)
	}
	if code, body = resend(f.o.owner, order2, ``); code != http.StatusAccepted || body["different_address"] != true {
		t.Fatalf("resend of a pdf_only invitation: %d %s", code, body["_raw"])
	}
	var to string
	if err := f.pool.QueryRow(ctx, `
		SELECT dj.recipient_email FROM delivery_jobs dj JOIN tickets t ON t.id = dj.ticket_id
		JOIN orders o ON o.checkout_session_id = t.checkout_session_id WHERE o.id = $1`, order2).Scan(&to); err != nil || to != ownerEmail {
		t.Fatalf("recipient of the pdf_only resend = %q (%v), want the caller %q", to, err, ownerEmail)
	}

	// Roles, isolation, unknown order.
	if code, _ = resend(f.agent, order1, ``); code != http.StatusForbidden {
		t.Fatalf("agent resend: %d, want 403", code)
	}
	foreign := newINV01Org(t, ctx, f.pool, f.secret)
	if code, _ = f.do(foreign.owner, http.MethodPost, "/v1/organizations/"+foreign.org.String()+"/invitations/"+order1.String()+"/resend", ``); code != http.StatusNotFound {
		t.Fatalf("foreign organization resend: %d, want 404", code)
	}
	if code, _ = resend(f.o.owner, uuid.New(), ``); code != http.StatusNotFound {
		t.Fatalf("unknown order resend: %d, want 404", code)
	}
}

// inv03Sender captures what the worker handler would send.
type inv03Sender struct {
	mu   sync.Mutex
	sent []email.Message
}

func (s *inv03Sender) Send(_ context.Context, m email.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, m)
	return nil
}

func (s *inv03Sender) all() []email.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]email.Message(nil), s.sent...)
}

// inv03CancelJobs lists the invitation.cancel_email jobs queued for an order.
func inv03CancelJobs(t *testing.T, f *inv02Fx, orderID uuid.UUID) [][]byte {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT payload::text FROM worker_jobs WHERE job_type = $1 AND payload->>'order_id' = $2`,
		delivery.JobTypeInvitationCancelEmail, orderID.String())
	if err != nil {
		t.Fatalf("cancel jobs: %v", err)
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, []byte(s))
	}
	return out
}

func TestINV03_CancelLetter_OnlyWhenTheGuestHasAnAddress(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = f.pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'order_id' IN
			(SELECT id::text FROM orders WHERE session_id = $2)`, delivery.JobTypeInvitationCancelEmail, f.session)
	})
	guest := "guest-" + uuid.NewString()[:8] + "@example.test"
	cancel := func(order uuid.UUID, body string) {
		t.Helper()
		code, resp := f.do(f.manager, http.MethodPost, "/v1/organizations/"+f.o.org.String()+"/invitations/"+order.String()+"/cancel", body)
		if code != http.StatusOK {
			t.Fatalf("cancel: %d %s", code, resp["_raw"])
		}
	}

	// A guest with an address, notify_guest left at its default: one letter job.
	withAddr, _ := inv03Issue(t, f, []string{"A|1|4", "A|1|5"}, "guest_email", guest)
	cancel(withAddr, `{}`)
	jobs := inv03CancelJobs(t, f, withAddr)
	if len(jobs) != 1 {
		t.Fatalf("cancel letter jobs with an address = %d, want 1", len(jobs))
	}

	// The worker handler writes the cancel template in invitation wording, to
	// the guest, naming the cancelled tickets.
	sender := &inv03Sender{}
	h := delivery.NewInvitationCancelEmailHandler(delivery.InvitationCancelEmailOptions{
		HandlerOptions: delivery.HandlerOptions{
			TicketQueries: gen.New(f.pool), CredentialQueries: gen.New(f.pool), Sender: sender,
		},
		DB: f.pool,
	})
	if err := h(ctx, jobs[0]); err != nil {
		t.Fatalf("cancel letter handler: %v", err)
	}
	msgs := sender.all()
	if len(msgs) != 1 || msgs[0].To != guest {
		t.Fatalf("sent %d letters, to %v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].Subject, "Invitation cancelled") && !strings.Contains(msgs[0].Subject, "Invitación cancelada") {
		t.Fatalf("subject = %q, want the invitation wording", msgs[0].Subject)
	}
	if strings.Contains(msgs[0].TextBody, "cancelled the session") || len(msgs[0].Attachments) != 0 {
		t.Fatalf("the letter speaks of a cancelled session or carries a file: %q", msgs[0].TextBody)
	}

	// notify_guest false: nothing queued.
	noNotify, _ := inv03Issue(t, f, []string{"A|1|6"}, "guest_email", guest)
	cancel(noNotify, `{"notify_guest": false}`)
	if n := len(inv03CancelJobs(t, f, noNotify)); n != 0 {
		t.Fatalf("notify_guest=false queued %d letters", n)
	}

	// No guest address (my_email, pdf_only): nobody to write to.
	mine, _ := inv03Issue(t, f, []string{"A|1|7"}, "my_email", "")
	cancel(mine, `{}`)
	pdfOnly, _ := inv03Issue(t, f, []string{"A|1|8"}, "pdf_only", "")
	cancel(pdfOnly, `{}`)
	if n := len(inv03CancelJobs(t, f, mine)) + len(inv03CancelJobs(t, f, pdfOnly)); n != 0 {
		t.Fatalf("an order without a guest address queued %d letters", n)
	}

	// Defence in depth: the worker itself refuses to write to the issuer.
	sender2 := &inv03Sender{}
	h2 := delivery.NewInvitationCancelEmailHandler(delivery.InvitationCancelEmailOptions{
		HandlerOptions: delivery.HandlerOptions{TicketQueries: gen.New(f.pool), CredentialQueries: gen.New(f.pool), Sender: sender2},
		DB:             f.pool,
	})
	var someTicket uuid.UUID
	if err := f.pool.QueryRow(ctx, `SELECT t.id FROM tickets t JOIN orders o ON o.checkout_session_id = t.checkout_session_id WHERE o.id = $1`, pdfOnly).Scan(&someTicket); err != nil {
		t.Fatalf("ticket: %v", err)
	}
	payload := []byte(`{"order_id":"` + pdfOnly.String() + `","ticket_ids":["` + someTicket.String() + `"]}`)
	if err := h2(ctx, payload); err != nil || len(sender2.all()) != 0 {
		t.Fatalf("handler for an order without an address: err %v, %d letters", err, len(sender2.all()))
	}
}

func TestINV03_SessionChange_LetterOfAGuestlessInvitationGoesToTheIssuer(t *testing.T) {
	f := newINV02Fx(t, 30*24*time.Hour)
	ctx := context.Background()
	guest := "guest-" + uuid.NewString()[:8] + "@example.test"
	var ownerEmail string
	if err := f.pool.QueryRow(ctx, `SELECT email FROM users WHERE id = $1`, f.ownerID).Scan(&ownerEmail); err != nil {
		t.Fatalf("owner email: %v", err)
	}
	withAddr, _ := inv03Issue(t, f, []string{"A|1|4"}, "guest_email", guest)
	mine, _ := inv03Issue(t, f, []string{"A|1|5"}, "my_email", "")
	pdfOnly, _ := inv03Issue(t, f, []string{"A|1|6"}, "pdf_only", "")

	// The recipient the letter worker resolves.
	for order, want := range map[uuid.UUID]string{withAddr: guest, mine: ownerEmail, pdfOnly: ownerEmail} {
		r, err := sessionchange.LoadOrderRecipient(ctx, f.pool, order)
		if err != nil || r.Email != want {
			t.Fatalf("recipient of %s = %q (%v), want %q", order, r.Email, err, want)
		}
	}

	// The dry run counts them apart.
	newStart := time.Now().UTC().Add(31 * 24 * time.Hour).Format(time.RFC3339)
	path := "/v1/organizations/" + f.o.org.String() + "/sessions/" + f.session.String() + "/change-impact?start_at=" + url.QueryEscape(newStart)
	code, body := f.do(f.o.owner, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("change-impact: %d %s", code, body["_raw"])
	}
	if body["orders"].(float64) != 3 || body["arena_orders"].(float64) != 3 ||
		body["invitations_to_issuer"].(float64) != 2 || body["no_address"].(float64) != 0 {
		t.Fatalf("change-impact: %s", body["_raw"])
	}
}
