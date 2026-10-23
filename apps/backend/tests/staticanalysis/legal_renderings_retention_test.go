package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// renderingDelete matches a DELETE or TRUNCATE aimed at legal_renderings
// (migration 0163, P3a): the terms HTML a buyer accepted at checkout is
// dispute evidence next to checkout_consents, which names it. No code may
// delete it; only a migration's Down section and test fixtures (_test.go,
// not scanned) may. UPDATE is refused by the table's own trigger.
var renderingDelete = regexp.MustCompile(`(?is)(DELETE\s+FROM|TRUNCATE(\s+TABLE)?)\s+(ONLY\s+)?legal_renderings\b[^;` + "`" + `]*`)

func TestLegalRenderings_NoCodeDeletesEvidence(t *testing.T) {
	root := repoRoot(t)
	for _, probe := range []string{"DELETE FROM legal_renderings WHERE org_id = $1", "TRUNCATE legal_renderings"} {
		if !renderingDelete.MatchString(probe) {
			t.Fatalf("the pattern misses %q", probe)
		}
	}
	var violations []string
	sawTable := false
	for _, dir := range []string{
		filepath.Join(root, "apps", "backend", "cmd"),
		filepath.Join(root, "apps", "backend", "internal"),
		filepath.Join(root, "ops"),
	} {
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
			if strings.Contains(text, "CREATE TABLE legal_renderings") {
				sawTable = true
			}
			for _, m := range renderingDelete.FindAllString(text, -1) {
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel+": "+strings.Join(strings.Fields(m), " "))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if !sawTable {
		t.Error("did not find the migration that creates legal_renderings; did the scan stop finding code?")
	}
	if len(violations) > 0 {
		t.Errorf("legal_renderings are dispute evidence; no code may delete them:\n  %s", strings.Join(violations, "\n  "))
	}
}
