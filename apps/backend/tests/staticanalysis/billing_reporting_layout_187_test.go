// billing_reporting_layout_187_test.go enforces feature #187 — "DDD split:
// billing / reporting".
//
// Four contracts are enforced by this file:
//
//  1. Canonical placement (billing).  The pure-domain billing package lives
//     at apps/backend/internal/domain/billing and the application-layer
//     namespace at apps/backend/internal/app/billing must also exist.
//
//  2. Canonical placement (reporting).  The pure-domain reporting package
//     lives at apps/backend/internal/domain/reporting and the
//     application-layer namespace at apps/backend/internal/app/reporting
//     must also exist.
//
//  3. No legacy top-level directories.  apps/backend/internal/billing/ and
//     apps/backend/internal/reporting/ (siblings of internal/adapters and
//     internal/platform) must NOT contain any .go source files. Those paths
//     are reserved as a layout regression detector — any code in them would
//     indicate that a non-canonical billing / reporting package layout has
//     been re-introduced.
//
//  4. Import direction.  No file in the repository may import the top-level
//     "internal/billing" or "internal/reporting" paths (without the
//     "domain/" or "app/" prefix). All billing / reporting domain imports
//     must use "internal/domain/{billing,reporting}" or
//     "internal/app/{billing,reporting}".
//
// All checks fail loudly with the offending file paths so a CI failure
// directly points to the source of the violation.
//
// Note: this gate intentionally permits the worker-handler packages at
// internal/platform/reporting and internal/platform/reportdelivery to
// remain in place. They host the worker dispatch glue and will migrate
// into internal/app/reporting one job type at a time in follow-up
// increments, as documented on feature #187.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestBillingReportingLayout187_NoLegacyTopLevelDirs asserts that any
// apps/backend/internal/billing/ or apps/backend/internal/reporting/
// directory contains no Go source files. The directories are allowed to
// exist (empty), but any .go file in them would indicate a regression of
// the layout established in feature #187.
func TestBillingReportingLayout187_NoLegacyTopLevelDirs(t *testing.T) {
	repo := repoRoot(t)

	var stragglers []match
	for _, sub := range []string{"billing", "reporting"} {
		legacy := filepath.Join(repo, "apps", "backend", "internal", sub)

		info, err := os.Stat(legacy)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("stat %s: %v", legacy, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s exists but is not a directory", legacy)
		}

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
	}

	reportHits(t,
		"Feature #187: apps/backend/internal/{billing,reporting}/ must not contain "+
			".go files. Move billing-domain rules to "+
			"apps/backend/internal/domain/billing/, billing orchestration to "+
			"apps/backend/internal/app/billing/, reporting-domain rules to "+
			"apps/backend/internal/domain/reporting/, and reporting "+
			"orchestration to apps/backend/internal/app/reporting/.",
		stragglers,
	)
}

// TestBillingReportingLayout187_DomainDirsExist asserts that the
// domain-layer namespaces established in feature #187 have not been
// deleted, and each contains at least one .go file so the package is
// discoverable by go/packages.
func TestBillingReportingLayout187_DomainDirsExist(t *testing.T) {
	repo := repoRoot(t)
	for _, sub := range []string{"billing", "reporting"} {
		dir := filepath.Join(repo, "apps", "backend", "internal", "domain", sub)

		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("feature #187: apps/backend/internal/domain/%s must exist (got: %v)", sub, err)
		}
		if !info.IsDir() {
			t.Fatalf("feature #187: %s exists but is not a directory", dir)
		}
		if !dirHasGoFile(t, dir) {
			t.Fatalf("feature #187: apps/backend/internal/domain/%s must contain at least one .go file", sub)
		}
	}
}

// TestBillingReportingLayout187_AppDirsExist asserts that the
// application-layer namespaces established in feature #187 have not been
// deleted.
func TestBillingReportingLayout187_AppDirsExist(t *testing.T) {
	repo := repoRoot(t)
	for _, sub := range []string{"billing", "reporting"} {
		dir := filepath.Join(repo, "apps", "backend", "internal", "app", sub)

		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("feature #187: apps/backend/internal/app/%s must exist (got: %v)", sub, err)
		}
		if !info.IsDir() {
			t.Fatalf("feature #187: %s exists but is not a directory", dir)
		}
		if !dirHasGoFile(t, dir) {
			t.Fatalf("feature #187: apps/backend/internal/app/%s must contain at least one .go file", sub)
		}
	}
}

// legacyBillingImportPattern matches the legacy top-level import path
// "internal/billing" without the "domain/" or "app/" prefix.
var legacyBillingImportPattern = regexp.MustCompile(
	`"github\.com/abhteam/arena_new/apps/backend/internal/billing"`,
)

// legacyReportingImportPattern matches the legacy top-level import path
// "internal/reporting" without the "domain/" or "app/" prefix. The pattern
// excludes "internal/platform/reporting" and "internal/platform/reportdelivery"
// which are intentionally permitted (worker-handler hosts).
var legacyReportingImportPattern = regexp.MustCompile(
	`"github\.com/abhteam/arena_new/apps/backend/internal/reporting"`,
)

// TestBillingReportingLayout187_NoLegacyImports asserts that no .go file in
// the repository (excluding this very test file) imports the legacy
// "internal/billing" or "internal/reporting" paths. All importers must use
// the new "internal/domain/{billing,reporting}" or
// "internal/app/{billing,reporting}" paths.
func TestBillingReportingLayout187_NoLegacyImports(t *testing.T) {
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
			if legacyBillingImportPattern.MatchString(line) ||
				legacyReportingImportPattern.MatchString(line) {
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
		"Feature #187: imports of the legacy paths "+
			"\"github.com/abhteam/arena_new/apps/backend/internal/billing\" and "+
			"\"github.com/abhteam/arena_new/apps/backend/internal/reporting\" "+
			"are forbidden. Use "+
			"\"github.com/abhteam/arena_new/apps/backend/internal/domain/{billing,reporting}\" "+
			"(pure rules) or "+
			"\"github.com/abhteam/arena_new/apps/backend/internal/app/{billing,reporting}\" "+
			"(orchestration) instead.",
		violations,
	)
}
