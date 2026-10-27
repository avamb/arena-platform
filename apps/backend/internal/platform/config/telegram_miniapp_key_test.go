package config

import (
	"strings"
	"testing"
)

func TestValidTelegramMiniappSecretKey(t *testing.T) {
	for _, tc := range []struct {
		name, v string
		want    bool
	}{
		{"64 hex lower", strings.Repeat("ab", 32), true},
		{"64 hex upper", strings.Repeat("AB", 32), true},
		{"empty", "", false},
		{"bot token", "123456:telegram-token", false},
		{"63 characters", strings.Repeat("a", 63), false},
		{"65 characters", strings.Repeat("a", 65), false},
		{"64 but not hex", strings.Repeat("z", 64), false},
	} {
		if got := ValidTelegramMiniappSecretKey(tc.v); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The key is an api secret only: the worker and the bot never read it, and an
// unset key is the normal state (the route answers 503).
func TestTelegramMiniappKey_ApiOnlyAndOptional(t *testing.T) {
	var found bool
	for _, s := range roleSecrets {
		if s.name == "TELEGRAM_MINIAPP_SECRET_KEY" {
			found = true
			if len(s.roles) != 1 || s.roles[0] != RoleAPI {
				t.Errorf("roles = %v, want only the api", s.roles)
			}
		}
	}
	if !found {
		t.Fatal("TELEGRAM_MINIAPP_SECRET_KEY is missing from roleSecrets")
	}
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleAPI])
	if _, err := LoadFor(RoleAPI); err != nil {
		t.Fatalf("an api without the key must start: %v", err)
	}
	t.Setenv("TELEGRAM_MINIAPP_SECRET_KEY", strings.Repeat("0f", 32))
	cfg, err := LoadFor(RoleAPI)
	if err != nil {
		t.Fatalf("an api with a derived key must start: %v", err)
	}
	if cfg.TelegramMiniappSecretKey != strings.Repeat("0f", 32) {
		t.Errorf("key not read: %q", cfg.TelegramMiniappSecretKey)
	}
}
