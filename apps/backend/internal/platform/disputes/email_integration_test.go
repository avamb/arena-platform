//go:build integration

package disputes_test

// Prerequisites: DATABASE_URL against a migrated scratch database.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
)

type mailCapture struct {
	mu   sync.Mutex
	sent []email.Message
}

func (m *mailCapture) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

// emailFixture seeds a paid order of an organization whose payment went
// through a provider without a dispute API (AllPay), and its owner.
type emailFixture struct {
	pool                         *pgxpool.Pool
	org, order, owner, cs, venue uuid.UUID
	event, session, channel, pay uuid.UUID
	ownerEmail                   string
}

func newEmailFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *emailFixture {
	t.Helper()
	f := &emailFixture{pool: pool, org: uuid.New(), order: uuid.New(), owner: uuid.New(), cs: uuid.New(),
		venue: uuid.New(), event: uuid.New(), session: uuid.New(), channel: uuid.New(), pay: uuid.New()}
	suffix := uuid.NewString()[:8]
	f.ownerEmail = "owner-" + suffix + "@example.test"
	res := uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", strings.Join(strings.Fields(sql)[:3], " "), err)
		}
	}
	t.Cleanup(func() { f.cleanup(context.Background()) })
	exec(`INSERT INTO organizations (id, name, slug, country, default_locale) VALUES ($1, $2, $3, 'IL', 'en')`, f.org, "Disp Email "+suffix, "disp-email-"+suffix)
	exec(`INSERT INTO users (id, email, password_hash, preferred_locale) VALUES ($1, $2, 'x', 'en')`, f.owner, f.ownerEmail)
	exec(`INSERT INTO memberships (user_id, org_id, role) VALUES ($1, $2, 'org_admin')`, f.owner, f.org)
	exec(`INSERT INTO venues (id, org_id, name, timezone) VALUES ($1, $2, 'Hall', 'Asia/Jerusalem')`, f.venue, f.org)
	exec(`INSERT INTO events (id, org_id, name, status, visibility) VALUES ($1, $2, 'Wine tasting', 'published', 'public')`, f.event, f.org)
	exec(`INSERT INTO sessions (id, event_id, venue_id, start_at, end_at, capacity_total, status, currency, currency_source)
	      VALUES ($1, $2, $3, now() - interval '3 days', now() - interval '3 days' + interval '2 hours', 50, 'scheduled', 'ILS', 'override')`, f.session, f.event, f.venue)
	exec(`INSERT INTO sales_channels (id, org_id, name, settings) VALUES ($1, $2, 'site', '{}'::jsonb)`, f.channel, f.org)
	exec(`INSERT INTO reservations (id, org_id, channel_id, session_id, quantity, state, expires_at, converted_at)
	      VALUES ($1, $2, $3, $4, 1, 'converted', now(), now())`, res, f.org, f.channel, f.session)
	exec(`INSERT INTO checkout_sessions (id, org_id, channel_id, reservation_id, state) VALUES ($1, $2, $3, $4, 'completed')`, f.cs, f.org, f.channel, res)
	exec(`INSERT INTO orders (id, org_id, channel_id, event_id, session_id, checkout_session_id, reservation_id,
	                          source, status, currency, subtotal, discount, charge, total, paid_at, buyer_name, buyer_email)
	      VALUES ($1, $2, $3, $4, $5, $6, $7, 'public_feed', 'paid', 'ILS', 12000, 0, 0, 12000, now() - interval '10 days', 'Dana Levi', 'dana@example.test')`,
		f.order, f.org, f.channel, f.event, f.session, f.cs, res)
	exec(`INSERT INTO payment_intents (id, checkout_session_id, org_id, provider, provider_payment_id, amount, currency, state)
	      VALUES ($1, $2, $3, 'allpay', $4, 12000, 'ILS', 'succeeded')`, f.pay, f.cs, f.org, f.cs.String())
	return f
}

func (f *emailFixture) cleanup(ctx context.Context) {
	for _, sql := range []string{
		`DELETE FROM worker_jobs WHERE job_type LIKE 'dispute.%' AND payload->>'dispute_id' IN (SELECT id::text FROM payment_disputes WHERE org_id = $1)`,
		`DELETE FROM payment_dispute_notices WHERE dispute_id IN (SELECT id FROM payment_disputes WHERE org_id = $1)`,
		`DELETE FROM payment_dispute_files WHERE package_id IN (SELECT id FROM payment_dispute_packages WHERE org_id = $1)`,
		`DELETE FROM payment_dispute_packages WHERE org_id = $1`,
		`DELETE FROM payment_dispute_events WHERE dispute_id IN (SELECT id FROM payment_disputes WHERE org_id = $1)`,
		`DELETE FROM payment_disputes WHERE org_id = $1`,
		`DELETE FROM payment_intents WHERE org_id = $1`,
		`DELETE FROM orders WHERE org_id = $1`,
		`DELETE FROM checkout_sessions WHERE org_id = $1`,
		`DELETE FROM reservations WHERE org_id = $1`,
		`DELETE FROM sales_channels WHERE org_id = $1`,
		`DELETE FROM sessions WHERE event_id IN (SELECT id FROM events WHERE org_id = $1)`,
		`DELETE FROM events WHERE org_id = $1`,
		`DELETE FROM venues WHERE org_id = $1`,
		`DELETE FROM memberships WHERE org_id = $1`,
	} {
		_, _ = f.pool.Exec(ctx, sql, f.org)
	}
	_, _ = f.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, f.owner)
	_, _ = f.pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, f.org)
}

