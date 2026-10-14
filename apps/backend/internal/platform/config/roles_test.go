package config

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// clearConfigEnv unsets every variable config.go reads, so a test sees only
// what it sets itself (a CI runner carries DATABASE_URL and friends).
func clearConfigEnv(t *testing.T) {
	t.Helper()
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	keys := regexp.MustCompile(`getenv[A-Za-z0-9]*\("([A-Z0-9_]+)"`).FindAllSubmatch(src, -1)
	if len(keys) < 50 {
		t.Fatalf("found only %d getenv keys in config.go; the pattern is out of date", len(keys))
	}
	for _, k := range keys {
		name := string(k[1])
		t.Setenv(name, "") // records the old value for Cleanup
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("unsetenv %s: %v", name, err)
		}
	}
}

const strongSecret = "a-very-strong-secret-for-production-use-32b"

// productionCommon is what every role needs in production.
var productionCommon = map[string]string{
	"APP_ENV":      "production",
	"DATABASE_URL": "postgres://arena:strongpw@db.example.com:5432/arena?sslmode=require",
}

// roleMinimum is, per role, the smallest set of its OWN variables that lets
// it start in production. Each one is required: dropping any fails.
var roleMinimum = map[Role]map[string]string{
	RoleAPI: {
		"JWT_SIGNING_SECRET":    strongSecret,
		"CORS_ALLOWED_ORIGINS":  "https://app.example.com",
		"STRIPE_WEBHOOK_SECRET": "whsec_production_test_secret",
		"APP_PUBLIC_URL":        "https://app.example.com",
	},
	RoleWorker: {
		"EMAIL_MODE":     "smtp",
		"SMTP_HOST":      "smtp-relay.brevo.com",
		"SMTP_FROM":      "tickets@example.com",
		"APP_PUBLIC_URL": "https://app.example.com",
		"OUTBOX_MODE":    "disabled",
	},
	RoleBot: {
		"JWT_SIGNING_SECRET":        strongSecret,
		"EVENTS_TELEGRAM_BOT_TOKEN": "123456:telegram-token",
	},
	RoleMigrate: {},
}

func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestLoadFor_EachRoleStartsWithItsOwnMinimum(t *testing.T) {
	for role, own := range roleMinimum {
		t.Run(string(role), func(t *testing.T) {
			clearConfigEnv(t)
			setEnv(t, productionCommon)
			setEnv(t, own)
			cfg, err := LoadFor(role)
			if err != nil {
				t.Fatalf("LoadFor(%s) with only its own variables: %v", role, err)
			}
			if cfg.Role != role {
				t.Errorf("cfg.Role = %q, want %q", cfg.Role, role)
			}
			if cfg.AppName != role.Binary() {
				t.Errorf("APP_NAME default = %q, want %q", cfg.AppName, role.Binary())
			}
		})
	}
}

func TestLoadFor_EachRoleFailsWithoutEachOfItsVariables(t *testing.T) {
	for role, own := range roleMinimum {
		all := map[string]string{}
		for k, v := range productionCommon {
			all[k] = v
		}
		for k, v := range own {
			all[k] = v
		}
		for _, drop := range sortedKeys(all) {
			if drop == "APP_ENV" {
				continue // without it the profile is development
			}
			t.Run(string(role)+"/without_"+drop, func(t *testing.T) {
				clearConfigEnv(t)
				for k, v := range all {
					if k != drop {
						t.Setenv(k, v)
					}
				}
				_, err := LoadFor(role)
				if err == nil {
					t.Fatalf("LoadFor(%s) without %s must fail in production", role, drop)
				}
				if !strings.Contains(err.Error(), drop) {
					t.Errorf("the error must name %s, got: %v", drop, err)
				}
			})
		}
	}
}

// The rules every binary shares stay on for every role.
func TestLoadFor_SharedProductionRulesApplyToEveryRole(t *testing.T) {
	shared := []struct{ name, key, value string }{
		{"text logs", "LOG_FORMAT", "text"},
		{"plaintext db", "DATABASE_URL", "postgres://arena:pw@db.example.com:5432/arena?sslmode=disable"},
		{"query logging", "DB_LOG_QUERIES", "true"},
		{"debug routes", "DEBUG_ROUTES_ENABLED", "true"},
		{"fault injection", "FAULT_INJECT_OUTBOX_AFTER_AUDIT", "true"},
		{"dev auth", "ENABLE_DEV_AUTH", "true"},
		{"stripe seam", "STRIPE_API_BASE_URL", "http://stub:12111/v1"},
		{"flitt seam", "FLITT_API_BASE_URL", "http://stub:12112"},
	}
	for role, own := range roleMinimum {
		for _, tc := range shared {
			t.Run(string(role)+"/"+tc.name, func(t *testing.T) {
				clearConfigEnv(t)
				setEnv(t, productionCommon)
				setEnv(t, own)
				t.Setenv(tc.key, tc.value)
				if _, err := LoadFor(role); err == nil {
					t.Fatalf("LoadFor(%s) with %s=%s must fail in production", role, tc.key, tc.value)
				}
			})
		}
	}
}

