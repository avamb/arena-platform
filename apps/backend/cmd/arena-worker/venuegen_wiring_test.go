package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/config"
)

func TestBuildVenueGenerator_WarnsOnAHalfConfiguredPair(t *testing.T) {
	const secret = "s3cr3t-token-value"
	cases := []struct {
		name, url, token string
		wantOn           bool
		wantLevel        string
		wantText         string
	}{
		{"both empty", "", "", false, "level=INFO", "venue generator is off"},
		{"both set", "http://venue-gen:8090", secret, true, "", ""},
		{"token missing", "http://venue-gen:8090", "", false, "level=WARN", "VENUE_GEN_TOKEN is not set"},
		{"url missing", "", secret, false, "level=WARN", "VENUE_GEN_URL is not set"},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		cfg := &config.Config{VenueGenURL: tc.url, VenueGenToken: tc.token}
		g := buildVenueGenerator(cfg, logger)
		if (g != nil) != tc.wantOn {
			t.Errorf("%s: generator on = %v, want %v", tc.name, g != nil, tc.wantOn)
		}
		out := buf.String()
		if tc.wantOn {
			if out != "" {
				t.Errorf("%s: unexpected log %q", tc.name, out)
			}
			continue
		}
		if !strings.Contains(out, tc.wantLevel) || !strings.Contains(out, tc.wantText) {
			t.Errorf("%s: log %q, want %s and %q", tc.name, out, tc.wantLevel, tc.wantText)
		}
		if strings.Contains(out, secret) {
			t.Errorf("%s: the log carries the token", tc.name)
		}
	}
}
