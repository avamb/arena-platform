//go:build integration

package main

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/domain/catalog/catalogimport"
)

// importBatch stores the description as ART with the flag 'art', read in the
// format of the run: HTML by default, plain text on request.
func TestImportBatch_DescriptionFormat(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var orgID string
	if err := pool.QueryRow(ctx, `SELECT id FROM organizations ORDER BY created_at LIMIT 1`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	sfx := strconv.FormatInt(time.Now().UnixNano(), 10)
	mk := func(id, desc, format string) catalogimport.Bil24SnapshotEvent {
		return catalogimport.Bil24SnapshotEvent{
			ExternalBil24ID: id + sfx, Title: "DESCFMT " + id, StartsAt: time.Now().Add(time.Hour),
			Description: desc, DescriptionFormat: format,
		}
	}
	events := []catalogimport.Bil24SnapshotEvent{
		mk("html", "One <b>bold</b>.\n\nTwo\nlines", ""),
		mk("txt", "<b>one</b> **two**", "text"),
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM events WHERE external_bil24_id = ANY($1)`, []string{events[0].ExternalBil24ID, events[1].ExternalBil24ID})
	}()
	if _, err := importBatch(ctx, pool, events, orgID); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		events[0].ExternalBil24ID: "<p>One <strong>bold</strong>.</p><p>Two<br>lines</p>",
		events[1].ExternalBil24ID: "<p>&lt;b&gt;one&lt;/b&gt; <strong>two</strong></p>",
	}
	for id, w := range want {
		var desc, format string
		if err := pool.QueryRow(ctx, `SELECT description, description_format FROM events WHERE external_bil24_id = $1`, id).Scan(&desc, &format); err != nil {
			t.Fatal(err)
		}
		if desc != w || format != "art" {
			t.Errorf("%s: stored %q / %q, want %q / art", id, desc, format, w)
		}
	}
}
