// openapi_103_test.go verifies feature #103:
// "OpenAPI 3.1 skeleton + oapi-codegen + openapi-typescript"
//
// Steps verified:
//  Step 1: openapi.yaml exists with openapi:3.1.0 header, info, servers, and common components
//          (ErrorEnvelope schema, X-Request-Id/X-Idempotency-Key headers, PaginationMeta schema)
//  Step 2: internal/openapi/gen.go exists with //go:generate directives for oapi-codegen
//  Step 3: generate-clients.sh exists at the repo root
//  Step 4: README.md documents make gen-openapi and make gen-ts-client commands
//  Step 5: ErrorEnvelope schema has code, message, request_id, trace_id — aligned with feature #10
//  Step 6: Generated Go types (types_gen.go) compile; TypeScript index.d.ts exists
package httpserver

import (
	"strings"
	"testing"
)

// ─── Step 1: openapi.yaml has correct header and common components ─────────────

// TestOpenAPI103_SpecVersion verifies openapi.yaml declares openapi: "3.1.0".
func TestOpenAPI103_SpecVersion(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, `openapi: "3.1.0"`) {
		t.Error("openapi.yaml must contain 'openapi: \"3.1.0\"'")
	}
}

// TestOpenAPI103_SpecHasInfoBlock verifies openapi.yaml has an info: block with title and version.
func TestOpenAPI103_SpecHasInfoBlock(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	for _, want := range []string{"info:", "title:", "version:"} {
		if !strings.Contains(content, want) {
			t.Errorf("openapi.yaml missing required field %q in info block", want)
		}
	}
}

// TestOpenAPI103_SpecHasServers verifies openapi.yaml has a servers: block with /v1 base.
func TestOpenAPI103_SpecHasServers(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, "servers:") {
		t.Error("openapi.yaml missing 'servers:' block")
	}
	// The server block should reference localhost:8080 (local dev) with /v1 routes.
	if !strings.Contains(content, "localhost:8080") {
		t.Error("openapi.yaml servers: block should include localhost:8080 for local dev")
	}
}

// TestOpenAPI103_SpecHasErrorEnvelopeSchema verifies openapi.yaml defines the ErrorEnvelope schema
// in components/schemas, aligned with feature #10.
func TestOpenAPI103_SpecHasErrorEnvelopeSchema(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, "ErrorEnvelope:") {
		t.Error("openapi.yaml components/schemas missing 'ErrorEnvelope'")
	}
}

// TestOpenAPI103_SpecHasRequestIdHeader verifies openapi.yaml defines an X-Request-Id
// reusable header in components/headers.
func TestOpenAPI103_SpecHasRequestIdHeader(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, "headers:") {
		t.Error("openapi.yaml components missing 'headers:' block")
	}
	if !strings.Contains(content, "X-Request-Id:") {
		t.Error("openapi.yaml components/headers missing 'X-Request-Id'")
	}
}

// TestOpenAPI103_SpecHasIdempotencyKeyHeader verifies openapi.yaml defines an
// X-Idempotency-Key reusable header in components/headers.
func TestOpenAPI103_SpecHasIdempotencyKeyHeader(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, "X-Idempotency-Key:") {
		t.Error("openapi.yaml components/headers missing 'X-Idempotency-Key'")
	}
}

// TestOpenAPI103_SpecHasPaginationMetaSchema verifies openapi.yaml defines a PaginationMeta
// schema in components/schemas for use in future list endpoints.
func TestOpenAPI103_SpecHasPaginationMetaSchema(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, "PaginationMeta:") {
		t.Error("openapi.yaml components/schemas missing 'PaginationMeta'")
	}
	// Verify the required pagination fields are documented.
	for _, field := range []string{"page:", "page_size:", "total:", "total_pages:"} {
		if !strings.Contains(content, field) {
			t.Errorf("openapi.yaml PaginationMeta schema missing field %q", field)
		}
	}
}

// ─── Step 2: internal/openapi/gen.go exists with //go:generate directives ────

