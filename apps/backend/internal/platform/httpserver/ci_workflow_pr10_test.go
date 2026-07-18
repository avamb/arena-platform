package httpserver

// PR-10 — CI release graph regression tests
//
// These tests assert the dependency graph and structural properties of
// .github/workflows/ci.yml so that a future edit cannot silently drop a
// required publication gate.
//
// Acceptance criteria (PR-10):
//   - A dedicated tagged integration job runs go test -tags=integration.
//   - Admin unit/build/container smoke job (admin-build) exists.
//   - Widget unit/build/size gates exist; real-backend acceptance (widget-acceptance)
//     is a required publication gate; mock-only E2E is non-gating.
//   - build-and-push and widget-publish both depend on ALL required gates.
//   - Failed/skipped required job prevents publication (needs: enforces this).
//   - PR builds never publish (if: master + push condition).
//   - Jobs have explicit timeouts.
//   - Concurrency cancellation is configured.
//   - Least-privilege top-level permissions are declared.

import (
	"strings"
	"testing"
)

// requiredGates lists every job that must appear in the needs: list of both
// build-and-push and widget-publish. Changing this list (without updating the
// workflow) will cause TestCIWorkflowPR10_DependencyGraph to fail.
var requiredGates = []string{
	"lint",
	"test",
	"integration",
	"openapi-check",
	"admin-build",
	"widget",
	"widget-acceptance",
}

// ─── Integration job ──────────────────────────────────────────────────────────

func TestCIWorkflowPR10_IntegrationJobExists(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "integration:") && !strings.Contains(content, "Integration (PostgreSQL") {
		t.Error("ci.yml missing integration job")
	}
}

func TestCIWorkflowPR10_IntegrationJobRunsTaggedTests(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "-tags=integration") {
		t.Error("ci.yml integration job does not run go test -tags=integration")
	}
}

func TestCIWorkflowPR10_IntegrationJobHasPostgresService(t *testing.T) {
	content := ciWorkflowContent(t)
	// The integration job needs its own postgres service (separate DB from test job)
	if strings.Count(content, "postgres:17") < 2 {
		t.Error("ci.yml must have at least two postgres:17 service declarations (test + integration jobs)")
	}
}

func TestCIWorkflowPR10_IntegrationJobHasTimeout(t *testing.T) {
	content := ciWorkflowContent(t)
	// The integration job must have a timeout long enough for Testcontainers
	if !strings.Contains(content, "timeout-minutes: 30") {
		t.Error("ci.yml integration job missing timeout-minutes: 30 (Testcontainers need time)")
	}
}

func TestCIWorkflowPR10_IntegrationJobUploadsFailureArtifacts(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "integration-test-failure-logs") {
		t.Error("ci.yml integration job missing failure artifact upload (integration-test-failure-logs)")
	}
}

// ─── Admin job ────────────────────────────────────────────────────────────────

func TestCIWorkflowPR10_AdminBuildJobExists(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "admin-build") {
		t.Error("ci.yml missing admin-build job")
	}
}

func TestCIWorkflowPR10_AdminBuildJobRunsUnitTests(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "npm --prefix apps/admin-web test") {
		t.Error("ci.yml admin-build job does not run admin unit tests")
	}
}

func TestCIWorkflowPR10_AdminBuildJobRunsTypeCheck(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "npm --prefix apps/admin-web run type-check") {
		t.Error("ci.yml admin-build job does not run type-check")
	}
}

func TestCIWorkflowPR10_AdminBuildJobRunsProductionBuild(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "npm --prefix apps/admin-web run build") {
		t.Error("ci.yml admin-build job does not run production build")
	}
}

func TestCIWorkflowPR10_AdminBuildJobRunsContainerSmoke(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "Container smoke test") && !strings.Contains(content, "arena-admin-web:ci") {
		t.Error("ci.yml admin-build job missing container smoke test")
	}
}

func TestCIWorkflowPR10_AdminBuildJobUploadsFailureArtifacts(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "admin-build-failure-logs") {
		t.Error("ci.yml admin-build job missing failure artifact upload (admin-build-failure-logs)")
	}
}

// ─── Widget job ───────────────────────────────────────────────────────────────

func TestCIWorkflowPR10_WidgetJobHasUnitTests(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "npm test") && !strings.Contains(content, "npm run test") {
		t.Error("ci.yml widget job missing unit tests")
	}
}

