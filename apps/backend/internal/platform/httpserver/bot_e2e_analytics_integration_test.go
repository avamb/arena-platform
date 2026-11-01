//go:build integration

package httpserver

// Helpers for the bot's product-analytics assertions (spec 50 §4.4, stage A4):
// the bot under test gets a real analytics.Writer on the scratch database, and
// the test reads analytics.events back after the writer is flushed.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/analytics"
)

// newBotAnalyticsWriter is a Writer that flushes every 50 ms.
func newBotAnalyticsWriter(pool *pgxpool.Pool) *analytics.Writer {
	return analytics.NewWriter(analytics.Options{DB: pool, FlushEvery: 50 * time.Millisecond})
}

// botAnalyticsRow is one stored event.
type botAnalyticsRow struct {
	Name    string
	IDKind  string
	IDValue string
	OrgID   *uuid.UUID
	Props   map[string]any
	Raw     string
}

// closeAndReadBotAnalytics flushes the writer and returns the bot events of
// the given identities in time order. Call it after the bot stopped.
func closeAndReadBotAnalytics(t *testing.T, pool *pgxpool.Pool, w *analytics.Writer, idValues ...string) []botAnalyticsRow {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := w.Close(ctx); err != nil {
		t.Fatalf("analytics writer close: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM analytics.events WHERE surface = 'bot' AND id_value = ANY($1)`, idValues)
	})
	rows, err := pool.Query(ctx, `SELECT name, id_kind, id_value, org_id, props::text FROM analytics.events
	        WHERE surface = 'bot' AND id_value = ANY($1) ORDER BY at, id`, idValues)
	if err != nil {
		t.Fatalf("read analytics.events: %v", err)
	}
	defer rows.Close()
	var out []botAnalyticsRow
	for rows.Next() {
		var r botAnalyticsRow
		if err := rows.Scan(&r.Name, &r.IDKind, &r.IDValue, &r.OrgID, &r.Raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(r.Raw), &r.Props); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func botAnalyticsNamed(rows []botAnalyticsRow, name string) []botAnalyticsRow {
	var out []botAnalyticsRow
	for _, r := range rows {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

// botAnalyticsHas reports whether some row of the named event carries all the
// given props.
func botAnalyticsHas(rows []botAnalyticsRow, name string, want map[string]any) bool {
	for _, r := range botAnalyticsNamed(rows, name) {
		ok := true
		for k, v := range want {
			if r.Props[k] != v {
				ok = false
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// assertNoneContain fails when any stored event mentions one of the strings:
// nothing a person typed may be in the table.
func assertNoneContain(t *testing.T, rows []botAnalyticsRow, secrets ...string) {
	t.Helper()
	for _, r := range rows {
		for _, s := range secrets {
			if s != "" && (strings.Contains(r.Raw, s) || strings.Contains(r.IDValue, s)) {
				t.Errorf("%s event carries %q: %s / %s", r.Name, s, r.IDValue, r.Raw)
			}
		}
	}
}
