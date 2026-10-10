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
)

// providerLiteralGuardedDirs are scanned recursively, non-test .go files only.
var providerLiteralGuardedDirs = []string{
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "hcheckout"),
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "hfeed"),
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "hpayments"),
	filepath.Join("apps", "backend", "internal", "platform", "httpserver", "htickets"),
	filepath.Join("apps", "backend", "internal", "platform", "eventbot"),
}

// providerNameLiteral matches a string literal that is exactly a connected
// provider's name, in any letter case. Add a provider here when its module
// joins internal/app/payments/modules.go.
var providerNameLiteral = regexp.MustCompile("(?i)[\"`](stripe|flitt|allpay)[\"`]")

// providerLiteralAllowlist lists "<repo-relative path>:<trimmed line>" entries
// that may keep a provider literal, each with the reason. Empty since PAY-01:
// every former occurrence went through the registry. An entry must explain
// why the literal is not a provider switch.
var providerLiteralAllowlist = map[string]string{}

func TestPaymentProviderLiterals_MatcherSanity(t *testing.T) {
	for _, s := range []string{`if p == "stripe" {`, `case "Flitt":`, "x := `allpay`"} {
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
