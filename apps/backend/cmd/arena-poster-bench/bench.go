package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
)

// Spec is one "provider:model" to compare.
type Spec struct {
	Provider string
	Model    string
}

func (s Spec) String() string { return s.Provider + ":" + s.Model }

// ParseSpecs reads "anthropic:claude-sonnet-5-5,gemini:gemini-3.8-flash". The
// model is everything after the first colon, so ids with colons survive.
func ParseSpecs(list string) ([]Spec, error) {
	var out []Spec
	for _, part := range strings.Split(list, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		provider, model, ok := strings.Cut(part, ":")
		provider = strings.ToLower(strings.TrimSpace(provider))
		model = strings.TrimSpace(model)
		if !ok || model == "" || (provider != "anthropic" && provider != "gemini") {
			return nil, fmt.Errorf("bad model spec %q: want anthropic:<model> or gemini:<model>", part)
		}
		out = append(out, Spec{Provider: provider, Model: model})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no model specs given")
	}
	return out, nil
}

// Keys are the provider keys read from the environment; never printed.
type Keys struct {
	Anthropic string
	Gemini    string
}

// Reader builds the posterread reader of a spec, or an error naming the
// missing environment variable (not its value).
func (k Keys) Reader(s Spec, timeout time.Duration) (posterread.Reader, error) {
	client := &http.Client{Timeout: timeout}
	switch s.Provider {
	case "anthropic":
		if strings.TrimSpace(k.Anthropic) == "" {
			return nil, fmt.Errorf("%s needs ANTHROPIC_KEY", s)
		}
		return posterread.NewAnthropic(k.Anthropic, s.Model, "", client), nil
	case "gemini":
		if strings.TrimSpace(k.Gemini) == "" {
			return nil, fmt.Errorf("%s needs GEMINI_KEY", s)
		}
		return posterread.NewGemini(k.Gemini, s.Model, "", client), nil
	}
	return nil, fmt.Errorf("unknown provider %q", s.Provider)
}

// Run is one image read by one model.
type Run struct {
	File   string           `json:"file"`
	Spec   string           `json:"spec"`
	Millis int64            `json:"millis"`
	Error  string           `json:"error,omitempty"`
	Facts  posterread.Facts `json:"facts"`
	Score  Score            `json:"score"`
}

// mediaTypeOf maps a file name to the media type the readers accept.
func mediaTypeOf(name string) (string, bool) {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg", true
	case ".png":
		return "image/png", true
	}
	return "", false
}

// RunAll reads every truth image with every reader, one call at a time.
// readers is parallel to specs. An unreadable file or an unsupported type is
// recorded as an error of every spec, so the report shows it.
func RunAll(ctx context.Context, dir string, truths []Truth, specs []Spec, readers []posterread.Reader, timeout time.Duration) []Run {
	var runs []Run
	for _, t := range truths {
		mt, okType := mediaTypeOf(t.File)
		var data []byte
		var readErr error
		if !okType {
			readErr = fmt.Errorf("unsupported file type %q (jpg or png only)", filepath.Ext(t.File))
		} else {
			data, readErr = os.ReadFile(filepath.Join(dir, t.File))
		}
		for i, spec := range specs {
			run := Run{File: t.File, Spec: spec.String()}
			if readErr != nil {
				run.Error = readErr.Error()
				run.Score = ScoreFacts(posterread.Facts{}, t)
				runs = append(runs, run)
				continue
			}
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			started := time.Now()
			facts, err := readers[i].Read(callCtx, data, mt)
			cancel()
			run.Millis = time.Since(started).Milliseconds()
			if err != nil {
				run.Error = err.Error()
			}
			run.Facts = facts
			run.Score = ScoreFacts(facts, t)
			runs = append(runs, run)
		}
	}
	return runs
}
