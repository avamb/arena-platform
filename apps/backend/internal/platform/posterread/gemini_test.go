package posterread

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const geminiOKBody = `{"candidates":[{"finishReason":"STOP","content":{"parts":[
	{"text":"thinking...","thought":true},
	{"text":"{\"name\":\"  Ночь открытой сцены \",\"date\":\"2026-10-24\",\"time\":\"19:0\",\"venue_name\":\"Test Hall Rika\",\"city\":\"Madrid\",\"age\":\"16\",\"description\":\"\",\"categories\":[{\"name\":\"Партер\",\"price\":34.9,\"currency\":\"eur\"},{\"name\":\"\",\"price\":10}]}"}
]}}],"usageMetadata":{"promptTokenCount":1200,"candidatesTokenCount":80,"thoughtsTokenCount":300}}`

// The request carries the key in a header (never the URL), the image inline,
// the JSON mime type and a response schema with upper-case types; the answer
// goes through the same normalization as the Anthropic reader.
func TestGemini_Read_RequestShapeAndNormalizes(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1beta/models/gemini-3.8-flash:generateContent" {
			t.Errorf("request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-goog-api-key") != "g-test" {
			t.Errorf("x-goog-api-key = %q", r.Header.Get("x-goog-api-key"))
		}
		if r.URL.RawQuery != "" || strings.Contains(r.URL.String(), "g-test") {
			t.Errorf("the key or a query leaked into the URL: %s", r.URL)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		_, _ = w.Write([]byte(geminiOKBody))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	r := NewGemini("g-test", "", srv.URL, srv.Client()).WithLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	facts, err := r.Read(context.Background(), []byte("\xff\xd8fake"), "image/jpeg")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	contents, _ := got["contents"].([]any)
	parts, _ := contents[0].(map[string]any)["parts"].([]any)
	inline, _ := parts[0].(map[string]any)["inlineData"].(map[string]any)
	if inline["mimeType"] != "image/jpeg" || inline["data"] == "" {
		t.Errorf("inlineData = %v", inline)
	}
	gc, _ := got["generationConfig"].(map[string]any)
	if gc["responseMimeType"] != "application/json" {
		t.Errorf("responseMimeType = %v", gc["responseMimeType"])
	}
	schema, _ := gc["responseSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	cats, _ := props["categories"].(map[string]any)
	item, _ := cats["items"].(map[string]any)
	if schema["type"] != "OBJECT" || cats["type"] != "ARRAY" || item["type"] != "OBJECT" {
		t.Errorf("schema types are not upper-case OpenAPI: %v", schema)
	}
	if _, ok := got["systemInstruction"].(map[string]any); !ok {
		t.Errorf("no systemInstruction: %v", got)
	}

	if facts.Name != "Ночь открытой сцены" || facts.Date != "2026-10-24" || facts.Time != "19:00" || facts.Age != "16+" || facts.City != "Madrid" {
		t.Fatalf("facts = %+v", facts)
	}
	if len(facts.Categories) != 1 || facts.Categories[0] != (Category{Name: "Партер", PriceMinor: 3490, Currency: "EUR"}) {
		t.Fatalf("categories = %+v", facts.Categories)
	}
	out := buf.String()
	if !strings.Contains(out, "provider=gemini") || !strings.Contains(out, "input_tokens=1200") || !strings.Contains(out, "output_tokens=380") {
		t.Errorf("usage log = %s", out)
	}
	if strings.Contains(out, "Ночь") || strings.Contains(out, "g-test") {
		t.Errorf("the log holds poster text or the key: %s", out)
	}
}

func TestGemini_Read_ErrorMapping(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"api error", 403, `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"API key not valid"}}`, "403: PERMISSION_DENIED: API key not valid"},
		{"rate limited", 429, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`, "429: RESOURCE_EXHAUSTED"},
		{"blocked prompt", 200, `{"promptFeedback":{"blockReason":"SAFETY"}}`, "blocked the request: SAFETY"},
		{"no candidate", 200, `{"candidates":[]}`, "no candidate"},
		{"cut at max tokens", 200, `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"parts":[{"text":"{\"name\":"}]}}]}`, "stopped with MAX_TOKENS"},
		{"empty text", 200, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"","thought":false}]}}]}`, "no JSON answer"},
		{"not json", 200, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"sorry"}]}}]}`, "decode gemini answer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := NewGemini("k", "", srv.URL, srv.Client()).Read(context.Background(), []byte("x"), "image/png")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestGemini_Read_InputChecksAndNoKey(t *testing.T) {
	if _, err := NewGemini("", "", "http://unused", nil).Read(context.Background(), []byte("x"), "image/png"); err != ErrNotConfigured {
		t.Fatalf("unconfigured reader err = %v", err)
	}
	if NewGemini("  ", "", "", nil).Configured() {
		t.Error("a blank key must not count as configured")
	}
	r := NewGemini("k", "", "http://unused", nil)
	if _, err := r.Read(context.Background(), nil, "image/png"); err == nil {
		t.Error("an empty image must be refused")
	}
	if _, err := r.Read(context.Background(), make([]byte, maxImageBytes+1), "image/png"); err == nil {
		t.Error("an oversized image must be refused")
	}
	if _, err := r.Read(context.Background(), []byte("x"), "image/gif"); err == nil {
		t.Error("an unsupported media type must be refused")
	}
}

func TestGemini_Read_TimeoutFollowsTheContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewGemini("k", "", srv.URL, nil).Read(ctx, []byte("x"), "image/png")
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err = %v after %v", err, time.Since(start))
	}
}