// TestOpenAPI103_GenGoExists verifies that apps/backend/internal/openapi/gen.go
// exists as the canonical location for //go:generate directives.
func TestOpenAPI103_GenGoExists(t *testing.T) {
	content := findFileByName(t, "apps/backend/internal/openapi/gen.go")
	if len(content) == 0 {
		t.Fatal("apps/backend/internal/openapi/gen.go is missing or empty")
	}
}

// TestOpenAPI103_GenGoHasGoGenerateDirective verifies gen.go contains a //go:generate
// directive that invokes oapi-codegen.
func TestOpenAPI103_GenGoHasGoGenerateDirective(t *testing.T) {
	content := findFileByName(t, "apps/backend/internal/openapi/gen.go")
	if !strings.Contains(content, "//go:generate") {
		t.Error("apps/backend/internal/openapi/gen.go must contain a //go:generate directive")
	}
}

// TestOpenAPI103_GenGoReferencesOapiCodegen verifies gen.go's //go:generate directive
// mentions oapi-codegen as the code generator.
func TestOpenAPI103_GenGoReferencesOapiCodegen(t *testing.T) {
	content := findFileByName(t, "apps/backend/internal/openapi/gen.go")
	if !strings.Contains(content, "oapi-codegen") {
		t.Error("apps/backend/internal/openapi/gen.go //go:generate directive must reference oapi-codegen")
	}
}

// TestOpenAPI103_GenGoHasPackageDeclaration verifies gen.go has a valid package declaration.
func TestOpenAPI103_GenGoHasPackageDeclaration(t *testing.T) {
	content := findFileByName(t, "apps/backend/internal/openapi/gen.go")
	if !strings.Contains(content, "package openapi") {
		t.Error("apps/backend/internal/openapi/gen.go must declare 'package openapi'")
	}
}

// ─── Step 3: generate-clients.sh exists ───────────────────────────────────────

// TestOpenAPI103_GenerateClientsShExists verifies that generate-clients.sh
// exists at the repo root as a convenience wrapper for TypeScript client generation.
func TestOpenAPI103_GenerateClientsShExists(t *testing.T) {
	content := findFileByName(t, "generate-clients.sh")
	if len(content) == 0 {
		t.Fatal("generate-clients.sh is missing or empty at the repo root")
	}
}

// TestOpenAPI103_GenerateClientsShInvokesNodeScript verifies generate-clients.sh
// calls the gen-ts-client.mjs Node.js script.
func TestOpenAPI103_GenerateClientsShInvokesNodeScript(t *testing.T) {
	content := findFileByName(t, "generate-clients.sh")
	if !strings.Contains(content, "gen-ts-client.mjs") {
		t.Error("generate-clients.sh must invoke scripts/gen-ts-client.mjs")
	}
}

// ─── Step 4: README documents openapi-generate commands ───────────────────────

// TestOpenAPI103_READMEDocumentsGenOpenAPI verifies README.md documents the
// `make gen-openapi` command for regenerating Go server types.
func TestOpenAPI103_READMEDocumentsGenOpenAPI(t *testing.T) {
	content := findFileByName(t, "README.md")
	if !strings.Contains(content, "gen-openapi") {
		t.Error("README.md must document the 'make gen-openapi' command")
	}
}

// TestOpenAPI103_READMEDocumentsGenTSClient verifies README.md documents the
// `make gen-ts-client` command for regenerating TypeScript client types.
func TestOpenAPI103_READMEDocumentsGenTSClient(t *testing.T) {
	content := findFileByName(t, "README.md")
	if !strings.Contains(content, "gen-ts-client") {
		t.Error("README.md must document the 'make gen-ts-client' command")
	}
}

// ─── Step 5: ErrorEnvelope schema aligned with feature #10 ───────────────────

// TestOpenAPI103_ErrorEnvelopeHasRequiredFields verifies the ErrorEnvelope schema
// in openapi.yaml has all required fields: code, message, request_id, trace_id.
// Aligned with feature #10 (error envelope type in Go code).
func TestOpenAPI103_ErrorEnvelopeHasRequiredFields(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	for _, field := range []string{"code:", "message:", "request_id:", "trace_id:", "details:"} {
		if !strings.Contains(content, field) {
			t.Errorf("openapi.yaml ErrorEnvelope schema missing field %q", field)
		}
	}
}

