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
		{"allpay seam", "ALLPAY_API_BASE_URL", "http://stub:12113"},
		{"brevo host", "BREVO_API_BASE_URL", "https://brevo.attacker.example"},
		{"llm host", "POSTER_LLM_BASE_URL", "https://llm.attacker.example"},
		{"event center link", "EVENT_CENTER_PUBLIC_URL", "http://ec.example.com"},
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

// The default values of the two API hosts are accepted, with or without a
// trailing slash; only another host is refused.
func TestLoadFor_DefaultAPIHostsAreAccepted(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleBot])
	t.Setenv("BREVO_API_BASE_URL", DefaultBrevoAPIBaseURL+"/")
	t.Setenv("POSTER_LLM_BASE_URL", DefaultPosterLLMBaseURL)
	if _, err := LoadFor(RoleBot); err != nil {
		t.Fatalf("the default API hosts must be accepted: %v", err)
	}
}

// Each conditional rule (one that only fires on a bad value) is checked for
// exactly the roles that read the setting: removing it from one of those
// roles, or extending it to a role that never reads the setting, fails here.
func TestLoadFor_ConditionalRulesFollowTheRoles(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		refused []Role
	}{
		{"bil24 gateway without token enforcement",
			map[string]string{"BIL24_COMPAT_ENABLED": "true", "BIL24_REQUIRE_TOKEN": "false"},
			[]Role{RoleAPI}},
		{"bil24 gateway with a plaintext public url",
			map[string]string{"BIL24_COMPAT_ENABLED": "true", "API_PUBLIC_URL": "http://api.example.com"},
			[]Role{RoleAPI, RoleWorker}},
		{"short brevo webhook token",
			map[string]string{"BREVO_WEBHOOK_TOKEN": "short"},
			[]Role{RoleAPI}},
		{"placeholder brevo webhook token",
			map[string]string{"BREVO_WEBHOOK_TOKEN": "changeme"},
			[]Role{RoleAPI}},
		{"short previous brevo webhook token",
			map[string]string{"BREVO_WEBHOOK_TOKEN_PREVIOUS": "short"},
			[]Role{RoleAPI}},
		{"wildcard cors",
			map[string]string{"CORS_ALLOWED_ORIGINS": "*"},
			[]Role{RoleAPI}},
		{"short jwt secret",
			map[string]string{"JWT_SIGNING_SECRET": "too-short"},
			[]Role{RoleAPI, RoleBot}},
		{"placeholder jwt secret",
			map[string]string{"JWT_SIGNING_SECRET": "dev-only-do-not-use-in-prod"},
			[]Role{RoleAPI, RoleBot}},
		{"plaintext app public url",
			map[string]string{"APP_PUBLIC_URL": "http://app.example.com"},
			[]Role{RoleAPI, RoleWorker}},
		{"plaintext api public url",
			map[string]string{"API_PUBLIC_URL": "http://api.example.com"},
			[]Role{RoleAPI, RoleWorker}},
		{"log-only email",
			map[string]string{"EMAIL_MODE": "log"},
			[]Role{RoleWorker}},
		{"smtp without a from address",
			map[string]string{"EMAIL_MODE": "smtp", "SMTP_HOST": "smtp.example.com", "SMTP_FROM": ""},
			[]Role{RoleWorker}},
		{"noop outbox",
			map[string]string{"OUTBOX_MODE": "noop"},
			[]Role{RoleWorker}},
		{"webhook outbox without url and secret",
			map[string]string{"OUTBOX_MODE": "webhook"},
			[]Role{RoleWorker}},
		{"local media without a signing secret",
			map[string]string{"MEDIA_BACKEND": "local", "MEDIA_LOCAL_ROOT": "/data/media"},
			[]Role{RoleAPI, RoleWorker}},
		{"local media with a short signing secret",
			map[string]string{"MEDIA_BACKEND": "local", "MEDIA_LOCAL_ROOT": "/data/media", "MEDIA_SIGNING_SECRET": "short"},
			[]Role{RoleAPI, RoleWorker}},
		{"unknown media backend",
			map[string]string{"MEDIA_BACKEND": "ftp"},
			[]Role{RoleAPI, RoleWorker}},
	}
	for _, tc := range cases {
		for role, own := range roleMinimum {
			t.Run(tc.name+"/"+string(role), func(t *testing.T) {
				clearConfigEnv(t)
				setEnv(t, productionCommon)
				setEnv(t, own)
				for k, v := range tc.env {
					if v == "" {
						if err := os.Unsetenv(k); err != nil {
							t.Fatal(err)
						}
						continue
					}
					t.Setenv(k, v)
				}
				_, err := LoadFor(role)
				if roleListed(tc.refused, role) && err == nil {
					t.Fatalf("%s must refuse to start with %v", role.Binary(), tc.env)
				}
				if !roleListed(tc.refused, role) && err != nil {
					t.Fatalf("%s never reads these settings and must start: %v", role.Binary(), err)
				}
			})
		}
	}
}

