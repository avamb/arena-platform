//go:build e2eposter

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
)

func TestScriptedReaderFollowsItsScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "script.json")
	calls := filepath.Join(dir, "calls.log")
	t.Setenv("E2E_POSTER_SCRIPT", script)
	t.Setenv("E2E_POSTER_CALLS", calls)
	r := injectedPosterReader()
	if r == nil {
		t.Fatal("the tagged build must inject a reader when a script is named")
	}
	// No script file: behaves as not configured.
	if _, err := r.Read(context.Background(), []byte("img"), "image/jpeg"); !errors.Is(err, posterread.ErrNotConfigured) {
		t.Fatalf("missing script: want ErrNotConfigured, got %v", err)
	}
	if err := os.WriteFile(script, []byte(`{"mode":"facts","facts":{"name":"Swan Lake","categories":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := r.Read(context.Background(), []byte("img"), "image/jpeg")
	if err != nil || f.Name != "Swan Lake" {
		t.Fatalf("facts mode: got %+v, %v", f, err)
	}
	if err := os.WriteFile(script, []byte(`{"mode":"fail"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(context.Background(), []byte("img"), "image/jpeg"); err == nil || errors.Is(err, posterread.ErrNotConfigured) {
		t.Fatalf("fail mode must be a plain read error, got %v", err)
	}
	log, _ := os.ReadFile(calls)
	if n := len(log); n == 0 {
		t.Fatal("every call must be logged")
	}
}
