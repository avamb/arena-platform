// pr2_30_388_test.go — regression pins for feature #388 (PR2-30 BLOCKER):
// "Make the CI integration gate actually run the resale BLOCKER proofs."
//
// The 2026-07-19 adversarial audit found the integration gate was green
// theater — three independent failures meant the PR2-04/PR2-27 resale
// regression proofs never executed:
//
//  1. The integration job env lacked JWT_SIGNING_SECRET, so arena-migrate
//     exited at config.Load ("JWT_SIGNING_SECRET is required when
//     ENABLE_DEV_AUTH=true") before any test ran.
//  2. The tests' precondition query joined sessions.org_id — a column that
//     does not exist (org ownership goes sessions → events → org) — so
//     row.Scan always errored and the tests skipped unconditionally.
//  3. arena-seed inserted no event/session/inventory rows, so the
//     (org, channel, session) triple could never resolve even with a
//     correct join.
//
// The live proof is the gate itself: `go test -tags=integration ./...` in CI
// now executes TestPR204Integration_* and TestPR227Integration_* against a
// migrated + seeded PostgreSQL. The structural pins below only prevent the
// three root causes from silently regressing; they are NOT the proof.
package httpserver

import (
	"os"
	"strings"
	"testing"
)

// pr230ReadPackageFile reads a file that lives in this package's directory
// (go test sets CWD to the package dir). The integration test files carry a
// build tag, so they are on disk but outside findFileByName's whitelist.
func pr230ReadPackageFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// pr230IntegrationJobSection extracts the integration job block from ci.yml.
func pr230IntegrationJobSection(t *testing.T) string {
	t.Helper()
	content := ciWorkflowContent(t)
	integIdx := strings.Index(content, "integration:")
	if integIdx < 0 {
		t.Fatal("ci.yml missing integration: job")
	}
	nextJobIdx := strings.Index(content[integIdx:], "openapi-check:")
	if nextJobIdx < 0 {
		nextJobIdx = len(content) - integIdx
	}
	return content[integIdx : integIdx+nextJobIdx]
}

// TestPR230_CIGate_IntegrationJobHasJWTSigningSecret pins root cause 1:
// without JWT_SIGNING_SECRET in the job env, arena-migrate and arena-seed
// exit at config.Load and the gate dies before running a single test.
func TestPR230_CIGate_IntegrationJobHasJWTSigningSecret(t *testing.T) {
	section := pr230IntegrationJobSection(t)
	if !strings.Contains(section, "JWT_SIGNING_SECRET") {
		t.Error("ci.yml integration job env missing JWT_SIGNING_SECRET; " +
			"arena-migrate/arena-seed exit at config.Load (ENABLE_DEV_AUTH defaults " +
			"to true under APP_ENV=development) and the gate never runs a test")
	}
}

// TestPR230_ResaleProofs_JoinThroughEvents pins root cause 2: the
// precondition query must resolve the org through events (sessions has no
// org_id column). A query against sessions.org_id always errors, and before
// #388 that error was converted into an unconditional skip.
func TestPR230_ResaleProofs_JoinThroughEvents(t *testing.T) {
	for _, file := range []string{
		"checkout_pr2_04_360_integration_test.go",
		"checkout_pr2_27_383_integration_test.go",
	} {
		content := pr230ReadPackageFile(t, file)
		if strings.Contains(content, "s.org_id = sc.org_id") {
			t.Errorf("%s still joins on sessions.org_id — that column does not exist; "+
				"the precondition Scan always errors and the proof never runs", file)
		}
		if !strings.Contains(content, "s.event_id = e.id") {
			t.Errorf("%s must resolve the org through events (sessions → events → org)", file)
		}
	}
}

// TestPR230_ResaleProofs_DoNotSkipOnPrecondition pins root cause 2's second
// half: precondition failures must fail the gate, not skip it. The only
// legitimate skip is the local-developer escape hatch when DATABASE_URL is
// entirely unset.
func TestPR230_ResaleProofs_DoNotSkipOnPrecondition(t *testing.T) {
	for _, file := range []string{
		"checkout_pr2_04_360_integration_test.go",
		"checkout_pr2_27_383_integration_test.go",
	} {
		content := pr230ReadPackageFile(t, file)
		skips := strings.Count(content, "t.Skip")
		if skips > 1 {
			t.Errorf("%s contains %d t.Skip calls; only the DATABASE_URL-unset guard "+
				"may skip — precondition failures must t.Fatal so the gate stays honest", file, skips)
		}
	}
}
