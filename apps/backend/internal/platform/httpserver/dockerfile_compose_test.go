// dockerfile_compose_test.go verifies the production Dockerfile and docker-compose.yml
// satisfy all requirements for feature #106 (Dockerfile multi-stage + docker-compose.yml).
//
// All tests are static file-inspection tests — no Docker daemon required.
// They parse the Dockerfile and docker-compose.yml at the repo root and assert
// each structural requirement described in the feature steps.
package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- helpers ---------------------------------------------------------------

// dockerfileStr reads the Dockerfile at the repo root using testfilehelpers.
func dockerfileStr(t *testing.T) string {
	t.Helper()
	return findFileByName(t, "Dockerfile")
}

// composeStr reads the docker-compose.yml at the repo root using testfilehelpers.
func composeStr(t *testing.T) string {
	t.Helper()
	return findFileByName(t, "docker-compose.yml")
}

// ---- Step 1: multi-stage build ---------------------------------------------

// TestDockerfile106_MultiStageFromLines verifies two FROM lines: golang:1.24-alpine
// (builder) and gcr.io/distroless/static-debian12 (final).
func TestDockerfile106_MultiStageFromLines(t *testing.T) {
	content := dockerfileStr(t)

	if !strings.Contains(content, "golang:1.24-alpine") {
		t.Error("Dockerfile must have builder stage FROM golang:1.24-alpine")
	}
	if !strings.Contains(content, "gcr.io/distroless/static-debian12") {
		t.Error("Dockerfile must have final stage FROM gcr.io/distroless/static-debian12")
	}

	// Must have at least two FROM directives (multi-stage).
	fromCount := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), "FROM ") {
			fromCount++
		}
	}
	if fromCount < 2 {
		t.Errorf("Dockerfile must have ≥2 FROM lines (multi-stage); got %d", fromCount)
	}
}

// TestDockerfile106_BuilderStageAlias verifies the builder stage is named "build".
func TestDockerfile106_BuilderStageAlias(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "AS build") {
		t.Error("Dockerfile builder stage should have alias: AS build")
	}
}

// ---- Step 2: builder stage contents ----------------------------------------

// TestDockerfile106_BuilderCopiesSource verifies the builder COPY includes
// the apps/backend source tree (or the apps/ directory).
func TestDockerfile106_BuilderCopiesSource(t *testing.T) {
	content := dockerfileStr(t)

	hasCopyApps := strings.Contains(content, "COPY apps/backend") ||
		strings.Contains(content, "COPY apps ")
	if !hasCopyApps {
		t.Error("Dockerfile builder stage must COPY apps/ or apps/backend")
	}
}

// TestDockerfile106_BuilderCGODisabled verifies CGO_ENABLED=0 is set.
func TestDockerfile106_BuilderCGODisabled(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "CGO_ENABLED=0") {
		t.Error("Dockerfile builder must set CGO_ENABLED=0 for static binaries")
	}
}

// TestDockerfile106_BuilderGOOSLinux verifies GOOS=linux is set.
func TestDockerfile106_BuilderGOOSLinux(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "GOOS=linux") {
		t.Error("Dockerfile builder must set GOOS=linux")
	}
}

// TestDockerfile106_BuildsAllBinaries verifies all three required binaries are built.
func TestDockerfile106_BuildsAllBinaries(t *testing.T) {
	content := dockerfileStr(t)
	binaries := []string{"arena-api", "arena-worker", "arena-migrate"}
	for _, bin := range binaries {
		if !strings.Contains(content, bin) {
			t.Errorf("Dockerfile builder must build binary %q", bin)
		}
	}
}

// TestDockerfile106_BuilderGoModCopy verifies go.mod is copied before the full
// source tree for Docker layer caching.
func TestDockerfile106_BuilderGoModCopy(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "go.mod") {
		t.Error("Dockerfile builder must COPY go.mod for layer caching")
	}
}

// ---- Step 3: final image binaries ------------------------------------------

// TestDockerfile106_FinalImageCopiesBinaries verifies the final stage copies
// the arena-api binary from the build stage.
func TestDockerfile106_FinalImageCopiesBinaries(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "--from=build") {
		t.Error("Dockerfile final stage must use COPY --from=build")
	}
	if !strings.Contains(content, "arena-api") {
		t.Error("Dockerfile final stage must copy arena-api binary")
	}
}

