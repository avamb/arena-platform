package migrations

import (
	"strings"
	"testing"
)

// The refund migrations that add foreign keys to busy tables (tickets,
// organizations, refunds) wait at most a few seconds for their locks, so a
// deploy that meets a long transaction fails fast instead of queueing every
// sale behind it.
func TestMigrations_ForeignKeysOnBusyTablesSetALockTimeout(t *testing.T) {
	for _, name := range []string{"sql/0143_refund_scan_blocks.sql", "sql/0145_refund_scan_exceptions.sql", "sql/0147_email_letters.sql"} {
		raw, err := FS.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		up := string(raw)
		i := strings.Index(up, "-- +goose Up")
		if i < 0 {
			t.Fatalf("%s: no Up section", name)
		}
		up = up[i:]
		lock := strings.Index(up, "SET LOCAL lock_timeout")
		create := strings.Index(up, "CREATE TABLE")
		if lock < 0 || create < 0 || lock > create {
			t.Errorf("%s: SET LOCAL lock_timeout must come before the first statement of the Up section", name)
		}
	}
}
