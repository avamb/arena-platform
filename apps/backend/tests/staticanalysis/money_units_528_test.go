// money_units_528_test.go enforces spec
// 08_architecture/20_bil24_gateway_money_units_spec_ru.md §2.3 (feature #528,
// wave W1-M): the Bil24-compatible gateway converts between the wire's MAJOR
// currency units and the database's MINOR units in EXACTLY ONE package,
// apps/backend/internal/adapters/bil24compat/money.
//
// The wave that introduced the rule had to undo three independent open-coded
// conversions (hbil24 emitting minor units verbatim, bil24wire dividing by
// 100 locally, the import multiplying by 100 locally) that had silently
// diverged from each other. A literal "/ 100" or "* 100" anywhere in the
// gateway's own packages is how that divergence starts, so it is banned
// outright: use money.Major / money.Minor / money.RoundMinor, and name any
// genuinely non-money factor (the percentScale of a fee percentage) as a
// constant.
package staticanalysis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moneyGuardedDirs are the packages that sit on the Bil24 money boundary.
// Relative to the repo root; every .go file directly inside is scanned
// (these packages are flat).
var moneyGuardedDirs = []string{
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "hbil24"),
	filepath.Join("apps", "backend", "internal", "platform", "macs"),
	filepath.Join("apps", "backend", "internal", "platform", "bil24wire"),
}

// moneyBannedLiterals are the open-coded scale conversions. They are matched
// as plain substrings including the space, which is what gofmt produces for a
// binary operator; the un-spaced forms are unreachable in gofmt'ed code.
var moneyBannedLiterals = []string{"/ 100", "* 100"}

// TestMoneyUnits528_NoOpenCodedScaleConversion asserts that no file in the
// guarded packages performs its own minor↔major conversion.
func TestMoneyUnits528_NoOpenCodedScaleConversion(t *testing.T) {
	repo := repoRoot(t)

	var violations []string
	for _, rel := range moneyGuardedDirs {
		dir := filepath.Join(repo, rel)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("feature #528: guarded package %s must exist: %v", rel, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			b, err := os.ReadFile(path) // #nosec G304 — repo-local test scan
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for i, line := range strings.Split(string(b), "\n") {
				code := stripGoComment(line)
				for _, bad := range moneyBannedLiterals {
					if strings.Contains(code, bad) {
						violations = append(violations,
							filepath.Join(rel, e.Name())+":"+itoa(i+1)+" contains "+bad+
								" — "+strings.TrimSpace(line))
					}
				}
			}
		}
	}
	if len(violations) > 0 {
		t.Errorf("feature #528 (spec 20 §2.3): money scale conversions must go through "+
			"internal/adapters/bil24compat/money (Major/Minor/RoundMinor), never an "+
			"open-coded literal:\n  %s", strings.Join(violations, "\n  "))
	}
}

// TestMoneyUnits528_MoneyPackageExists asserts the single conversion point is
// present and still exports the three entry points the guarded packages use.
func TestMoneyUnits528_MoneyPackageExists(t *testing.T) {
	repo := repoRoot(t)
	dir := filepath.Join(repo, "apps", "backend", "internal", "adapters", "bil24compat", "money")
	src, err := os.ReadFile(filepath.Join(dir, "money.go")) // #nosec G304 — repo-local
	if err != nil {
		t.Fatalf("feature #528: %s/money.go must exist: %v", dir, err)
	}
	for _, sym := range []string{"func Major(", "func Minor(", "func ScaleFor(", "func RoundMinor("} {
		if !strings.Contains(string(src), sym) {
			t.Errorf("feature #528: bil24compat/money must export %q", strings.TrimSuffix(sym, "("))
		}
	}
}

// stripGoComment removes a trailing // comment so prose like "divide by / 100"
// in a doc comment does not count as a violation. It is deliberately naive: a
// "//" inside a string literal would truncate the line early, which can only
// ever produce a FALSE NEGATIVE on that one line, never a false alarm.
func stripGoComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		return line[:i]
	}
	return line
}