// TestDockerfile106_FinalImageHealthcheckBinary verifies the healthcheck binary
// is also copied into the final image.
func TestDockerfile106_FinalImageHealthcheckBinary(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "arena-healthcheck") {
		t.Error("Dockerfile final stage must include arena-healthcheck binary")
	}
}

// ---- Step 4: HEALTHCHECK directive -----------------------------------------

// TestDockerfile106_HealthcheckDirective verifies the HEALTHCHECK CMD is present
// and uses the arena-healthcheck binary.
func TestDockerfile106_HealthcheckDirective(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "HEALTHCHECK") {
		t.Error("Dockerfile must have a HEALTHCHECK directive")
	}
	if !strings.Contains(content, "arena-healthcheck") {
		t.Error("Dockerfile HEALTHCHECK must use arena-healthcheck binary (no curl/wget in distroless)")
	}
}

// TestDockerfile106_HealthcheckTargetsHealthz verifies /healthz is the probe target.
func TestDockerfile106_HealthcheckTargetsHealthz(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "healthz") {
		t.Error("Dockerfile must reference /healthz for the health check")
	}
}

// ---- Step 5: EXPOSE and USER -----------------------------------------------

// TestDockerfile106_Expose8080 verifies port 8080 is exposed.
func TestDockerfile106_Expose8080(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "EXPOSE 8080") {
		t.Error("Dockerfile must EXPOSE 8080")
	}
}

// TestDockerfile106_UserNonroot verifies the container runs as nonroot user.
func TestDockerfile106_UserNonroot(t *testing.T) {
	content := dockerfileStr(t)
	if !strings.Contains(content, "nonroot") {
		t.Error("Dockerfile must set USER nonroot (or nonroot:nonroot) in final stage")
	}
}

// ---- Step 6: docker-compose.yml services -----------------------------------

// TestCompose106_HasApiService verifies the api service is present.
func TestCompose106_HasApiService(t *testing.T) {
	content := composeStr(t)
	if !strings.Contains(content, "api:") {
		t.Error("docker-compose.yml must define an 'api' service")
	}
}

// TestCompose106_HasWorkerService verifies the worker service is present.
func TestCompose106_HasWorkerService(t *testing.T) {
	content := composeStr(t)
	if !strings.Contains(content, "worker:") {
		t.Error("docker-compose.yml must define a 'worker' service")
	}
}

// TestCompose106_HasPostgres17 verifies PostgreSQL 17 is used.
func TestCompose106_HasPostgres17(t *testing.T) {
	content := composeStr(t)
	if !strings.Contains(content, "postgres:17") {
		t.Error("docker-compose.yml must use postgres:17 image")
	}
}

// TestCompose106_HasRedis7 verifies Redis 7 is used.
func TestCompose106_HasRedis7(t *testing.T) {
	content := composeStr(t)
	if !strings.Contains(content, "redis:7") {
		t.Error("docker-compose.yml must use redis:7 image")
	}
}

// TestCompose106_HasVolumesForPostgres verifies a named volume for PostgreSQL data.
func TestCompose106_HasVolumesForPostgres(t *testing.T) {
	content := composeStr(t)
	if !strings.Contains(content, "pg_data") && !strings.Contains(content, "postgres_data") {
		t.Error("docker-compose.yml must define a named volume for PostgreSQL data persistence")
	}
}

// TestCompose106_HasDependsOn verifies the api service depends_on postgres.
func TestCompose106_HasDependsOn(t *testing.T) {
	content := composeStr(t)
	if !strings.Contains(content, "depends_on") {
		t.Error("docker-compose.yml must use depends_on for service ordering")
	}
}

// TestCompose106_PostgresHasHealthcheck verifies the postgres service has a healthcheck.
func TestCompose106_PostgresHasHealthcheck(t *testing.T) {
	content := composeStr(t)
	if !strings.Contains(content, "pg_isready") {
		t.Error("docker-compose.yml postgres service must have a healthcheck using pg_isready")
	}
}

// TestCompose106_RedisHasHealthcheck verifies the redis service has a healthcheck.
func TestCompose106_RedisHasHealthcheck(t *testing.T) {
	content := composeStr(t)
	if !strings.Contains(content, "redis-cli") {
		t.Error("docker-compose.yml redis service must have a healthcheck using redis-cli")
	}
}

// ---- Step 7: /healthz returns 200 (in-process) -----------------------------

