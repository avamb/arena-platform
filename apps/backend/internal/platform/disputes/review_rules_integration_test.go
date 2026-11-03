//go:build integration

package disputes_test

// Prerequisites: DATABASE_URL against a migrated scratch database.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/disputes"
)

// TestDisputes_DeadlinesReopenAndStaleSends covers the rules the review of
// the dispute branch added: a deadline arena only estimated never expires a
// dispute, a past deadline is refused, a later deadline reopens an expired
// letter dispute, a send names the package it saw, and a queued letter goes
// out only while the dispute still waits for it, with the package's text.
func TestDisputes_DeadlinesReopenAndStaleSends(t *testing.T) {
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

	d, err := svc.CreateManual(ctx, f.org, disputes.ManualInput{OrderID: f.order, Reason: "fraudulent", ProviderDisputeID: "CB-REV"}, actor)
	if err != nil {
		t.Fatal(err)
	}
	if d.DueBySource != "default" {
		t.Fatalf("a dispute without a deadline has source %q", d.DueBySource)
	}
	state := func() (string, *string) {
		t.Helper()
		var st string
		var outcome *string
		if err := pool.QueryRow(ctx, `SELECT state, outcome FROM payment_disputes WHERE id = $1`, d.ID).Scan(&st, &outcome); err != nil {
			t.Fatal(err)
		}
		return st, outcome
	}

	// An estimated deadline that passed does not close the dispute.
	if _, err := pool.Exec(ctx, `UPDATE payment_disputes SET due_by = now() - interval '1 hour' WHERE id = $1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunWatch(ctx, disputes.Notifier{}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if st, _ := state(); st != disputes.StateNeedsResponse {
		t.Fatalf("an estimated deadline expired the dispute: %s", st)
	}

	// A deadline the organizer gave does.
	if _, err := pool.Exec(ctx, `UPDATE payment_disputes SET due_by_source = 'organizer' WHERE id = $1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunWatch(ctx, disputes.Notifier{}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if st, _ := state(); st != disputes.StateExpired {
		t.Fatalf("a passed deadline did not expire the dispute: %s", st)
	}

	past := time.Now().Add(-time.Hour).UTC()
	if _, err := svc.SetDeadline(ctx, f.org, d.ID, disputes.DeadlineInput{DueBy: &past}, actor); !errors.Is(err, disputes.ErrDueInPast) {
		t.Fatalf("a past deadline = %v", err)
	}
	later := time.Now().Add(4 * 24 * time.Hour).UTC().Truncate(time.Second)
	if d, err = svc.SetDeadline(ctx, f.org, d.ID, disputes.DeadlineInput{DueBy: &later}, actor); err != nil {
		t.Fatal(err)
	}
	if st, outcome := state(); st != disputes.StateNeedsResponse || outcome != nil {
		t.Fatalf("a later deadline did not reopen it: %s %v", st, outcome)
	}
	var expiredNotices int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM payment_dispute_notices WHERE dispute_id = $1 AND notice_key = 'expired'`, d.ID).Scan(&expiredNotices); err != nil {
		t.Fatal(err)
	}
	if expiredNotices != 0 {
		t.Fatal("the expiry notice of a reopened dispute stayed claimed")
	}

	if _, err := svc.Build(ctx, f.org, d.ID, actor); err != nil {
		t.Fatal(err)
	}
	pkg, err := svc.LatestPackage(ctx, f.org, d.ID)
	if err != nil || pkg == nil || pkg.EmailDraft == nil {
		t.Fatalf("package = %v %v", pkg, err)
	}
	to := "chargebacks@provider.example"
	if _, err := svc.SetDeadline(ctx, f.org, d.ID, disputes.DeadlineInput{RespondTo: &to}, actor); err != nil {
		t.Fatal(err)
	}
	// The organizer looked at an older version than the newest.
	if _, err := svc.Submit(ctx, f.org, d.ID, disputes.SubmitInput{PackageVersion: pkg.Version + 1}, actor); !errors.Is(err, disputes.ErrPackageChanged) {
		t.Fatalf("a send of another version = %v", err)
	}
	if _, err := svc.Submit(ctx, f.org, d.ID, disputes.SubmitInput{PackageVersion: pkg.Version}, actor); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM worker_jobs WHERE job_type = 'dispute.send_email' AND payload->>'dispute_id' = $1`, d.ID.String()).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var job map[string]any
	_ = json.Unmarshal(payload, &job)
	if job["to"] != to {
		t.Fatalf("the letter goes to %v; want the address entered after the build", job["to"])
	}
	if body, _ := job["body"].(string); body != "" {
		t.Fatal("the queue keeps a copy of a letter nobody edited")
	}

	mail := &mailCapture{}
	handler := svc.EmailJobHandler(mail)
	// The dispute stopped waiting for this letter (put back meanwhile).
	if _, err := pool.Exec(ctx, `UPDATE payment_disputes SET state = 'draft_ready' WHERE id = $1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := handler(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 0 {
		t.Fatal("a letter left for a dispute that no longer waits for it")
	}
	if _, err := pool.Exec(ctx, `UPDATE payment_disputes SET state = 'submitted' WHERE id = $1`, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := handler(ctx, payload); err != nil {
		t.Fatal(err)
	}
	if len(mail.sent) != 2 || mail.sent[0].TextBody != pkg.EmailDraft.Body || mail.sent[0].To != to {
		t.Fatalf("sent %d letters, first to %q", len(mail.sent), func() string {
			if len(mail.sent) > 0 {
				return mail.sent[0].To
			}
			return ""
		}())
	}

	// Accepting after the send is refused, and the send stays.
	if _, err := svc.Accept(ctx, f.org, d.ID, actor); !errors.Is(err, disputes.ErrAlreadyAnswered) {
		t.Fatalf("accept after the send = %v", err)
	}
}
