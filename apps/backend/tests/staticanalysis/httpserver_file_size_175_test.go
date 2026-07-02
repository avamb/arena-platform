// httpserver_file_size_175_test.go enforces feature #175 — "Split the
// remaining oversized files in internal/platform/httpserver (>600 lines)".
//
// # Contract
//
// Every non-test .go source file in internal/platform/httpserver must be
// ≤ httpserverFileSizeLimit lines (including comments and blank lines).
//
// The limit (400 lines) is the per-file budget called out in feature #175
// step 4 ("Целевой лимит файла: ≤ 400 строк (включая комментарии)").
//
// # Allowlist (migration backlog)
//
// Feature #175 is delivered as a sequence of incremental refactors (the same
// "step 1" pattern used by features #183 / #184 / #185 / #186 / #187, each
// of which established the canonical layout and a static gate without
// finishing the full migration in a single pass).
//
// The httpserverOversizedAllowlist enumerates handler files that are still
// awaiting a split. Each entry is a hard upper bound — the file may shrink,
// and once a file drops below httpserverFileSizeLimit it MUST be removed
// from the allowlist. The test fails loudly if:
//
//   - any non-allowlisted, non-test file exceeds httpserverFileSizeLimit, OR
//   - an allowlisted file exceeds its recorded bound (regression: the file
//     has grown), OR
//   - an allowlisted file dropped below httpserverFileSizeLimit (the entry
//     is now stale and must be deleted to keep the backlog honest).
//
// This shape gives the migration both a ratchet (no new oversized files,
// no growth in existing ones) and a forcing function (entries vanish as
// files are split).
package staticanalysis

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// httpserverFileSizeLimit is the per-file line budget mandated by
// feature #175 step 4.
const httpserverFileSizeLimit = 400

// httpserverOversizedAllowlist lists handler files in
// apps/backend/internal/platform/httpserver/ that exceed
// httpserverFileSizeLimit and have not yet been split. Each value is the
// upper bound (inclusive) on the file's current line count — the file may
// only shrink. When a file drops below httpserverFileSizeLimit, delete its
// entry from this map.
//
// Bounds captured 2026-06-25 when the gate was introduced. The
// reconciliation.go file (was 624 LOC) was split in the same change set
// into reconciliation.go, reconciliation_submit.go, reconciliation_query.go
// and reconciliation_review.go and is therefore NOT in this allowlist —
// it has already crossed the budget and may not regress.
var httpserverOversizedAllowlist = map[string]int{
	// Group A — files explicitly listed in the feature #175 description
	// (originally >600 LOC). These are the primary migration targets.
	// Entries for events.go, refunds.go, payment_intents.go,
	// ticket_tiers.go, complimentary.go, checkout.go, barcode_batches.go,
	// promo_codes.go, reservations.go, billing_ledger.go and geo.go were
	// removed when the httpserver refactoring (phases 1a–1k) moved those
	// files into domain sub-packages (hcatalog, hcheckout, hbarcode,
	// htickets, hbilling, hgeo); this gate only scans the top-level
	// package directory.
	"bil24_compat.go":         749,
	"sessions.go":             666,
	"external_allocations.go": 645,

	// Group B — files in the 400–600 LOC range that also exceed the
	// budget. Not in the feature #175 description by name, but the
	// per-file budget is hard so they are listed here as a documented
	// backlog. Each may only shrink; once a file drops to ≤ 400 LOC its
	// entry must be removed. Entries for auth_login.go, barcodes.go,
	// channels.go, orgs.go, scanner_snapshot.go, superadmin.go and
	// venues.go were removed after the phase 1a–1i sub-package moves.
	"feed_tokens.go":          414,
	"public_feed.go":          509,
	"public_feed_checkout.go": 410,
	"wp_webhooks.go":          589,
}

