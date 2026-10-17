//go:build !e2eposter

package main

import "github.com/abhteam/arena_new/apps/backend/internal/platform/httpserver"

// injectedPosterReader is the test seam of the live browser acceptance
// (poster_reader_e2e.go, build tag e2eposter). In every normal build it returns
// a nil interface, so the server builds its own reader from POSTER_LLM_API_KEY.
func injectedPosterReader() httpserver.PosterReader { return nil }
