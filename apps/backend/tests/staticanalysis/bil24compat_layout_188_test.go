// bil24compat_layout_188_test.go enforces feature #188 — "DDD split:
// Bil24 compatibility adapter boundary".
//
// Three contracts are enforced by this file:
//
//  1. Canonical placement.  The Bil24 wire-format adapter package lives at
//     apps/backend/internal/adapters/bil24compat and must contain at least
//     one .go file so the package is discoverable by go/packages. The
//     legacy Bil24 wire surface (request/response envelope, result codes,
//     ID translation helpers) lives in this adapter package; the HTTP
//     layer re-exports / forwards to it.
//
//  2. Wire-format ownership.  Sentinel wire-format identifiers
//     (ResultCodeOK, ResultCodeUnknownCommand, ResultCodeInvalidRequest,
//     ResultCodeNotFound, ResultCodeInternalError, TranslateLegacyID,
//     TranslatePlatformID, ErrLegacyIDNotFound) must appear at least once
//     in the adapter package. This guards against accidental deletion or
//     a re-introduction of these names in the HTTP layer as the
//     authoritative definition.
//
//  3. Adapter purity.  Files under apps/backend/internal/adapters/
//     bil24compat/ must not import anything from
//     apps/backend/internal/platform/httpserver. The dependency direction
//     is HTTP → adapter, never the other way; otherwise the boundary
//     would be cyclic and the adapter could not be re-used by future
//     non-HTTP delivery mechanisms (CLI replays, batch importers).
//
// The Bil24 compat contract test suite under apps/backend/tests/compat/
// bil24 is not modified by this file — it remains the byte-for-byte gate
// for the wire protocol and runs independently.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestBil24CompatLayout188_AdapterDirExists asserts that the adapter
// package established in feature #188 has not been deleted, and contains
// at least one .go file so it is discoverable by go/packages.
func TestBil24CompatLayout188_AdapterDirExists(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "adapters", "bil24compat")

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("feature #188: apps/backend/internal/adapters/bil24compat must exist (got: %v)", err)
	}
	if !info.IsDir() {
		t.Fatalf("feature #188: %s exists but is not a directory", dir)
	}
	if !dirHasGoFile(t, dir) {
		t.Fatalf("feature #188: apps/backend/internal/adapters/bil24compat must contain at least one .go file")
	}
}

// bil24CompatRequiredSymbols lists the wire-format sentinels that must
// appear at least once in apps/backend/internal/adapters/bil24compat. The
// check is a textual "contains" scan against the package's .go files; it
// is intentionally coarse so simple renames are caught early.
var bil24CompatRequiredSymbols = []string{
	"ResultCodeOK",
	"ResultCodeUnknownCommand",
	"ResultCodeInvalidRequest",
	"ResultCodeNotFound",
	"ResultCodeInternalError",
	"TranslateLegacyID",
	"TranslatePlatformID",
	"ErrLegacyIDNotFound",
}

// TestBil24CompatLayout188_AdapterOwnsWireSymbols asserts that the Bil24
// wire-format sentinels are defined in the adapter package. We collect the
// .go file contents under internal/adapters/bil24compat and require each
// sentinel to appear at least once.
func TestBil24CompatLayout188_AdapterOwnsWireSymbols(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "adapters", "bil24compat")

	var combined strings.Builder
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
		if strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		contents, ferr := os.ReadFile(path)
		if ferr != nil {
			return ferr
		}
		combined.Write(contents)
		combined.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}

	body := combined.String()
	var missing []match
	for _, sym := range bil24CompatRequiredSymbols {
		if !strings.Contains(body, sym) {
			missing = append(missing, match{
				Path: "apps/backend/internal/adapters/bil24compat/",
				Line: 0,
				Text: "missing wire-format sentinel: " + sym,
			})
		}
	}
	reportHits(t,
		"Feature #188: the Bil24 wire-format sentinels above must remain "+
			"defined in apps/backend/internal/adapters/bil24compat. The HTTP "+
			"layer (internal/platform/httpserver) is allowed to re-export them "+
			"via aliases and forwarders, but the authoritative definition lives "+
			"in the adapter package.",
		missing,
	)
}

// forbiddenAdapterImportPatterns lists import paths that the Bil24 compat
// adapter must NOT depend on. The HTTP layer is allowed to depend on the
// adapter, but not the other way around — otherwise the boundary becomes
// cyclic and the adapter cannot be reused by non-HTTP delivery
// mechanisms (CLI replay, batch import, future protobuf gateway).
var forbiddenAdapterImportPatterns = []*regexp.Regexp{
	regexp.MustCompile(`"github\.com/abhteam/arena_new/apps/backend/internal/platform/httpserver`),
}

// TestBil24CompatLayout188_AdapterHasNoHTTPImports asserts that no .go
// file under apps/backend/internal/adapters/bil24compat/ imports the HTTP
// layer. The check inspects only the import block — comments mentioning
// the forbidden paths are not flagged.
func TestBil24CompatLayout188_AdapterHasNoHTTPImports(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "adapters", "bil24compat")

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
			for _, pat := range forbiddenAdapterImportPatterns {
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
		"Feature #188: the Bil24 compat adapter "+
			"apps/backend/internal/adapters/bil24compat must NOT import "+
			"internal/platform/httpserver. The dependency direction is "+
			"HTTP → adapter, never the other way.",
		violations,
	)
}