func TestCIWorkflowPR10_WidgetJobHasSizeGate(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "npm run size") {
		t.Error("ci.yml widget job missing size gate")
	}
}

func TestCIWorkflowPR10_WidgetAcceptanceJobExists(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "widget-acceptance:") {
		t.Error("ci.yml missing widget-acceptance job (real backend E2E)")
	}
}

func TestCIWorkflowPR10_WidgetPublishIsSeparateFromWidgetBuild(t *testing.T) {
	content := ciWorkflowContent(t)
	// widget-publish must be a separate job (not inside widget job)
	if !strings.Contains(content, "widget-publish:") {
		t.Error("ci.yml missing widget-publish job (publication must be gated separately from build)")
	}
}

// ─── Dependency graph (publication gates) ────────────────────────────────────

// TestCIWorkflowPR10_DependencyGraph asserts that both build-and-push and
// widget-publish list every required gate in their needs: blocks. This is the
// primary regression guard: a future removal of a gate from needs: will fail
// this test before CI is silently broken.
func TestCIWorkflowPR10_DependencyGraph(t *testing.T) {
	content := ciWorkflowContent(t)

	// Locate the build-and-push needs block.
	// Strategy: find "build-and-push:" then scan the next 300 chars for each gate.
	bapIdx := strings.Index(content, "build-and-push:")
	if bapIdx < 0 {
		t.Fatal("ci.yml missing build-and-push job")
	}
	// Look within a reasonable window after "build-and-push:" for the needs block.
	// The needs: block must appear before the "steps:" key of the same job.
	bapWindow := content[bapIdx:]
	stepsIdx := strings.Index(bapWindow, "    steps:")
	if stepsIdx < 0 {
		stepsIdx = 500
	}
	bapNeeds := bapWindow[:stepsIdx]

	for _, gate := range requiredGates {
		if !strings.Contains(bapNeeds, gate) {
			t.Errorf("build-and-push does not list required gate %q in needs:", gate)
		}
	}

	// Locate the widget-publish needs block.
	wpIdx := strings.Index(content, "widget-publish:")
	if wpIdx < 0 {
		t.Fatal("ci.yml missing widget-publish job")
	}
	wpWindow := content[wpIdx:]
	wpStepsIdx := strings.Index(wpWindow, "    steps:")
	if wpStepsIdx < 0 {
		wpStepsIdx = 500
	}
	wpNeeds := wpWindow[:wpStepsIdx]

	for _, gate := range requiredGates {
		if !strings.Contains(wpNeeds, gate) {
			t.Errorf("widget-publish does not list required gate %q in needs:", gate)
		}
	}
}

// ─── Publication guard (PRs must not publish) ─────────────────────────────────

func TestCIWorkflowPR10_BuildAndPushOnlyOnMasterPush(t *testing.T) {
	content := ciWorkflowContent(t)
	// Both the if: condition and the job itself must be present.
	if !strings.Contains(content, "refs/heads/master") {
		t.Error("ci.yml missing master branch condition on build-and-push")
	}
	// Verify the event_name == push guard exists.
	if !strings.Contains(content, "github.event_name == 'push'") {
		t.Error("ci.yml missing event_name == 'push' guard; PR builds may publish")
	}
}

func TestCIWorkflowPR10_WidgetPublishOnlyOnMasterPush(t *testing.T) {
	content := ciWorkflowContent(t)
	// widget-publish must have the same master+push guard.
	wpIdx := strings.Index(content, "widget-publish:")
	if wpIdx < 0 {
		t.Fatal("ci.yml missing widget-publish job")
	}
	wpSection := content[wpIdx:]
	if !strings.Contains(wpSection, "refs/heads/master") {
		t.Error("widget-publish missing refs/heads/master condition; it may publish from PRs")
	}
}

// ─── Timeouts ─────────────────────────────────────────────────────────────────

