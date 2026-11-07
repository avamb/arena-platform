// source_migration_test.go guards the drift between the Go-side consent
// journal sources (Source* constants) and the marketing_consents_source_check
// CHECK constraint. Two wave-2 branches widened that CHECK independently
// (site forms added 'site', the marketing import added 'import'); the later
// migration re-created the list without 'site' and every site subscription
// then failed with 23514. The check reads the embedded migration FS, so it
// runs without a database.
package marketing

import (
	"sort"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

const sourceConstraintName = "marketing_consents_source_check"

func latestSourceCheckSQL(t *testing.T) (string, string) {
	t.Helper()
	entries, err := migrations.FS.ReadDir(migrations.Dir)
	if err != nil {
		t.Fatalf("read embedded migrations dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for i := len(names) - 1; i >= 0; i-- {
		raw, err := migrations.FS.ReadFile(migrations.Dir + "/" + names[i])
		if err != nil {
			t.Fatalf("read %s: %v", names[i], err)
		}
		up := string(raw)
		if idx := strings.Index(up, "-- +goose Down"); idx >= 0 {
			up = up[:idx]
		}
		if idx := strings.Index(up, "ADD CONSTRAINT "+sourceConstraintName); idx >= 0 {
			return names[i], up[idx:]
		}
	}
	t.Fatalf("no migration defines %s", sourceConstraintName)
	return "", ""
}

func TestConsentSources_MatchMigrationCheckConstraint(t *testing.T) {
	file, def := latestSourceCheckSQL(t)
	if end := strings.Index(def, ";"); end >= 0 {
		def = def[:end]
	}
	for _, src := range []string{SourceWidget, SourceSite, SourceConfirmLink, SourceWithdraw, SourceOrganizer, SourceErasure, SourceImport} {
		if !strings.Contains(def, "'"+src+"'") {
			t.Errorf("%s: %s does not allow source %q", file, sourceConstraintName, src)
		}
	}
}