// Staging separates secrets too: without MEDIA_SIGNING_SECRET the worker
// would sign with nothing the api accepts, so staging demands it as well and
// never falls back to the JWT secret.
func TestLoadFor_StagingNeedsMediaSigningSecret(t *testing.T) {
	cfg := &Config{AppEnv: EnvStaging, JWTSecretStub: "jwt-secret"}
	if got := string(cfg.MediaSigningKey()); got != "" {
		t.Errorf("staging MediaSigningKey must not fall back to the JWT secret, got %q", got)
	}
	for _, role := range []Role{RoleAPI, RoleWorker} {
		t.Run(string(role), func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("APP_ENV", "staging")
			t.Setenv("DATABASE_URL", productionCommon["DATABASE_URL"])
			t.Setenv("JWT_SIGNING_SECRET", strongSecret)
			t.Setenv("MEDIA_BACKEND", "local")
			t.Setenv("MEDIA_LOCAL_ROOT", "/data/media")
			_, err := LoadFor(role)
			if err == nil || !strings.Contains(err.Error(), "MEDIA_SIGNING_SECRET") {
				t.Fatalf("staging %s without MEDIA_SIGNING_SECRET must fail, got: %v", role, err)
			}
			t.Setenv("MEDIA_SIGNING_SECRET", "staging-media-secret")
			if _, err := LoadFor(role); err != nil {
				t.Fatalf("staging %s with MEDIA_SIGNING_SECRET must start: %v", role, err)
			}
		})
	}
	t.Run("development keeps the fallback", func(t *testing.T) {
		clearConfigEnv(t)
		t.Setenv("APP_ENV", "development")
		t.Setenv("DATABASE_URL", productionCommon["DATABASE_URL"])
		t.Setenv("JWT_SIGNING_SECRET", "dev-secret")
		t.Setenv("MEDIA_BACKEND", "local")
		t.Setenv("MEDIA_LOCAL_ROOT", "/data/media")
		cfg, err := LoadFor(RoleWorker)
		if err != nil {
			t.Fatalf("development must keep working without MEDIA_SIGNING_SECRET: %v", err)
		}
		if string(cfg.MediaSigningKey()) != "dev-secret" {
			t.Errorf("development must fall back to the JWT secret")
		}
	})
}

// A container without APP_ENV runs as development with every production
// check off; the binary says so loudly.
func TestLoadFor_MissingAppEnvIsALoudWarning(t *testing.T) {
	for role := range roleMinimum {
		t.Run(string(role), func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("DATABASE_URL", productionCommon["DATABASE_URL"])
			t.Setenv("JWT_SIGNING_SECRET", strongSecret)
			cfg, err := LoadFor(role)
			if err != nil {
				t.Fatalf("LoadFor(%s): %v", role, err)
			}
			if !strings.Contains(strings.Join(cfg.Warnings(), " | "), "APP_ENV is not set") {
				t.Errorf("%s without APP_ENV must warn, got %v", role, cfg.Warnings())
			}
			t.Setenv("APP_ENV", "development")
			cfg, err = LoadFor(role)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(cfg.Warnings(), " | "), "APP_ENV") {
				t.Errorf("an explicit APP_ENV must not warn, got %v", cfg.Warnings())
			}
		})
	}
}

