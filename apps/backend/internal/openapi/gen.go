// Package openapi registers code generation directives for regenerating
// Go server types from the Arena OpenAPI 3.1 specification.
//
// # Generated output
//
// The generated Go types live in:
//
//	../adapters/http/openapi/types_gen.go
//
// The TypeScript client types live in:
//
//	../../openapi/clients/ts/index.d.ts
//
// # Running code generation
//
// Preferred — run from the repo root so all paths resolve correctly:
//
//	make gen-openapi       # regenerate Go server types via oapi-codegen
//	make gen-ts-client     # regenerate TypeScript client types via openapi-typescript
//
// Or via go generate (also run from the repo root):
//
//	go generate ./apps/backend/internal/openapi/...
//
// # Important: run from the repo root
//
// The //go:generate directive below must be executed from the repository root
// (not the package directory) so that the output path in oapi-codegen.yaml
// resolves correctly. `make gen-openapi` handles this automatically.
//
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.4.1 --config=apps/backend/openapi/oapi-codegen.yaml apps/backend/openapi/openapi.yaml
package openapi