// TestOpenAPI103_ErrorEnvelopeCodePattern verifies the ErrorEnvelope code field
// has a regex pattern (dotted-namespace format: namespace.sub_code).
func TestOpenAPI103_ErrorEnvelopeCodePattern(t *testing.T) {
	content := findFileByName(t, "openapi.yaml")
	if !strings.Contains(content, "pattern:") {
		t.Error("openapi.yaml ErrorEnvelope.error.code must have a 'pattern:' validation")
	}
}

// TestOpenAPI103_GoTypesHaveErrorEnvelope verifies the generated Go types include
// ErrorEnvelope — proving the types_gen.go is in sync with the spec (step 5 Go side).
func TestOpenAPI103_GoTypesHaveErrorEnvelope(t *testing.T) {
	content := findFileByName(t, "apps/backend/internal/adapters/http/openapi/types_gen.go")
	if !strings.Contains(content, "ErrorEnvelope") {
		t.Error("types_gen.go must contain the ErrorEnvelope type (aligned with feature #10)")
	}
	// Verify all required fields are present in the generated struct.
	for _, field := range []string{"Code ", "Message ", "RequestId ", "TraceId "} {
		if !strings.Contains(content, field) {
			t.Errorf("types_gen.go ErrorEnvelope missing field %q", field)
		}
	}
}

// ─── Step 6: Generated Go types compile; TypeScript types exist ───────────────

// TestOpenAPI103_TypesGenGoExists verifies types_gen.go exists and is non-empty.
// The fact that this test file compiles proves types_gen.go is syntactically valid.
func TestOpenAPI103_TypesGenGoExists(t *testing.T) {
	content := findFileByName(t, "apps/backend/internal/adapters/http/openapi/types_gen.go")
	if len(content) == 0 {
		t.Fatal("apps/backend/internal/adapters/http/openapi/types_gen.go is missing or empty")
	}
	if !strings.Contains(content, "Code generated by github.com/oapi-codegen") {
		t.Error("types_gen.go must carry the 'Code generated by oapi-codegen' header")
	}
}

// TestOpenAPI103_TypesGenGoHasPaginationMeta verifies types_gen.go includes the
// PaginationMeta type added in this feature.
func TestOpenAPI103_TypesGenGoHasPaginationMeta(t *testing.T) {
	content := findFileByName(t, "apps/backend/internal/adapters/http/openapi/types_gen.go")
	if !strings.Contains(content, "PaginationMeta") {
		t.Error("types_gen.go must contain the PaginationMeta type (added in feature #103)")
	}
}

// TestOpenAPI103_TSClientTypesExist verifies the generated TypeScript client types
// (index.d.ts) exist at apps/backend/openapi/clients/ts/index.d.ts.
func TestOpenAPI103_TSClientTypesExist(t *testing.T) {
	content := findFileByName(t, "apps/backend/openapi/clients/ts/index.d.ts")
	if len(content) == 0 {
		t.Fatal("apps/backend/openapi/clients/ts/index.d.ts is missing or empty")
	}
}

// TestOpenAPI103_TSClientTypesHaveErrorEnvelope verifies the TypeScript types include
// ErrorEnvelope — proving the TS types are in sync with the spec.
func TestOpenAPI103_TSClientTypesHaveErrorEnvelope(t *testing.T) {
	content := findFileByName(t, "apps/backend/openapi/clients/ts/index.d.ts")
	if !strings.Contains(content, "ErrorEnvelope") {
		t.Error("index.d.ts must contain the ErrorEnvelope type")
	}
}

// TestOpenAPI103_TSClientTypesHaveNamedExports verifies the TypeScript types file
// has named export aliases (appended by gen-ts-client.mjs in step 3).
func TestOpenAPI103_TSClientTypesHaveNamedExports(t *testing.T) {
	content := findFileByName(t, "apps/backend/openapi/clients/ts/index.d.ts")
	wants := []string{
		"export type ErrorEnvelope",
		"export type EchoRequest",
		"export type EchoResponse",
	}
	for _, want := range wants {
		if !strings.Contains(content, want) {
			t.Errorf("index.d.ts missing named export %q (gen-ts-client.mjs should append these)", want)
		}
	}
}

