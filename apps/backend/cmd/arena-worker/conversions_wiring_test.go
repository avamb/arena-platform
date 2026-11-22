package main

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

// TestConversionsWiring_MetaBoxAndSenders: the worker opens the Meta token
// key only when the Meta connection is on, and aims the Meta sender at the
// configured Graph host and version.
func TestConversionsWiring_MetaBoxAndSenders(t *testing.T) {
	key := "v1:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	on := &config.Config{MetaAppID: "1", MetaAppSecret: "s", MetaLoginConfigID: "c", MetaTokenEncKey: key,
		MetaGraphVersion: "v25.0", MetaGraphBaseURL: "http://graph.stub"}
	if b := buildMetaTokenBox(on, testLogger()); b == nil || b.CurrentKeyID() != "v1" {
		t.Fatalf("box with Meta on = %v", b)
	}
	off := &config.Config{MetaTokenEncKey: key}
	if b := buildMetaTokenBox(off, testLogger()); b != nil {
		t.Fatal("box built while the Meta connection is off")
	}
	if b := buildMetaTokenBox(nil, testLogger()); b != nil {
		t.Fatal("box built from a nil config")
	}
	s := conversionsSenders(on)
	if s.Meta.BaseURL != "http://graph.stub" || s.Meta.Version != "v25.0" {
		t.Fatalf("meta sender = %+v", s.Meta)
	}
	if buildConversionsStore(nil, off, testLogger()) == nil {
		t.Fatal("no store")
	}
}
