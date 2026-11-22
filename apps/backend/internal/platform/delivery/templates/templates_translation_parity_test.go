package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Translation parity (spec 63, L-4): a letter in any language must carry the
// same {{...}} actions and the same HTML tags as the English letter of the
// same kind. A translator may move words around, never drop or invent a
// placeholder, a link or a block. Run: go test ./internal/platform/delivery/templates -run TranslationParity

// parityLocales are the languages added for the Lampyris site (spec 63, L-4).
// The older languages are held to the same rule where they already follow it.
var parityLocales = []string{"uk", "pl", "hu", "et", "lv", "lt", "fi"}

var (
	actionRe  = regexp.MustCompile(`\{\{-?\s*(.*?)\s*-?\}\}`)
	htmlTagRe = regexp.MustCompile(`</?([a-zA-Z][a-zA-Z0-9]*)`)
	htmlLang  = regexp.MustCompile(`<html lang="([^"]+)"`)
	// {{/* ... */}} comments hold prose in the file's own language.
	commentRe = regexp.MustCompile(`(?s)\{\{/\*.*?\*/\}\}`)
)

func parityTokens(src string) (actions, tags []string) {
	src = commentRe.ReplaceAllString(src, "")
	for _, m := range actionRe.FindAllStringSubmatch(src, -1) {
		actions = append(actions, strings.Join(strings.Fields(m[1]), " "))
	}
	for _, m := range htmlTagRe.FindAllStringSubmatch(src, -1) {
		tags = append(tags, strings.ToLower(m[1]))
	}
	sort.Strings(actions)
	sort.Strings(tags)
	return actions, tags
}

func diffMultiset(want, got []string) (missing, extra []string) {
	count := map[string]int{}
	for _, w := range want {
		count[w]++
	}
	for _, g := range got {
		count[g]--
	}
	for k, n := range count {
		for ; n > 0; n-- {
			missing = append(missing, k)
		}
		for ; n < 0; n++ {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func TestTranslationParityWithEnglish(t *testing.T) {
	files, err := filepath.Glob("*.en.tmpl")
	if err != nil || len(files) == 0 {
		t.Fatalf("no English templates found: %v", err)
	}
	partials, _ := filepath.Glob(filepath.Join("partials", "*.en.tmpl"))
	files = append(files, partials...)
	checked := 0
	for _, enPath := range files {
		enSrc, err := os.ReadFile(enPath)
		if err != nil {
			t.Fatal(err)
		}
		wantActions, wantTags := parityTokens(string(enSrc))
		for _, loc := range parityLocales {
			path := strings.TrimSuffix(enPath, ".en.tmpl") + "." + loc + ".tmpl"
			src, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue // a kind that is not translated falls back to English
			}
			if err != nil {
				t.Fatal(err)
			}
			checked++
			gotActions, gotTags := parityTokens(string(src))
			if miss, extra := diffMultiset(wantActions, gotActions); len(miss)+len(extra) > 0 {
				t.Errorf("%s: placeholders differ from English: missing %q extra %q", path, miss, extra)
			}
			if miss, extra := diffMultiset(wantTags, gotTags); len(miss)+len(extra) > 0 {
				t.Errorf("%s: HTML tags differ from English: missing %q extra %q", path, miss, extra)
			}
			if m := htmlLang.FindStringSubmatch(string(src)); m != nil && m[1] != loc {
				t.Errorf("%s: <html lang=%q>, want %q", path, m[1], loc)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no translated templates were checked")
	}
}

// The kinds a buyer of the Lampyris site can receive must exist in every new
// language: no silent English fallback for them.
var buyerKindsForSiteLocales = []string{
	TemplateKindTicket, TemplateKindInvitation, TemplateKindChange, TemplateKindCancel,
	TemplateKindRefund, TemplateKindMyTicketsLink, TemplateKindMarketingConfirm,
	TemplateKindCheckoutReminder, TemplateKindLatePayment,
	TemplateKindSaleSubscribeConfirm, TemplateKindSaleOpen,
}

func TestSiteLocalesHaveEveryBuyerKind(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	for _, loc := range parityLocales {
		for _, kind := range buyerKindsForSiteLocales {
			if got := r.ResolveLocale(kind, loc); got != loc {
				t.Errorf("%s.%s: resolves to %q, template file is missing", kind, loc, got)
			}
		}
		if _, err := os.Stat(filepath.Join("partials", "contact."+loc+".tmpl")); err != nil {
			t.Errorf("contact partial for %s: %v", loc, err)
		}
	}
}