// The poster-reading key lives on arena-api since 2026-10-14 (the bot calls
// POST .../media/{id}/poster-facts): the bot holding it is a foreign secret
// and the api holding it is not.
func TestLoadFor_PosterLLMKeyBelongsToTheAPI(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleBot])
	t.Setenv("POSTER_LLM_API_KEY", "sk-ant-should-not-be-printed")
	cfg, err := LoadFor(RoleBot)
	if err != nil {
		t.Fatalf("a poster key must not stop the bot: %v", err)
	}
	joined := strings.Join(cfg.Warnings(), "\n")
	if !strings.Contains(joined, "POSTER_LLM_API_KEY") {
		t.Errorf("the bot must be warned that POSTER_LLM_API_KEY is not its own, got: %s", joined)
	}
	if strings.Contains(joined, "sk-ant-should-not-be-printed") {
		t.Errorf("warnings must not print a secret value, got: %s", joined)
	}

	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleAPI])
	t.Setenv("POSTER_LLM_API_KEY", "sk-ant-should-not-be-printed")
	cfg, err = LoadFor(RoleAPI)
	if err != nil {
		t.Fatalf("api with a poster key: %v", err)
	}
	if strings.Contains(strings.Join(cfg.Warnings(), "\n"), "POSTER_LLM_API_KEY") {
		t.Errorf("the api owns POSTER_LLM_API_KEY, got: %v", cfg.Warnings())
	}
}

func TestLoadFor_PosterReadsPerOrgPerDay(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleAPI])
	cfg, err := LoadFor(RoleAPI)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	if cfg.PosterReadsPerOrgPerDay != 50 {
		t.Errorf("default cap = %d, want 50", cfg.PosterReadsPerOrgPerDay)
	}
	t.Setenv("POSTER_READS_PER_ORG_PER_DAY", "0")
	if cfg, err = LoadFor(RoleAPI); err != nil || cfg.PosterReadsPerOrgPerDay != 0 {
		t.Errorf("0 must switch the cap off: cap=%v err=%v", cfg, err)
	}
	t.Setenv("POSTER_READS_PER_ORG_PER_DAY", "-1")
	if _, err = LoadFor(RoleAPI); err == nil {
		t.Error("a negative cap must be refused")
	}
}

func TestLoadFor_BillingCollectSince(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleWorker])
	if cfg, err := LoadFor(RoleWorker); err != nil || !cfg.BillingCollectSince.IsZero() {
		t.Fatalf("unset: err=%v since=%v, want collection off", err, cfg.BillingCollectSince)
	}
	t.Setenv("BILLING_COLLECT_SINCE", "2026-10-01T00:00:00+02:00")
	cfg, err := LoadFor(RoleWorker)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BillingCollectSince.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-09-30T22:00:00Z" {
		t.Fatalf("BillingCollectSince = %s, want 2026-09-30T22:00:00Z", got)
	}
	t.Setenv("BILLING_COLLECT_SINCE", "yesterday")
	if _, err := LoadFor(RoleWorker); err == nil {
		t.Fatal("a value that is not RFC 3339 must be refused")
	}
}

// The text-formatting route (spec 43, RT-04) uses the poster key and base URL
// and has its own model and daily caps.
func TestLoadFor_TextFormatSettings(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleAPI])
	cfg, err := LoadFor(RoleAPI)
	if err != nil {
		t.Fatalf("api: %v", err)
	}
	if cfg.TextFormatLLMModel != "claude-haiku-5-5" || cfg.TextFormatsPerUserPerDay != 100 || cfg.TextFormatsPerOrgPerDay != 500 {
		t.Errorf("defaults = %q, %d, %d", cfg.TextFormatLLMModel, cfg.TextFormatsPerUserPerDay, cfg.TextFormatsPerOrgPerDay)
	}
	t.Setenv("TEXT_FORMAT_LLM_MODEL", " claude-test ")
	t.Setenv("TEXT_FORMATS_PER_USER_PER_DAY", "7")
	t.Setenv("TEXT_FORMATS_PER_ORG_PER_DAY", "0")
	cfg, err = LoadFor(RoleAPI)
	if err != nil {
		t.Fatalf("api with settings: %v", err)
	}
	if cfg.TextFormatLLMModel != "claude-test" || cfg.TextFormatsPerUserPerDay != 7 || cfg.TextFormatsPerOrgPerDay != 0 {
		t.Errorf("settings = %q, %d, %d", cfg.TextFormatLLMModel, cfg.TextFormatsPerUserPerDay, cfg.TextFormatsPerOrgPerDay)
	}
	for _, name := range []string{"TEXT_FORMATS_PER_USER_PER_DAY", "TEXT_FORMATS_PER_ORG_PER_DAY"} {
		t.Setenv(name, "-1")
		if _, err = LoadFor(RoleAPI); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=-1 must be refused, got %v", name, err)
		}
		t.Setenv(name, "5")
	}
	t.Setenv("TEXT_FORMATS_PER_ORG_PER_DAY", "many")
	if _, err = LoadFor(RoleAPI); err == nil {
		t.Error("a cap that is not a number must be refused")
	}
}
