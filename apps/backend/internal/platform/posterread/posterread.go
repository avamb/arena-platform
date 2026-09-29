// Package posterread reads an event poster with a vision model and returns
// the facts printed on it — the name, the date and time, the venue, the
// age rating, the prices — so the Telegram event-center wizard can offer
// them as one-tap answers (owner decision 2026-09-29: "подключить нейронку,
// чтобы она прочитала афишу и предложила подставить").
//
// Privacy: only the poster image travels to the model — public marketing
// material by nature — never a buyer's data, never the organization's
// keys. The model is Anthropic's Claude over the Messages API; the
// extraction is forced through a tool call so the answer is structured
// JSON, not prose to parse. Every value is a HINT: the wizard shows it as
// a button the organizer confirms, and never writes it on its own.
package posterread

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Facts is what a poster says, normalized: empty means "not on the poster".
type Facts struct {
	Name        string     `json:"name"`
	Date        string     `json:"date"` // YYYY-MM-DD
	Time        string     `json:"time"` // HH:MM, 24h
	VenueName   string     `json:"venue_name"`
	City        string     `json:"city"`
	Age         string     `json:"age"` // "0+", "6+", "12+", "16+", "18+"
	Description string     `json:"description"`
	Categories  []Category `json:"categories"`
}

// Category is one ticket price printed on the poster.
type Category struct {
	Name       string `json:"name"`
	PriceMinor int64  `json:"price_minor"`
	Currency   string `json:"currency"` // ISO 4217, "" when the poster shows no symbol
}

// Empty reports whether nothing usable was read.
func (f Facts) Empty() bool {
	return f.Name == "" && f.Date == "" && f.Time == "" && f.VenueName == "" && f.City == "" &&
		f.Age == "" && f.Description == "" && len(f.Categories) == 0
}

// Reader reads a poster. mediaType is image/jpeg or image/png.
type Reader interface {
	Read(ctx context.Context, image []byte, mediaType string) (Facts, error)
}

// ErrNotConfigured is returned by a Reader built without an API key.
var ErrNotConfigured = errors.New("posterread: no API key configured")

const (
	// DefaultModel is a fast vision model; the poster is a page of large
	// print, not a reasoning task. Overridable through POSTER_LLM_MODEL.
	DefaultModel = "claude-sonnet-5"
	// DefaultBaseURL is the Messages API origin.
	DefaultBaseURL = "https://api.anthropic.com"
	// anthropicVersion is the API version header every call carries.
	anthropicVersion = "2023-06-01"
	// maxImageBytes is the API's own per-image ceiling.
	maxImageBytes = 5 << 20
	// requestTimeout bounds one read; the wizard shows "reading…" meanwhile.
	requestTimeout = 45 * time.Second
	toolName       = "poster_facts"
)

// Anthropic reads posters through the Claude Messages API.
type Anthropic struct {
	apiKey  string
	model   string
	baseURL string
	http    *http.Client
}

// NewAnthropic builds a reader; an empty apiKey yields one that answers
// ErrNotConfigured (the wizard then simply offers no hints). Empty model and
// baseURL take the defaults; a nil client gets one with requestTimeout.
func NewAnthropic(apiKey, model, baseURL string, client *http.Client) *Anthropic {
	if model == "" {
		model = DefaultModel
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &Anthropic{apiKey: strings.TrimSpace(apiKey), model: model, baseURL: strings.TrimRight(baseURL, "/"), http: client}
}

// Configured reports whether the reader has a key.
func (a *Anthropic) Configured() bool { return a != nil && a.apiKey != "" }

// toolSchema is the JSON schema the model fills in. Every field is optional:
// a poster rarely says everything, and a missing value must come back
// empty, never invented.
var toolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"name":        map[string]any{"type": "string", "description": "The event's title as printed, without the venue or date."},
		"date":        map[string]any{"type": "string", "description": "The event date as YYYY-MM-DD; empty when the poster shows none or the year is unclear."},
		"time":        map[string]any{"type": "string", "description": "The start time as HH:MM in 24-hour clock; empty when absent."},
		"venue_name":  map[string]any{"type": "string", "description": "The venue's name as printed (a hall, a theatre, a club), without the address."},
		"city":        map[string]any{"type": "string", "description": "The city, when printed."},
		"age":         map[string]any{"type": "string", "description": "The age rating exactly as one of 0+, 6+, 12+, 16+, 18+; empty when absent."},
		"description": map[string]any{"type": "string", "description": "A short description for buyers taken from the poster's own text (up to 400 characters); empty when the poster has no such text."},
		"categories": map[string]any{
			"type":        "array",
			"description": "Ticket prices printed on the poster, one per category.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":     map[string]any{"type": "string", "description": "The category's name; 'Ticket' when the poster prints a single price with no name."},
					"price":    map[string]any{"type": "number", "description": "The price in major units, e.g. 25 or 34.90."},
					"currency": map[string]any{"type": "string", "description": "ISO 4217 code of the printed currency (EUR, CZK, USD…); empty when no symbol is printed."},
				},
				"required": []string{"name", "price"},
			},
		},
	},
}