// A secret of another service is not an error, only a warning that names
// it — never its value.
func TestLoadFor_ForeignSecretIsOnlyAWarning(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleBot])
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_should_not_be_printed")
	t.Setenv("SMTP_PASSWORD", "smtp-password-should-not-be-printed")
	cfg, err := LoadFor(RoleBot)
	if err != nil {
		t.Fatalf("a foreign secret must not stop the bot: %v", err)
	}
	joined := strings.Join(cfg.Warnings(), "\n")
	for _, name := range []string{"STRIPE_WEBHOOK_SECRET", "SMTP_PASSWORD", "arena-bot"} {
		if !strings.Contains(joined, name) {
			t.Errorf("warnings must name %s, got: %s", name, joined)
		}
	}
	for _, value := range []string{"whsec_should_not_be_printed", "smtp-password-should-not-be-printed"} {
		if strings.Contains(joined, value) {
			t.Errorf("warnings must not print a secret value, got: %s", joined)
		}
	}
	if strings.Contains(joined, "JWT_SIGNING_SECRET") || strings.Contains(joined, "EVENTS_TELEGRAM_BOT_TOKEN") {
		t.Errorf("the bot's own secrets are not foreign, got: %s", joined)
	}
}

func TestLoadFor_MigrateNeedsNoSecretEvenWithDevAuthDefault(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DATABASE_URL", "postgres://arena:arena@localhost:5432/arena?sslmode=disable")
	if _, err := LoadFor(RoleMigrate); err != nil {
		t.Fatalf("migrate in development must not need JWT_SIGNING_SECRET: %v", err)
	}
}

// Load (RoleAll) still demands the union, so nothing that used it got weaker.
func TestLoad_RoleAllStillDemandsEveryService(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleAPI])
	_, err := Load()
	if err == nil {
		t.Fatal("Load() with only the api's variables must fail: the worker's are missing")
	}
	for _, want := range []string{"EMAIL_MODE", "OUTBOX_MODE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Load() error must name %s, got: %v", want, err)
		}
	}
}

func TestLoadFor_UnknownRoleIsRefused(t *testing.T) {
	if _, err := LoadFor(Role("cron")); err == nil {
		t.Fatal("an unknown role must be refused")
	}
}

// The worker signs the media links the api verifies and no longer holds the
// JWT secret, so production never falls back to it and every backend needs
// MEDIA_SIGNING_SECRET.
func TestMediaSigning_ProductionHasNoJWTFallback(t *testing.T) {
	cfg := &Config{AppEnv: EnvProduction, JWTSecretStub: "jwt-secret"}
	if got := string(cfg.MediaSigningKey()); got != "" {
		t.Errorf("production MediaSigningKey must not fall back to the JWT secret, got %q", got)
	}
	for _, role := range []Role{RoleAPI, RoleWorker} {
		t.Run(string(role), func(t *testing.T) {
			clearConfigEnv(t)
			setEnv(t, productionCommon)
			setEnv(t, roleMinimum[role])
			t.Setenv("MEDIA_BACKEND", "s3")
			t.Setenv("MEDIA_S3_ENDPOINT", "https://s3.example.com")
			t.Setenv("MEDIA_S3_REGION", "auto")
			t.Setenv("MEDIA_S3_BUCKET", "media")
			t.Setenv("MEDIA_S3_ACCESS_KEY_ID", "key")
			t.Setenv("MEDIA_S3_SECRET_ACCESS_KEY", "secret")
			_, err := LoadFor(role)
			if err == nil || !strings.Contains(err.Error(), "MEDIA_SIGNING_SECRET") {
				t.Fatalf("MEDIA_BACKEND=s3 without MEDIA_SIGNING_SECRET must fail for %s, got: %v", role, err)
			}
			t.Setenv("MEDIA_SIGNING_SECRET", strongSecret)
			if _, err := LoadFor(role); err != nil {
				t.Fatalf("with MEDIA_SIGNING_SECRET %s must start: %v", role, err)
			}
		})
	}
}

// Every secret the role table names exists as a variable config.go reads.
func TestRoleSecrets_NameRealVariables(t *testing.T) {
	src, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range roleSecrets {
		if !strings.Contains(string(src), `"`+s.name+`"`) {
			t.Errorf("roleSecrets names %s, which config.go never reads", s.name)
		}
		if len(s.roles) == 0 {
			t.Errorf("%s has no role", s.name)
		}
	}
}
