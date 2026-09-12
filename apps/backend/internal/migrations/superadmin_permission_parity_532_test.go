// superadmin_permission_parity_532_test.go — static guardrail for feature
// #532 (W1-S0b, spec 08_architecture/21_superadmin_org_access_parity_ru.md
// §3.2): every permission seeded by a migration numbered ABOVE 0100 (the
// parity migration) must grant that permission to platform_superadmin in
// the SAME migration file, or a real superadmin will silently 403 on the
// new surface the way it did for order.read/write, customer.read/import
// and api_key.manage/import.bil24_session before 0100 closed the gap.
//
// This runs against the embedded migrations FS (no live database), so it
// executes in the Unit CI job where DATABASE_URL points at an unmigrated
// schema.
package migrations_test

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/migrations"
)

// parityPinnedMigrationVersion is the version of the migration that grants
// platform_superadmin the full permission catalogue as it stood at that
// time (feature #532). Migrations numbered at or below this version are
// covered by that one-shot catch-up grant; only later migrations must carry
// their own grant.
const parityPinnedMigrationVersion = 100

// insertPermissionsPattern matches an INSERT that seeds new rows into the
// permissions table (case-insensitive, tolerant of the schema-qualified or
// bare table name used across the migration set).
var insertPermissionsPattern = regexp.MustCompile(`(?i)INSERT\s+INTO\s+permissions\b`)

// permissionNameLiteralPattern extracts single-quoted string literals from a
// VALUES list such as ('order.read', 'Read orders ...'), ('order.write', ...)
// — the first literal on each row is the permission name.
var permissionNameLiteralPattern = regexp.MustCompile(`\(\s*'([a-z0-9_.]+)'\s*,`)

func TestSuperadminPermissionParity532_LaterMigrationsGrantTheirOwnPermissions(t *testing.T) {
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
	sort.Strings(names) // zero-padded numeric prefixes sort lexically = numerically

	for _, name := range names {
		version, ok := migrationVersion(name)
		if !ok || version <= parityPinnedMigrationVersion {
			continue
		}

		raw, err := migrations.FS.ReadFile(migrations.Dir + "/" + name)
		if err != nil {
			t.Fatalf("read embedded migration %s: %v", name, err)
		}
		up := upSection(string(raw))

		if !insertPermissionsPattern.MatchString(up) {
			continue // this migration doesn't seed any new permissions
		}

		seeded := seededPermissionNames(up)
		if len(seeded) == 0 {
			// The migration inserts into permissions but this test's literal
			// extraction couldn't find any names — fail loudly rather than
			// silently passing a migration this guardrail can't parse.
			t.Errorf("migration %s matches INSERT INTO permissions but no permission name literals were extracted; "+
				"update permissionNameLiteralPattern in this test if the migration uses a new INSERT shape", name)
			continue
		}

		for _, permName := range seeded {
			if !grantsToSuperadminInSameFile(up, permName) {
				t.Errorf(
					"migration %s seeds permission %q but does not grant it to "+
						"platform_superadmin in the same file; a real superadmin will "+
						"403 permissions.denied on the surface this permission gates "+
						"(see AGENTS.md: grant new permissions to platform_superadmin "+
						"in the same migration that seeds them)",
					name, permName,
				)
			}
		}
	}
}

// migrationVersion parses the leading numeric prefix of a migration
// filename, mirroring migrations.Head()'s own parsing.
func migrationVersion(filename string) (int64, bool) {
	parts := strings.SplitN(filename, "_", 2)
	if len(parts) == 0 {
		return 0, false
	}
	v, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// upSection returns only the goose Up block: the schema this migration adds
// going forward. A Down block deliberately narrows things again and must
// not be inspected for "does it grant platform_superadmin" purposes.
func upSection(sql string) string {
	up := sql
	if idx := strings.Index(up, "-- +goose Down"); idx >= 0 {
		up = up[:idx]
	}
	if idx := strings.Index(up, "-- +goose Up"); idx >= 0 {
		up = up[idx+len("-- +goose Up"):]
	}
	return up
}

// seededPermissionNames extracts every permission-name literal from the
// first VALUES-style INSERT INTO permissions statement in up.
func seededPermissionNames(up string) []string {
	idx := insertPermissionsPattern.FindStringIndex(up)
	if idx == nil {
		return nil
	}
	// Scope the literal search to the statement itself (up to the next ';')
	// so unrelated later statements in the same file aren't misread as
	// permission names.
	stmt := up[idx[1]:]
	if semi := strings.Index(stmt, ";"); semi >= 0 {
		stmt = stmt[:semi]
	}
	matches := permissionNameLiteralPattern.FindAllStringSubmatch(stmt, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names
}

// crossJoinAllPermissionsPattern matches the 0071/0100 "grant everything"
// idiom: a role_permissions insert that CROSS JOINs the bare permissions
// table for platform_superadmin, which covers every permission — including
// ones seeded later in the very same file — without naming any of them.
var crossJoinAllPermissionsPattern = regexp.MustCompile(`(?is)INSERT\s+INTO\s+role_permissions[\s\S]*?platform_superadmin[\s\S]*?CROSS\s+JOIN\s+permissions\b`)

// grantsToSuperadminInSameFile reports whether up contains a DIFFERENT
// statement from the one that seeded permName into the permissions table —
// specifically a role_permissions grant — that names both
// 'platform_superadmin' and permName. Scanning statement-by-statement (not
// the whole file) avoids the false positive of matching the permission's own
// seeding literal in "INSERT INTO permissions (...) VALUES ('permName', ...)".
func grantsToSuperadminInSameFile(up, permName string) bool {
	if crossJoinAllPermissionsPattern.MatchString(up) {
		return true
	}
	for _, stmt := range strings.Split(up, ";") {
		if insertPermissionsPattern.MatchString(stmt) {
			continue // the seeding statement itself; not a grant
		}
		if !strings.Contains(stmt, "role_permissions") {
			continue
		}
		if strings.Contains(stmt, "platform_superadmin") && strings.Contains(stmt, "'"+permName+"'") {
			return true
		}
	}
	return false
}