func TestCIWorkflowPR10_JobsHaveTimeouts(t *testing.T) {
	content := ciWorkflowContent(t)

	// Count that the number of timeout-minutes declarations equals the number
	// of jobs. We have 9 jobs (lint, test, integration, openapi-check,
	// admin-build, widget, widget-acceptance, build-and-push, widget-publish)
	// so there must be exactly 9 timeout-minutes declarations.
	const expectedJobCount = 9
	timeoutCount := strings.Count(content, "timeout-minutes:")
	if timeoutCount < expectedJobCount {
		t.Errorf("ci.yml has only %d timeout-minutes: declarations; expected at least %d (one per job)", timeoutCount, expectedJobCount)
	}

	// Also verify that the integration job (which needs the most time) has a
	// sufficiently long timeout for Testcontainers.
	if !strings.Contains(content, "timeout-minutes: 30") {
		t.Error("ci.yml missing timeout-minutes: 30 (required for integration/widget-acceptance/build jobs)")
	}
}

// ─── Concurrency cancellation ─────────────────────────────────────────────────

func TestCIWorkflowPR10_ConcurrencyCancellationConfigured(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "concurrency:") {
		t.Error("ci.yml missing top-level concurrency: block")
	}
	if !strings.Contains(content, "cancel-in-progress: true") {
		t.Error("ci.yml missing cancel-in-progress: true in concurrency block")
	}
}

// ─── Least-privilege permissions ──────────────────────────────────────────────

func TestCIWorkflowPR10_TopLevelPermissionsLeastPrivilege(t *testing.T) {
	content := ciWorkflowContent(t)
	// Top-level permissions: contents: read must appear before the first job.
	jobsIdx := strings.Index(content, "\njobs:")
	if jobsIdx < 0 {
		t.Fatal("ci.yml missing jobs: section")
	}
	preamble := content[:jobsIdx]
	if !strings.Contains(preamble, "permissions:") {
		t.Error("ci.yml missing top-level permissions: declaration (least-privilege requirement)")
	}
	if !strings.Contains(preamble, "contents: read") {
		t.Error("ci.yml top-level permissions does not set contents: read (least-privilege default)")
	}
}

// ─── Failure artifacts ────────────────────────────────────────────────────────

func TestCIWorkflowPR10_FailureArtifactsUploaded(t *testing.T) {
	content := ciWorkflowContent(t)
	// Each critical job should upload artifacts on failure.
	artifacts := []string{
		"integration-test-failure-logs",
		"admin-build-failure-logs",
	}
	for _, artifact := range artifacts {
		if !strings.Contains(content, artifact) {
			t.Errorf("ci.yml missing failure artifact: %s", artifact)
		}
	}
}

// ─── All required jobs present (PR-10 superset) ───────────────────────────────

func TestCIWorkflowPR10_AllRequiredJobsPresent(t *testing.T) {
	content := ciWorkflowContent(t)
	allJobs := []string{
		"lint",
		"test",
		"integration",
		"openapi-check",
		"admin-build",
		"widget",
		"widget-acceptance",
		"build-and-push",
		"widget-publish",
	}
	for _, job := range allJobs {
		if !strings.Contains(content, job) {
			t.Errorf("ci.yml missing required job: %s", job)
		}
	}
}

// ─── Deliberate gate failure demonstration ────────────────────────────────────

// TestCIWorkflowPR10_GateEnforcement verifies that the needs: structure
// enforces the gate. This is a structural test: if build-and-push needs
// integration, then a failing integration job would skip build-and-push.
// We verify this by asserting that both jobs exist and that build-and-push
// declares integration as a dependency.
func TestCIWorkflowPR10_GateEnforcement(t *testing.T) {
	content := ciWorkflowContent(t)

	if !strings.Contains(content, "integration:") {
		t.Fatal("ci.yml missing integration job — gate cannot be enforced")
	}

	bapIdx := strings.Index(content, "build-and-push:")
	if bapIdx < 0 {
		t.Fatal("ci.yml missing build-and-push job")
	}

	// Verify integration appears in build-and-push's needs block.
	bapSection := content[bapIdx:]
	stepsIdx := strings.Index(bapSection, "    steps:")
	if stepsIdx < 0 {
		stepsIdx = 500
	}
	needsBlock := bapSection[:stepsIdx]

	if !strings.Contains(needsBlock, "integration") {
		t.Error("build-and-push does not declare integration as a dependency — gate not enforced")
	}

	// Verify widget-acceptance appears (real backend gate, not mock-only).
	if !strings.Contains(needsBlock, "widget-acceptance") {
		t.Error("build-and-push does not declare widget-acceptance as a dependency — real acceptance gate not enforced")
	}
}