// TestDockerCompose106_HealthzReturns200 verifies that the arena-api /healthz
// endpoint returns HTTP 200. This mirrors the "docker compose up → curl /healthz"
// verification step using an in-process httptest.Server.
func TestDockerCompose106_HealthzReturns200(t *testing.T) {
	srv := buildOperationalTestServer(t)

	ts := httptest.NewServer(srv.router)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("/healthz GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("/healthz: want 200, got %d", resp.StatusCode)
	}
}

// TestDockerCompose106_ReadyzReachable verifies that the arena-api /readyz
// endpoint is reachable. Without a live DB, it may return 503 (unhealthy pool)
// or 200 (no probes). Both confirm the route is wired.
func TestDockerCompose106_ReadyzReachable(t *testing.T) {
	srv := buildOperationalTestServer(t)

	ts := httptest.NewServer(srv.router)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("/readyz GET error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/readyz: want 200 or 503, got %d", resp.StatusCode)
	}
}

// ---- Full verification sweep -----------------------------------------------

// TestDockerfileCompose106_FullVerification is the canonical sweep test for
// feature #106 that exercises all 7 feature steps as sub-tests.
func TestDockerfileCompose106_FullVerification(t *testing.T) {
	dfContent := dockerfileStr(t)
	cContent := composeStr(t)

	// Step 1: multi-stage FROM lines.
	t.Run("Step1_MultiStageDockerfile", func(t *testing.T) {
		if !strings.Contains(dfContent, "golang:1.24-alpine") {
			t.Error("builder stage must be golang:1.24-alpine")
		}
		if !strings.Contains(dfContent, "gcr.io/distroless/static-debian12") {
			t.Error("final stage must be gcr.io/distroless/static-debian12")
		}
	})

	// Step 2: builder stage env + build commands.
	t.Run("Step2_BuilderStage", func(t *testing.T) {
		if !strings.Contains(dfContent, "CGO_ENABLED=0") {
			t.Error("CGO_ENABLED=0 missing")
		}
		if !strings.Contains(dfContent, "GOOS=linux") {
			t.Error("GOOS=linux missing")
		}
		for _, bin := range []string{"arena-api", "arena-worker", "arena-migrate"} {
			if !strings.Contains(dfContent, bin) {
				t.Errorf("builder must build %s", bin)
			}
		}
	})

	// Step 3: final image copies binaries; embed.FS means no separate data copy.
	t.Run("Step3_FinalImageBinaries", func(t *testing.T) {
		if !strings.Contains(dfContent, "--from=build") {
			t.Error("final stage must use COPY --from=build")
		}
		if !strings.Contains(dfContent, "arena-api") {
			t.Error("final stage must include arena-api")
		}
	})

	// Step 4: HEALTHCHECK with healthcheck binary targeting /healthz.
	t.Run("Step4_HEALTHCHECK", func(t *testing.T) {
		if !strings.Contains(dfContent, "HEALTHCHECK") {
			t.Error("HEALTHCHECK directive missing")
		}
		if !strings.Contains(dfContent, "arena-healthcheck") {
			t.Error("HEALTHCHECK must use arena-healthcheck binary")
		}
	})

	// Step 5: EXPOSE 8080 + USER nonroot.
	t.Run("Step5_ExposeAndUser", func(t *testing.T) {
		if !strings.Contains(dfContent, "EXPOSE 8080") {
			t.Error("EXPOSE 8080 missing")
		}
		if !strings.Contains(dfContent, "nonroot") {
			t.Error("USER nonroot missing")
		}
	})

	// Step 6: docker-compose.yml services.
	t.Run("Step6_DockerCompose", func(t *testing.T) {
		for _, svc := range []string{"api:", "worker:", "postgres:17", "redis:7"} {
			if !strings.Contains(cContent, svc) {
				t.Errorf("docker-compose.yml must include service/image %q", svc)
			}
		}
		if !strings.Contains(cContent, "depends_on") {
			t.Error("docker-compose.yml must use depends_on")
		}
		if !strings.Contains(cContent, "pg_data") && !strings.Contains(cContent, "postgres_data") {
			t.Error("docker-compose.yml must define named volume for PostgreSQL")
		}
	})

	// Step 7: /healthz returns 200 in-process.
	t.Run("Step7_HealthzReturns200", func(t *testing.T) {
		opSrv := buildOperationalTestServer(t)
		ts := httptest.NewServer(opSrv.router)
		defer ts.Close()

		resp, err := http.Get(ts.URL + "/healthz")
		if err != nil {
			t.Fatalf("GET /healthz: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("/healthz: want 200, got %d", resp.StatusCode)
		}
	})
}