const systemPrompt = "You read event posters for a ticketing platform. Extract only what is printed on the poster; " +
	"never guess a missing value and never invent one. Dates: when the year is not printed, leave the date empty. " +
	"Return the facts through the poster_facts tool."

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
	Content []struct {
		Type  string          `json:"type"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// rawFacts is the tool input as the model writes it (prices in major units).
type rawFacts struct {
	Name        string `json:"name"`
	Date        string `json:"date"`
	Time        string `json:"time"`
	VenueName   string `json:"venue_name"`
	City        string `json:"city"`
	Age         string `json:"age"`
	Description string `json:"description"`
	Categories  []struct {
		Name     string  `json:"name"`
		Price    float64 `json:"price"`
		Currency string  `json:"currency"`
	} `json:"categories"`
}

// Read sends the poster and returns the normalized facts.
func (a *Anthropic) Read(ctx context.Context, image []byte, mediaType string) (Facts, error) {
	if !a.Configured() {
		return Facts{}, ErrNotConfigured
	}
	if len(image) == 0 || len(image) > maxImageBytes {
		return Facts{}, fmt.Errorf("posterread: image of %d bytes is outside the 1..%d range", len(image), maxImageBytes)
	}
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		return Facts{}, fmt.Errorf("posterread: unsupported media type %q", mediaType)
	}
	body := messagesRequest{
		Model:     a.model,
		MaxTokens: 1024,
		System:    systemPrompt,
		Tools: []toolDef{{
			Name:        toolName,
			Description: "Record the facts printed on an event poster.",
			InputSchema: toolSchema,
		}},
		ToolChoice: map[string]any{"type": "tool", "name": toolName},
		Messages: []messageContent{{
			Role: "user",
			Content: []map[string]any{
				{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": base64.StdEncoding.EncodeToString(image)}},
				{"type": "text", "text": "Read this event poster and record its facts with the poster_facts tool."},
			},
		}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Facts{}, fmt.Errorf("posterread: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return Facts{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	res, err := a.http.Do(req)
	if err != nil {
		return Facts{}, fmt.Errorf("posterread: call model: %w", err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Facts{}, fmt.Errorf("posterread: read answer: %w", err)
	}
	var parsed messagesResponse
	_ = json.Unmarshal(out, &parsed)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		msg := strings.TrimSpace(string(out))
		if parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Type + ": " + parsed.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return Facts{}, fmt.Errorf("posterread: model answered %d: %s", res.StatusCode, msg)
	}
	for _, c := range parsed.Content {
		if c.Type == "tool_use" && c.Name == toolName {
			var rf rawFacts
			if err := json.Unmarshal(c.Input, &rf); err != nil {
				return Facts{}, fmt.Errorf("posterread: decode tool input: %w", err)
			}
			return normalize(rf), nil
		}
	}
	return Facts{}, errors.New("posterread: the model returned no poster_facts tool call")
}

var (
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	timeRe = regexp.MustCompile(`^(\d{1,2})[:.](\d{1,2})$`)
	ageRe  = regexp.MustCompile(`^\s*(\d{1,2})\s*\+?\s*$`)
)

// normalize trims every value and drops what does not pass the wizard's own
// formats: a date that is not a real calendar day, a time outside 24h, an
// age outside the five ratings, a price that is not positive.
func normalize(rf rawFacts) Facts {
	f := Facts{
		Name:        clip(rf.Name, 200),
		VenueName:   clip(rf.VenueName, 120),
		City:        clip(rf.City, 80),
		Description: clip(rf.Description, 4000),
	}
	if d := strings.TrimSpace(rf.Date); dateRe.MatchString(d) {
		if t, err := time.Parse("2006-01-02", d); err == nil && t.Year() >= 2000 && t.Year() <= 2100 {
			f.Date = d
		}
	}
	if m := timeRe.FindStringSubmatch(strings.TrimSpace(rf.Time)); m != nil {
		var h, mi int
		_, _ = fmt.Sscanf(m[1]+" "+m[2], "%d %d", &h, &mi)
		if h >= 0 && h <= 23 && mi >= 0 && mi <= 59 {
			f.Time = fmt.Sprintf("%02d:%02d", h, mi)
		}
	}
	if m := ageRe.FindStringSubmatch(rf.Age); m != nil {
		switch m[1] {
		case "0", "6", "12", "16", "18":
			f.Age = m[1] + "+"
		}
	}
	for _, c := range rf.Categories {
		name := clip(c.Name, 80)
		if name == "" || c.Price <= 0 || c.Price > 1_000_000 {
			continue
		}
		cur := strings.ToUpper(strings.TrimSpace(c.Currency))
		if len(cur) != 3 || strings.Trim(cur, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
			cur = ""
		}
		f.Categories = append(f.Categories, Category{Name: name, PriceMinor: int64(c.Price*100 + 0.5), Currency: cur})
	}
	return f
}

func clip(s string, max int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > max {
		return strings.TrimSpace(string(r[:max]))
	}
	return s
}
