// nopanic_176_test.go enforces feature #176 — "Audit and remove panic() in
// production code".
//
// Contract enforced by this file:
//
//	No .go file under apps/backend/ may contain a literal `panic(` call
//	unless one of the following exemptions applies:
//
//	  1. The file is a *_test.go file (test code may panic freely).
//	  2. The file lives under apps/backend/cmd/ (CLI / migration tools
//	     where a fatal error legitimately aborts the process).
//	  3. The panic line — or the immediately preceding line — carries the
//	     literal marker `// allow:panic` with a justification comment. This
//	     marker is the explicit escape hatch for category (a) "programmer
//	     invariant" panics and category (c) "initialization-time" panics
//	     classified in the feature-#176 audit (see
//	     ops/codequality/panic-audit-176.md).
//
// The audit performed in feature #176 found 9 panic( call sites in
// non-test, non-cmd production code (a 10th — the networkscope.NewScoper
// nil-dependency guard from feature #207 — was annotated later). Every one
// is either a constructor / boot-time precondition guard, a documented
// Must* variant, the idiomatic "rollback-and-rethrow" pattern in defer, or
// the dedicated debug endpoint that exists to exercise the panic-recoverer
// middleware. All have been annotated with `// allow:panic: <reason>` and
// are therefore exempt.
//
// If this test starts failing, a new panic( has been introduced in
// production code without an audit-approved exemption. Choose one:
//
//   - If the new panic represents a recoverable runtime/IO condition,
//     replace it with an error return (and have the HTTP layer log
//     slog.Error + return 5xx).
//   - If the new panic is genuinely a programmer invariant or boot-time
//     precondition, annotate it with `// allow:panic: <one-sentence reason>`
//     directly above (or on the same line as) the panic( call and update
//     the audit document.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// panicCallPattern matches a literal panic( token. We intentionally do NOT
// match strings like `// panic(` or `recover()`; the leading-word-boundary
// `\bpanic\(` plus the comment-stripping logic in the scan loop below filter
// those out.
var panicCallPattern = regexp.MustCompile(`\bpanic\(`)

// allowMarkerPattern matches the `// allow:panic` escape hatch. The marker
// may appear on the same source line as the panic( call (trailing comment)
// OR on the immediately preceding line (leading comment), to accommodate
// both styles found in the codebase after the feature-#176 audit.
var allowMarkerPattern = regexp.MustCompile(`//\s*allow:panic\b`)

// TestNoUnaudittedPanic walks apps/backend/, skips test files, skips the
// cmd/ subtree, strips line-comments, and flags any remaining `panic(` call
// that is not covered by an `// allow:panic` marker on the same or
// preceding line.
//
// This is the static-analysis gate required by step 5 of feature #176.
func TestNoUnaudittedPanic(t *testing.T) {
	root := backendDir(t)
	repo := repoRoot(t)

	cmdPrefix := filepath.ToSlash(filepath.Join("apps", "backend", "cmd")) + "/"
	selfPrefix := "apps/backend/tests/staticanalysis/"

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

		// Exempt the cmd/ subtree (CLI/migration entry points).
		if strings.HasPrefix(rel, cmdPrefix) {
			return nil
		}
		// Exempt this very test directory (it spells `panic(` as a regex).
		if strings.HasPrefix(rel, selfPrefix) {
			return nil
		}

		contents, ferr := os.ReadFile(path)
		if ferr != nil {
			return ferr
		}
		lines := strings.Split(string(contents), "\n")

		for i, line := range lines {
			// Strip any trailing line-comment so we don't false-match on
			// doc comments like `// foo panic( bar`. Marker detection
			// runs against the original line text.
			code := stripLineComment(line)
			if !panicCallPattern.MatchString(code) {
				continue
			}

			// Same-line trailing-comment marker.
			if allowMarkerPattern.MatchString(line) {
				continue
			}

			// Walk backward through the contiguous block of comment-only
			// lines (// …) directly above the panic and accept the marker
			// anywhere in that block. This accommodates multi-line
			// justification comments without forcing the marker onto the
			// line immediately above panic(.
			matched := false
			for j := i - 1; j >= 0; j-- {
				trimmed := strings.TrimSpace(lines[j])
				if trimmed == "" {
					// Blank line ends the comment block.
					break
				}
				if !strings.HasPrefix(trimmed, "//") {
					// Non-comment code line ends the block.
					break
				}
				if allowMarkerPattern.MatchString(lines[j]) {
					matched = true
					break
				}
			}
			if matched {
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
		`Feature #176 step 5: panic( is forbidden in apps/backend/ outside cmd/, `+
			`test files, and lines explicitly annotated with `+"`// allow:panic: <reason>`"+
			`. Convert recoverable IO/user-input panics to error returns + slog.Error + 5xx, `+
			`OR add an // allow:panic marker on (or above) the panic line with a one-sentence `+
			`justification and update ops/codequality/panic-audit-176.md.`,
		violations,
	)
}

// stripLineComment removes the trailing `// …` portion of a Go source line
// while respecting string literals. This is a pragmatic implementation —
// good enough for the panic-detection use case where every false candidate
// we have ever seen sits inside either (a) a doc-comment line or (b) a
// trailing comment annotation. We do not need a full Go lexer here.
func stripLineComment(line string) string {
	inString := false
	inRawString := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inString:
			if c == '\\' && i+1 < len(line) {
				i++
				continue
			}
			if c == '"' {
				inString = false
			}
		case inRawString:
			if c == '`' {
				inRawString = false
			}
		default:
			if c == '"' {
				inString = true
				continue
			}
			if c == '`' {
				inRawString = true
				continue
			}
			if c == '/' && i+1 < len(line) && line[i+1] == '/' {
				return line[:i]
			}
		}
	}
	return line
}
