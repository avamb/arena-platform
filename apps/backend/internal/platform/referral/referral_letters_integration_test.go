//go:build integration

// referral_letters_integration_test.go — the letters and notices of "Refer a
// friend" against a live database: the referrer's cashback letter (queued with
// the state change, sent once), the "could not return" letter with the
// organizer contact, and the organizer's reversal notice (chat and letter).
package referral_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/adapters/email"
	"github.com/abhteam/arena_new/apps/backend/internal/adapters/postgres/gen"
	"github.com/abhteam/arena_new/apps/backend/internal/domain/payments"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/delivery"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/referral"
)

// captureSender records what would be mailed. It is not a DevOnlySender, so
// the handlers treat the send as real.
type captureSender struct {
	mu   sync.Mutex
	msgs []email.Message
}

func (c *captureSender) Send(_ context.Context, m email.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, m)
	return nil
}

func (c *captureSender) all() []email.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]email.Message(nil), c.msgs...)
}

// fakeAnnouncer is the organization's sales chat.
type fakeAnnouncer struct {
	mu                sync.Mutex
	texts             []string
	chats, delivered  int
	err               error
	calls             int
	lastOrg           string
	failFirstDelivery bool
}

func (a *fakeAnnouncer) AnnounceToOrg(_ context.Context, orgID, text string) (int, int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.lastOrg = orgID
	if a.err != nil {
		return 0, 0, a.err
	}
	if a.failFirstDelivery && a.calls == 1 {
		return 1, 0, nil
	}
	a.texts = append(a.texts, text)
	return a.chats, a.delivered, nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (f *fixture) cashbackHandler(sender email.Sender) func(context.Context, []byte) error {
	q := gen.New(f.pool)
	return delivery.NewReferralCashbackEmailHandler(delivery.ReferralEmailOptions{
		HandlerOptions: delivery.HandlerOptions{
			TicketQueries: q, DeliveryJobQueries: q, CredentialQueries: q, Sender: sender, Logger: quietLogger(),
		},
		DB: f.pool,
	})
}

func (f *fixture) jobs(t *testing.T, jobType string) []string {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT payload::text FROM worker_jobs WHERE job_type = $1 AND payload->>'reward_id' IN
		   (SELECT id::text FROM referral_rewards WHERE org_id = $2) ORDER BY id`, jobType, f.org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestLetters_PaidRewardQueuesTheCashbackLetter_SentOnce(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)

	// Still 'due': no letter yet.
	if n := len(f.jobs(t, referral.JobTypeCashbackEmail)); n != 0 {
		t.Fatalf("%d letters queued for a due reward", n)
	}
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatalf("Payout: %v", err)
	}
	jobs := f.jobs(t, referral.JobTypeCashbackEmail)
	if len(jobs) != 1 || !strings.Contains(jobs[0], `"kind": "sent"`) {
		t.Fatalf("queued letters = %v; want exactly one of kind sent", jobs)
	}
	// A repeated payout changes nothing and queues nothing.
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatalf("second Payout: %v", err)
	}
	if n := len(f.jobs(t, referral.JobTypeCashbackEmail)); n != 1 {
		t.Fatalf("%d letters queued after a repeated payout; want 1", n)
	}

	// The handler writes once, even when the job is delivered twice.
	cs := &captureSender{}
	h := f.cashbackHandler(cs)
	payload := []byte(jobs[0])
	for i := 0; i < 2; i++ {
		if err := h(ctx, payload); err != nil {
			t.Fatalf("handler run %d: %v", i, err)
		}
	}
	msgs := cs.all()
	if len(msgs) != 1 {
		t.Fatalf("%d letters sent; want exactly 1", len(msgs))
	}
	m := msgs[0]
	if m.To != f.referrerEmail {
		t.Errorf("letter to %q; want the referrer %q", m.To, f.referrerEmail)
	}
	for _, want := range []string{code.Code, "3.00 EUR"} {
		if !strings.Contains(m.TextBody, want) || !strings.Contains(m.HTMLBody, want) || (want == "3.00 EUR" && !strings.Contains(m.Subject, want)) {
			t.Errorf("the letter lacks %q:\nsubject: %s\n%s", want, m.Subject, m.TextBody)
		}
	}
	if !strings.Contains(m.TextBody, "Anna Petrova") {
		t.Errorf("the letter does not greet the referrer by name:\n%s", m.TextBody)
	}
	if strings.Contains(strings.ToLower(m.TextBody), "friend-"+f.suffix) {
		t.Errorf("the referrer's letter shows the friend's address")
	}
	if f.count(t, `SELECT count(*) FROM referral_rewards WHERE id = $1 AND letter_sent_at IS NOT NULL`, rw.ID) != 1 {
		t.Error("letter_sent_at is not set")
	}
	// The letter is recorded (dispute evidence) and linked to the refund.
	if f.count(t, `SELECT count(*) FROM email_letters WHERE org_id = $1 AND kind = 'refund' AND status <> 'sending'`, f.org) != 1 {
		t.Error("the cashback letter is not recorded as sent")
	}
	if f.count(t, `SELECT count(*) FROM refunds WHERE org_id = $1 AND buyer_notice_letter_id IS NOT NULL AND buyer_notified_at IS NOT NULL`, f.org) != 1 {
		t.Error("the cashback refund is not linked to its letter")
	}
}

func TestLetters_ProviderRefusalSendsTheCouldNotReturnLetterWithTheOrganizerContact(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	f.module.answers = []func(payments.RefundRequest) (payments.RefundResult, error){declinedAnswer}
	contact := "help-" + f.suffix + "@org.example"
	f.exec(t, `UPDATE organizations SET contact_email = $2 WHERE id = $1`, f.org, contact)
	ctx := context.Background()
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatalf("Payout: %v", err)
	}
	if got := f.rewardOf(t, friend); got.State != referral.StateNotPossible {
		t.Fatalf("reward = %+v; want not_possible", got)
	}
	jobs := f.jobs(t, referral.JobTypeCashbackEmail)
	if len(jobs) != 1 || !strings.Contains(jobs[0], `"kind": "unavailable"`) {
		t.Fatalf("queued letters = %v; want exactly one of kind unavailable", jobs)
	}

	cs := &captureSender{}
	if err := f.cashbackHandler(cs)(ctx, []byte(jobs[0])); err != nil {
		t.Fatalf("handler: %v", err)
	}
	msgs := cs.all()
	if len(msgs) != 1 {
		t.Fatalf("%d letters sent; want 1", len(msgs))
	}
	m := msgs[0]
	// The amount owed is the cashback the programme promised (reward row), the
	// organizer contact is the ticket letters' rule: the organization's public
	// address of a channel without a hosted page, also as Reply-To.
	if !strings.Contains(m.TextBody, "3.00 EUR") || !strings.Contains(m.TextBody, code.Code) {
		t.Errorf("the letter lacks the amount or the code:\n%s", m.TextBody)
	}
	if strings.Count(m.TextBody, contact) < 2 || m.ReplyTo != contact {
		t.Errorf("the organizer contact is not offered in the body and footer (reply-to %q):\n%s", m.ReplyTo, m.TextBody)
	}
	// Not recorded as a buyer letter: email_letters has no kind for it.
	if f.count(t, `SELECT count(*) FROM email_letters WHERE org_id = $1`, f.org) != 0 {
		t.Error("the unavailable letter was recorded under a refund kind")
	}
	// Once only.
	if err := f.cashbackHandler(cs)(ctx, []byte(jobs[0])); err != nil {
		t.Fatal(err)
	}
	if len(cs.all()) != 1 {
		t.Error("the unavailable letter was sent twice")
	}
}

func TestLetters_NoLetterWhenTheRewardMovedOnOrNothingWasOwed(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	// A letter queued for a reward that is still due says nothing true.
	cs := &captureSender{}
	if err := f.cashbackHandler(cs)(ctx, []byte(`{"reward_id":"`+rw.ID.String()+`","kind":"sent"}`)); err != nil {
		t.Fatal(err)
	}
	if len(cs.all()) != 0 {
		t.Fatal("a letter was sent for a reward that is not paid")
	}
	// A friend who got the money back before the payout: cancelled, no letter.
	f.exec(t, `UPDATE orders SET status = 'refunded' WHERE id = $1`, friend)
	if err := f.svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(f.jobs(t, referral.JobTypeCashbackEmail)); n != 0 {
		t.Fatalf("%d letters queued for a cancelled reward", n)
	}
}

func TestLetters_FriendReversalTellsTheOrganizerOnce(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	contact := "owner-" + f.suffix + "@org.example"
	f.exec(t, `UPDATE organizations SET contact_email = $2 WHERE id = $1`, f.org, contact)
	ann := &fakeAnnouncer{chats: 1, delivered: 1}
	svc := referral.NewService(referral.Options{Pool: f.pool, Engine: f.engine,
		Notifier: referral.NewOrganizerNotifier(f.pool, ann, quietLogger()), Logger: quietLogger()})
	code := f.issue(t)
	friendEmail := "friend-" + f.suffix + "@example.com"
	friend := f.friend(t, code, 3000, friendEmail)
	rw := f.rewardOf(t, friend)
	if err := svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE orders SET status = 'refunded' WHERE id = $1`, friend)

	rep, err := svc.Sweep(ctx)
	if err != nil || rep.Reversed != 1 || rep.Notified != 1 {
		t.Fatalf("sweep: %+v %v; want 1 recorded and 1 notified", rep, err)
	}
	if len(ann.texts) != 1 || ann.lastOrg != f.org.String() {
		t.Fatalf("chat notices = %d to %q; want 1 to the organization", len(ann.texts), ann.lastOrg)
	}
	text := ann.texts[0]
	var friendNumber int64
	if err := f.pool.QueryRow(ctx, `SELECT system_id FROM orders WHERE id = $1`, friend).Scan(&friendNumber); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{code.Code, "3.00"} {
		if !strings.Contains(text, want) {
			t.Errorf("the chat notice lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, friendEmail) || strings.Contains(text, f.referrerEmail) || strings.Contains(text, "@") {
		t.Errorf("the chat notice carries an e-mail address:\n%s", text)
	}
	if f.count(t, `SELECT count(*) FROM referral_rewards WHERE id = $1 AND reversal_notified_at IS NOT NULL`, rw.ID) != 1 {
		t.Error("reversal_notified_at is not set after the notice")
	}
	jobs := f.jobs(t, referral.JobTypeReversalEmail)
	if len(jobs) != 1 {
		t.Fatalf("%d reversal letters queued; want 1", len(jobs))
	}
	// A settled sweep tells nobody again.
	if rep, err = svc.Sweep(ctx); err != nil || rep.Notified != 0 || ann.calls != 1 || len(f.jobs(t, referral.JobTypeReversalEmail)) != 1 {
		t.Fatalf("settled sweep: %+v %v, %d chat calls", rep, err, ann.calls)
	}

	// The owners' letter goes to the organization (no members here, so the
	// contact address), carries the facts and no buyer data.
	cs := &captureSender{}
	h := delivery.NewReferralReversalEmailHandler(delivery.ReferralReversalEmailOptions{Sender: cs, DB: f.pool, Logger: quietLogger()})
	if err := h(ctx, []byte(jobs[0])); err != nil {
		t.Fatalf("reversal letter: %v", err)
	}
	msgs := cs.all()
	if len(msgs) != 1 || msgs[0].To != contact {
		t.Fatalf("letters = %+v; want one to %s", msgs, contact)
	}
	for _, want := range []string{code.Code, "3.00 EUR"} {
		if !strings.Contains(msgs[0].TextBody, want) {
			t.Errorf("the owners' letter lacks %q:\n%s", want, msgs[0].TextBody)
		}
	}
	if strings.Contains(msgs[0].TextBody, friendEmail) || strings.Contains(msgs[0].HTMLBody, friendEmail) {
		t.Error("the owners' letter shows the friend's address")
	}
}

func TestLetters_ReversalNoticeIsRetriedWhenNoChatTookIt_WithoutASecondLetter(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	ann := &fakeAnnouncer{chats: 1, delivered: 1, failFirstDelivery: true}
	svc := referral.NewService(referral.Options{Pool: f.pool, Engine: f.engine,
		Notifier: referral.NewOrganizerNotifier(f.pool, ann, quietLogger()), Logger: quietLogger()})
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	rw := f.rewardOf(t, friend)
	if err := svc.Payout(ctx, rw.ID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE orders SET status = 'partially_refunded' WHERE id = $1`, friend)

	// The chat exists but took nothing: not marked, the sweep reports it.
	rep, err := svc.Sweep(ctx)
	if err == nil || rep.Notified != 0 {
		t.Fatalf("first sweep: %+v %v; want an error and nothing notified", rep, err)
	}
	if f.count(t, `SELECT count(*) FROM referral_rewards WHERE id = $1 AND reversal_notified_at IS NOT NULL`, rw.ID) != 0 {
		t.Fatal("marked as notified although no chat took the notice")
	}
	// The next pass delivers it; the owners' letter was queued only once.
	if rep, err = svc.Sweep(ctx); err != nil || rep.Notified != 1 {
		t.Fatalf("second sweep: %+v %v; want 1 notified", rep, err)
	}
	if n := len(f.jobs(t, referral.JobTypeReversalEmail)); n != 1 {
		t.Fatalf("%d reversal letters queued across two passes; want 1", n)
	}
	if len(ann.texts) != 1 {
		t.Fatalf("%d chat notices; want 1", len(ann.texts))
	}
}

func TestLetters_ReversalWithoutAChatStillSendsTheLetter(t *testing.T) {
	f := newFixture(t, 10, 10, 100)
	ctx := context.Background()
	// No sales bot at all: a nil announcer.
	svc := referral.NewService(referral.Options{Pool: f.pool, Engine: f.engine,
		Notifier: referral.NewOrganizerNotifier(f.pool, nil, quietLogger()), Logger: quietLogger()})
	code := f.issue(t)
	friend := f.friend(t, code, 3000, "friend-"+f.suffix+"@example.com")
	if err := svc.Payout(ctx, f.rewardOf(t, friend).ID); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE orders SET status = 'refunded' WHERE id = $1`, friend)
	if rep, err := svc.Sweep(ctx); err != nil || rep.Notified != 1 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	if n := len(f.jobs(t, referral.JobTypeReversalEmail)); n != 1 {
		t.Fatalf("%d reversal letters; want 1", n)
	}
}
