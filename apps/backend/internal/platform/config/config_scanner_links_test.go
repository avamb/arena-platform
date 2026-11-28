package config

import (
	"strings"
	"testing"
)

func TestConfig_ScannerLinks(t *testing.T) {
	fp := strings.TrimSuffix(strings.Repeat("AA:", 32), ":")

	cfg := validProductionBase()
	cfg.ScannerIOSTeamID = "ABCDE12345"
	cfg.ScannerAndroidCertSHA256 = []string{fp}
	if err := cfg.Validate(); err != nil && strings.Contains(err.Error(), "SCANNER_") {
		t.Fatalf("valid scanner link settings refused: %v", err)
	}

	cfg = validProductionBase()
	cfg.ScannerAndroidCertSHA256 = []string{"not-a-fingerprint"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "SCANNER_ANDROID_CERT_SHA256") {
		t.Fatalf("a bad fingerprint must be refused, got %v", err)
	}

	cfg = validProductionBase()
	cfg.ScannerIOSTeamID = "short"
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "SCANNER_IOS_TEAM_ID") {
		t.Fatalf("a bad team id must be refused, got %v", err)
	}
}

func TestConfig_ScannerLinks_Parse(t *testing.T) {
	got := parseScannerCertList(" aa:bb , ,CC:DD ")
	if len(got) != 2 || got[0] != "AA:BB" || got[1] != "CC:DD" {
		t.Fatalf("unexpected %v", got)
	}
}
