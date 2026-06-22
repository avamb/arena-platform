// Package migrations embeds the goose-format SQL migration files that
// arena-migrate (and integration tests) apply to the PostgreSQL schema.
//
// The migrations are baked into every binary via go:embed so a production
// container never depends on the source tree being present at runtime —
// the arena-migrate binary is fully self-contained.
package migrations

import "embed"

// FS exposes the embedded migration files. Goose is configured to read
// from the "sql" subdirectory via SetBaseFS + Up/UpContext("sql", ...).
//
//go:embed sql/*.sql
var FS embed.FS

// Dir is the path inside FS where the .sql files live.
const Dir = "sql"
