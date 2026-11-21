package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// reissueDelete matches any statement that removes rows of the reissue
// journal (spec 63 section 4.9).
var reissueDelete = regexp.MustCompile(`(?is)\b(DELETE\s+FROM|TRUNCATE(\s+TABLE)?)\s+(ticket_reissues|ticket_reissue_items)\b`)

// TestTicketReissues_NoCodeDeletesEvidence: the journal of ticket reissues
// keeps where an old barcode went. Only a migration's Down section and test
// fixtures may delete it.
func TestTicketReissues_NoCodeDeletesEvidence(t *testing.T) {
	root := repoRoot(t)
	for _, probe := range []string{"DELETE FROM ticket_reissues WHERE org_id = $1", "TRUNCATE ticket_reissue_items"} {
		if !reissueDelete.MatchString(probe) {
			t.Fatalf("the pattern misses %q", probe)
		}
	}
	dirs := []string{
		filepath.Join(root, "apps", "backend", "cmd"),
		filepath.Join(root, "apps", "backend", "internal"),
		filepath.Join(root, "ops"),
	}
	var violations []string
	sawTable := false
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == ".gomodcache" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := filepath.Ext(path)
			if (ext != ".go" && ext != ".sql" && ext != ".sh") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, rerr := os.ReadFile(path) // #nosec G304 — repo-local test scan
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			text := string(src)
			if ext == ".sql" {
				if i := strings.Index(text, "-- +goose Down"); i >= 0 {
					text = text[:i]
				}
			}
			if strings.Contains(text, "CREATE TABLE ticket_reissues") {
				sawTable = true
			}
			if reissueDelete.MatchString(text) {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if !sawTable {
		t.Fatal("the scan never saw the migration that creates ticket_reissues: the scan roots are wrong")
	}
	if len(violations) > 0 {
		t.Errorf("the reissue journal is evidence and is never deleted outside a migration's Down section:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
