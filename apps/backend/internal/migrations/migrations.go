// Package migrations embeds the goose-format SQL migration files that
// arena-migrate (and integration tests) apply to the PostgreSQL schema.
//
// The migrations are baked into every binary via go:embed so a production
// container never depends on the source tree being present at runtime —
// the arena-migrate binary is fully self-contained.
package migrations

import (
	"embed"
	"fmt"
	"strconv"
	"strings"
)

// FS exposes the embedded migration files. Goose is configured to read
// from the "sql" subdirectory via SetBaseFS + Up/UpContext("sql", ...).
//
//go:embed sql/*.sql
var FS embed.FS

// Dir is the path inside FS where the .sql files live.
const Dir = "sql"

// Head returns the filename of the highest-numbered embedded migration.
// It reads the embedded FS at package init time so callers can assert
// the migration head without opening a database connection.
func Head() (string, error) {
	entries, err := FS.ReadDir(Dir)
	if err != nil {
		return "", fmt.Errorf("migrations.Head: read embedded dir %q: %w", Dir, err)
	}

	var headFile string
	var headVersion int64 = -1

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		// Parse the leading numeric prefix (e.g. "0064" from "0064_delivery_jobs_processing.sql").
		parts := strings.SplitN(name, "_", 2)
		if len(parts) == 0 {
			continue
		}
		v, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		if v > headVersion {
			headVersion = v
			headFile = name
		}
	}

	if headFile == "" {
		return "", fmt.Errorf("migrations.Head: no .sql files found in embedded FS dir %q", Dir)
	}
	return headFile, nil
}
