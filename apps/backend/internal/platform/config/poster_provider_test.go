package config

import (
	"strings"
	"testing"
)

func apiEnv(t *testing.T) {
	t.Helper()
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleAPI])
}

func TestLoadFor_PosterProviderDefaults(t *testing.T) {
	apiEnv(t)
	cfg, err := LoadFor(RoleAPI)
	if err != nil {
		t.Fatalf("LoadFor: %v", err)
	}
	if cfg.PosterLLMProvider != PosterProviderAnthropic || cfg.PosterLLMFallbackProvider != "" ||
		cfg.PosterGeminiModel != DefaultPosterGeminiModel || cfg.PosterGeminiBaseURL != DefaultPosterGeminiBaseURL || cfg.GeminiAPIKey != "" {
		t.Fatalf("defaults = %q %q %q %q %q", cfg.PosterLLMProvider, cfg.PosterLLMFallbackProvider, cfg.PosterGeminiModel, cfg.PosterGeminiBaseURL, cfg.GeminiAPIKey)
	}
}

func TestLoadFor_PosterProviderValues(t *testing.T) {
	cases := []struct {
		name, provider, fallback string
		wantErr                  string
	}{
		{"gemini primary", "gemini", "", ""},
		{"case and spaces are folded", " Gemini ", " ANTHROPIC ", ""},
		{"anthropic with gemini fallback", "anthropic", "gemini", ""},
		{"unknown primary", "openai", "", "POSTER_LLM_PROVIDER"},
		{"unknown fallback", "anthropic", "openai", "POSTER_LLM_FALLBACK_PROVIDER"},
		{"same provider twice", "gemini", "gemini", "must differ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiEnv(t)
			t.Setenv("POSTER_LLM_PROVIDER", tc.provider)
			t.Setenv("POSTER_LLM_FALLBACK_PROVIDER", tc.fallback)
			cfg, err := LoadFor(RoleAPI)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("LoadFor: %v", err)
				}
				if cfg.PosterLLMProvider != strings.ToLower(strings.TrimSpace(tc.provider)) {
					t.Errorf("provider = %q", cfg.PosterLLMProvider)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// The test seam of the Gemini host is refused in production unless it is the
// built-in default, and allowed in development.
func TestLoadFor_PosterGeminiBaseURLIsRefusedInProduction(t *testing.T) {
	apiEnv(t)
	t.Setenv("POSTER_GEMINI_BASE_URL", "http://stub:8081")
	_, err := LoadFor(RoleAPI)
	if err == nil || !strings.Contains(err.Error(), "POSTER_GEMINI_BASE_URL") {
		t.Fatalf("err = %v", err)
	}
	apiEnv(t)
	t.Setenv("POSTER_GEMINI_BASE_URL", DefaultPosterGeminiBaseURL+"/")
	if _, err := LoadFor(RoleAPI); err != nil {
		t.Fatalf("the default host must be accepted: %v", err)
	}
	clearConfigEnv(t)
	setEnv(t, map[string]string{"APP_ENV": "development", "DATABASE_URL": productionCommon["DATABASE_URL"]})
	setEnv(t, roleMinimum[RoleAPI])
	t.Setenv("POSTER_GEMINI_BASE_URL", "http://127.0.0.1:9999")
	cfg, err := LoadFor(RoleAPI)
	if err != nil {
		t.Fatalf("a stub host must be allowed in development: %v", err)
	}
	if cfg.PosterGeminiBaseURL != "http://127.0.0.1:9999" {
		t.Errorf("development base URL = %q", cfg.PosterGeminiBaseURL)
	}
}

// GEMINI_API_KEY belongs to the api, like POSTER_LLM_API_KEY: the bot holding
// it gets the "set but unused" warning, which never prints the value.
func TestLoadFor_GeminiKeyBelongsToTheAPI(t *testing.T) {
	clearConfigEnv(t)
	setEnv(t, productionCommon)
	setEnv(t, roleMinimum[RoleBot])
	t.Setenv("GEMINI_API_KEY", "AIza-should-not-be-printed")
	cfg, err := LoadFor(RoleBot)
	if err != nil {
		t.Fatalf("a Gemini key must not stop the bot: %v", err)
	}
	joined := strings.Join(cfg.Warnings(), "\n")
	if !strings.Contains(joined, "GEMINI_API_KEY") || strings.Contains(joined, "AIza-should-not-be-printed") {
		t.Errorf("warnings = %s", joined)
	}
	apiEnv(t)
	t.Setenv("GEMINI_API_KEY", "AIza-should-not-be-printed")
	cfg, err = LoadFor(RoleAPI)
	if err != nil {
		t.Fatalf("api with a Gemini key: %v", err)
	}
	if strings.Contains(strings.Join(cfg.Warnings(), "\n"), "GEMINI_API_KEY") {
		t.Errorf("the api owns GEMINI_API_KEY, got: %v", cfg.Warnings())
	}
}
