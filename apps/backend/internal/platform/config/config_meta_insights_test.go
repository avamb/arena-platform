package config

import (
	"strings"
	"testing"
	"time"
)

// META_INSIGHTS_EVERY (spec 66 section 5) is optional, a Go duration of at
// least 15 minutes, and checked on the worker only.
func TestMetaInsightsEvery(t *testing.T) {
	cases := []struct {
		name, value string
		role        Role
		want        time.Duration
		errPart     string
	}{
		{"unset", "", RoleWorker, 0, ""},
		{"30m", "30m", RoleWorker, 30 * time.Minute, ""},
		{"15m is the minimum", "15m", RoleWorker, 15 * time.Minute, ""},
		{"too short", "5m", RoleWorker, 0, "META_INSIGHTS_EVERY must be empty or at least 15m0s"},
		{"negative", "-1h", RoleWorker, 0, "META_INSIGHTS_EVERY must be empty or at least"},
		{"not a duration", "often", RoleWorker, 0, "META_INSIGHTS_EVERY must be a duration"},
		{"the api does not check it", "5m", RoleAPI, 5 * time.Minute, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearConfigEnv(t)
			setEnv(t, productionCommon)
			setEnv(t, roleMinimum[c.role])
			if c.value != "" {
				t.Setenv("META_INSIGHTS_EVERY", c.value)
			}
			cfg, err := LoadFor(c.role)
			if c.errPart != "" {
				if err == nil || !strings.Contains(err.Error(), c.errPart) {
					t.Fatalf("LoadFor(%s) error = %v, want %q", c.role, err, c.errPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadFor(%s): %v", c.role, err)
			}
			if cfg.MetaInsightsEvery != c.want {
				t.Errorf("MetaInsightsEvery = %s, want %s", cfg.MetaInsightsEvery, c.want)
			}
		})
	}
}
