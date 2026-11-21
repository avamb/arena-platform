package delivery

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// brandRE finds the platform's brand written the old way. In a letter the
// brand is ArenaSoldOut.com and nothing else (owner decision 2026-11-12): not
// "Arena", not "Arena Platform", not "Arena Sold Out". A lower-case "arena" in
// a domain or a code name is a different thing and does not match.
var brandRE = regexp.MustCompile(`\bArena\b`)

// emailSourceDirs are the folders whose non-test Go files build the text of a
// letter, relative to this package. Every file in them is scanned, so a new
// sender added there is covered without a change to this list. A new folder
// that builds letters must be added here.
var emailSourceDirs = []string{
	".",
	"templates",
	"../authemail",
	"../reportdelivery",
}

// emailI18nKeys are the i18n keys that only letters use. A key shared with
// the bot or the web screens does not belong here.
var emailI18nKeys = regexp.MustCompile(`^\["contact\.email\.[a-z_]+"\]`)

func scanBrand(t *testing.T, name, body string) {
	t.Helper()
	for i, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if brandRE.MatchString(line) {
			t.Errorf("%s:%d writes the brand as \"Arena\": %s", name, i+1, strings.TrimSpace(line))
		}
	}
}

// TestLetterTextsNameTheBrandArenaSoldOutCom pins the owner's rule: every
// template and every Go text of a letter says ArenaSoldOut.com. Hebrew letters
// keep the brand in Latin letters, so they are scanned the same way.
func TestLetterTextsNameTheBrandArenaSoldOutCom(t *testing.T) {
	scanned := 0
	for _, dir := range emailSourceDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				if name == "partials" && dir == "templates" {
					sub, err := os.ReadDir(filepath.Join(dir, name))
					if err != nil {
						t.Fatal(err)
					}
					for _, s := range sub {
						raw, err := os.ReadFile(filepath.Join(dir, name, s.Name()))
						if err != nil {
							t.Fatal(err)
						}
						scanBrand(t, filepath.Join(dir, name, s.Name()), string(raw))
						scanned++
					}
				}
				continue
			}
			isGo := strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
			if !isGo && !strings.HasSuffix(name, ".tmpl") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			body := string(raw)
			if strings.HasSuffix(name, ".tmpl") {
				// A template has no // comments: scan every line.
				body = strings.ReplaceAll(body, "\n", "\n ")
				for i, line := range strings.Split(body, "\n") {
					if brandRE.MatchString(line) {
						t.Errorf("%s:%d writes the brand as \"Arena\": %s", filepath.Join(dir, name), i+1, strings.TrimSpace(line))
					}
				}
			} else {
				scanBrand(t, filepath.Join(dir, name), body)
			}
			scanned++
		}
	}
	if scanned < 100 {
		t.Fatalf("the guard scanned only %d files, the folders moved?", scanned)
	}
}

// TestLetterI18nKeysNameTheBrandArenaSoldOutCom does the same for the i18n
// keys that only letters use (contact.email.*), in every language.
func TestLetterI18nKeysNameTheBrandArenaSoldOutCom(t *testing.T) {
	files, err := filepath.Glob("../i18n/locales/*.toml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no locale files found: %v", err)
	}
	keys := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		inKey := false
		for i, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.HasPrefix(line, "[") {
				inKey = emailI18nKeys.MatchString(line)
				if inKey {
					keys++
				}
				continue
			}
			if inKey && (brandRE.MatchString(line) || strings.Contains(line, "Arena Sold Out")) {
				t.Errorf("%s:%d writes the brand wrongly in a letter text: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
	if keys < 5 {
		t.Fatalf("the guard found only %d contact.email keys", keys)
	}
}