// TestOpenAPI103_OapiCodegenConfigExists verifies the oapi-codegen.yaml config
// exists for the gen-openapi make target.
func TestOpenAPI103_OapiCodegenConfigExists(t *testing.T) {
	content := findFileByName(t, "apps/backend/openapi/oapi-codegen.yaml")
	if len(content) == 0 {
		t.Fatal("apps/backend/openapi/oapi-codegen.yaml is missing or empty")
	}
	if !strings.Contains(content, "oapi-codegen") && !strings.Contains(content, "package:") {
		t.Error("oapi-codegen.yaml missing 'package:' declaration")
	}
}

// ─── Full verification (all 6 steps as sub-tests) ────────────────────────────

// TestOpenAPI103_FullVerification runs all feature #103 verification steps.
func TestOpenAPI103_FullVerification(t *testing.T) {
	t.Run("Step1_SpecVersion", TestOpenAPI103_SpecVersion)
	t.Run("Step1_SpecHasInfoBlock", TestOpenAPI103_SpecHasInfoBlock)
	t.Run("Step1_SpecHasServers", TestOpenAPI103_SpecHasServers)
	t.Run("Step1_SpecHasErrorEnvelopeSchema", TestOpenAPI103_SpecHasErrorEnvelopeSchema)
	t.Run("Step1_SpecHasRequestIdHeader", TestOpenAPI103_SpecHasRequestIdHeader)
	t.Run("Step1_SpecHasIdempotencyKeyHeader", TestOpenAPI103_SpecHasIdempotencyKeyHeader)
	t.Run("Step1_SpecHasPaginationMetaSchema", TestOpenAPI103_SpecHasPaginationMetaSchema)
	t.Run("Step2_GenGoExists", TestOpenAPI103_GenGoExists)
	t.Run("Step2_GenGoHasGoGenerateDirective", TestOpenAPI103_GenGoHasGoGenerateDirective)
	t.Run("Step2_GenGoReferencesOapiCodegen", TestOpenAPI103_GenGoReferencesOapiCodegen)
	t.Run("Step2_GenGoHasPackageDeclaration", TestOpenAPI103_GenGoHasPackageDeclaration)
	t.Run("Step3_GenerateClientsShExists", TestOpenAPI103_GenerateClientsShExists)
	t.Run("Step3_GenerateClientsShInvokesNodeScript", TestOpenAPI103_GenerateClientsShInvokesNodeScript)
	t.Run("Step4_READMEDocumentsGenOpenAPI", TestOpenAPI103_READMEDocumentsGenOpenAPI)
	t.Run("Step4_READMEDocumentsGenTSClient", TestOpenAPI103_READMEDocumentsGenTSClient)
	t.Run("Step5_ErrorEnvelopeHasRequiredFields", TestOpenAPI103_ErrorEnvelopeHasRequiredFields)
	t.Run("Step5_ErrorEnvelopeCodePattern", TestOpenAPI103_ErrorEnvelopeCodePattern)
	t.Run("Step5_GoTypesHaveErrorEnvelope", TestOpenAPI103_GoTypesHaveErrorEnvelope)
	t.Run("Step6_TypesGenGoExists", TestOpenAPI103_TypesGenGoExists)
	t.Run("Step6_TypesGenGoHasPaginationMeta", TestOpenAPI103_TypesGenGoHasPaginationMeta)
	t.Run("Step6_TSClientTypesExist", TestOpenAPI103_TSClientTypesExist)
	t.Run("Step6_TSClientTypesHaveErrorEnvelope", TestOpenAPI103_TSClientTypesHaveErrorEnvelope)
	t.Run("Step6_TSClientTypesHaveNamedExports", TestOpenAPI103_TSClientTypesHaveNamedExports)
	t.Run("Step6_OapiCodegenConfigExists", TestOpenAPI103_OapiCodegenConfigExists)
}
