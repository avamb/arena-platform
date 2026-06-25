// context_background_178_test.go enforces feature #178 — "Forbid
// context.Background() outside cmd/ and tests".
//
// Contract enforced by this file:
//
//	No .go source file under apps/backend/internal/ may contain a literal
//	`context.Background()` call expression unless one of the following
//	exemptions applies:
//
//	  1. The file is a *_test.go file (test code may root its own ctx).
//	  2. The file lives under apps/backend/cmd/ (CLI / migration / server
//	     entry points where the process owns the top-level ctx).
//	  3. The file lives under apps/backend/internal/tests/ (shared test
//	     infrastructure such as pgtest — test helpers that themselves root
//	     a ctx for testcontainers, TruncateAll, WithTx etc).
//
// Rationale: a background-rooted ctx inside the request or worker call
// path breaks request cancellation, per-request timeouts and OpenTelemetry
// trace propagation. When a detached deadline is genuinely required (e.g.
// a `tx.Rollback` defer that must run even when the request ctx has been
// cancelled), code MUST use `context.WithoutCancel(ctx)` (Go 1.21+) so
// trace/log values still propagate, or an explicit
// `context.WithTimeout(parent, …)`.
//
// If this test starts failing, a new `context.Background()` has been
// introduced in production code. Replace it with either:
//
//   - the inherited request/job/transaction ctx, OR
//   - `context.WithoutCancel(<parent>)` if you need a detached lifetime,
//     OR
//   - an explicit `context.WithTimeout(<parent>, …)`.
//
// If you legitimately need a new root ctx (a long-lived background
// goroutine started by an adapter constructor), derive it once from the
// constructor's ctx via `context.WithoutCancel` and stash it on the
// receiver — see internal/platform/database.Pool.bgCtx for the pattern.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bgCallPattern matches a literal `context.Background()` call expression.
// Aliased imports (e.g. `ctx "context"`) are not used anywhere in this
// codebase; should they appear, extend this pattern.
var bgCallPattern = regexp.MustCompile(`\bcontext\.Background\s*\(\s*\)`)

// TestNoContextBackgroundOutsideWhitelist_178 walks apps/backend/internal/,
// skips test files and the tests/ subtree, strips line-comments, and flags
// any remaining `context.Background()` call.
func TestNoContextBackgroundOutsideWhitelist_178(t *testing.T) {
	root := filepath.Join(backendDir(t), "internal")
	repo := repoRoot(t)

	testsSubtree := filepath.ToSlash(filepath.Join("apps", "backend", "internal", "tests")) + "/"

	var violations []match

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if _, blocked := defaultSkipDirs[d.Name()]; blocked {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") {
			return nil
		}
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(repo, path)
		rel = filepath.ToSlash(rel)

		// Exempt the shared test-infrastructure subtree (pgtest etc.).
		if strings.HasPrefix(rel, testsSubtree) {
			return nil
		}

		contents, ferr := os.ReadFile(path)
		if ferr != nil {
			return ferr
		}
		lines := strings.Split(string(contents), "\n")

		for i, line := range lines {
			// Strip any trailing line-comment so doc comments mentioning
			// "context.Background()" do not false-match.
			code := stripLineComment(line)
			if !bgCallPattern.MatchString(code) {
				continue
			}
			violations = append(violations, match{
				Path: rel,
				Line: i + 1,
				Text: strings.TrimSpace(line),
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	reportHits(t,
		`Feature #178: context.Background() is forbidden in apps/backend/internal/ `+
			`outside *_test.go files and the internal/tests/ helper subtree. `+
			`Replace with the inherited request/job/transaction ctx; if you need `+
			`a detached lifetime, use context.WithoutCancel(<parent>) (Go 1.21+) `+
			`or an explicit context.WithTimeout(<parent>, …). For long-lived `+
			`background goroutines started in constructors, stash a `+
			`context.WithoutCancel(ctx) once on the receiver — see `+
			`internal/platform/database.Pool.bgCtx for the pattern.`,
		violations,
	)
}
