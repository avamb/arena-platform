package config

import (
	"strings"
	"testing"
)

// testMetaKey is a valid secretbox key spec: 32 bytes of 0x01, std base64.
const testMetaKey = "v1:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="

var metaFull = map[string]string{
	"META_APP_ID":             "1130175216502536",
	"META_APP_SECRET":         "meta-secret-should-not-be-printed",
	"META_LOGIN_CONFIG_ID":    "987654321",
	"META_TOKEN_ENC_KEY":      testMetaKey,
	"META_OAUTH_REDIRECT_URL": "https://api.example.com/v1/meta/oauth/callback",
}

func TestMeta_DefaultsAreOff(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleAPI])
	cfg, err := LoadFor(RoleAPI)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetaEnabled() {
		t.Error("Meta must be off without META_APP_ID")
	}
	if cfg.MetaGraphVersion != DefaultMetaGraphVersion || DefaultMetaGraphVersion != "v25.0" {
		t.Errorf("MetaGraphVersion = %q, want v25.0", cfg.MetaGraphVersion)
	}
	if cfg.MetaGraphBaseURL != "" || cfg.MetaAssetEdges != "" {
		t.Errorf("seam and edges must default to empty: %q, %q", cfg.MetaGraphBaseURL, cfg.MetaAssetEdges)
	}
}

func TestMeta_ProductionStartsWithEveryValue(t *testing.T) {
	for _, role := range []Role{RoleAPI, RoleWorker} {
		t.Run(string(role), func(t *testing.T) {
			clearConfigEnv(t)
			setEnv(t, productionCommon)
			setEnv(t, roleMinimum[role])
			setEnv(t, metaFull)
			t.Setenv("META_GRAPH_VERSION", "v26.1")
			t.Setenv("META_ASSET_EDGES", "adspixels,owned_ad_accounts")
			cfg, err := LoadFor(role)
			if err != nil {
				t.Fatalf("%s with every Meta value: %v", role, err)
			}
			if !cfg.MetaEnabled() {
				t.Error("MetaEnabled must be true")
			}
			if cfg.MetaGraphVersion != "v26.1" || cfg.MetaAssetEdges != "adspixels,owned_ad_accounts" {
				t.Errorf("parsed = %q, %q", cfg.MetaGraphVersion, cfg.MetaAssetEdges)
			}
			if strings.Contains(strings.Join(cfg.Warnings(), "\n"), "META_") {
				t.Errorf("%s owns the Meta secrets, got: %v", role, cfg.Warnings())
			}
		})
	}
}

func TestMeta_ProductionErrorsNameEachMissingValue(t *testing.T) {
	for _, drop := range []string{"META_APP_SECRET", "META_LOGIN_CONFIG_ID", "META_TOKEN_ENC_KEY", "META_OAUTH_REDIRECT_URL"} {
		t.Run(drop, func(t *testing.T) {
			clearConfigEnv(t)
			setEnv(t, productionCommon)
			setEnv(t, roleMinimum[RoleAPI])
			for k, v := range metaFull {
				if k != drop {
					t.Setenv(k, v)
				}
			}
			_, err := LoadFor(RoleAPI)
			if err == nil || !strings.Contains(err.Error(), drop) {
				t.Fatalf("without %s the api must refuse to start naming it, got: %v", drop, err)
			}
			if strings.Contains(err.Error(), metaFull["META_APP_SECRET"]) {
				t.Errorf("the error prints the app secret: %v", err)
			}
		})
	}
}

func TestMeta_BadKeyErrorHidesTheKey(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleWorker])
	setEnv(t, metaFull)
	bad := "v1:c2hvcnQta2V5LW1hdGVyaWFs"
	t.Setenv("META_TOKEN_ENC_KEY", bad)
	_, err := LoadFor(RoleWorker)
	if err == nil || !strings.Contains(err.Error(), "META_TOKEN_ENC_KEY") {
		t.Fatalf("a short key must be refused, got: %v", err)
	}
	if strings.Contains(err.Error(), "c2hvcnQta2V5LW1hdGVyaWFs") {
		t.Errorf("the error prints key material: %v", err)
	}
}

func TestMeta_BotIgnoresMetaButWarnsAboutSecrets(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleBot])
	t.Setenv("META_APP_ID", "1")
	t.Setenv("META_APP_SECRET", "meta-secret-should-not-be-printed")
	cfg, err := LoadFor(RoleBot)
	if err != nil {
		t.Fatalf("the bot never reads Meta settings: %v", err)
	}
	joined := strings.Join(cfg.Warnings(), "\n")
	if !strings.Contains(joined, "META_APP_SECRET") {
		t.Errorf("the bot must be warned about META_APP_SECRET, got: %s", joined)
	}
	if strings.Contains(joined, "meta-secret-should-not-be-printed") {
		t.Errorf("warnings print a secret: %s", joined)
	}
}

func TestMetaEnabled_NeedsEveryValueAndAParsableKey(t *testing.T) {
	full := Config{MetaAppID: "1", MetaAppSecret: "s", MetaLoginConfigID: "c", MetaTokenEncKey: testMetaKey}
	if !full.MetaEnabled() {
		t.Fatal("full config must be enabled")
	}
	for name, mut := range map[string]func(*Config){
		"no app id":       func(c *Config) { c.MetaAppID = "" },
		"no secret":       func(c *Config) { c.MetaAppSecret = "" },
		"no login config": func(c *Config) { c.MetaLoginConfigID = "" },
		"no key":          func(c *Config) { c.MetaTokenEncKey = "" },
		"bad key":         func(c *Config) { c.MetaTokenEncKey = "v1:AAAA" },
	} {
		c := full
		mut(&c)
		if c.MetaEnabled() {
			t.Errorf("%s: MetaEnabled must be false", name)
		}
	}
	var nilCfg *Config
	if nilCfg.MetaEnabled() {
		t.Error("nil config must be disabled")
	}
}
