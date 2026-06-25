// payments_layout_185_test.go enforces feature #185 — "DDD split:
// checkout / payments / refunds".
//
// Two contracts are enforced by this file:
//
//  1. Canonical placement.  The shared payments domain package lives at
//     apps/backend/internal/domain/payments. The legacy location at
//     apps/backend/internal/payments/ (a top-level sibling of internal/
//     adapters and internal/platform) must NOT contain any .go source files.
//     Likewise, the application-layer orchestrator namespace at
//     apps/backend/internal/app/payments must exist.
//
//  2. Import direction.  No file in the codebase may import the legacy
//     location "internal/payments" (without the "domain/" prefix). All
//     payment-domain imports must use
//     "github.com/abhteam/arena_new/apps/backend/internal/domain/payments".
//
// Both checks fail loudly with the offending file paths so a CI failure
// directly points to the source of the violation.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestPaymentsLayout185_LegacyDirIsEmpty asserts that the legacy
// internal/payments/ directory (if it still exists on disk) contains no Go
// source files. The directory is allowed to exist as an empty placeholder
// for compatibility with stale workspaces, but any .go file in it would
// indicate a regression of the move performed in feature #185.
func TestPaymentsLayout185_LegacyDirIsEmpty(t *testing.T) {
	repo := repoRoot(t)
	legacy := filepath.Join(repo, "apps", "backend", "internal", "payments")

	info, err := os.Stat(legacy)
	if err != nil {
		if os.IsNotExist(err) {
			// Best outcome — the legacy dir was removed entirely.
			return
		}
		t.Fatalf("stat %s: %v", legacy, err)
	}
	if !info.IsDir() {
		t.Fatalf("%s exists but is not a directory", legacy)
	}

	var stragglers []match
	err = filepath.WalkDir(legacy, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, _ := filepath.Rel(repo, path)
		stragglers = append(stragglers, match{
			Path: filepath.ToSlash(rel),
			Line: 0,
			Text: "(file present in legacy location)",
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", legacy, err)
	}

	reportHits(t,
		"Feature #185: apps/backend/internal/payments/ must not contain .go files. "+
			"Move the file to apps/backend/internal/domain/payments/ (pure domain rules) "+
			"or apps/backend/internal/app/payments/ (orchestration) and update its imports.",
		stragglers,
	)
}

// TestPaymentsLayout185_AppDirExists asserts that the application-layer
// namespace established in feature #185 has not been deleted. Without this
// guard, a careless cleanup of the (intentionally minimal) app/payments
// package would silently undo the layout contract.
func TestPaymentsLayout185_AppDirExists(t *testing.T) {
	repo := repoRoot(t)
	appDir := filepath.Join(repo, "apps", "backend", "internal", "app", "payments")

	info, err := os.Stat(appDir)
	if err != nil {
		t.Fatalf("feature #185: apps/backend/internal/app/payments must exist (got: %v)", err)
	}
	if !info.IsDir() {
		t.Fatalf("feature #185: %s exists but is not a directory", appDir)
	}

	// Require at least one .go file (the doc.go skeleton) so the package is
	// discoverable by go/packages and by future agents reading the tree.
	entries, err := os.ReadDir(appDir)
	if err != nil {
		t.Fatalf("read %s: %v", appDir, err)
	}
	hasGoFile := false
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			hasGoFile = true
			break
		}
	}
	if !hasGoFile {
		t.Fatalf("feature #185: apps/backend/internal/app/payments must contain at least one .go file")
	}
}

// legacyPaymentsImportPattern matches the legacy import path. We deliberately
// use a negative lookbehind-style check by ensuring "/domain/payments" does
// NOT precede the captured "/internal/payments" path: any occurrence of the
// bare "internal/payments" string (without "/domain/") inside an import block
// is a violation.
var legacyPaymentsImportPattern = regexp.MustCompile(
	`"github\.com/abhteam/arena_new/apps/backend/internal/payments"`,
)

// TestPaymentsLayout185_NoLegacyImports asserts that no .go file in the
// repository (excluding this very test file) imports the legacy
// "internal/payments" path. All importers must use the new
// "internal/domain/payments" path.
func TestPaymentsLayout185_NoLegacyImports(t *testing.T) {
	root := repoRoot(t)
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
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, selfPrefix) {
			return nil
		}

		contents, ferr := os.ReadFile(path)
		if ferr != nil {
			return ferr
		}
		for i, line := range strings.Split(string(contents), "\n") {
			if legacyPaymentsImportPattern.MatchString(line) {
				violations = append(violations, match{
					Path: rel,
					Line: i + 1,
					Text: strings.TrimSpace(line),
				})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	reportHits(t,
		"Feature #185: imports of the legacy path "+
			"\"github.com/abhteam/arena_new/apps/backend/internal/payments\" are forbidden. "+
			"Use \"github.com/abhteam/arena_new/apps/backend/internal/domain/payments\" instead.",
		violations,
	)
}