// TestHttpserverFileSize175 enforces the three-part contract documented at
// the top of this file.
func TestHttpserverFileSize175(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "platform", "httpserver")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	type violation struct {
		Path string
		Why  string
	}
	var violations []violation

	seen := make(map[string]bool, len(httpserverOversizedAllowlist))

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(dir, name)
		lines, lerr := countFileLines(path)
		if lerr != nil {
			t.Fatalf("count %s: %v", path, lerr)
		}

		bound, allowlisted := httpserverOversizedAllowlist[name]
		if allowlisted {
			seen[name] = true
			switch {
			case lines > bound:
				violations = append(violations, violation{
					Path: "internal/platform/httpserver/" + name,
					Why: sprintfInt(
						"file is on the feature #175 allowlist with bound %d "+
							"but now has %d lines (file grew — split it further "+
							"or update the bound to the new lower value)",
						bound, lines,
					),
				})
			case lines <= httpserverFileSizeLimit:
				violations = append(violations, violation{
					Path: "internal/platform/httpserver/" + name,
					Why: sprintfInt(
						"file is on the feature #175 allowlist but now has %d "+
							"lines, which is ≤ the budget of %d. Delete its "+
							"entry from httpserverOversizedAllowlist so the "+
							"backlog stays honest.",
						lines, httpserverFileSizeLimit,
					),
				})
			}
			continue
		}

		if lines > httpserverFileSizeLimit {
			violations = append(violations, violation{
				Path: "internal/platform/httpserver/" + name,
				Why: sprintfInt(
					"file has %d lines, which exceeds the feature #175 "+
						"per-file budget of %d. Split it into handler-, "+
						"mapper-, and validation-scoped files (see #175 "+
						"steps 1–3) or, if you are deliberately deferring "+
						"the split, add an explicit entry to "+
						"httpserverOversizedAllowlist with a justification "+
						"in claude-progress.txt.",
					lines, httpserverFileSizeLimit,
				),
			})
		}
	}

	// Stale allowlist entries: file listed but no longer exists.
	for name := range httpserverOversizedAllowlist {
		if seen[name] {
			continue
		}
		violations = append(violations, violation{
			Path: "internal/platform/httpserver/" + name,
			Why: "file is on the feature #175 allowlist but does not exist " +
				"on disk anymore. Delete its entry from " +
				"httpserverOversizedAllowlist.",
		})
	}

	if len(violations) == 0 {
		return
	}

	sort.Slice(violations, func(i, j int) bool {
		return violations[i].Path < violations[j].Path
	})

	var b strings.Builder
	b.WriteString("feature #175: internal/platform/httpserver/ file-size " +
		"gate failed. Each non-test source file must be ≤ ")
	b.WriteString(itoa(httpserverFileSizeLimit))
	b.WriteString(" lines, or be explicitly listed (with a hard upper bound) " +
		"in httpserverOversizedAllowlist as a documented migration backlog " +
		"item.\n\nViolations:\n")
	for _, v := range violations {
		b.WriteString("  - ")
		b.WriteString(v.Path)
		b.WriteString(": ")
		b.WriteString(v.Why)
		b.WriteString("\n")
	}
	t.Fatal(b.String())
}

// countFileLines returns the number of newlines in path. This matches the
// classical `wc -l` semantics that produced the bounds captured in
// httpserverOversizedAllowlist. Go source files conventionally end with a
// trailing newline (enforced by gofmt), so this is also the natural
// "number of lines" intuition for any well-formed file in this tree.
func countFileLines(path string) (int, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return bytes.Count(contents, []byte{'\n'}), nil
}

// sprintfInt builds a formatted string using only the standard library
// primitives already available in this package (itoa). It keeps the test
// file dependency-light and avoids pulling in fmt across the package for
// a single use.
func sprintfInt(format string, args ...int) string {
	var b strings.Builder
	i := 0
	for k := 0; k < len(format); k++ {
		c := format[k]
		if c == '%' && k+1 < len(format) && format[k+1] == 'd' && i < len(args) {
			b.WriteString(itoa(args[i]))
			i++
			k++
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