// TestDisputes_EmailChannel: a dispute of a provider without a dispute API
// is entered from the provider's letter, gets the same package plus a
// letter draft, waits for the organizer's press, and then one letter goes to
// the provider with the PDFs (Reply-To the owner) and a copy to the owner.
func TestDisputes_EmailChannel(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to PostgreSQL (%v); skipping", err)
	}
	defer pool.Close()
	f := newEmailFixture(t, ctx, pool)
	svc := disputes.New(disputes.Options{DB: pool})
	actor := disputes.Actor{UserID: &f.owner, Label: "user"}

	// a foreign order is refused
	if _, err := svc.CreateManual(ctx, f.org, disputes.ManualInput{OrderID: uuid.New(), Reason: "fraudulent"}, actor); err == nil {
		t.Fatal("a foreign order was accepted")
	}
	d, err := svc.CreateManual(ctx, f.org, disputes.ManualInput{OrderID: f.order, Reason: "product_not_received", ProviderDisputeID: "CB-77"}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if d.Channel != disputes.ChannelManual || d.DueBySource != "default" || d.DueBy == nil || d.AmountMinor != 12000 || d.ReasonGroup != disputes.ReasonNotReceived {
		t.Fatalf("manual dispute = %+v", d)
	}
	if again, err := svc.CreateManual(ctx, f.org, disputes.ManualInput{OrderID: f.order, Reason: "fraudulent", ProviderDisputeID: "CB-77"}, actor); err != nil || again.ID != d.ID {
		t.Fatalf("the same reference made a second dispute: %v %v", again.ID, err)
	}

	// Submitting before the package exists sends nothing.
	if _, err := svc.Submit(ctx, f.org, d.ID, disputes.SubmitInput{}, actor); !errors.Is(err, disputes.ErrNoPackage) {
		t.Fatalf("submit without a package = %v", err)
	}
	res, err := svc.Build(ctx, f.org, d.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	pkg := res.Package
	if pkg == nil || pkg.EmailDraft == nil || len(pkg.Files) < 2 {
		t.Fatalf("package = %+v", pkg)
	}
	// AllPay has a published address, so the draft already has a recipient.
	if pkg.EmailDraft.To != "info@allpay.co.il" && pkg.EmailDraft.To != "" {
		t.Fatalf("draft recipient = %q", pkg.EmailDraft.To)
	}

	// The organizer's deadline replaces the 7-day estimate.
	due := time.Now().Add(5 * 24 * time.Hour).UTC().Truncate(time.Second)
	to := "chargebacks@provider.example"
	if d, err = svc.SetDeadline(ctx, f.org, d.ID, disputes.DeadlineInput{DueBy: &due, RespondTo: &to}, actor); err != nil {
		t.Fatal(err)
	}
	if d.DueBySource != "organizer" || !d.DueBy.Equal(due) || d.RespondTo != to {
		t.Fatalf("after the deadline: %+v", d)
	}

	// The press queues the letter; nothing has been mailed yet.
	if d, err = svc.Submit(ctx, f.org, d.ID, disputes.SubmitInput{Email: &disputes.EmailDraft{To: to, Subject: "Chargeback CB-77", Body: "Please find the evidence attached."}}, actor); err != nil {
		t.Fatal(err)
	}
	if d.State != disputes.StateSubmitted {
		t.Fatalf("state after submit = %s", d.State)
	}
	if _, err := svc.Submit(ctx, f.org, d.ID, disputes.SubmitInput{}, actor); !errors.Is(err, disputes.ErrAlreadyAnswered) {
		t.Fatalf("second submit = %v", err)
	}
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM worker_jobs WHERE job_type = 'dispute.send_email' AND payload->>'dispute_id' = $1`, d.ID.String()).Scan(&payload); err != nil {
		t.Fatalf("send job: %v", err)
	}
	mail := &mailCapture{}
	handler := svc.EmailJobHandler(mail)
	if err := handler(ctx, payload); err != nil {
		t.Fatal(err)
	}
	// a retry of the job sends nothing more
	if err := handler(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 2 {
		t.Fatalf("sent %d letters; want the answer and the owner's copy", len(mail.sent))
	}
	answer, cp := mail.sent[0], mail.sent[1]
	if answer.To != to || answer.ReplyTo != f.ownerEmail || len(answer.Attachments) == 0 || answer.Subject != "Chargeback CB-77" {
		t.Fatalf("answer = to %q reply %q att %d subject %q", answer.To, answer.ReplyTo, len(answer.Attachments), answer.Subject)
	}
	for _, a := range answer.Attachments {
		if a.ContentType != "application/pdf" || !strings.HasPrefix(string(a.Data), "%PDF-") {
			t.Fatalf("attachment %s is not a PDF", a.Filename)
		}
	}
	if cp.To != f.ownerEmail || !strings.HasPrefix(cp.Subject, "[Copy]") {
		t.Fatalf("copy = %q %q", cp.To, cp.Subject)
	}
	evs, err := svc.Events(ctx, f.org, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	var sentEvent *disputes.Event
	for i := range evs {
		if evs[i].Kind == "email_sent" {
			sentEvent = &evs[i]
		}
	}
	if sentEvent == nil {
		t.Fatal("no email_sent journal line")
	}
	var journal map[string]any
	_ = json.Unmarshal(sentEvent.Payload, &journal)
	if journal["to"] != to || journal["attachments"] == nil {
		t.Fatalf("journal = %v", journal)
	}
	if strings.Contains(string(sentEvent.Payload), "Please find the evidence") {
		t.Fatal("the journal keeps the letter body; it should keep only its hash")
	}

	// Another organization cannot read it.
	if _, err := svc.Get(ctx, uuid.New(), d.ID); !errors.Is(err, disputes.ErrNotFound) {
		t.Fatalf("foreign read = %v", err)
	}
}
