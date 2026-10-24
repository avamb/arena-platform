package textformat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultModel is a fast, cheap model: the task is marking up a page of
	// text, not reasoning. Overridable through TEXT_FORMAT_LLM_MODEL.
	DefaultModel = "claude-haiku-5-5"
	// DefaultBaseURL is the Messages API origin.
	DefaultBaseURL = "https://api.anthropic.com"
	// anthropicVersion is the API version header every call carries.
	anthropicVersion = "2023-06-01"
	// RequestTimeout bounds one call; the router gives a request 30 seconds
	// and the route answers within this much plus the verification.
	RequestTimeout = 25 * time.Second
	toolName       = "format_text"

	minMaxTokens = 256
	maxMaxTokens = 8192
	// maxAnswerBytes bounds what is read of the answer.
	maxAnswerBytes = 1 << 20
)

// APIError is a non-2xx answer of the model API. It carries the status and
// the API's short error type only, never its message, which may quote the
// request: an error is safe to log.
type APIError struct {
	Status int
	Type   string
}

func (e *APIError) Error() string {
	if e.Type == "" {
		return fmt.Sprintf("textformat: model answered %d", e.Status)
	}
	return fmt.Sprintf("textformat: model answered %d (%s)", e.Status, e.Type)
}

// ErrNoToolCall is returned when the answer holds no format_text call.
var ErrNoToolCall = errors.New("textformat: the model returned no format_text tool call")

// ErrTruncated is returned when the model ran out of tokens, so its answer
// is cut.
var ErrTruncated = errors.New("textformat: the model's answer was cut at max_tokens")

// Anthropic formats texts through the Claude Messages API.
type Anthropic struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

// NewAnthropic builds a formatter; an empty apiKey yields one that answers
// ErrNotConfigured. Empty model and baseURL take the defaults; a nil client
// gets one with RequestTimeout.
func NewAnthropic(apiKey, model, baseURL string, client *http.Client) *Anthropic {
	if model == "" {
		model = DefaultModel
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: RequestTimeout}
	}
	return &Anthropic{apiKey: strings.TrimSpace(apiKey), model: model, baseURL: strings.TrimRight(baseURL, "/"), http: client}
}

// Configured reports whether the formatter has a key.
func (a *Anthropic) Configured() bool { return a != nil && a.apiKey != "" }

// toolSchema is the JSON schema the model fills in.
var toolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"html": map[string]any{
			"type":        "string",
			"description": "The organizer's text, unchanged, with only the allowed HTML tags added.",
		},
	},
	"required": []string{"html"},
}

// MaxTokensFor is the token budget of the answer: one and a half times the
// text, estimated at three characters a token, between 256 and 8192.
func MaxTokensFor(text string) int {
	est := utf8.RuneCountInString(text) / 3
	n := est * 3 / 2
	if n < minMaxTokens {
		return minMaxTokens
	}
	if n > maxMaxTokens {
		return maxMaxTokens
	}
	return n
}

// wire shapes of the Messages API, only what is used.
type messagesRequest struct {
	Model      string           `json:"model"`
	MaxTokens  int              `json:"max_tokens"`
	System     string           `json:"system"`
	Tools      []toolDef        `json:"tools"`
	ToolChoice map[string]any   `json:"tool_choice"`
	Messages   []messageContent `json:"messages"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type messageContent struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

type messagesResponse struct {
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Error *struct {
		Type string `json:"type"`
	} `json:"error"`
}

// Format sends the text and returns the model's HTML, unverified.
func (a *Anthropic) Format(ctx context.Context, in Input) (Output, error) {
	if !a.Configured() {
		return Output{}, ErrNotConfigured
	}
	if _, ok := ParseKind(string(in.Kind)); !ok {
		return Output{}, fmt.Errorf("textformat: unknown kind %q", in.Kind)
	}
	body := messagesRequest{
		Model:     a.model,
		MaxTokens: MaxTokensFor(in.Text),
		System:    SystemPrompt(in.Kind),
		Tools: []toolDef{{
			Name:        toolName,
			Description: "Return the organizer's text with HTML markup added and nothing else changed.",
			InputSchema: toolSchema,
		}},
		ToolChoice: map[string]any{"type": "tool", "name": toolName},
		Messages: []messageContent{{
			Role:    "user",
			Content: []map[string]any{{"type": "text", "text": UserMessage(in)}},
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Output{}, fmt.Errorf("textformat: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return Output{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	res, err := a.http.Do(req)
	if err != nil {
		// The transport error names the URL, never the body: safe to wrap.
		return Output{}, fmt.Errorf("textformat: call model: %w", err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, maxAnswerBytes))
	if err != nil {
		return Output{}, fmt.Errorf("textformat: read answer: %w", err)
	}
	var parsed messagesResponse
	_ = json.Unmarshal(out, &parsed)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		e := &APIError{Status: res.StatusCode}
		if parsed.Error != nil {
			e.Type = clip(parsed.Error.Type, 64)
		}
		return Output{}, e
	}
	if parsed.StopReason == "max_tokens" {
		return Output{}, ErrTruncated
	}
	for _, c := range parsed.Content {
		if c.Type == "tool_use" && c.Name == toolName {
			var ti struct {
				HTML string `json:"html"`
			}
			if err := json.Unmarshal(c.Input, &ti); err != nil {
				return Output{}, fmt.Errorf("textformat: decode tool input: %w", err)
			}
			return Output{HTML: ti.HTML}, nil
		}
	}
	return Output{}, ErrNoToolCall
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
