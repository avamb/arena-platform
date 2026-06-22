package httpserver

// Feature #108 — GitHub Actions: lint + test + build + push
//
// Static inspection tests that verify the CI workflow file is correctly
// structured with all required jobs, triggers, and configuration.
// No external services are required — all tests run by reading the YAML file.
//
// Uses the shared findFileByName helper (testfilehelpers_test.go) so path
// resolution works both in normal `go test` runs and in Docker/-trimpath builds.

import (
	"strings"
	"testing"
)

// ciWorkflowContent returns the content of .github/workflows/ci.yml.
func ciWorkflowContent(t *testing.T) string {
	t.Helper()
	return findFileByName(t, ".github/workflows/ci.yml")
}

// ─── Step 1: file exists with correct triggers ────────────────────────────────

func TestCIWorkflow108_FileExists(t *testing.T) {
	content := ciWorkflowContent(t)
	if len(content) == 0 {
		t.Fatal("ci.yml is empty or could not be read")
	}
}

func TestCIWorkflow108_TriggerOnPushMain(t *testing.T) {
	content := ciWorkflowContent(t)
	checks := []string{
		"push:",
		"branches:",
		"- main",
	}
	for _, want := range checks {
		if !strings.Contains(content, want) {
			t.Errorf("ci.yml missing push trigger string %q", want)
		}
	}
}

func TestCIWorkflow108_TriggerOnPullRequest(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "pull_request") {
		t.Error("ci.yml missing pull_request trigger")
	}
}

// ─── Step 2: lint job ─────────────────────────────────────────────────────────

func TestCIWorkflow108_LintJobExists(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "lint:") && !strings.Contains(content, "name: Lint") {
		t.Error("ci.yml missing lint job")
	}
}

func TestCIWorkflow108_LintJobUsesSetupGo124(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "setup-go") {
		t.Error("ci.yml lint job missing actions/setup-go step")
	}
	if !strings.Contains(content, "1.24") {
		t.Error("ci.yml does not specify Go 1.24")
	}
}

func TestCIWorkflow108_LintJobUsesGolangciLintAction(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "golangci/golangci-lint-action") {
		t.Error("ci.yml missing golangci/golangci-lint-action")
	}
}

// ─── Step 3: test job ─────────────────────────────────────────────────────────

func TestCIWorkflow108_TestJobExists(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "test:") && !strings.Contains(content, "name: Test") {
		t.Error("ci.yml missing test job")
	}
}

func TestCIWorkflow108_TestJobHasPostgresService(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "postgres:17") {
		t.Error("ci.yml test job missing postgres:17 service")
	}
	if !strings.Contains(content, "services:") {
		t.Error("ci.yml test job missing services: block")
	}
}

func TestCIWorkflow108_TestJobRunsWithRaceDetector(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "-race") {
		t.Error("ci.yml test job missing -race flag")
	}
}

func TestCIWorkflow108_TestJobGeneratesCoverageProfile(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "-coverprofile") && !strings.Contains(content, "coverage.out") {
		t.Error("ci.yml test job missing -coverprofile flag or coverage.out reference")
	}
}

// ─── Step 4: build-and-push job ───────────────────────────────────────────────

func TestCIWorkflow108_BuildAndPushJobExists(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "build-and-push") {
		t.Error("ci.yml missing build-and-push job")
	}
}

func TestCIWorkflow108_BuildAndPushOnlyOnMain(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "refs/heads/main") {
		t.Error("ci.yml build-and-push job missing main branch condition")
	}
}

func TestCIWorkflow108_BuildAndPushUsesDockerBuildPushAction(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "docker/build-push-action") {
		t.Error("ci.yml missing docker/build-push-action")
	}
}

func TestCIWorkflow108_BuildAndPushTagsWithSHAAndLatest(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "type=sha") {
		t.Error("ci.yml build-and-push missing SHA tag")
	}
	if !strings.Contains(content, "latest") {
		t.Error("ci.yml build-and-push missing latest tag")
	}
}

