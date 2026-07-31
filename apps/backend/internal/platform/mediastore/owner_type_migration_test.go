// owner_type_migration_test.go guards the drift between the Go-side
// AllowedOwnerTypes allowlist and the PostgreSQL CHECK constraint that
// actually enforces it.
//
// The two lists are declared in different languages and different files, so
// nothing but a test keeps them in sync. When they drift, POST /v1/media
// accepts the upload, streams the bytes into storage, and only then fails the
// INSERT with a 23514 constraint violation — an expensive, confusing 500 that
// AB-25 hit on its first live run.
//
// The check reads the embedded migration FS rather than a live database, so it
// runs in the Unit CI job where DATABASE_URL points at an unmigrated schema.
package mediastore

import (
	"sort"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

// ownerTypeConstraintName is the CHECK constraint on media_objects.owner_type
// introduced by migration 0052 and widened by later migrations.
const ownerTypeConstraintName = "media_objects_owner_type_check"

// latestOwnerTypeCheckSQL returns the Up-section text of the highest-numbered
// migration that (re)defines the owner_type CHECK constraint. Because each
// widening migration drops and re-adds the constraint, the newest definition
// is the one PostgreSQL ends up enforcing.
func latestOwnerTypeCheckSQL(t *testing.T) (filename, upSQL string) {
	t.Helper()

	entries, err := migrations.FS.ReadDir(migrations.Dir)
	if err != nil {
		t.Fatalf("read embedded migrations dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	// Zero-padded numeric prefixes make lexical order equal numeric order.
	sort.Strings(names)

	for i := len(names) - 1; i >= 0; i-- {
		raw, readErr := migrations.FS.ReadFile(migrations.Dir + "/" + names[i])
		if readErr != nil {
			t.Fatalf("read embedded migration %s: %v", names[i], readErr)
		}
		// Only the Up section describes the schema this branch ships; the
		// Down section deliberately narrows the constraint again.
		up := string(raw)
		if idx := strings.Index(up, "-- +goose Down"); idx >= 0 {
			up = up[:idx]
		}
		if strings.Contains(up, "ADD CONSTRAINT "+ownerTypeConstraintName) {
			return names[i], up
		}
	}
	t.Fatalf("no migration defines %s", ownerTypeConstraintName)
	return "", ""
}

func TestAllowedOwnerTypes_MatchMigrationCheckConstraint(t *testing.T) {
	filename, upSQL := latestOwnerTypeCheckSQL(t)

	for ownerType := range AllowedOwnerTypes {
		if !strings.Contains(upSQL, "'"+ownerType+"'") {
			t.Errorf(
				"owner_type %q is accepted by mediastore.AllowedOwnerTypes but is not "+
					"listed in the %s CHECK constraint defined by %s; uploads would fail "+
					"with a 23514 constraint violation after the bytes are already stored",
				ownerType, ownerTypeConstraintName, filename,
			)
		}
	}
}

func TestAllowedOwnerTypes_SeatingPlanSVGIsMigrated(t *testing.T) {
	// AB-25 specifically: the seating drawer uploads the authoring SVG under
	// this owner type before creating a seating_plan_versions row.
	if _, ok := AllowedOwnerTypes["seating_plan_svg"]; !ok {
		t.Fatal("AllowedOwnerTypes is missing seating_plan_svg")
	}
	filename, upSQL := latestOwnerTypeCheckSQL(t)
	if !strings.Contains(upSQL, "'seating_plan_svg'") {
		t.Errorf(
			"migration %s does not widen %s to accept 'seating_plan_svg'",
			filename, ownerTypeConstraintName,
		)
	}
}
