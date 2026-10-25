// Command arena-poster-bench compares vision models at reading event posters.
// It is an operator tool: it is not built into the image and it never runs in
// production. It reads a directory of poster images and a truth.json that says
// what each poster really prints, runs every "provider:model" you list over
// every image and writes a JSON result plus a Markdown summary.
//
//	ANTHROPIC_KEY=... GEMINI_KEY=... go run ./apps/backend/cmd/arena-poster-bench \
//	  -dir ./posters -models anthropic:claude-sonnet-5-5,gemini:gemini-3.8-flash
//
// truth.json is a list of {"file","title","date","time","venue","city",
// "prices":[25,34.9],"age"}; any field may be null (unknown, not scored).
// The keys come only from the environment and are never printed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "arena-poster-bench:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("arena-poster-bench", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory with the poster images (jpg/png)")
	truthPath := fs.String("truth", "", "truth.json (default: <dir>/truth.json)")
	models := fs.String("models", "", "comma list of provider:model, e.g. anthropic:claude-sonnet-5-5,gemini:gemini-3.8-flash")
	out := fs.String("out", "poster-bench", "output path without extension: writes <out>.json and <out>.md")
	timeout := fs.Duration("timeout", 60*time.Second, "limit of one model call")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("-dir is required")
	}
	specs, err := ParseSpecs(*models)
	if err != nil {
		return err
	}
	tp := *truthPath
	if tp == "" {
		tp = filepath.Join(*dir, "truth.json")
	}
	truths, err := LoadTruth(tp)
	if err != nil {
		return err
	}
	keys := Keys{Anthropic: os.Getenv("ANTHROPIC_KEY"), Gemini: os.Getenv("GEMINI_KEY")}
	readers := make([]posterread.Reader, len(specs))
	for i, s := range specs {
		r, err := keys.Reader(s, *timeout)
		if err != nil {
			return err
		}
		readers[i] = r
	}

	runs := RunAll(context.Background(), *dir, truths, specs, readers, *timeout)
	rep := NewReport(specs, runs, time.Now())
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out+".json", raw, 0o600); err != nil {
		return err
	}
	md := rep.Markdown()
	if err := os.WriteFile(*out+".md", []byte(md), 0o600); err != nil {
		return err
	}
	fmt.Print(md)
	fmt.Fprintf(os.Stderr, "\nwrote %s.json and %s.md\n", *out, *out)
	return nil
}

// LoadTruth reads and checks truth.json.
func LoadTruth(path string) ([]Truth, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read truth: %w", err)
	}
	var truths []Truth
	if err := json.Unmarshal(raw, &truths); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(truths) == 0 {
		return nil, fmt.Errorf("%s lists no posters", path)
	}
	for i, t := range truths {
		if strings.TrimSpace(t.File) == "" {
			return nil, fmt.Errorf("%s: entry %d has no file", path, i)
		}
		if filepath.Base(t.File) != t.File {
			return nil, fmt.Errorf("%s: entry %d: file %q must be a name inside -dir", path, i, t.File)
		}
	}
	return truths, nil
}
