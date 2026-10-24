// cmd_config_role_test.go keeps every binary loading its configuration with
// an explicit role (config.LoadFor(config.RoleX)). Production validation
// checks only what that role reads, so each container can carry only its
// own secrets; config.Load (RoleAll) demands every service's secrets and
// would make the split impossible again.
//
// A new binary that loads the configuration must be added to cmdConfigRole
// with its role on purpose.
package staticanalysis

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// cmdConfigRole maps each binary under cmd/ that loads the configuration to
// the role it must declare. A binary that does not import the config package
// at all (arena-healthcheck, arena-bil24-import) needs no entry.
var cmdConfigRole = map[string]string{
	"arena-api":     "config.RoleAPI",
	"arena-worker":  "config.RoleWorker",
	"arena-bot":     "config.RoleBot",
	"arena-migrate": "config.RoleMigrate",
	"arena-seed":    "config.RoleMigrate",
	// One-shot database tool of the rich-text rollout (spec 44, RT-02).
	"arena-richtext-backfill": "config.RoleMigrate",
	// The web event center's live twin tests fixture (a one-shot database tool).
	"arena-e2e-seed": "config.RoleMigrate",
}

var (
	configImport = regexp.MustCompile(`"github\.com/abhteam/arena_new/apps/backend/internal/platform/config"`)
	configLoad   = regexp.MustCompile(`\bconfig\.Load\(`)
	configLoadFr = regexp.MustCompile(`\bconfig\.LoadFor\(\s*(config\.Role[A-Za-z]+)\s*\)`)
)

func TestCmdConfigRole_EveryBinaryDeclaresItsRole(t *testing.T) {
	cmdDir := filepath.Join(repoRoot(t), "apps", "backend", "cmd")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		t.Fatalf("read %s: %v", cmdDir, err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		files, _ := filepath.Glob(filepath.Join(cmdDir, name, "*.go"))
		var src strings.Builder
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			src.Write(b)
			src.WriteByte('\n')
		}
		code := src.String()
		if !configImport.MatchString(code) {
			continue
		}
		seen[name] = true

		if configLoad.MatchString(code) {
			t.Errorf("cmd/%s calls config.Load(): use config.LoadFor with the binary's own role", name)
		}
		want, listed := cmdConfigRole[name]
		if !listed {
			t.Errorf("cmd/%s loads the configuration but has no role in cmdConfigRole", name)
			continue
		}
		calls := configLoadFr.FindAllStringSubmatch(code, -1)
		if len(calls) == 0 {
			t.Errorf("cmd/%s must load its configuration with config.LoadFor(%s)", name, want)
		}
		for _, c := range calls {
			if c[1] != want {
				t.Errorf("cmd/%s loads with %s, want %s", name, c[1], want)
			}
			if c[1] == "config.RoleAll" {
				t.Errorf("cmd/%s must not use config.RoleAll: it demands every service's secrets", name)
			}
		}
	}
	for name := range cmdConfigRole {
		if !seen[name] {
			t.Errorf("cmdConfigRole lists %s, which does not load the configuration (renamed or removed?)", name)
		}
	}
}
