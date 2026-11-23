package contenttranslate

// machine.go - the machine translator of an event's texts (spec 63 section
// 16.4). The Anthropic client follows internal/platform/textformat: one forced
// tool call, the key never in an error, a read cap on the answer. The model
// gets the original fields of ONE event and ONE target language and returns the
// same fields translated; the name of the event is never translated and never
// written by the machine (the Name field of Fields stays nil).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultModel is the model of the machine translation, overridable through
	// TRANSLATE_LLM_MODEL.
	DefaultModel = "claude-sonnet-5-5"
	// DefaultBaseURL is the Messages API origin.
	DefaultBaseURL = "https://api.anthropic.com"

	anthropicVersion = "2023-06-01"
	// RequestTimeout bounds one call: a long description is a long answer, and
	// the worker job has no request deadline of its own.
	RequestTimeout = 3 * time.Minute
	toolName       = "return_translation"

	minMaxTokens   = 1024
	maxMaxTokens   = 16000
	tokenSlack     = 512
	maxAnswerBytes = 2 << 20
)

// ErrNotConfigured is returned by a translator that has no API key.
var ErrNotConfigured = errors.New("contenttranslate: machine translation is not configured")

// ErrNoToolCall is returned when the answer holds no return_translation call.
var ErrNoToolCall = errors.New("contenttranslate: the model returned no translation tool call")

// ErrTruncated is returned when the model ran out of tokens: the answer is cut.
var ErrTruncated = errors.New("contenttranslate: the model's answer was cut at max_tokens")

// ErrIncomplete is returned when the model stopped for any reason but a
// finished tool call.
var ErrIncomplete = errors.New("contenttranslate: the model did not finish the translation call")

// ErrAnswerTooLarge is returned when a successful answer is over the read cap.
var ErrAnswerTooLarge = errors.New("contenttranslate: the model's answer is too large")

// NameAlteredError is returned when the name of the event stands in an original
// field but not, unchanged, in its translation.
type NameAlteredError struct {
	Field string
	Name  string
}

func (e *NameAlteredError) Error() string {
	return "contenttranslate: the event name is not kept unchanged in " + e.Field
}

// APIError is a non-2xx answer of the model API. It carries the status and the
// API's short error type only, never its message (which may quote the request).
type APIError struct {
	Status int
	Type   string
}

func (e *APIError) Error() string {
	if e.Type == "" {
		return fmt.Sprintf("contenttranslate: model answered %d", e.Status)
	}
	return fmt.Sprintf("contenttranslate: model answered %d (%s)", e.Status, e.Type)
}

// Permanent reports whether repeating the same request cannot help: a refused
// field, an altered name, a cut answer, or a 4xx other than a timeout or a rate
// limit. Everything else (network, 5xx, 429, 529) may pass with a later try.
func Permanent(err error) bool {
	var fe *FieldError
	var ne *NameAlteredError
	var api *APIError
	switch {
	case errors.As(err, &fe), errors.As(err, &ne):
		return true
	case errors.Is(err, ErrTruncated), errors.Is(err, ErrAnswerTooLarge), errors.Is(err, ErrNotConfigured):
		return true
	case errors.As(err, &api):
		return api.Status >= 400 && api.Status < 500 && api.Status != http.StatusRequestTimeout && api.Status != http.StatusTooManyRequests
	}
	return false
}

// TranslateInput is one request: the original texts of an event and the pair
// of languages. Only non-empty fields are sent and returned. Name is the event
// name; it is context for the model and the reference of the name check, not
// something to translate.
type TranslateInput struct {
	From, To string
	Name     string
	Fields   Fields
}

// Translator translates the non-empty fields of in. The result holds the same
// fields translated (nil where the input was empty); its Name is always nil.
type Translator interface {
	Translate(ctx context.Context, in TranslateInput) (Fields, error)
}

// containsName reports whether text holds name as plain text or as HTML text
// (&amp; for &).
func containsName(text, name string) bool {
	return strings.Contains(text, name) || strings.Contains(text, html.EscapeString(name))
}

// CheckName enforces the name rule of spec 63 section 16.4: a field of the
// original that holds the event name must hold it, unchanged, in the
// translation. A field of the original without the name is not checked.
func CheckName(name string, orig, translated Fields) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	for _, p := range []struct {
		field    string
		from, to *string
	}{
		{"short_description", orig.ShortDescription, translated.ShortDescription},
		{"description_html", orig.DescriptionHTML, translated.DescriptionHTML},
		{"report_text_html", orig.ReportTextHTML, translated.ReportTextHTML},
	} {
		if p.from == nil || !containsName(*p.from, name) {
			continue
		}
		if p.to == nil || !containsName(*p.to, name) {
			return &NameAlteredError{Field: p.field, Name: name}
		}
	}
	return nil
}

// Anthropic translates through the Claude Messages API.
type Anthropic struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

// NewAnthropic builds a translator; an empty apiKey yields one that answers
// ErrNotConfigured. Empty model and baseURL take the defaults; a nil client
// gets one with RequestTimeout.
func NewAnthropic(apiKey, model, baseURL string, client *http.Client) *Anthropic {
	if strings.TrimSpace(model) == "" {
		model = DefaultModel
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: RequestTimeout}
	}
	return &Anthropic{apiKey: strings.TrimSpace(apiKey), model: model, baseURL: strings.TrimRight(baseURL, "/"), http: client}
}

