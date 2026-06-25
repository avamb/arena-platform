// inventory_layout_184_test.go enforces feature #184 — "DDD split: inventory
// (reservations / external_allocations)".
//
// Four contracts are enforced by this file:
//
//  1. Canonical placement.  The shared inventory domain package lives at
//     apps/backend/internal/domain/inventory. The application-layer
//     orchestrator namespace at apps/backend/internal/app/inventory must also
//     exist.
//
//  2. No legacy top-level directory.  apps/backend/internal/inventory/ (a
//     sibling of internal/adapters and internal/platform) must NOT contain
//     any .go source files. The directory is permitted to exist as an empty
//     placeholder, but any .go file in it would indicate that someone has
//     re-introduced a non-canonical inventory package layout.
//
//  3. Import direction (legacy path).  No file in the repository may import
//     a top-level "internal/inventory" path (without the "domain/" or "app/"
//     prefix). All inventory imports must use "internal/domain/inventory"
//     or "internal/app/inventory".
//
//  4. Import direction (domain purity).  Files under
//     apps/backend/internal/domain/inventory/ must not import any package
//     under apps/backend/internal/adapters/... or
//     apps/backend/internal/platform/httpserver. Domain code is allowed to
//     depend only on the standard library and other internal/domain/*
//     packages.
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

// TestInventoryLayout184_NoLegacyTopLevelDir asserts that any
// apps/backend/internal/inventory/ directory contains no Go source files.
// The directory is allowed to exist (empty), but any .go file would
// indicate a regression of the layout established in feature #184.
func TestInventoryLayout184_NoLegacyTopLevelDir(t *testing.T) {
	repo := repoRoot(t)
	legacy := filepath.Join(repo, "apps", "backend", "internal", "inventory")

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
		"Feature #184: apps/backend/internal/inventory/ must not contain .go files. "+
			"Move the file to apps/backend/internal/domain/inventory/ (pure domain rules) "+
			"or apps/backend/internal/app/inventory/ (orchestration) and update its imports.",
		stragglers,
	)
}

// TestInventoryLayout184_DomainDirExists asserts that the domain-layer
// namespace established in feature #184 has not been deleted, and contains
// at least one .go file so the package is discoverable by go/packages.
func TestInventoryLayout184_DomainDirExists(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "domain", "inventory")

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("feature #184: apps/backend/internal/domain/inventory must exist (got: %v)", err)
	}
	if !info.IsDir() {
		t.Fatalf("feature #184: %s exists but is not a directory", dir)
	}
	if !dirHasGoFile(t, dir) {
		t.Fatalf("feature #184: apps/backend/internal/domain/inventory must contain at least one .go file")
	}
}

// TestInventoryLayout184_AppDirExists asserts that the application-layer
// namespace established in feature #184 has not been deleted.
func TestInventoryLayout184_AppDirExists(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "app", "inventory")

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("feature #184: apps/backend/internal/app/inventory must exist (got: %v)", err)
	}
	if !info.IsDir() {
		t.Fatalf("feature #184: %s exists but is not a directory", dir)
	}
	if !dirHasGoFile(t, dir) {
		t.Fatalf("feature #184: apps/backend/internal/app/inventory must contain at least one .go file")
	}
}

// legacyInventoryImportPattern matches the legacy top-level import path
// "internal/inventory" without the "domain/" or "app/" prefix. We anchor on
// the surrounding quote and the import-path separator so that paths such as
// "internal/domain/inventory" and "internal/app/inventory" do NOT match.
var legacyInventoryImportPattern = regexp.MustCompile(
	`"github\.com/abhteam/arena_new/apps/backend/internal/inventory"`,
)

// TestInventoryLayout184_NoLegacyImports asserts that no .go file in the
// repository (excluding this very test file) imports the legacy
// "internal/inventory" path. All importers must use either
// "internal/domain/inventory" or "internal/app/inventory".
func TestInventoryLayout184_NoLegacyImports(t *testing.T) {
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
			if legacyInventoryImportPattern.MatchString(line) {
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
		"Feature #184: imports of the legacy path "+
			"\"github.com/abhteam/arena_new/apps/backend/internal/inventory\" are forbidden. "+
			"Use \"github.com/abhteam/arena_new/apps/backend/internal/domain/inventory\" "+
			"(pure rules) or \"github.com/abhteam/arena_new/apps/backend/internal/app/inventory\" "+
			"(orchestration) instead.",
		violations,
	)
}

// TestInventoryLayout184_DomainHasNoAdapterOrHTTPImports asserts that no
// .go file under apps/backend/internal/domain/inventory/ imports any
// package from internal/adapters/ or internal/platform/httpserver. The
// check inspects only the import block — comments mentioning the
// forbidden paths (e.g. for documentation) are not flagged.
//
// The forbidden-import patterns are shared with the catalog layout gate
// (see forbiddenDomainImportPatterns in catalog_layout_183_test.go); this
// test reuses that set rather than redefining it so the rule is enforced
// identically across every bounded context.
func TestInventoryLayout184_DomainHasNoAdapterOrHTTPImports(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "domain", "inventory")

	var violations []match
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
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
		rel = filepath.ToSlash(rel)

		contents, ferr := os.ReadFile(path)
		if ferr != nil {
			return ferr
		}
		inImportBlock := false
		for i, line := range strings.Split(string(contents), "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(trimmed, "import ("):
				inImportBlock = true
				continue
			case trimmed == ")" && inImportBlock:
				inImportBlock = false
				continue
			case strings.HasPrefix(trimmed, "import \""):
				// Single-line import.
			default:
				if !inImportBlock {
					continue
				}
			}
			for _, pat := range forbiddenDomainImportPatterns {
				if pat.MatchString(line) {
					violations = append(violations, match{
						Path: rel,
						Line: i + 1,
						Text: strings.TrimSpace(line),
					})
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}

	reportHits(t,
		"Feature #184: the pure-domain layer apps/backend/internal/domain/inventory "+
			"must NOT import internal/adapters/* or internal/platform/httpserver. "+
			"Move adapter-dependent code into internal/app/inventory (the application "+
			"orchestrator layer) and depend on it from the HTTP layer instead.",
		violations,
	)
}
