// channel_provider_mirror_test.go — PAY-02 (spec 36 §5). Migration 0132
// dropped sales_channels_provider_check; the channel handlers validate a
// provider against the module registry. One mirror of that list is left on
// purpose: the `enum` of CreateChannelRequest.provider and
// UpdateChannelRequest.provider in openapi.yaml, which documents the values
// for API clients. AGENTS.md: an enum-like mirror drifts silently, so this
// test pins it to the registry the way
// mediastore.TestAllowedOwnerTypes_MatchMigrationCheckConstraint pins its
// pair. A new module therefore needs a spec edit (and regenerated clients),
// never a migration.
package staticanalysis

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"

	paymodules "github.com/abhteam/arena_new/apps/backend/internal/app/payments"
)

func TestChannelProviderEnum_MatchesModuleRegistry(t *testing.T) {
	path := filepath.Join(repoRoot(t), "apps", "backend", "openapi", "openapi.yaml")
	raw, err := os.ReadFile(path) // #nosec G304 — repo-local spec
	if err != nil {
		t.Fatalf("read openapi.yaml: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse openapi.yaml: %v", err)
	}
	want := paymodules.ChannelProviders()
	sort.Strings(want)
	for _, schema := range []string{"CreateChannelRequest", "UpdateChannelRequest"} {
		s, ok := doc.Components.Schemas[schema]
		if !ok {
			t.Fatalf("openapi.yaml has no schema %s", schema)
		}
		got := append([]string(nil), s.Properties["provider"].Enum...)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s.provider enum = %v; the module registry's channel providers are %v — "+
				"update openapi.yaml (and regenerate the clients) when a module joins internal/app/payments/modules.go",
				schema, got, want)
		}
	}
	if def := paymodules.DefaultChannelProvider(); !paymodules.IsChannelProvider(def) {
		t.Errorf("DefaultChannelProvider %q is not a channel provider", def)
	}
}
