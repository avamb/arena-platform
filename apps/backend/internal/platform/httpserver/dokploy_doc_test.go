// dokploy_doc_test.go verifies that the deploy/DOKPLOY.md documentation file
// exists and contains all sections required by feature #107.
//
// Feature #107 — "Dokploy deployment notes + production env checklist"
// Steps verified:
//
//  1. deploy/DOKPLOY.md exists with a step-by-step Dokploy deployment guide
//     (creating app, connecting Postgres service, specifying Dockerfile,
//     configuring env vars, healthcheck path, expose port).
//  2. Checklist of mandatory production env variables references the actual
//     variable names from .env.example.
//  3. Documentation states that migrations run separately via arena-migrate
//     before arena-api starts (or init-container pattern).
//  4. Documentation addresses secret rotation: not in this milestone but
//     indicates where it is expected.
package httpserver

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// findDokployMD locates deploy/DOKPLOY.md in the repository and returns its
// content.  Uses the same two-strategy approach as findFileByName (runtime.Caller
// then CWD walk) so the test works under -trimpath Docker builds.
func findDokployMD(t *testing.T) string {
	t.Helper()

	locate := func(repoRoot string) string {
		p := filepath.Join(repoRoot, "deploy", "DOKPLOY.md")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		return ""
	}

	// Strategy 1: compile-time absolute path.
	_, thisFile, _, ok := runtime.Caller(0)
	if ok && filepath.IsAbs(thisFile) {
		dir := filepath.Dir(thisFile)
		repoRoot := dir
		for i := 0; i < 5; i++ {
			repoRoot = filepath.Dir(repoRoot)
		}
		if p := locate(repoRoot); p != "" {
			data, err := os.ReadFile(p)
			if err == nil {
				return string(data)
			}
		}
	}

	// Strategy 2: CWD walk until go.mod found.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("findDokployMD: cannot determine working directory: %v", err)
	}
	dir := cwd
	for i := 0; i < 10; i++ {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			if p := locate(dir); p != "" {
				data, err := os.ReadFile(p)
				if err == nil {
					return string(data)
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	t.Fatalf("findDokployMD: cannot locate deploy/DOKPLOY.md; cwd=%s", cwd)
	return ""
}

// ----------------------------------------------------------------------------
// Step 1 — Deployment guide structure
// ----------------------------------------------------------------------------

func TestDokploy107_FileExists(t *testing.T) {
	content := findDokployMD(t)
	if len(content) == 0 {
		t.Fatal("deploy/DOKPLOY.md is empty")
	}
}

func TestDokploy107_HasDokployAppCreationSection(t *testing.T) {
	content := findDokployMD(t)
	// Must mention creating an application in Dokploy.
	for _, phrase := range []string{"Dokploy", "Application", "Create"} {
		if !strings.Contains(content, phrase) {
			t.Errorf("deploy/DOKPLOY.md missing expected text %q", phrase)
		}
	}
}

func TestDokploy107_MentionsDockerfile(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "Dockerfile") {
		t.Error("deploy/DOKPLOY.md must specify the Dockerfile path")
	}
}

func TestDokploy107_MentionsPostgresService(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "PostgreSQL") {
		t.Error("deploy/DOKPLOY.md must describe attaching a PostgreSQL service")
	}
}

func TestDokploy107_MentionsExposePort8080(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "8080") {
		t.Error("deploy/DOKPLOY.md must mention port 8080")
	}
}

func TestDokploy107_MentionsHealthcheckPath(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "/healthz") {
		t.Error("deploy/DOKPLOY.md must document the /healthz healthcheck path")
	}
}

func TestDokploy107_MentionsEnvironmentVariables(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "Environment Variable") {
		t.Error("deploy/DOKPLOY.md must have an environment variables section")
	}
}

// ----------------------------------------------------------------------------
// Step 2 — Production env variable checklist
// ----------------------------------------------------------------------------

func TestDokploy107_ChecklistAppEnvProduction(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "APP_ENV=production") {
		t.Error("deploy/DOKPLOY.md checklist must include APP_ENV=production")
	}
}

func TestDokploy107_ChecklistDatabaseURL(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "DATABASE_URL") {
		t.Error("deploy/DOKPLOY.md checklist must include DATABASE_URL")
	}
}

func TestDokploy107_ChecklistJWTSigningSecret(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "JWT_SIGNING_SECRET") {
		t.Error("deploy/DOKPLOY.md checklist must include JWT_SIGNING_SECRET")
	}
}

func TestDokploy107_ChecklistOtelExporterEndpoint(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Error("deploy/DOKPLOY.md checklist must include OTEL_EXPORTER_OTLP_ENDPOINT")
	}
}

func TestDokploy107_ChecklistLogLevel(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "LOG_LEVEL") {
		t.Error("deploy/DOKPLOY.md checklist must include LOG_LEVEL")
	}
}

