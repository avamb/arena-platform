//go:build integration

package contenttranslate

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestContentTranslations_Integration(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	nonce := uuid.NewString()[:8]
	orgID, orgEn, eventID, eventEn := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = pool.Exec(c, `DELETE FROM content_translations WHERE org_id = ANY($1)`, []uuid.UUID{orgID, orgEn})
		_, _ = pool.Exec(c, `DELETE FROM events WHERE org_id = ANY($1)`, []uuid.UUID{orgID, orgEn})
		_, _ = pool.Exec(c, `DELETE FROM organizations WHERE id = ANY($1)`, []uuid.UUID{orgID, orgEn})
	})
	exec(`INSERT INTO organizations (id, name, slug, default_locale) VALUES ($1, $2, $3, 'ru-RU')`,
		orgID, "CT Org "+nonce, "ct-org-"+nonce)
	exec(`INSERT INTO organizations (id, name, slug, default_locale) VALUES ($1, $2, $3, 'x')`,
		orgEn, "CT Org EN "+nonce, "ct-org-en-"+nonce)
	exec(`INSERT INTO events (id, org_id, name, description) VALUES ($1, $2, $3, 'Original text')`,
		eventID, orgID, "CT Event "+nonce)
	exec(`INSERT INTO events (id, org_id, name) VALUES ($1, $2, $3)`, eventEn, orgEn, "CT Event EN "+nonce)

	// content_locale is filled from the organization's default_locale.
	orig, err := LoadEventOriginal(ctx, pool, orgID, eventID)
	if err != nil {
		t.Fatalf("LoadEventOriginal: %v", err)
	}
	if orig.ContentLocale != "ru" {
		t.Fatalf("content_locale = %q; want ru", orig.ContentLocale)
	}
	origEn, err := LoadEventOriginal(ctx, pool, orgEn, eventEn)
	if err != nil {
		t.Fatalf("LoadEventOriginal en: %v", err)
	}
	if origEn.ContentLocale != "en" {
		t.Fatalf("content_locale with a bad default_locale = %q; want en", origEn.ContentLocale)
	}
	if _, err := LoadEventOriginal(ctx, pool, orgEn, eventID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("event of another org: err = %v; want ErrNoRows", err)
	}

	// Report text of the original is sanitized.
	if err := SetEventReportText(ctx, pool, orgID, eventID, sp(`<p>Report</p><script>x()</script>`)); err != nil {
		t.Fatalf("SetEventReportText: %v", err)
	}
	orig, _ = LoadEventOriginal(ctx, pool, orgID, eventID)
	if orig.ReportTextHTML == nil || strings.Contains(*orig.ReportTextHTML, "script") {
		t.Fatalf("report text = %v", orig.ReportTextHTML)
	}
	if err := SetEventReportText(ctx, pool, orgEn, eventID, sp("x")); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("report text into another org's event: err = %v", err)
	}

	hash := orig.Hash()
	manual, err := PutManual(ctx, pool, Translation{
		OwnerType: OwnerEvent, OwnerID: eventID, OrgID: orgID, Locale: "cs-CZ",
		Fields:     Fields{Name: sp("Ruční název"), DescriptionHTML: sp(`<p>Ruční</p><script>x()</script>`)},
		SourceHash: hash,
	})
	if err != nil {
		t.Fatalf("PutManual: %v", err)
	}
	if manual.Locale != "cs" || manual.Source != SourceManual || strings.Contains(*manual.Fields.DescriptionHTML, "script") {
		t.Fatalf("manual row = %+v", manual)
	}

	machine := Translation{
		OwnerType: OwnerEvent, OwnerID: eventID, OrgID: orgID, Locale: "cs",
		Fields:     Fields{DescriptionHTML: sp("<p>Strojový</p>")},
		SourceHash: "machine-hash",
	}
	written, err := PutMachine(ctx, pool, machine)
	if err != nil {
		t.Fatalf("PutMachine: %v", err)
	}
	if written {
		t.Fatal("PutMachine reported a write over a manual row")
	}
	got, err := Get(ctx, pool, OwnerEvent, eventID, "cs")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Source != SourceManual || *got.Fields.DescriptionHTML != *manual.Fields.DescriptionHTML || got.SourceHash != hash {
		t.Fatalf("manual row changed by machine: %+v", got)
	}

	// A new locale is written by machine.
	machineDe := machine
	machineDe.Locale = "de"
	if written, err := PutMachine(ctx, pool, machineDe); err != nil || !written {
		t.Fatalf("PutMachine de: written=%v err=%v", written, err)
	}
	// ...and a machine row is replaced by machine again.
	machineDe.Fields.DescriptionHTML = sp("<p>Neu</p>")
	if written, err := PutMachine(ctx, pool, machineDe); err != nil || !written {
		t.Fatalf("PutMachine de again: written=%v err=%v", written, err)
	}

	// A manual text is replaced by machine only after it is deleted (spec 63 §16.4).
	if ok, err := Delete(ctx, pool, orgID, OwnerEvent, eventID, "cs"); err != nil || !ok {
		t.Fatalf("Delete manual cs: ok=%v err=%v", ok, err)
	}
	written, err = PutMachine(ctx, pool, machine)
	if err != nil || !written {
		t.Fatalf("PutMachine after delete: written=%v err=%v", written, err)
	}
	got, _ = Get(ctx, pool, OwnerEvent, eventID, "cs")
	if got.Source != SourceMachine || got.Fields.Name != nil || !strings.Contains(*got.Fields.DescriptionHTML, "Strojový") {
		t.Fatalf("machine after delete did not write: %+v", got)
	}

	// Another organization cannot overwrite the row.
	foreign := machine
	foreign.OrgID = orgEn
	if written, err := PutMachine(ctx, pool, foreign); err != nil || written {
		t.Fatalf("foreign org PutMachine: written=%v err=%v", written, err)
	}

	list, err := List(ctx, pool, OwnerEvent, eventID)
	if err != nil || len(list) != 2 || list[0].Locale != "cs" || list[1].Locale != "de" {
		t.Fatalf("List = %+v, %v", list, err)
	}
	many, err := ListForOwners(ctx, pool, OwnerEvent, []uuid.UUID{eventID, eventEn})
	if err != nil || len(many[eventID]) != 2 || len(many[eventEn]) != 0 {
		t.Fatalf("ListForOwners = %+v, %v", many, err)
	}

	if ok, err := Delete(ctx, pool, orgID, OwnerEvent, eventID, "de"); err != nil || !ok {
		t.Fatalf("Delete de: %v %v", ok, err)
	}
	if ok, err := Delete(ctx, pool, orgID, OwnerEvent, eventID, "de"); err != nil || ok {
		t.Fatalf("Delete de twice: %v %v", ok, err)
	}
	if _, err := Get(ctx, pool, OwnerEvent, eventID, "de"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Get deleted: %v", err)
	}

	// Deleting the event row deletes its translations.
	exec(`DELETE FROM events WHERE id = $1`, eventID)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM content_translations WHERE owner_type = 'event' AND owner_id = $1`, eventID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d translations survived the event delete", n)
	}
}
