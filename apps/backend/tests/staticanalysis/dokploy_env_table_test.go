// dokploy_env_table_test.go keeps deploy/DOKPLOY.md §2.6 ("Which service
// gets which variable") complete: every environment variable the config
// package reads must be named in that table, so the per-container
// environment of the production compose can be built from it alone.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var configGetenvKey = regexp.MustCompile(`getenv[A-Za-z0-9]*\("([A-Z0-9_]+)"`)

func TestDokployEnvTable_NamesEveryConfigVariable(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "apps", "backend", "internal", "platform", "config", "config.go"))
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	doc, err := os.ReadFile(filepath.Join(root, "deploy", "DOKPLOY.md"))
	if err != nil {
		t.Fatalf("read DOKPLOY.md: %v", err)
	}
	text := string(doc)
	start := strings.Index(text, "### 2.6 Which service gets which variable")
	if start < 0 {
		t.Fatal("deploy/DOKPLOY.md has no section 2.6 'Which service gets which variable'")
	}
	section := text[start:]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}

	keys := map[string]bool{}
	for _, m := range configGetenvKey.FindAllStringSubmatch(string(src), -1) {
		keys[m[1]] = true
	}
	if len(keys) < 50 {
		t.Fatalf("found only %d variables in config.go; the pattern is out of date", len(keys))
	}
	var missing []string
	for k := range keys {
		if !strings.Contains(section, "`"+k+"`") {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("deploy/DOKPLOY.md §2.6 does not name these variables config.go reads: %s",
			strings.Join(missing, ", "))
	}
}
