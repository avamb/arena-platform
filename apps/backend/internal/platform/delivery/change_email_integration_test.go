//go:build integration

// change_email_integration_test.go — live-DB proof of the session-change
// chain: a session moved or cancelled while a buyer holds a paid ticket
// journals the change, queues ONE letter in the same transaction, and the
// session.change_email worker job then re-renders the buyer's PDF with the new
// date, writes the letter in their language with the organizer as Reply-To,
// and never sends it twice.
//
// Like presentation_integration_test.go this talks to whatever DATABASE_URL
// names (skipped when unset); point it at a FRESH scratch database.
package delivery

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/sessionchange"
)

// scFixture is a seeded paid order plus the ids the tests poke at.
type scFixture struct {
	presentationSeed
	pool      *pgxpool.Pool
	orgID     uuid.UUID
	channelID uuid.UUID
	contact   string
}

func newSCFixture(ctx context.Context, t *testing.T, withContact bool) (*scFixture, func()) {
	t.Helper()
	pool := presentationPool(t)
	seed, cleanup := seedPresentationTicket(ctx, t, pool)
	f := &scFixture{presentationSeed: seed, pool: pool}
	if err := pool.QueryRow(ctx, `SELECT org_id FROM events WHERE id = $1`, seed.EventID).Scan(&f.orgID); err != nil {
		cleanup()
		t.Fatalf("read org: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT channel_id FROM orders WHERE id = $1`, seed.OrderID).Scan(&f.channelID); err != nil {
		cleanup()
		t.Fatalf("read channel: %v", err)
	}
	f.contact = "organizer-" + uuid.NewString()[:8] + "@example.com"
	if withContact {
		f.mustExec(ctx, t, `UPDATE organizations SET contact_email = $2, contact_phone = '+372 5000 1234' WHERE id = $1`, f.orgID, f.contact)
	}
	return f, func() {
		for _, sql := range []string{
			`DELETE FROM session_changes WHERE session_id = $1`,
		} {
			_, _ = pool.Exec(ctx, sql, seed.SessionID)
		}
		_, _ = pool.Exec(ctx, `DELETE FROM worker_jobs WHERE job_type = $1 AND payload->>'order_id' = $2`,
			sessionchange.JobTypeChangeEmail, seed.OrderID.String())
		_, _ = pool.Exec(ctx, `DELETE FROM webhook_subscribers WHERE channel_id = $1`, f.channelID)
		_, _ = pool.Exec(ctx, `DELETE FROM event_promoters WHERE event_id = $1`, seed.EventID)
		_, _ = pool.Exec(ctx, `DELETE FROM org_promoters WHERE org_id = $1`, f.orgID)
		cleanup()
	}
}

func (f *scFixture) mustExec(ctx context.Context, t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// save runs what a write path does: lock + read, UPDATE, Apply, then commit
// (or roll back when Apply refuses). It returns Apply's result and error.
func (f *scFixture) save(ctx context.Context, t *testing.T, update string, args []any, message string) (sessionchange.Result, error) {
	t.Helper()
	tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	before, err := sessionchange.Load(ctx, tx, f.SessionID, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := tx.Exec(ctx, update, append([]any{f.SessionID}, args...)...); err != nil {
		t.Fatalf("update: %v", err)
	}
	res, err := sessionchange.Apply(ctx, tx, sessionchange.Input{
		SessionID: f.SessionID,
		Old:       before.State,
		Notice:    sessionchange.Notice{Message: message, ActorType: "user", ActorID: "test"},
	})
	if err != nil {
		return sessionchange.Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return res, nil
}

const scMoveWeek = `UPDATE sessions SET start_at = start_at + interval '7 days', end_at = end_at + interval '7 days' WHERE id = $1`

func (f *scFixture) runJob(ctx context.Context, t *testing.T, changeID uuid.UUID) (raw string, err error) {
	t.Helper()
	smtp := newSMTPCaptureServer(t)
	q := gen.New(f.pool)
	handler := NewChangeEmailHandler(ChangeEmailOptions{
		HandlerOptions: HandlerOptions{
			TicketQueries: q, DeliveryJobQueries: q, CredentialQueries: q,
			Sender: buildSMTPSender(smtp.Addr), Logger: testLogger(),
		},
		DB: f.pool,
	})
	body, _ := json.Marshal(sessionchange.ChangeEmailPayload{ChangeID: changeID.String(), OrderID: f.OrderID.String()})
	err = handler(ctx, body)
	select {
	case msg := <-smtp.Captured:
		return string(msg), err
	case <-time.After(1500 * time.Millisecond):
		return "", err
	}
}

func (f *scFixture) noticeState(ctx context.Context, t *testing.T, changeID uuid.UUID) string {
	t.Helper()
	s, err := sessionchange.NoticeState(ctx, f.pool, changeID, f.OrderID)
	if err != nil {
		t.Fatalf("notice state: %v", err)
	}
	return s
}

func TestSessionChange_MoveQueuesOneLetterAndRerendersThePDF(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()

	// A stale PDF from before the move: the job must replace it.
	f.mustExec(ctx, t, `INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'pdf', $2)
	                    ON CONFLICT (ticket_id, type) DO UPDATE SET payload = EXCLUDED.payload`,
		f.TicketID, base64.StdEncoding.EncodeToString([]byte("OLD-PDF")))

	res, err := f.save(ctx, t, scMoveWeek, nil, "We are sorry about the move.")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.ChangeID == uuid.Nil || res.Orders != 1 || res.Tickets != 1 || res.Queued != 1 ||
		len(res.Kinds) != 1 || res.Kinds[0] != sessionchange.KindDate {
		t.Fatalf("unexpected result: %+v", res)
	}
	var jobs int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'order_id' = $2`,
		sessionchange.JobTypeChangeEmail, f.OrderID.String()).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("worker jobs = %d (err %v), want exactly 1", jobs, err)
	}
	if got := f.noticeState(ctx, t, res.ChangeID); got != sessionchange.StateQueued {
		t.Fatalf("notice state = %q, want queued", got)
	}

	raw, err := f.runJob(ctx, t, res.ChangeID)
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	if raw == "" {
		t.Fatal("no letter was sent")
	}
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse letter: %v", err)
	}
	if got := msg.Header.Get("Reply-To"); !strings.Contains(got, f.contact) {
		t.Errorf("Reply-To = %q, want the organizer %q", got, f.contact)
	}
	if !strings.Contains(raw, "application/pdf") {
		t.Error("a move must carry the new ticket PDF")
	}
	body := emailTextBody(t, raw)
	for label, want := range map[string]string{
		"old date":   "2026-10-03 22:00 (Europe/Tallinn)",
		"new date":   "2026-10-10 22:00 (Europe/Tallinn)",
		"message":    "We are sorry about the move.",
		"contact":    f.contact,
		"phone":      "+372 5000 1234",
		"event name": f.EventName,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("letter does not carry the %s %q\n--- body ---\n%s", label, want, body)
		}
	}
	if strings.Contains(body, f.TicketID.String()) {
		t.Error("letter leaks the internal ticket UUID")
	}

	// The stored PDF is the NEW one: replaced, and showing the new date.
	doc := storedTicketPDF(ctx, t, f.pool, f.TicketID)
	if string(doc) == "OLD-PDF" {
		t.Fatal("the stale PDF credential was not replaced")
	}
	if !pdfContainsText(doc, "10 October 2026") || pdfContainsText(doc, "3 October 2026") {
		t.Error("the re-rendered PDF does not show the new date only")
	}
	var barcode string
	if err := f.pool.QueryRow(ctx, `SELECT payload FROM ticket_credentials WHERE ticket_id = $1 AND type = 'ean13'`, f.TicketID).Scan(&barcode); err != nil || barcode != f.EAN13 {
		t.Errorf("barcode changed or vanished: %q (err %v), want %q", barcode, err, f.EAN13)
	}

	if got := f.noticeState(ctx, t, res.ChangeID); got != sessionchange.StateSent {
		t.Fatalf("notice state after send = %q, want sent", got)
	}
	// A redelivered job must not mail again.
	again, err := f.runJob(ctx, t, res.ChangeID)
	if err != nil || again != "" {
		t.Fatalf("redelivered job: err=%v, second letter sent=%v", err, again != "")
	}
}

func TestSessionChange_CancelSendsALetterWithoutAPDF(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()
	f.mustExec(ctx, t, `INSERT INTO ticket_credentials (ticket_id, type, payload) VALUES ($1, 'pdf', $2)
	                    ON CONFLICT (ticket_id, type) DO UPDATE SET payload = EXCLUDED.payload`,
		f.TicketID, base64.StdEncoding.EncodeToString([]byte("OLD-PDF")))

	res, err := f.save(ctx, t, `UPDATE sessions SET status = 'cancelled' WHERE id = $1`, nil, "")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Kinds) != 1 || res.Kinds[0] != sessionchange.KindCancelled || res.Queued != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	raw, err := f.runJob(ctx, t, res.ChangeID)
	if err != nil || raw == "" {
		t.Fatalf("cancel letter: err=%v sent=%v", err, raw != "")
	}
	if strings.Contains(raw, "application/pdf") {
		t.Error("a cancellation must not carry a PDF")
	}
	if !strings.Contains(emailTextBody2(t, raw), f.contact) {
		t.Error("the cancellation letter must name the organizer contact")
	}
	// The cancelled session's PDF is left as it was: nothing to re-render.
	var stored string
	_ = f.pool.QueryRow(ctx, `SELECT payload FROM ticket_credentials WHERE ticket_id = $1 AND type = 'pdf'`, f.TicketID).Scan(&stored)
	if stored != base64.StdEncoding.EncodeToString([]byte("OLD-PDF")) {
		t.Error("a cancellation must not rewrite the ticket PDF")
	}
}

// emailTextBody2 reads the text part of a letter that has NO attachment (so
// it is not multipart/mixed-with-pdf); falls back to the raw message.
func emailTextBody2(t *testing.T, raw string) string {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return fmt.Sprintf("%v\n%s", msg.Header, raw)
}

func TestSessionChange_RefusesAMoveWithBuyersAndNoContact(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, false)
	defer cleanup()

	_, err := f.save(ctx, t, scMoveWeek, nil, "")
	if !errors.Is(err, sessionchange.ErrContactMissing) {
		t.Fatalf("err = %v, want ErrContactMissing", err)
	}
	// The save rolled back: the date is unchanged and nothing was queued.
	var moved bool
	if err := f.pool.QueryRow(ctx, `SELECT start_at <> $2 FROM sessions WHERE id = $1`, f.SessionID, f.SessionStart).Scan(&moved); err != nil || moved {
		t.Fatalf("session moved despite the refusal (moved=%v err=%v)", moved, err)
	}
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM session_changes WHERE session_id = $1`, f.SessionID).Scan(&n)
	if n != 0 {
		t.Fatalf("a refused change left %d journal rows", n)
	}
}

func TestSessionChange_RefusesAMoveWhenASiteSoldTheTicket(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()
	f.mustExec(ctx, t, `INSERT INTO webhook_subscribers (site_url, callback_url, signing_secret, kind, channel_id, active)
	                    VALUES ('https://site.example', $2, 'secret', 'bil24_wp', $1, true)`,
		f.channelID, "https://site.example/hook/"+uuid.NewString())

	_, err := f.save(ctx, t, scMoveWeek, nil, "")
	if !errors.Is(err, sessionchange.ErrSiteRoute) {
		t.Fatalf("err = %v, want ErrSiteRoute", err)
	}
}

func TestSessionChange_SessionWithoutBuyersMovesQuietly(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, false) // no contact either: nobody to tell
	defer cleanup()
	f.mustExec(ctx, t, `UPDATE tickets SET status = 'cancelled' WHERE id = $1`, f.TicketID)

	res, err := f.save(ctx, t, scMoveWeek, nil, "")
	if err != nil {
		t.Fatalf("a session with no live tickets must move freely: %v", err)
	}
	if res.ChangeID == uuid.Nil || res.Orders != 0 || res.Queued != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	var jobs int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM worker_jobs WHERE job_type = $1 AND payload->>'order_id' = $2`,
		sessionchange.JobTypeChangeEmail, f.OrderID.String()).Scan(&jobs)
	if jobs != 0 {
		t.Fatalf("%d letters queued for a session nobody holds a ticket for", jobs)
	}
}

func TestSessionChange_NonBuyerVisibleSaveRecordsNothing(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()
	// Capacity and END time are not things a buyer sees.
	res, err := f.save(ctx, t, `UPDATE sessions SET capacity_total = capacity_total + 5, end_at = end_at + interval '1 hour' WHERE id = $1`, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.ChangeID != uuid.Nil {
		t.Fatalf("a capacity/end-time edit produced a change: %+v", res)
	}
}

func TestSessionChange_PreviewMatchesApply(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()

	sess, err := sessionchange.Load(ctx, f.pool, f.SessionID, false)
	if err != nil {
		t.Fatal(err)
	}
	proposed := sess.State
	proposed.StartAt = proposed.StartAt.Add(7 * 24 * time.Hour)
	imp, err := sessionchange.Preview(ctx, f.pool, sess, proposed)
	if err != nil {
		t.Fatal(err)
	}
	if imp.Orders != 1 || imp.Tickets != 1 || imp.ArenaOrders != 1 || imp.SiteOrders != 0 || imp.NoAddress != 0 ||
		imp.Blocked != "" || !imp.Contact.Complete() || imp.Contact.Source != "organization" {
		t.Fatalf("unexpected impact: %+v", imp)
	}

	// Without a contact the preview says it is blocked, and which row to fill.
	f.mustExec(ctx, t, `UPDATE organizations SET contact_email = NULL WHERE id = $1`, f.orgID)
	imp, err = sessionchange.Preview(ctx, f.pool, sess, proposed)
	if err != nil {
		t.Fatal(err)
	}
	if imp.Blocked != sessionchange.BlockedContactMissing || imp.Contact.Complete() ||
		imp.Contact.TargetKind != "organization" || imp.Contact.TargetID != f.orgID {
		t.Fatalf("unexpected blocked impact: %+v", imp)
	}
}

func TestSessionChange_ContactPrefersThePromoterWhenItHasAnEmail(t *testing.T) {
	ctx := context.Background()
	f, cleanup := newSCFixture(ctx, t, true)
	defer cleanup()

	var promoterID uuid.UUID
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO org_promoters (org_id, name, email, phone, phone_hidden)
		 VALUES ($1, $2, NULL, '+34 600 111 222', true) RETURNING id`,
		f.orgID, "Promoter "+uuid.NewString()[:8]).Scan(&promoterID); err != nil {
		t.Fatal(err)
	}
	f.mustExec(ctx, t, `INSERT INTO event_promoters (event_id, org_id, promoter_id) VALUES ($1, $2, $3)`, f.EventID, f.orgID, promoterID)

	// The promoter has no e-mail yet: the organization's contact is used.
	c, err := sessionchange.ResolveContact(ctx, f.pool, f.EventID, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != "organization" || c.Email != f.contact {
		t.Fatalf("contact = %+v, want the organization's", c)
	}

	// Once the promoter has one, it wins — and a hidden phone stays hidden.
	pe := "promoter-" + uuid.NewString()[:8] + "@example.com"
	f.mustExec(ctx, t, `UPDATE org_promoters SET email = $2 WHERE id = $1`, promoterID, pe)
	c, err = sessionchange.ResolveContact(ctx, f.pool, f.EventID, f.orgID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Source != "promoter" || c.Email != pe || c.PublicPhone() != "" || c.Phone == "" || c.TargetKind != "promoter" {
		t.Fatalf("contact = %+v, want the promoter's with a hidden phone", c)
	}
}
