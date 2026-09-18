// macs_export_test.go — unit tests for the ?tickets=/?revoked_since= query
// param contract of GET .../sessions/{id}/macs-export (owner decision
// 2026-09-18). Pure unit tests — no live PostgreSQL required.
package hcatalog

import (
	"net/url"
	"testing"
	"time"

	"github.com/abhteam/arena_new/apps/backend/internal/platform/macs"
)

func TestParseMACSExportParams_DefaultIsAll(t *testing.T) {
	mode, since, errCode, _ := parseMACSExportParams(url.Values{})
	if errCode != "" {
		t.Fatalf("unexpected error code %q", errCode)
	}
	if mode != macs.ExportModeAll {
		t.Errorf("default mode = %q, want %q (backward compatibility)", mode, macs.ExportModeAll)
	}
	if since != nil {
		t.Errorf("default revokedSince = %v, want nil", since)
	}
}

func TestParseMACSExportParams_ValidAndRevokedModes(t *testing.T) {
	for _, want := range []macs.ExportMode{macs.ExportModeValid, macs.ExportModeRevoked, macs.ExportModeAll} {
		q := url.Values{"tickets": {string(want)}}
		mode, _, errCode, _ := parseMACSExportParams(q)
		if errCode != "" {
			t.Fatalf("tickets=%s: unexpected error code %q", want, errCode)
		}
		if mode != want {
			t.Errorf("tickets=%s: mode = %q, want %q", want, mode, want)
		}
	}
}

func TestParseMACSExportParams_InvalidTicketsValue(t *testing.T) {
	q := url.Values{"tickets": {"bogus"}}
	_, _, errCode, errMsg := parseMACSExportParams(q)
	if errCode != "macs.invalid_tickets_mode" {
		t.Errorf("errCode = %q, want macs.invalid_tickets_mode", errCode)
	}
	if errMsg == "" {
		t.Error("expected a non-empty error message")
	}
}

func TestParseMACSExportParams_RevokedSinceRequiresRevokedMode(t *testing.T) {
	q := url.Values{"tickets": {"valid"}, "revoked_since": {"2026-09-01T00:00:00Z"}}
	_, _, errCode, _ := parseMACSExportParams(q)
	if errCode != "macs.revoked_since_requires_revoked" {
		t.Errorf("errCode = %q, want macs.revoked_since_requires_revoked", errCode)
	}

	// Same check when tickets= is omitted (defaults to "all").
	q2 := url.Values{"revoked_since": {"2026-09-01T00:00:00Z"}}
	_, _, errCode2, _ := parseMACSExportParams(q2)
	if errCode2 != "macs.revoked_since_requires_revoked" {
		t.Errorf("errCode = %q, want macs.revoked_since_requires_revoked", errCode2)
	}
}

func TestParseMACSExportParams_RevokedSinceMustBeRFC3339(t *testing.T) {
	q := url.Values{"tickets": {"revoked"}, "revoked_since": {"2026-09-01"}}
	_, _, errCode, _ := parseMACSExportParams(q)
	if errCode != "macs.invalid_revoked_since" {
		t.Errorf("errCode = %q, want macs.invalid_revoked_since", errCode)
	}
}

func TestParseMACSExportParams_RevokedSinceParsedOnValidInput(t *testing.T) {
	q := url.Values{"tickets": {"revoked"}, "revoked_since": {"2026-09-01T12:30:00Z"}}
	mode, since, errCode, _ := parseMACSExportParams(q)
	if errCode != "" {
		t.Fatalf("unexpected error code %q", errCode)
	}
	if mode != macs.ExportModeRevoked {
		t.Errorf("mode = %q, want revoked", mode)
	}
	want := time.Date(2026, 9, 1, 12, 30, 0, 0, time.UTC)
	if since == nil || !since.Equal(want) {
		t.Errorf("revokedSince = %v, want %v", since, want)
	}
}
