// payment_provider_literals_test.go enforces spec
// 08_architecture/36_payment_modules_refunds_acquiring_ru.md §4.5 and §5
// (PAY-01): the code shared by every provider never switches on a provider's
// NAME.
//
// Before PAY-01 the choice of provider was hard-coded as `switch`/`if` on
// "stripe" / "flitt" / "allpay" in five places plus the provider catalogue;
// adding a provider meant finding all of them, and missing one meant a
// silent fall-through to Stripe's behaviour. Now the core asks the module
// registry (internal/app/payments/modules.go — the one file where provider
// names appear as a list) for the entry stored in a channel's or a config's
// `provider` value and acts on its Descriptor and on the optional interfaces
// the built module implements.
//
// "manual" (paid outside arena, the Bil24 gateway's sites) and "none" (a free
// order) are not modules but markers of "no provider" and stay allowed.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
)

// providerLiteralGuardedDirs are scanned recursively, non-test .go files only.
var providerLiteralGuardedDirs = []string{
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "hcheckout"),
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "hfeed"),
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "hpayments"),
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "htickets"),
	filepath.Join("apps", "backend", "internal", "platform", "eventbot"),
	// PAY-02: the channel CRUD and workspace provisioning held the last
	// hand-kept copies of the channel provider list.
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "hcatalog"),
	filepath.Join("apps", "backend", "internal", "platform", "provisioning"),
}

// providerNameLiteral matches a string literal that is exactly a registered
// provider's name, in any letter case. Since PAY-02 (review item L2) the
// alternation is DERIVED from the module registry, so a module joining
// internal/app/payments/modules.go is guarded the moment it is registered —
// no hand-kept regex to forget. "manual" is registered as a declared
// provider but is the "paid outside arena" marker, allowed in shared code.
var providerNameLiteral = providerLiteralMatcher(paymodules.Registry().Names())

// providerLiteralMarkers are registered names that are not provider switches
// but markers of "no provider" (spec 36 §5).
var providerLiteralMarkers = map[string]bool{"manual": true}

func providerLiteralMatcher(names []string) *regexp.Regexp {
	alts := make([]string, 0, len(names))
	for _, n := range names {
		if providerLiteralMarkers[n] {
			continue
		}
		alts = append(alts, regexp.QuoteMeta(n))
	}
	return regexp.MustCompile("(?i)[\"`](" + strings.Join(alts, "|") + ")[\"`]")
}

// providerLiteralAllowlist lists "<repo-relative path>:<trimmed line>" entries
// that may keep a provider literal, each with the reason. Empty since PAY-01:
// every former occurrence went through the registry. An entry must explain
// why the literal is not a provider switch.
var providerLiteralAllowlist = map[string]string{}

func TestPaymentProviderLiterals_MatcherSanity(t *testing.T) {
	for _, s := range []string{`if p == "stripe" {`, `case "Flitt":`, "x := `allpay`", `p == "yookassa"`} {
		if !providerNameLiteral.MatchString(s) {
			t.Errorf("matcher misses %q", s)
		}
	}
	for _, s := range []string{`"manual"`, `"none"`, `"Stripe-Signature"`, `"webhook.flitt_requires_config_route"`, `stripe.Name`} {
		if providerNameLiteral.MatchString(s) {
			t.Errorf("matcher wrongly flags %q", s)
		}
	}
}

// TestPaymentProviderLiterals_MatcherFollowsTheRegistry proves the L2
// derivation: every registered module name is matched, the "no provider"
// markers are not, and a provider registered tomorrow would be.
func TestPaymentProviderLiterals_MatcherFollowsTheRegistry(t *testing.T) {
	for _, n := range paymodules.Registry().Names() {
		got := providerNameLiteral.MatchString(`"` + n + `"`)
		if want := !providerLiteralMarkers[n]; got != want {
			t.Errorf("matcher on %q = %v; want %v", n, got, want)
		}
	}
	future := providerLiteralMatcher(append(paymodules.Registry().Names(), "newpay"))
	if !future.MatchString(`case "newpay":`) {
		t.Error("a newly registered provider name is not guarded")
	}
}

func TestPaymentProviderLiterals_NoProviderNamesInSharedCode(t *testing.T) {
	repo := repoRoot(t)
	var hits []match
	for _, rel := range providerLiteralGuardedDirs {
		root := filepath.Join(repo, rel)
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("PAY-01: guarded package %s must exist: %v", rel, err)
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
				return nil
			}
			b, err := os.ReadFile(path) // #nosec G304 — repo-local test scan
			if err != nil {
				return err
			}
			relPath, _ := filepath.Rel(repo, path)
			relPath = filepath.ToSlash(relPath)
			for i, line := range strings.Split(string(b), "\n") {
				code := stripGoComment(line)
				if !providerNameLiteral.MatchString(code) {
					continue
				}
				trimmed := strings.TrimSpace(line)
				if _, ok := providerLiteralAllowlist[relPath+":"+trimmed]; ok {
					continue
				}
				hits = append(hits, match{Path: relPath, Line: i + 1, Text: trimmed})
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", rel, err)
		}
	}
	reportHits(t,
		"PAY-01 (spec 36 §5): a provider-name literal in code shared by every provider. "+
			"Ask the registry (internal/app/payments.Registry()) for the entry named by the "+
			"channel's or config's provider value and act on its Descriptor / optional "+
			"interfaces; the provider list lives only in internal/app/payments/modules.go.",
		hits,
	)
}
