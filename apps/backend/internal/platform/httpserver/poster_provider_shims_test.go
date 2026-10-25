package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func posterStub(t *testing.T, status int, body string) (*httptest.Server, *int) {
	t.Helper()
	calls := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*calls++
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

const (
	anthropicPosterAnswer = `{"content":[{"type":"tool_use","name":"poster_facts","input":{"name":"From Claude"}}]}`
	geminiPosterAnswer    = `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"{\"name\":\"From Gemini\"}"}]}}]}`
)

func TestResolvePosterReader_ProviderChoice(t *testing.T) {
	// No key at all, whatever the provider: disabled, as before.
	for _, provider := range []string{"", "anthropic", "gemini"} {
		if r := resolvePosterReader(Options{Config: &config.Config{PosterLLMProvider: provider}}); r != nil {
			t.Errorf("provider %q without a key must give no reader", provider)
		}
	}
	// The provider's own key is what counts: a Gemini key does not enable anthropic.
	if r := resolvePosterReader(Options{Config: &config.Config{GeminiAPIKey: "g"}}); r != nil {
		t.Error("a Gemini key alone must not enable the default anthropic provider")
	}

	claude, _ := posterStub(t, 200, anthropicPosterAnswer)
	gem, _ := posterStub(t, 200, geminiPosterAnswer)
	base := config.Config{
		PosterLLMAPIKey: "sk", PosterLLMBaseURL: claude.URL,
		GeminiAPIKey: "g", PosterGeminiBaseURL: gem.URL,
	}
	for provider, want := range map[string]string{"": "From Claude", "anthropic": "From Claude", "gemini": "From Gemini"} {
		cfg := base
		cfg.PosterLLMProvider = provider
		r := resolvePosterReader(Options{Config: &cfg})
		if r == nil {
			t.Fatalf("provider %q: no reader", provider)
		}
		facts, err := r.Read(context.Background(), []byte("x"), "image/png")
		if err != nil || facts.Name != want {
			t.Errorf("provider %q: facts %+v err %v, want %q", provider, facts, err, want)
		}
	}
}

func TestResolvePosterReader_FallbackIsTriedOnceOnFailure(t *testing.T) {
	gem, gemCalls := posterStub(t, 500, `{"error":{"status":"INTERNAL","message":"down"}}`)
	claude, claudeCalls := posterStub(t, 200, anthropicPosterAnswer)
	cfg := config.Config{
		PosterLLMProvider: "gemini", PosterLLMFallbackProvider: "anthropic",
		GeminiAPIKey: "g", PosterGeminiBaseURL: gem.URL,
		PosterLLMAPIKey: "sk", PosterLLMBaseURL: claude.URL,
	}
	r := resolvePosterReader(Options{Config: &cfg})
	facts, err := r.Read(context.Background(), []byte("x"), "image/png")
	if err != nil || facts.Name != "From Claude" || *gemCalls != 1 || *claudeCalls != 1 {
		t.Fatalf("facts %+v err %v gemini calls %d anthropic calls %d", facts, err, *gemCalls, *claudeCalls)
	}
	if p, f := posterProviders(&cfg); p != "gemini" || f != "anthropic" {
		t.Errorf("providers = %q %q", p, f)
	}
}

// A primary without its key is replaced by a keyed fallback; a fallback
// without a key is ignored.
func TestResolvePosterReader_UnkeyedProvidersDoNotCount(t *testing.T) {
	claude, _ := posterStub(t, 200, anthropicPosterAnswer)
	onlyFallback := config.Config{PosterLLMProvider: "gemini", PosterLLMFallbackProvider: "anthropic", PosterLLMAPIKey: "sk", PosterLLMBaseURL: claude.URL}
	if p, f := posterProviders(&onlyFallback); p != "anthropic" || f != "" {
		t.Errorf("providers = %q %q", p, f)
	}
	if r := resolvePosterReader(Options{Config: &onlyFallback}); r == nil {
		t.Error("a keyed fallback must serve when the primary has no key")
	}
	noFallbackKey := config.Config{PosterLLMAPIKey: "sk", PosterLLMBaseURL: claude.URL, PosterLLMFallbackProvider: "gemini"}
	if p, f := posterProviders(&noFallbackKey); p != "anthropic" || f != "" {
		t.Errorf("providers = %q %q", p, f)
	}
}