// Configured reports whether the translator has a key.
func (a *Anthropic) Configured() bool { return a != nil && a.apiKey != "" }

// SystemPrompt is the instruction of every translation call.
const SystemPrompt = `You translate the texts of one event for a ticketing site.
Translate from the source language to the target language, both given in the request.
Rules:
- Keep the HTML structure exactly: the same tags, the same order, the same links (href values unchanged). Translate only the text between tags.
- Never translate proper names: names of people, bands, companies, places, venues and the event itself. Keep them exactly as written in the source, in the same script.
- The event name is given for reference. Wherever it stands in a text it must stay unchanged in the translation.
- Do not add, drop, summarize or comment on anything. Do not translate the keys or the field set.
- Return every field you were given, and only those, through the tool.`

var toolSchemaProps = map[string]any{
	"short_description": map[string]any{"type": "string", "description": "The short description translated."},
	"description_html":  map[string]any{"type": "string", "description": "The description translated, with the same HTML structure."},
	"report_text_html":  map[string]any{"type": "string", "description": "The photo-report text translated, with the same HTML structure."},
}

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

// maxTokensFor is the token budget of an answer: two tokens a rune of input
// plus slack (Hebrew and Cyrillic cost much), between 1024 and 16000.
func maxTokensFor(runes int) int {
	n := runes*2 + tokenSlack
	if n < minMaxTokens {
		return minMaxTokens
	}
	if n > maxMaxTokens {
		return maxMaxTokens
	}
	return n
}

// Translate sends the non-empty fields and returns them translated. The answer
// goes through SanitizeFields and the name check; it never carries a Name.
func (a *Anthropic) Translate(ctx context.Context, in TranslateInput) (Fields, error) {
	if !a.Configured() {
		return Fields{}, ErrNotConfigured
	}
	send := map[string]string{}
	props := map[string]any{}
	var required []string
	runes := 0
	for _, f := range []struct {
		key string
		val *string
	}{
		{"short_description", in.Fields.ShortDescription},
		{"description_html", in.Fields.DescriptionHTML},
		{"report_text_html", in.Fields.ReportTextHTML},
	} {
		if f.val == nil || strings.TrimSpace(*f.val) == "" {
			continue
		}
		send[f.key] = *f.val
		props[f.key] = toolSchemaProps[f.key]
		required = append(required, f.key)
		runes += utf8.RuneCountInString(*f.val)
	}
	if len(send) == 0 {
		return Fields{}, nil
	}
	userMsg, err := json.Marshal(map[string]any{
		"source_language": in.From,
		"target_language": in.To,
		"event_name":      in.Name,
		"fields":          send,
	})
	if err != nil {
		return Fields{}, fmt.Errorf("contenttranslate: encode request: %w", err)
	}
	body := messagesRequest{
		Model:     a.model,
		MaxTokens: maxTokensFor(runes),
		System:    SystemPrompt,
		Tools: []toolDef{{
			Name:        toolName,
			Description: "Return the translated fields and nothing else.",
			InputSchema: map[string]any{"type": "object", "properties": props, "required": required},
		}},
		ToolChoice: map[string]any{"type": "tool", "name": toolName},
		Messages: []messageContent{{
			Role:    "user",
			Content: []map[string]any{{"type": "text", "text": string(userMsg)}},
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Fields{}, fmt.Errorf("contenttranslate: encode request: %w", err)
	}
	got, err := a.call(ctx, raw)
	if err != nil {
		return Fields{}, err
	}
	clean, err := SanitizeFields(got)
	if err != nil {
		return Fields{}, err
	}
	clean.Name = nil
	if err := CheckName(in.Name, in.Fields, clean); err != nil {
		return Fields{}, err
	}
	return clean, nil
}

func (a *Anthropic) call(ctx context.Context, raw []byte) (Fields, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return Fields{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	res, err := a.http.Do(req)
	if err != nil {
		return Fields{}, fmt.Errorf("contenttranslate: call model: %w", err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, maxAnswerBytes+1))
	if err != nil {
		return Fields{}, fmt.Errorf("contenttranslate: read answer: %w", err)
	}
	tooLarge := len(out) > maxAnswerBytes
	var parsed messagesResponse
	if !tooLarge {
		_ = json.Unmarshal(out, &parsed)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		e := &APIError{Status: res.StatusCode}
		if parsed.Error != nil && len(parsed.Error.Type) <= 64 {
			e.Type = parsed.Error.Type
		}
		return Fields{}, e
	}
	if tooLarge {
		return Fields{}, ErrAnswerTooLarge
	}
	switch parsed.StopReason {
	case "tool_use":
	case "max_tokens":
		return Fields{}, ErrTruncated
	default:
		return Fields{}, ErrIncomplete
	}
	for _, c := range parsed.Content {
		if c.Type == "tool_use" && c.Name == toolName {
			var ti struct {
				ShortDescription *string `json:"short_description"`
				DescriptionHTML  *string `json:"description_html"`
				ReportTextHTML   *string `json:"report_text_html"`
			}
			if err := json.Unmarshal(c.Input, &ti); err != nil {
				return Fields{}, fmt.Errorf("contenttranslate: decode tool input: %w", err)
			}
			return Fields{ShortDescription: ti.ShortDescription, DescriptionHTML: ti.DescriptionHTML, ReportTextHTML: ti.ReportTextHTML}, nil
		}
	}
	return Fields{}, ErrNoToolCall
}
