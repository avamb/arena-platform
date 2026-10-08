//go:build integration

package onboarding

// Live-database test of the Telegram channel of the application: the six-digit
// e-mail code (guess limit, hourly limit, expiry), account scoping, the site
// link and the at-most-once announcement of decisions.
//
//	DATABASE_URL=... JWT_SIGNING_SECRET=x go.exe test -tags integration \
//	  ./apps/backend/internal/platform/onboarding/ -run TestBotChannel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func queuedCode(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	var code string
	err := pool.QueryRow(context.Background(), `SELECT payload->>'code' FROM worker_jobs
WHERE job_type = 'onboarding.email' AND payload->>'email' = $1 AND payload->>'kind' = 'code'
ORDER BY created_at DESC, id DESC LIMIT 1`, email).Scan(&code)
	if err != nil || len(code) != 6 {
		t.Fatalf("queued code %q: %v", code, err)
	}
	return code
}

func wrongCode(code string) string {
	if code == "000000" {
		return "111111"
	}
	return "000000"
}

func TestBotChannel_CodeAccountScopeAndNotices(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	svc := New(Options{Pool: pool, SiteURL: "https://site.example.test", AdminURL: "https://admin.example.test"})
	email := fmt.Sprintf("onb-bot-%d@example.com", time.Now().UnixNano())
	cleanupApplication(t, pool, email)
	tg := time.Now().UnixNano()%1_000_000_000 + 5_000_000_000
	other := tg + 1

	app, created, err := svc.BotStart(ctx, BotStartInput{
		TelegramUserID: tg, TelegramUsername: "@ana_perez", FirstName: "Ana", LastName: "Pérez",
		Phone: "+34 600 111 222", Email: strings.ToUpper(email), Locale: "es-ES",
	})
	if err != nil || !created {
		t.Fatalf("BotStart: created=%v err=%v", created, err)
	}
	if app.Status != StatusDraft || app.Email != email || app.EmailConfirmedAt != nil {
		t.Fatalf("started = %+v", app)
	}
	if got := app.Answers.String("telegram_username"); got != "ana_perez" {
		t.Fatalf("telegram_username answer = %q", got)
	}

	// Asking twice returns the same application.
	again, created, err := svc.BotStart(ctx, BotStartInput{TelegramUserID: tg, FirstName: "X", LastName: "Y", Phone: "+34600000000", Email: "x@example.com"})
	if err != nil || created || again.ID != app.ID {
		t.Fatalf("second BotStart: created=%v id=%v err=%v", created, again, err)
	}

	// Another Telegram account never reaches it.
	if _, err := svc.BotGet(ctx, app.ID, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BotGet as another account: %v", err)
	}
	if _, err := svc.BotConfirmEmail(ctx, app.ID, other, "123456"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BotConfirmEmail as another account: %v", err)
	}
	if _, err := svc.BotOpen(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("BotOpen as another account: %v", err)
	}

	// A site link needs a confirmed e-mail.
	if _, err := svc.BotSiteLink(ctx, app.ID, tg); !errors.Is(err, ErrWrongState) {
		t.Fatalf("site link before confirmation: %v", err)
	}

	// Five wrong guesses burn the code, even the right one then fails.
	code := queuedCode(t, pool, email)
	for i := 0; i < emailCodeMaxGuesses; i++ {
		if _, err := svc.BotConfirmEmail(ctx, app.ID, tg, wrongCode(code)); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("wrong guess %d: %v", i, err)
		}
	}
	if _, err := svc.BotConfirmEmail(ctx, app.ID, tg, code); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("the right code after five wrong guesses: %v", err)
	}

	// A new code works, the old one no longer does; at most three an hour.
	if err := svc.BotSendCode(ctx, app.ID, tg); err != nil {
		t.Fatalf("BotSendCode: %v", err)
	}
	fresh := queuedCode(t, pool, email)
	if _, err := svc.BotConfirmEmail(ctx, app.ID, tg, wrongCode(fresh)); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("wrong code after resend: %v", err)
	}
	if err := svc.BotSendCode(ctx, app.ID, tg); err != nil {
		t.Fatalf("BotSendCode 3rd: %v", err)
	}
	if err := svc.BotSendCode(ctx, app.ID, tg); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("fourth code in an hour: %v", err)
	}
	latest := queuedCode(t, pool, email)
	confirmed, err := svc.BotConfirmEmail(ctx, app.ID, tg, latest)
	if err != nil || confirmed.EmailConfirmedAt == nil {
		t.Fatalf("confirm with the latest code: %v %+v", err, confirmed)
	}
	// Confirmed twice is harmless; a code can no longer be requested.
	if _, err := svc.BotConfirmEmail(ctx, app.ID, tg, latest); err != nil {
		t.Fatalf("second confirm: %v", err)
	}
	if err := svc.BotSendCode(ctx, app.ID, tg); !errors.Is(err, ErrWrongState) {
		t.Fatalf("code after confirmation: %v", err)
	}

	// The site link now exists and carries a working resume token.
	link, err := svc.BotSiteLink(ctx, app.ID, tg)
	if err != nil || !strings.HasPrefix(link, "https://site.example.test/start/confirm?token=") {
		t.Fatalf("site link %q: %v", link, err)
	}
	terms, privacy := svc.SiteLinks()
	if terms != "https://site.example.test/terms" || privacy != "https://site.example.test/privacy" {
		t.Fatalf("site links: %q %q", terms, privacy)
	}

	// Answers, submit.
	if _, err := svc.BotSubmit(ctx, app.ID, tg); err == nil {
		t.Fatal("an incomplete application was submitted")
	}
	if _, err := svc.BotSaveAnswers(ctx, app.ID, tg, fullAnswers("ES")); err != nil {
		t.Fatalf("BotSaveAnswers: %v", err)
	}
	submitted, err := svc.BotSubmit(ctx, app.ID, tg)
	if err != nil || submitted.Status != StatusPendingApproval {
		t.Fatalf("BotSubmit: %v %+v", err, submitted)
	}

	// No decision, no notice for this application.
	mine := func(ns []BotNotice) *BotNotice {
		for i := range ns {
			if ns[i].ApplicationID == app.ID {
				return &ns[i]
			}
		}
		return nil
	}
	ns, err := svc.BotClaimNotices(ctx, 100)
	if err != nil || mine(ns) != nil {
		t.Fatalf("notice before a decision: %v %+v", err, ns)
	}

	// A request for more details is announced once, with the question; the
	// answer and a later rejection are announced again, once each.
	if _, err := pool.Exec(ctx, `UPDATE onboarding_applications SET status = 'info_requested', info_request_message = 'Add the VAT number', updated_at = now() WHERE id = $1`, app.ID); err != nil {
		t.Fatal(err)
	}
	ns, _ = svc.BotClaimNotices(ctx, 100)
	n := mine(ns)
	if n == nil || n.Status != "info_requested" || n.Message != "Add the VAT number" || n.TelegramUserID != tg || n.Locale != "es" {
		t.Fatalf("info_requested notice: %+v", n)
	}
	if ns, _ = svc.BotClaimNotices(ctx, 100); mine(ns) != nil {
		t.Fatal("the same decision was announced twice")
	}
	if _, err := pool.Exec(ctx, `UPDATE onboarding_applications SET status = 'rejected', updated_at = now() WHERE id = $1`, app.ID); err != nil {
		t.Fatal(err)
	}
	ns, _ = svc.BotClaimNotices(ctx, 100)
	if n = mine(ns); n == nil || n.Status != "rejected" {
		t.Fatalf("rejected notice: %+v", n)
	}
	if ns, _ = svc.BotClaimNotices(ctx, 100); mine(ns) != nil {
		t.Fatal("the rejection was announced twice")
	}

	// A closed application frees the account: a new one can be started, and it
	// is a different application.
	next, created, err := svc.BotStart(ctx, BotStartInput{
		TelegramUserID: tg, FirstName: "Ana", LastName: "Pérez", Phone: "+34600111222", Email: email, Locale: "es",
	})
	if err != nil || !created || next.ID == app.ID || next.ID == uuid.Nil {
		t.Fatalf("restart after rejection: created=%v err=%v", created, err)
	}
}