func TestCIWorkflow108_BuildAndPushUsesRegistrySecrets(t *testing.T) {
	content := ciWorkflowContent(t)
	secrets := []string{
		"REGISTRY_URL",
		"REGISTRY_USERNAME",
		"REGISTRY_PASSWORD",
	}
	for _, secret := range secrets {
		if !strings.Contains(content, secret) {
			t.Errorf("ci.yml missing registry secret reference: %s", secret)
		}
	}
}

// ─── Step 5: openapi-check job ────────────────────────────────────────────────

func TestCIWorkflow108_OpenAPICheckJobExists(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "openapi-check") {
		t.Error("ci.yml missing openapi-check job")
	}
}

func TestCIWorkflow108_OpenAPICheckRunsMakeGenOpenAPI(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "make gen-openapi") {
		t.Error("ci.yml openapi-check job does not call 'make gen-openapi'")
	}
}

func TestCIWorkflow108_OpenAPICheckUsesDiffToDetectDrift(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "git diff") {
		t.Error("ci.yml openapi-check job does not use 'git diff' to detect drift")
	}
}

// ─── Step 6: all jobs present on scaffold repo ────────────────────────────────

func TestCIWorkflow108_AllRequiredJobsPresent(t *testing.T) {
	content := ciWorkflowContent(t)
	requiredJobs := []string{"lint", "test", "openapi-check", "build-and-push"}
	for _, job := range requiredJobs {
		if !strings.Contains(content, job) {
			t.Errorf("ci.yml missing required job: %s", job)
		}
	}
}

func TestCIWorkflow108_WorkflowNameSet(t *testing.T) {
	content := ciWorkflowContent(t)
	if !strings.Contains(content, "name: CI") && !strings.Contains(content, "name: ci") {
		t.Error("ci.yml missing workflow name")
	}
}

// ─── Step 7: README badge ─────────────────────────────────────────────────────

func TestCIWorkflow108_READMEHasCIBadge(t *testing.T) {
	content := findFileByName(t, "README.md")

	// Badge should reference the CI workflow
	hasBadge := strings.Contains(content, "ci.yml") ||
		strings.Contains(content, "workflows/CI") ||
		strings.Contains(content, "actions/workflows") ||
		strings.Contains(content, "badge.svg")
	if !hasBadge {
		t.Error("README.md missing CI status badge referencing ci.yml or GitHub Actions")
	}
}

// ─── Full verification ────────────────────────────────────────────────────────

func TestCIWorkflow108_FullVerification(t *testing.T) {
	content := ciWorkflowContent(t)

	t.Run("Step1_TriggersConfigured", func(t *testing.T) {
		if !strings.Contains(content, "push:") || !strings.Contains(content, "pull_request") {
			t.Error("missing push or pull_request triggers")
		}
	})

	t.Run("Step2_LintJob", func(t *testing.T) {
		if !strings.Contains(content, "golangci/golangci-lint-action") {
			t.Error("missing golangci-lint-action")
		}
	})

	t.Run("Step3_TestJobWithPostgresAndRace", func(t *testing.T) {
		if !strings.Contains(content, "postgres:17") || !strings.Contains(content, "-race") {
			t.Error("missing postgres:17 service or -race flag")
		}
	})

	t.Run("Step4_BuildAndPushWithSecrets", func(t *testing.T) {
		if !strings.Contains(content, "docker/build-push-action") || !strings.Contains(content, "REGISTRY_URL") {
			t.Error("missing docker/build-push-action or REGISTRY_URL secret")
		}
	})

	t.Run("Step5_OpenAPICheckJob", func(t *testing.T) {
		if !strings.Contains(content, "openapi-check") || !strings.Contains(content, "make gen-openapi") {
			t.Error("missing openapi-check job or make gen-openapi command")
		}
	})

	t.Run("Step6_AllJobsPresent", func(t *testing.T) {
		for _, job := range []string{"lint", "test", "openapi-check", "build-and-push"} {
			if !strings.Contains(content, job) {
				t.Errorf("missing job: %s", job)
			}
		}
	})

	t.Run("Step7_READMEBadge", func(t *testing.T) {
		readme := findFileByName(t, "README.md")
		if !strings.Contains(readme, "ci.yml") && !strings.Contains(readme, "badge.svg") {
			t.Error("README.md missing CI badge")
		}
	})
}
