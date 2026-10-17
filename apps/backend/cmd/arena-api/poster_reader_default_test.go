//go:build !e2eposter

package main

import "testing"

// A normal build has no injected reader: the poster route answers 503 "not
// configured" without POSTER_LLM_API_KEY and never falls back to a fake.
func TestInjectedPosterReaderIsNilInNormalBuild(t *testing.T) {
	t.Setenv("E2E_POSTER_SCRIPT", "should-be-ignored.json")
	if r := injectedPosterReader(); r != nil {
		t.Fatalf("a normal build must not inject a poster reader, got %T", r)
	}
}