func TestDokploy107_ChecklistEnableDevAuthFalse(t *testing.T) {
	content := findDokployMD(t)
	// Production requires ENABLE_DEV_AUTH=false
	if !strings.Contains(content, "ENABLE_DEV_AUTH=false") {
		t.Error("deploy/DOKPLOY.md checklist must include ENABLE_DEV_AUTH=false")
	}
}

func TestDokploy107_ChecklistReferencesEnvExample(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, ".env.example") {
		t.Error("deploy/DOKPLOY.md must reference .env.example")
	}
}

// ----------------------------------------------------------------------------
// Step 3 — Migrations run separately before arena-api starts
// ----------------------------------------------------------------------------

func TestDokploy107_MentionsArenaMigrate(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "arena-migrate") {
		t.Error("deploy/DOKPLOY.md must mention the arena-migrate command")
	}
}

func TestDokploy107_MigrationsRunBeforeAPI(t *testing.T) {
	content := findDokployMD(t)
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "before") || !strings.Contains(lower, "migrat") {
		t.Error("deploy/DOKPLOY.md must state that migrations run before arena-api starts")
	}
}

func TestDokploy107_MentionsInitContainerPattern(t *testing.T) {
	content := findDokployMD(t)
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "init-container") && !strings.Contains(lower, "init container") {
		t.Error("deploy/DOKPLOY.md must mention the init-container pattern as an alternative")
	}
}

func TestDokploy107_HasMigrationSection(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "Run Database Migrations") &&
		!strings.Contains(content, "Migration") {
		t.Error("deploy/DOKPLOY.md must have a dedicated migrations section")
	}
}

// ----------------------------------------------------------------------------
// Step 4 — Secret rotation: not in milestone but documented
// ----------------------------------------------------------------------------

func TestDokploy107_HasSecretRotationSection(t *testing.T) {
	content := findDokployMD(t)
	if !strings.Contains(content, "Secret Rotation") && !strings.Contains(content, "secret rotation") {
		t.Error("deploy/DOKPLOY.md must have a Secret Rotation section")
	}
}

func TestDokploy107_SecretRotationNotInMilestone(t *testing.T) {
	content := findDokployMD(t)
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "out of scope") && !strings.Contains(lower, "not in this milestone") {
		t.Error("deploy/DOKPLOY.md must state that secret rotation is out of scope for this milestone")
	}
}

func TestDokploy107_SecretRotationIndicatesWhere(t *testing.T) {
	content := findDokployMD(t)
	lower := strings.ToLower(content)
	// Must point to a future milestone or identity module where rotation is expected.
	if !strings.Contains(lower, "milestone") && !strings.Contains(lower, "subsequent") {
		t.Error("deploy/DOKPLOY.md must indicate where secret rotation is expected (future milestone)")
	}
}

// ----------------------------------------------------------------------------
// Full verification sub-test (combines all 4 steps)
// ----------------------------------------------------------------------------

func TestDokploy107_FullVerification(t *testing.T) {
	content := findDokployMD(t)

	steps := []struct {
		name  string
		check func() bool
	}{
		{
			"Step1_DeploymentGuideStructure",
			func() bool {
				return strings.Contains(content, "Dokploy") &&
					strings.Contains(content, "Dockerfile") &&
					strings.Contains(content, "PostgreSQL") &&
					strings.Contains(content, "8080") &&
					strings.Contains(content, "/healthz")
			},
		},
		{
			"Step2_ProductionEnvChecklist",
			func() bool {
				return strings.Contains(content, "APP_ENV=production") &&
					strings.Contains(content, "DATABASE_URL") &&
					strings.Contains(content, "JWT_SIGNING_SECRET") &&
					strings.Contains(content, "OTEL_EXPORTER_OTLP_ENDPOINT") &&
					strings.Contains(content, "LOG_LEVEL") &&
					strings.Contains(content, "ENABLE_DEV_AUTH=false") &&
					strings.Contains(content, ".env.example")
			},
		},
		{
			"Step3_MigrationsRunFirst",
			func() bool {
				lower := strings.ToLower(content)
				return strings.Contains(content, "arena-migrate") &&
					strings.Contains(lower, "before") &&
					(strings.Contains(lower, "init-container") || strings.Contains(lower, "init container"))
			},
		},
		{
			"Step4_SecretRotationDocumented",
			func() bool {
				lower := strings.ToLower(content)
				return (strings.Contains(content, "Secret Rotation") || strings.Contains(lower, "secret rotation")) &&
					(strings.Contains(lower, "out of scope") || strings.Contains(lower, "not in this milestone")) &&
					(strings.Contains(lower, "milestone") || strings.Contains(lower, "subsequent"))
			},
		},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			if !step.check() {
				t.Errorf("deploy/DOKPLOY.md does not satisfy %s", step.name)
			}
		})
	}
}
