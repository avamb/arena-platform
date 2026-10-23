//go:build e2eposter

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"
	"github.com/abhteam/arena_new/apps/backend/internal/platform/posterread"
)

// This file exists ONLY in a binary built with `-tags e2eposter` (the Event
// Center live harness does that). It replaces the vision model with a scripted
// reader, so the real route POST /v1/organizations/{org}/media/{id}/poster-facts
// (auth, org access, media lookup, quota, bytes read) runs for real and only the
// model call is faked. It holds no key, makes no network call and has no fallback
// in a normal build.
//
//	E2E_POSTER_SCRIPT  path of a JSON file read on EVERY call:
//	                   {"mode":"facts","facts":{...},"delay_ms":0}
//	                   {"mode":"fail"}            -> a read error (route answers 503 poster_read_failed)
//	                   {"mode":"not_configured"}  -> posterread.ErrNotConfigured
//	                   a missing file behaves like not_configured.
//	E2E_POSTER_CALLS   path of a file that gets one JSON line per call:
//	                   {"sha256":"...","media_type":"image/jpeg","bytes":123}
type scriptedReader struct {
	script string
	calls  string
	mu     sync.Mutex
}

type readerScript struct {
	Mode    string           `json:"mode"`
	Facts   posterread.Facts `json:"facts"`
	DelayMS int              `json:"delay_ms"`
}

func injectedPosterReader() httpserver.PosterReader {
	script := os.Getenv("E2E_POSTER_SCRIPT")
	if script == "" {
		return nil
	}
	return &scriptedReader{script: script, calls: os.Getenv("E2E_POSTER_CALLS")}
}

func (r *scriptedReader) Read(ctx context.Context, image []byte, mediaType string) (posterread.Facts, error) {
	if r.calls != "" {
		sum := sha256.Sum256(image)
		line, _ := json.Marshal(map[string]any{"sha256": hex.EncodeToString(sum[:]), "media_type": mediaType, "bytes": len(image)})
		r.mu.Lock()
		if f, err := os.OpenFile(r.calls, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
		r.mu.Unlock()
	}
	raw, err := os.ReadFile(r.script)
	if err != nil {
		return posterread.Facts{}, posterread.ErrNotConfigured
	}
	var s readerScript
	if err := json.Unmarshal(raw, &s); err != nil {
		return posterread.Facts{}, errors.New("e2e poster script is not valid JSON")
	}
	if s.DelayMS > 0 {
		select {
		case <-time.After(time.Duration(s.DelayMS) * time.Millisecond):
		case <-ctx.Done():
			return posterread.Facts{}, ctx.Err()
		}
	}
	switch s.Mode {
	case "facts":
		return s.Facts, nil
	case "not_configured":
		return posterread.Facts{}, posterread.ErrNotConfigured
	default:
		return posterread.Facts{}, errors.New("scripted poster read failure")
	}
}
