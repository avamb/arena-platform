// email_letters_retention_test.go keeps the retention rule of
// emailstatus.MinRetention true: buyer letters, their provider events and
// their opens/clicks are dispute evidence and are kept at least 24 months.
// No production code and no migration may delete or truncate them, except
// the two statements that remove a letter the mail relay never accepted
// (status 'sending' — nothing was sent, so there is nothing to prove).
//
// A future purge job must keep the 24 months and be added here on purpose,
// with the documentation (docs/ops/brevo_delivery_status_webhook_ru.md §5)
// changed in the same commit.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// letterDelete matches a DELETE or TRUNCATE aimed at one of the letter
// tables, across a line break.
var letterDelete = regexp.MustCompile(`(?is)(DELETE\s+FROM|TRUNCATE(\s+TABLE)?)\s+(ONLY\s+)?(email_letters|email_letter_events|email_letter_interactions)\b[^;` + "`" + `]*`)

// sendingOnly is the only deletion allowed: a letter still 'sending'.
var sendingOnly = regexp.MustCompile(`(?is)status\s*=\s*'sending'`)

func TestEmailLetters_NoCodeDeletesEvidenceEarly(t *testing.T) {
	root := repoRoot(t)
	dirs := []string{
		filepath.Join(root, "apps", "backend", "cmd"),
		filepath.Join(root, "apps", "backend", "internal"),
		filepath.Join(root, "ops"),
	}
	var violations []string
	allowed := 0
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
				// A migration's Down section may drop the tables it created.
				if i := strings.Index(text, "-- +goose Down"); i >= 0 {
					text = text[:i]
				}
			}
			for _, m := range letterDelete.FindAllString(text, -1) {
				if sendingOnly.MatchString(m) {
					allowed++
					continue
				}
				rel, _ := filepath.Rel(root, path)
				violations = append(violations, rel+": "+strings.Join(strings.Fields(m), " "))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(violations) > 0 {
		t.Errorf("letters are dispute evidence and are kept at least 24 months (emailstatus.MinRetention);"+
			" only a letter still 'sending' may be deleted:\n  %s", strings.Join(violations, "\n  "))
	}
	if allowed == 0 {
		t.Error("expected the 'sending' clean-up statements of internal/platform/emailstatus; did the scan stop finding code?")
	}
}
