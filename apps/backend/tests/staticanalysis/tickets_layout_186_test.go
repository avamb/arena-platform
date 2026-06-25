// tickets_layout_186_test.go enforces feature #186 — "DDD split: tickets /
// complimentary / barcode_batches / promo_codes".
//
// Three contracts are enforced by this file:
//
//  1. Canonical placement.  The shared ticketing domain package lives at
//     apps/backend/internal/domain/tickets. The application-layer orchestrator
//     namespace at apps/backend/internal/app/tickets must also exist.
//
//  2. No legacy top-level directory.  apps/backend/internal/tickets/ (a
//     sibling of internal/adapters and internal/platform) must NOT contain
//     any .go source files. The directory is permitted to exist as an empty
//     placeholder, but any .go file in it would indicate that someone has
//     re-introduced a non-canonical ticketing package layout.
//
//  3. Import direction.  No file in the repository may import a top-level
//     "internal/tickets" path (without the "domain/" or "app/" prefix). All
//     ticketing-domain imports must use "internal/domain/tickets" or
//     "internal/app/tickets".
//
// All checks fail loudly with the offending file paths so a CI failure
// directly points to the source of the violation.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTicketsLayout186_NoLegacyTopLevelDir asserts that any
// apps/backend/internal/tickets/ directory contains no Go source files. The
// directory is allowed to exist (empty), but any .go file would indicate a
// regression of the layout established in feature #186.
func TestTicketsLayout186_NoLegacyTopLevelDir(t *testing.T) {
	repo := repoRoot(t)
	legacy := filepath.Join(repo, "apps", "backend", "internal", "tickets")

	info, err := os.Stat(legacy)
	if err != nil {
		if os.IsNotExist(err) {
			// Best outcome — the legacy dir does not exist at all.
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
			Text: "(file present in legacy top-level location)",
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", legacy, err)
	}

	reportHits(t,
		"Feature #186: apps/backend/internal/tickets/ must not contain .go files. "+
			"Move the file to apps/backend/internal/domain/tickets/ (pure domain rules) "+
			"or apps/backend/internal/app/tickets/ (orchestration) and update its imports.",
		stragglers,
	)
}

// TestTicketsLayout186_DomainDirExists asserts that the domain-layer namespace
// established in feature #186 has not been deleted, and contains at least one
// .go file so the package is discoverable by go/packages.
func TestTicketsLayout186_DomainDirExists(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "domain", "tickets")

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("feature #186: apps/backend/internal/domain/tickets must exist (got: %v)", err)
	}
	if !info.IsDir() {
		t.Fatalf("feature #186: %s exists but is not a directory", dir)
	}
	if !dirHasGoFile(t, dir) {
		t.Fatalf("feature #186: apps/backend/internal/domain/tickets must contain at least one .go file")
	}
}

// TestTicketsLayout186_AppDirExists asserts that the application-layer
// namespace established in feature #186 has not been deleted.
func TestTicketsLayout186_AppDirExists(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "app", "tickets")

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("feature #186: apps/backend/internal/app/tickets must exist (got: %v)", err)
	}
	if !info.IsDir() {
		t.Fatalf("feature #186: %s exists but is not a directory", dir)
	}
	if !dirHasGoFile(t, dir) {
		t.Fatalf("feature #186: apps/backend/internal/app/tickets must contain at least one .go file")
	}
}

// legacyTicketsImportPattern matches the legacy top-level import path
// "internal/tickets" without the "domain/" or "app/" prefix. We anchor on
// the surrounding quote and the import-path separator so that paths such as
// "internal/domain/tickets" and "internal/app/tickets" do NOT match.
var legacyTicketsImportPattern = regexp.MustCompile(
	`"github\.com/abhteam/arena_new/apps/backend/internal/tickets"`,
)

// TestTicketsLayout186_NoLegacyImports asserts that no .go file in the
// repository (excluding this very test file) imports the legacy
// "internal/tickets" path. All importers must use either
// "internal/domain/tickets" or "internal/app/tickets".
func TestTicketsLayout186_NoLegacyImports(t *testing.T) {
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
			if legacyTicketsImportPattern.MatchString(line) {
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
		"Feature #186: imports of the legacy path "+
			"\"github.com/abhteam/arena_new/apps/backend/internal/tickets\" are forbidden. "+
			"Use \"github.com/abhteam/arena_new/apps/backend/internal/domain/tickets\" "+
			"(pure rules) or \"github.com/abhteam/arena_new/apps/backend/internal/app/tickets\" "+
			"(orchestration) instead.",
		violations,
	)
}

// dirHasGoFile reports whether dir contains at least one regular .go file.
func dirHasGoFile(tb testing.TB, dir string) bool {
	tb.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}
