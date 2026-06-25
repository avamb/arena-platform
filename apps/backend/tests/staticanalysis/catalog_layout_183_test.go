// catalog_layout_183_test.go enforces feature #183 — "DDD split: catalog
// (events / sessions / ticket_tiers)".
//
// Four contracts are enforced by this file:
//
//  1. Canonical placement.  The shared catalog domain package lives at
//     apps/backend/internal/domain/catalog. The application-layer
//     orchestrator namespace at apps/backend/internal/app/catalog must also
//     exist.
//
//  2. No legacy top-level directory.  apps/backend/internal/catalog/ (a
//     sibling of internal/adapters and internal/platform) must NOT contain
//     any .go source files. The directory is permitted to exist as an empty
//     placeholder, but any .go file in it would indicate that someone has
//     re-introduced a non-canonical catalog package layout.
//
//  3. Import direction (legacy path).  No file in the repository may import
//     a top-level "internal/catalog" path (without the "domain/" or "app/"
//     prefix). All catalog imports must use "internal/domain/catalog" or
//     "internal/app/catalog".
//
//  4. Import direction (domain purity).  Files under
//     apps/backend/internal/domain/catalog/ must not import any package
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

// TestCatalogLayout183_NoLegacyTopLevelDir asserts that any
// apps/backend/internal/catalog/ directory contains no Go source files. The
// directory is allowed to exist (empty), but any .go file would indicate a
// regression of the layout established in feature #183.
func TestCatalogLayout183_NoLegacyTopLevelDir(t *testing.T) {
	repo := repoRoot(t)
	legacy := filepath.Join(repo, "apps", "backend", "internal", "catalog")

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
		"Feature #183: apps/backend/internal/catalog/ must not contain .go files. "+
			"Move the file to apps/backend/internal/domain/catalog/ (pure domain rules) "+
			"or apps/backend/internal/app/catalog/ (orchestration) and update its imports.",
		stragglers,
	)
}

// TestCatalogLayout183_DomainDirExists asserts that the domain-layer namespace
// established in feature #183 has not been deleted, and contains at least one
// .go file so the package is discoverable by go/packages.
func TestCatalogLayout183_DomainDirExists(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "domain", "catalog")

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("feature #183: apps/backend/internal/domain/catalog must exist (got: %v)", err)
	}
	if !info.IsDir() {
		t.Fatalf("feature #183: %s exists but is not a directory", dir)
	}
	if !dirHasGoFile(t, dir) {
		t.Fatalf("feature #183: apps/backend/internal/domain/catalog must contain at least one .go file")
	}
}

// TestCatalogLayout183_AppDirExists asserts that the application-layer
// namespace established in feature #183 has not been deleted.
func TestCatalogLayout183_AppDirExists(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "app", "catalog")

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("feature #183: apps/backend/internal/app/catalog must exist (got: %v)", err)
	}
	if !info.IsDir() {
		t.Fatalf("feature #183: %s exists but is not a directory", dir)
	}
	if !dirHasGoFile(t, dir) {
		t.Fatalf("feature #183: apps/backend/internal/app/catalog must contain at least one .go file")
	}
}

// legacyCatalogImportPattern matches the legacy top-level import path
// "internal/catalog" without the "domain/" or "app/" prefix. We anchor on
// the surrounding quote and the import-path separator so that paths such as
// "internal/domain/catalog" and "internal/app/catalog" do NOT match.
var legacyCatalogImportPattern = regexp.MustCompile(
	`"github\.com/abhteam/arena_new/apps/backend/internal/catalog"`,
)

// TestCatalogLayout183_NoLegacyImports asserts that no .go file in the
// repository (excluding this very test file) imports the legacy
// "internal/catalog" path. All importers must use either
// "internal/domain/catalog" or "internal/app/catalog".
func TestCatalogLayout183_NoLegacyImports(t *testing.T) {
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
			if legacyCatalogImportPattern.MatchString(line) {
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
		"Feature #183: imports of the legacy path "+
			"\"github.com/abhteam/arena_new/apps/backend/internal/catalog\" are forbidden. "+
			"Use \"github.com/abhteam/arena_new/apps/backend/internal/domain/catalog\" "+
			"(pure rules) or \"github.com/abhteam/arena_new/apps/backend/internal/app/catalog\" "+
			"(orchestration) instead.",
		violations,
	)
}

// forbiddenDomainImportPatterns lists import-path prefixes that the
// pure-domain layer is forbidden to depend on. The pure-domain layer must
// not reach into HTTP serving or persistence adapters, since that would
// invert the dependency direction the DDD split is meant to establish.
var forbiddenDomainImportPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"github\.com/abhteam/arena_new/apps/backend/internal/adapters/`),
	regexp.MustCompile(`"github\.com/abhteam/arena_new/apps/backend/internal/platform/httpserver`),
}

// TestCatalogLayout183_DomainHasNoAdapterOrHTTPImports asserts that no .go
// file under apps/backend/internal/domain/catalog/ imports any package from
// internal/adapters/ or internal/platform/httpserver. The check inspects only
// the import block — comments mentioning the forbidden paths (e.g. for
// documentation) are not flagged.
func TestCatalogLayout183_DomainHasNoAdapterOrHTTPImports(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "domain", "catalog")

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
		"Feature #183: the pure-domain layer apps/backend/internal/domain/catalog "+
			"must NOT import internal/adapters/* or internal/platform/httpserver. "+
			"Move adapter-dependent code into internal/app/catalog (the application "+
			"orchestrator layer) and depend on it from the HTTP layer instead.",
		violations,
	)
}
