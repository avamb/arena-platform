// csvexport_guard_test.go keeps the rule of
// 08_architecture/35_telegram_event_center_full_ru.md §5.1 true: every CSV an
// organizer downloads is written by internal/platform/csvexport, the one
// writer that knows how a spreadsheet mangles a barcode, a phone or a
// formula-looking name. A handler under httpserver that imports encoding/csv
// directly bypasses those rules, so the import is refused there.
//
// A reader (parsing an uploaded CSV) is a different concern and is allowed
// on the allowlist with its reason; a WRITER on the allowlist is a legacy
// contract that predates csvexport and is documented as such.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// encodingCSVImport matches an import of the standard library CSV package.
var encodingCSVImport = regexp.MustCompile(`"encoding/csv"`)

// csvImportAllowlist are the httpserver files that may import encoding/csv,
// each with why. Adding a file here is a decision, not a convenience.
var csvImportAllowlist = map[string]string{
	// Parses an UPLOADED barcode batch (a reader, not a writer): no
	// spreadsheet presentation rule applies to bytes arena receives.
	filepath.Join("hbarcode", "barcode_batches.go"): "reads an uploaded CSV",
	// The organization's promo usage report predates csvexport
	// (2026-09-22) and its comma-separated, snake_case-header shape is the
	// documented contract of `GET .../promo-code-redemptions?format=csv`
	// (openapi.yaml, TestPromoRedemptionsCSV). The event-center file for
	// the same data is `GET .../promo-codes/{id}/redemptions.csv` through
	// csvexport; this legacy writer stays until its consumers move.
	filepath.Join("hcheckout", "promo_report.go"): "legacy promo report contract",
}

func TestCSVExportGuard_HttpserverWritesCSVOnlyThroughCsvexport(t *testing.T) {
	root := filepath.Join(repoRoot(t), "apps", "backend", "internal", "platform", "httpserver")

	var violations []string
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, rerr := os.ReadFile(path) // #nosec G304 — repo-local test scan
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		if !encodingCSVImport.MatchString(stripBlockAndLineComments(string(src))) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		seen[rel] = true
		if _, ok := csvImportAllowlist[rel]; !ok {
			violations = append(violations, rel+" imports encoding/csv: write CSV through internal/platform/csvexport")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	for rel := range csvImportAllowlist {
		if !seen[rel] {
			violations = append(violations, rel+" is on the csv allowlist but no longer imports encoding/csv: remove it")
		}
	}
	if len(violations) > 0 {
		t.Errorf("CSV under httpserver goes through csvexport (spec 35 §5.1):\n  %s", strings.Join(violations, "\n  "))
	}
}

// TestCSVExportGuard_CsvexportIsTheOnlyPlatformWriter is the other half: no
// package under internal/platform besides csvexport (and the allowlisted
// httpserver files above) imports encoding/csv to WRITE a file. Readers
// outside httpserver (customer imports) are listed with their reason.
func TestCSVExportGuard_CsvexportIsTheOnlyPlatformWriter(t *testing.T) {
	backend := filepath.Join(repoRoot(t), "apps", "backend")
	platform := filepath.Join(backend, "internal", "platform")
	allowed := map[string]string{
		filepath.Join("customerimport", "parsers.go"): "reads an uploaded customer CSV",
	}
	for rel := range csvImportAllowlist {
		allowed[filepath.Join("httpserver", rel)] = "see csvImportAllowlist"
	}

	var violations []string
	_ = filepath.WalkDir(platform, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(platform, path)
		if strings.HasPrefix(rel, "csvexport"+string(filepath.Separator)) {
			return nil
		}
		src, rerr := os.ReadFile(path) // #nosec G304 — repo-local test scan
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		if !encodingCSVImport.MatchString(stripBlockAndLineComments(string(src))) {
			return nil
		}
		if _, ok := allowed[rel]; !ok {
			violations = append(violations, rel+" imports encoding/csv outside csvexport")
		}
		return nil
	})
	if len(violations) > 0 {
		t.Errorf("internal/platform writes CSV only through csvexport:\n  %s", strings.Join(violations, "\n  "))
	}
}
