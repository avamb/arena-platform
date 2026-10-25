package posterread

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DefaultGeminiModel is Google's current stable Flash model with image
	// input and structured output (ai.google.dev/gemini-api/docs/models,
	// checked 2026-10-25). Overridable through POSTER_GEMINI_MODEL.
	DefaultGeminiModel = "gemini-3.8-flash"
	// DefaultGeminiBaseURL is the Generative Language API origin.
	DefaultGeminiBaseURL = "https://generativelanguage.googleapis.com"
	// geminiMaxOutputTokens leaves room for the model's thinking tokens, which
	// count against the limit on the Gemini 3 models, so an answer is not cut
	// before the JSON starts.
	geminiMaxOutputTokens = 8192
)

// Gemini reads posters through the Gemini generateContent API with the image
// inline and the answer constrained to a response schema.
type Gemini struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
	log     *slog.Logger
}

// NewGemini builds a reader; an empty apiKey yields one that answers
// ErrNotConfigured. Empty model and baseURL take the defaults; a nil client
// gets one with requestTimeout.
func NewGemini(apiKey, model, baseURL string, client *http.Client) *Gemini {
	if model == "" {
		model = DefaultGeminiModel
	}
	if baseURL == "" {
		baseURL = DefaultGeminiBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &Gemini{apiKey: strings.TrimSpace(apiKey), model: model, baseURL: strings.TrimRight(baseURL, "/"), http: client}
}

// Configured reports whether the reader has a key.
func (g *Gemini) Configured() bool { return g != nil && g.apiKey != "" }

// WithLogger makes the reader log one line per call (see Anthropic.WithLogger).
func (g *Gemini) WithLogger(l *slog.Logger) *Gemini {
	g.log = l
	return g
}

// geminiSchema is toolSchema in the OpenAPI-subset dialect of Gemini's
// responseSchema (upper-case type names, as the API reference spells them).
var geminiSchema = toGeminiSchema(toolSchema)

// toGeminiSchema copies a JSON schema, upper-casing every "type" and keeping
// properties, items, required and description.
func toGeminiSchema(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		switch k {
		case "type":
			if s, ok := v.(string); ok {
				out[k] = strings.ToUpper(s)
				continue
			}
			out[k] = v
		case "properties":
			props, _ := v.(map[string]any)
			conv := make(map[string]any, len(props))
			for name, p := range props {
				if pm, ok := p.(map[string]any); ok {
					conv[name] = toGeminiSchema(pm)
				}
			}
			out[k] = conv
		case "items":
			if im, ok := v.(map[string]any); ok {
				out[k] = toGeminiSchema(im)
			}
		default:
			out[k] = v
		}
	}
	return out
}

// wire shapes of generateContent, only what is used.
type geminiRequest struct {
	SystemInstruction geminiContent   `json:"systemInstruction"`
	Contents          []geminiContent `json:"contents"`
	GenerationConfig  geminiGenConfig `json:"generationConfig"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text       string      `json:"text,omitempty"`
	InlineData *geminiBlob `json:"inlineData,omitempty"`
}

type geminiBlob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiGenConfig struct {
	ResponseMimeType string         `json:"responseMimeType"`
	ResponseSchema   map[string]any `json:"responseSchema"`
	MaxOutputTokens  int            `json:"maxOutputTokens"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text    string `json:"text"`
				Thought bool   `json:"thought"`
			} `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
	Error *struct {
		Code    int    `json:"code"`
		Status  string `json:"status"`
		Message string `json:"message"`
	} `json:"error"`
}

// Read sends the poster and returns the normalized facts.
func (g *Gemini) Read(ctx context.Context, image []byte, mediaType string) (Facts, error) {
	if !g.Configured() {
		return Facts{}, ErrNotConfigured
	}
	if err := checkImage(image, mediaType); err != nil {
		return Facts{}, err
	}
	started := time.Now()
	body := geminiRequest{
		SystemInstruction: geminiContent{Parts: []geminiPart{{Text: geminiSystemPrompt}}},
		Contents: []geminiContent{{
			Role: "user",
			Parts: []geminiPart{
				{InlineData: &geminiBlob{MimeType: mediaType, Data: base64.StdEncoding.EncodeToString(image)}},
				{Text: "Read this event poster and return its facts as JSON."},
			},
		}},
		GenerationConfig: geminiGenConfig{
			ResponseMimeType: "application/json",
			ResponseSchema:   geminiSchema,
			MaxOutputTokens:  geminiMaxOutputTokens,
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Facts{}, fmt.Errorf("posterread: encode request: %w", err)
	}
	endpoint := g.baseURL + "/v1beta/models/" + url.PathEscape(g.model) + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return Facts{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The key travels in a header, never in the URL: a URL ends up in logs and
	// in the text of transport errors.
	req.Header.Set("x-goog-api-key", g.apiKey)
	res, err := g.http.Do(req)
	if err != nil {
		return Facts{}, fmt.Errorf("posterread: call gemini: %w", err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Facts{}, fmt.Errorf("posterread: read answer: %w", err)
	}
	var parsed geminiResponse
	_ = json.Unmarshal(out, &parsed)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(out))
		if parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Status + ": " + parsed.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return Facts{}, fmt.Errorf("posterread: gemini answered %d: %s", res.StatusCode, msg)
	}
	u := parsed.UsageMetadata
	logUsage(g.log, "gemini", g.model, u.PromptTokenCount, u.CandidatesTokenCount+u.ThoughtsTokenCount, time.Since(started))
	if br := parsed.PromptFeedback.BlockReason; br != "" {
		return Facts{}, fmt.Errorf("posterread: gemini blocked the request: %s", br)
	}
	if len(parsed.Candidates) == 0 {
		return Facts{}, fmt.Errorf("posterread: gemini returned no candidate")
	}
	cand := parsed.Candidates[0]
	if cand.FinishReason != "" && cand.FinishReason != "STOP" {
		return Facts{}, fmt.Errorf("posterread: gemini stopped with %s", cand.FinishReason)
	}
	var text strings.Builder
	for _, p := range cand.Content.Parts {
		if !p.Thought {
			text.WriteString(p.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return Facts{}, fmt.Errorf("posterread: gemini returned no JSON answer")
	}
	var rf rawFacts
	if err := json.Unmarshal([]byte(text.String()), &rf); err != nil {
		return Facts{}, fmt.Errorf("posterread: decode gemini answer: %w", err)
	}
	return normalize(rf), nil
}

const geminiSystemPrompt = "You read event posters for a ticketing platform. Extract only what is printed on the poster; " +
	"never guess a missing value and never invent one. Dates: when the year is not printed, leave the date empty. " +
	"Use an empty string for a text value that is not on the poster and an empty list when no price is printed. " +
	"Answer with the JSON object of the response schema only."
