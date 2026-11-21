//go:build integration

package marketingimport

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRun_ImportsSuppressionsAndSubscribersIdempotently(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()
	orgID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		orgID, "MktImp "+orgID.String()[:8], "mktimp-"+orgID.String()[:8]); err != nil {
		t.Fatalf("organization: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM organizations WHERE id = $1`, orgID) })

	// An address the platform already knows as confirmed: Brevo says it unsubscribed.
	if _, err := pool.Exec(ctx, `
		INSERT INTO marketing_subscriptions (org_id, email, email_normalized, status, text_version, source,
		    granted_at, confirmed_at, unsubscribe_token)
		VALUES ($1, 'carol@example.com', 'carol@example.com', 'confirmed', 'v', 'widget', now(), now(), $2),
		       ($1, 'gina@example.com', 'gina@example.com', 'confirmed', 'v', 'widget', now(), now(), $3)`,
		orgID, strings.Repeat(orgID.String()[:8], 8), strings.Repeat(orgID.String()[24:32], 8)); err != nil {
		t.Fatalf("existing: %v", err)
	}

	brevo := "email,name,blacklisted,unsubscribed,consent_date,consent_source\n" +
		"anna@example.com,Anna,0,0,2025-03-04,popup\n" +
		"bob@example.com,Bob,1,0,2024-01-01,popup\n" +
		"carol@example.com,Carol,0,1,,\n" +
		"dave@example.com,Dave,0,0,,\n" +
		"gina@example.com,Gina,0,0,2025-05-05,popup\n"
	club := "date,name,email,phone\n" +
		"2025-11-19 10:15:00,Ira,ira@example.com,+972\n" +
		"2025-12-01,Anna again,ANNA@example.com,\n" +
		"2025-12-02,Bob again,bob@example.com,\n"
	r1, _, err := ParseBrevo(strings.NewReader(brevo))
	if err != nil {
		t.Fatal(err)
	}
	r2, _, err := ParseElementor(strings.NewReader(club))
	if err != nil {
		t.Fatal(err)
	}
	recs := append(r1, r2...)
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	dry, err := Run(ctx, pool, orgID, recs, true, now)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM marketing_subscriptions WHERE org_id = $1`, orgID).Scan(&n); err != nil || n != 2 {
		t.Fatalf("dry run wrote rows: n=%d err=%v", n, err)
	}

	first, err := Run(ctx, pool, orgID, recs, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if first != dry {
		t.Errorf("dry-run counts differ from the real run:\n dry   %+v\n first %+v", dry, first)
	}
	want := Counts{BrevoRows: 5, ElementorRows: 3, DuplicatesMerged: 2, NoConsent: 1,
		SuppressionsNew: 1, SuppressionsApplied: 1, SubscribersNew: 2, SubscribersPresent: 1, SubscribersSuppressed: 1}
	if first != want {
		t.Errorf("counts:\n got  %+v\n want %+v", first, want)
	}

	status := func(email string) string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT status FROM marketing_subscriptions WHERE org_id = $1 AND email_normalized = $2`, orgID, email).Scan(&s); err != nil {
			return "none"
		}
		return s
	}
	for email, st := range map[string]string{"anna@example.com": "confirmed", "bob@example.com": "withdrawn",
		"carol@example.com": "withdrawn", "dave@example.com": "none", "gina@example.com": "confirmed", "ira@example.com": "confirmed"} {
		if got := status(email); got != st {
			t.Errorf("%s = %s, want %s", email, got, st)
		}
	}
	// Anna's earliest consent (2025-03-04, Brevo) is the evidence.
	var src, ver string
	var granted time.Time
	if err := pool.QueryRow(ctx, `SELECT source, text_version, granted_at FROM marketing_subscriptions WHERE org_id=$1 AND email_normalized='anna@example.com'`, orgID).Scan(&src, &ver, &granted); err != nil {
		t.Fatal(err)
	}
	if src != "brevo_import" || ver != "import:brevo_import:popup" || !granted.Equal(time.Date(2025, 3, 4, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("anna evidence: %s %s %s", src, ver, granted)
	}
	var journalRows int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM marketing_consents WHERE org_id = $1 AND source = 'import'`, orgID).Scan(&journalRows)
	if journalRows != 6 {
		t.Errorf("journal rows = %d, want 6", journalRows)
	}

	second, err := Run(ctx, pool, orgID, recs, false, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if second.SuppressionsNew != 0 || second.SuppressionsApplied != 0 || second.SubscribersNew != 0 {
		t.Errorf("a second run changed something: %+v", second)
	}
	var after int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM marketing_consents WHERE org_id = $1`, orgID).Scan(&after)
	var before = journalRows
	if after != before {
		t.Errorf("journal grew on re-run: %d -> %d", before, after)
	}
}
