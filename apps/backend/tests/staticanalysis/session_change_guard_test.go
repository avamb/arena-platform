// session_change_guard_test.go keeps the rule of
// 08_architecture/30_session_change_notifications_ru.md §4 true: a session's
// date, time, venue or status is written ONLY by the paths that call
// sessionchange.Apply in the same transaction, so a session can never move or
// be cancelled without its buyers' letters being queued.
//
// The guard has two halves:
//
//   - every call of the sessions UPDATE/soft-delete queries (UpdateSession,
//     SoftDeleteSession) outside the allowlist fails;
//   - every file on the allowlist that rewrites an EXISTING session must
//     import the sessionchange package and call Apply.
//
// A new writer is not forbidden, it just has to join the allowlist on purpose
// — which is the moment somebody reads this comment.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// sessionWriterCall matches a call of one of the session-writing queries.
var sessionWriterCall = regexp.MustCompile(`\.(UpdateSession|SoftDeleteSession)\(`)

// sessionWriterAllowlist are the production files allowed to call them, each
// with whether it rewrites existing sessions (and so must call Apply).
//
// hcatalog/sessions.go also soft-deletes a session it has JUST created when a
// seating bind fails; that session has no buyers and the call is not a change.
var sessionWriterAllowlist = map[string]bool{
	filepath.Join("internal", "platform", "httpserver", "hcatalog", "sessions.go"):     true,
	filepath.Join("internal", "platform", "httpserver", "himports", "import_arena.go"): true,
	filepath.Join("internal", "platform", "httpserver", "himports", "import_exec.go"):  true,
}

// sessionUpdateSQL finds an UPDATE of the sessions table up to its WHERE.
var sessionUpdateSQL = regexp.MustCompile(`(?is)UPDATE\s+sessions\s+(?:s\s+)?SET(.*?)\bWHERE\b`)

// buyerVisibleAssignment finds an assignment to a buyer-visible column.
var buyerVisibleAssignment = regexp.MustCompile(`(?i)\b(start_at|end_at|venue_id|status|deleted_at)\s*=`)

func TestSessionChangeGuard_OnlyAllowlistedFilesWriteSessions(t *testing.T) {
	backend := filepath.Join(repoRoot(t), "apps", "backend")

	var violations []string
	called := map[string]bool{}
	for _, root := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(backend, root), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(backend, path)
			// The wrappers themselves live in gen/; their call sites are what is guarded.
			if strings.Contains(rel, filepath.Join("adapters", "postgres", "gen")+string(filepath.Separator)) {
				return nil
			}
			src, rerr := os.ReadFile(path) // #nosec G304 — repo-local test scan
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			code := stripBlockAndLineComments(string(src))
			if !sessionWriterCall.MatchString(code) {
				return nil
			}
			called[rel] = true
			if !sessionWriterAllowlist[rel] {
				violations = append(violations, rel+" calls UpdateSession/SoftDeleteSession but is not on the sessionchange allowlist")
			}
			return nil
		})
	}
	for rel := range sessionWriterAllowlist {
		if !called[rel] {
			violations = append(violations, rel+" is on the allowlist but no longer writes sessions: remove it")
			continue
		}
		src, err := os.ReadFile(filepath.Join(backend, rel)) // #nosec G304 — repo-local
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(src), "sessionchange.Apply(") && !strings.Contains(string(src), "applySessionChange(") {
			violations = append(violations, rel+" writes sessions but never calls sessionchange.Apply (or applySessionChange)")
		}
	}
	if len(violations) > 0 {
		t.Errorf("a session must not move or be cancelled without sessionchange.Apply in the same transaction:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestSessionChangeGuard_NoOtherSQLWritesBuyerVisibleColumns covers the
// hand-written SQL in gen/ and everywhere else: an UPDATE of sessions that
// assigns the start, end, venue, status or deletion marker belongs to the
// sessions.sql.go wrappers only.
func TestSessionChangeGuard_NoOtherSQLWritesBuyerVisibleColumns(t *testing.T) {
	backend := filepath.Join(repoRoot(t), "apps", "backend")
	sessionsSQL := filepath.Join("internal", "adapters", "postgres", "gen", "sessions.sql.go")

	var violations []string
	for _, root := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(backend, root), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(backend, path)
			if rel == sessionsSQL {
				return nil
			}
			src, rerr := os.ReadFile(path) // #nosec G304 — repo-local test scan
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			for _, m := range sessionUpdateSQL.FindAllStringSubmatch(string(src), -1) {
				if a := buyerVisibleAssignment.FindString(m[1]); a != "" {
					violations = append(violations, rel+": UPDATE sessions assigns "+strings.TrimSpace(a)+" outside gen/sessions.sql.go")
				}
			}
			return nil
		})
	}
	if len(violations) > 0 {
		t.Errorf("buyer-visible session columns are written only through the guarded wrappers:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// stripBlockAndLineComments blanks Go comments so prose mentioning a query
// name is not mistaken for a call.
func stripBlockAndLineComments(src string) string {
	block := regexp.MustCompile(`(?s)/\*.*?\*/`)
	src = block.ReplaceAllString(src, "")
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		lines[i] = stripGoComment(l)
	}
	return strings.Join(lines, "\n")
}
