// testfilehelpers_test.go provides shared file-finding utilities for
// httpserver package tests that need to read project-level files such as
// openapi/openapi.yaml and README.md at test time.
//
// The strategy mirrors the approach used in openapi_drift_test.go:
//  1. Use runtime.Caller(0) to get the absolute path of the test source file,
//     then walk upward to find the target file.
//  2. Fall back to os.Getwd() for -trimpath / Docker environments where
//     runtime.Caller returns a module-relative (non-absolute) path.
//
// Both strategies navigate from the httpserver package directory (6 levels
// below the repo root) to the repo root, then locate well-known files.
package httpserver

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// findFileByName locates a named project file and returns its content as a
// string. Supported names:
//
//   - "openapi.yaml"  → apps/backend/openapi/openapi.yaml (relative to repo root)
//   - "README.md"     → README.md at the repo root
//
// The function uses two strategies (runtime.Caller then CWD) so it works both
// in normal `go test` runs and in Docker/CI builds that use -trimpath.
func findFileByName(t *testing.T, name string) string {
	t.Helper()

	// Strategy 1: compile-time absolute path via runtime.Caller.
	_, thisFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(thisFile) {
		// thisFile = .../apps/backend/internal/platform/httpserver/testfilehelpers_test.go
		// Navigate up to the repo root: dir(thisFile) → httpserver/ → platform/ →
		// internal/ → backend/ → apps/ → repo-root (5 steps).
		dir := filepath.Dir(thisFile)
		repoRoot := dir
		for i := 0; i < 5; i++ {
			repoRoot = filepath.Dir(repoRoot)
		}
		if candidate := resolveFileInRepo(repoRoot, name); candidate != "" {
			data, err := os.ReadFile(candidate)
			if err == nil {
				return string(data)
			}
		}
	}

	// Strategy 2: CWD-based fallback for -trimpath / Docker environments.
	// `go test` sets CWD to the package directory being tested.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("findFileByName: cannot determine working directory: %v", err)
	}

	// Walk upward from CWD looking for the repo root (signalled by the presence
	// of go.mod) and then resolve the target file.
	dir := cwd
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			// Found repo root.
			if candidate := resolveFileInRepo(dir, name); candidate != "" {
				data, err := os.ReadFile(candidate)
				if err == nil {
					return string(data)
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatalf("findFileByName: cannot locate %q; cwd=%s", name, cwd)
	return ""
}

// resolveFileInRepo maps a well-known filename to its canonical path within the
// repository rooted at repoRoot. Returns the path if the file exists, or "".
func resolveFileInRepo(repoRoot, name string) string {
	var candidates []string
	switch name {
	case "openapi.yaml":
		// The spec lives at apps/backend/openapi/openapi.yaml inside the repo.
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "openapi", "openapi.yaml"),
			// Also try a direct openapi/ at the repo root in case the layout changes.
			filepath.Join(repoRoot, "openapi", "openapi.yaml"),
		}
	case "README.md":
		candidates = []string{
			filepath.Join(repoRoot, "README.md"),
		}
	case "0004_scaffold_echo.sql":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "migrations", "sql", "0004_scaffold_echo.sql"),
		}
	case "scaffold_echo.sql.go":
		candidates = []string{
			filepath.Join(repoRoot, "apps", "backend", "internal", "adapters", "postgres", "gen", "scaffold_echo.sql.go"),
		}
	default:
		// Generic fallback: try the file directly at the repo root.
		candidates = []string{
			filepath.Join(repoRoot, name),
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
