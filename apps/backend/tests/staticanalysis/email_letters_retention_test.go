// email_letters_retention_test.go keeps the retention rule of
// emailstatus.MinRetention true: buyer letters, their provider events and
// their opens/clicks are dispute evidence and are kept at least 24 months,
// counted from the event date (emailstatus.RetentionAnchorSQL).
// No production code and no migration may delete or truncate them, except
// the two statements that remove a letter the mail relay never accepted
// (status 'sending' — nothing was sent, so there is nothing to prove).
//
// A future purge job must keep the 24 months, count them from
// emailstatus.RetentionAnchorSQL (never from sent_at alone), and be added here on purpose,
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

// sendingOnly is the only deletion allowed: a DELETE whose WHERE starts
// with "status = 'sending' AND" (an optional table alias in front) and has no
// OR anywhere, so no other condition can widen it to letters that were sent.
// TRUNCATE has no WHERE and never matches.
var sendingOnly = regexp.MustCompile(`(?is)^DELETE\s+FROM\s+email_letters(\s+\w+)?\s+WHERE\s+(\w+\.)?status\s*=\s*'sending'\s+AND\b`)

// abandonDelete is Abandon's removal of the one letter the relay refused a
// moment ago: by id, still 'sending', nothing else.
var abandonDelete = regexp.MustCompile(`(?is)^DELETE\s+FROM\s+email_letters\s+WHERE\s+id\s*=\s*\$1\s+AND\s+status\s*=\s*'sending'\s*$`)

var anyOr = regexp.MustCompile(`(?i)\bOR\b`)

// allowedLetterDelete reports whether a matched statement is the permitted
// clean-up of letters still 'sending'.
func allowedLetterDelete(stmt string) bool {
	return sendingOnly.MatchString(stmt) && !anyOr.MatchString(stmt)
}

// TestEmailLetters_AllowedDeleteShape pins what the scan lets through
// (review L4: "status = 'sending' OR …" used to pass).
func TestEmailLetters_AllowedDeleteShape(t *testing.T) {
	cases := []struct {
		stmt string
		ok   bool
	}{
		{"DELETE FROM email_letters WHERE status = 'sending' AND sent_at < now() - $1::interval", true},
		{"DELETE FROM email_letters l\nWHERE l.status = 'sending' AND l.sent_at < now() - $1::interval AND NOT EXISTS (SELECT 1)", true},
		{"DELETE FROM email_letters WHERE id = $1 AND status = 'sending'", false},
		{"DELETE FROM email_letters WHERE status = 'sending' OR sent_at < now() - $1::interval", false},
		{"DELETE FROM email_letters WHERE status = 'sending' AND (sent_at < now() OR true)", false},
		{"DELETE FROM email_letters WHERE status = 'sent' AND sent_at < now()", false},
		{"DELETE FROM email_letter_events WHERE status = 'sending' AND true", false},
		{"TRUNCATE email_letters", false},
	}
	for _, c := range cases {
		if got := allowedLetterDelete(c.stmt); got != c.ok {
			t.Errorf("allowedLetterDelete(%q) = %v, want %v", c.stmt, got, c.ok)
		}
	}
}

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
				if allowedLetterDelete(m) || abandonDelete.MatchString(m) {
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
		t.Errorf("letters are dispute evidence and are kept at least 24 months from the event date (emailstatus.MinRetention, RetentionAnchorSQL);"+
			" only a letter still 'sending' may be deleted:\n  %s", strings.Join(violations, "\n  "))
	}
	if allowed == 0 {
		t.Error("expected the 'sending' clean-up statements of internal/platform/emailstatus; did the scan stop finding code?")
	}
}

// consentDelete matches a DELETE or TRUNCATE aimed at checkout_consents (P2,
// migration 0155): the buyer's consent at checkout is dispute evidence with
// the same rule as the letters - at least 24 and at most 36 months from the
// event date - and there is no purge yet. Only a migration's Down section and
// test fixtures (_test.go, not scanned) may remove rows. A future purge must
// count from the event date (emailstatus.RetentionAnchorSQL is the model) and
// be allowed here on purpose.
var consentDelete = regexp.MustCompile(`(?is)(DELETE\s+FROM|TRUNCATE(\s+TABLE)?)\s+(ONLY\s+)?checkout_consents\b[^;` + "`" + `]*`)

func TestCheckoutConsents_NoCodeDeletesEvidence(t *testing.T) {
	root := repoRoot(t)
	for _, probe := range []string{"DELETE FROM checkout_consents WHERE org_id = $1", "TRUNCATE TABLE checkout_consents"} {
		if !consentDelete.MatchString(probe) {
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
			if strings.Contains(text, "CREATE TABLE checkout_consents") {
				sawTable = true
			}
			for _, m := range consentDelete.FindAllString(text, -1) {
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
		t.Error("did not find the migration that creates checkout_consents; did the scan stop finding code?")
	}
	if len(violations) > 0 {
		t.Errorf("checkout_consents are dispute evidence kept at least 24 months from the event date; no code may delete them:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
