// openapi_docs_test.go verifies that every operation, parameter, and schema
// property in openapi.yaml carries a non-empty 'description' field (and that
// every operation also carries a non-empty 'summary').
//
// Feature #67: "All endpoints have descriptions in OpenAPI"
//
//	Step 1: Parse openapi.yaml
//	Step 2: Loop over every path.method -- assert summary non-empty and description non-empty
//	Step 3: Loop over parameters -- assert description non-empty
//	Step 4: Loop over schemas.properties -- assert description non-empty
//	Step 5: Print list of any undocumented items, exit 1 if any exist
package httpserver

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// findOpenAPIYAMLForDocs locates openapi.yaml by walking up from the
// current working directory. This approach avoids relying on runtime.Caller
// (which returns a module-relative path under -trimpath and cannot be used to
// open files on disk).
func findOpenAPIYAMLForDocs(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	dir := cwd
	for i := 0; i < 12; i++ {
		// Direct relative candidate (when cwd is inside apps/backend/openapi subtree).
		candidate := filepath.Join(dir, "openapi", "openapi.yaml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		// apps/backend/openapi candidate (from repo root or intermediate parent).
		candidate2 := filepath.Join(dir, "apps", "backend", "openapi", "openapi.yaml")
		if _, err := os.Stat(candidate2); err == nil {
			return candidate2
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // filesystem root reached
		}
		dir = parent
	}
	t.Fatalf("could not locate apps/backend/openapi/openapi.yaml from %s", cwd)
	return ""
}

// ---------------------------------------------------------------------------
// Minimal YAML tree parser
// ---------------------------------------------------------------------------

// yamlNode is a simple parsed representation of a YAML mapping entry.
// We only need key presence and string values for this feature.
type yamlNode struct {
	key      string
	value    string // non-empty if this is a scalar leaf
	children []*yamlNode
	indent   int
}

// parseOpenAPISimple parses the openapi.yaml file into a tree of yamlNode.
// This is a minimal, purpose-built parser for the specific structure of OpenAPI
// YAML — it handles block mappings and sequences but not inline JSON/flow style.
func parseOpenAPISimple(t *testing.T) *yamlNode {
	t.Helper()
	path := findOpenAPIYAMLForDocs(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open openapi.yaml: %v", err)
	}
	defer f.Close()

	root := &yamlNode{key: "__root__", indent: -1}
	stack := []*yamlNode{root}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()

		// Skip blank lines and comment-only lines.
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Measure indentation (spaces only).
		indent := 0
		for _, ch := range line {
			if ch == ' ' {
				indent++
			} else {
				break
			}
		}

		// Detect and strip sequence item prefix "- ".
		isSeqItem := false
		body := trimmed
		if strings.HasPrefix(body, "- ") {
			isSeqItem = true
			body = strings.TrimPrefix(body, "- ")
		} else if body == "-" {
			isSeqItem = true
			body = ""
		}

		// Parse "key: value" or "key:" from body.
		var key, value string
		if idx := strings.Index(body, ": "); idx >= 0 {
			key = body[:idx]
			value = strings.TrimSpace(body[idx+2:])
			// Strip inline YAML comments.
			if ci := strings.Index(value, " #"); ci >= 0 {
				value = strings.TrimSpace(value[:ci])
			}
			// Strip surrounding quotes from scalar values.
			value = strings.Trim(value, `"'`)
		} else if strings.HasSuffix(body, ":") {
			key = strings.TrimSuffix(body, ":")
		} else if isSeqItem {
			// Inline sequence item value (not a mapping key).
			key = body
			value = body
		} else {
			// Continuation of a multi-line scalar — append to parent value.
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.value += " " + trimmed
			}
			continue
		}

		node := &yamlNode{key: key, value: value, indent: indent}

		// Pop the stack until we find a parent at a strictly smaller indent.
		for len(stack) > 1 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		parent := stack[len(stack)-1]
		parent.children = append(parent.children, node)
		stack = append(stack, node)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan openapi.yaml: %v", err)
	}
	return root
}

// ---------------------------------------------------------------------------
// Tree helpers
// ---------------------------------------------------------------------------

// findChildNode returns the first child of node whose key equals name, or nil.
func findChildNode(node *yamlNode, name string) *yamlNode {
	for _, c := range node.children {
		if c.key == name {
			return c
		}
	}
	return nil
}

// nodeHasDesc returns true if node has a child "description" with non-empty value.
func nodeHasDesc(node *yamlNode) bool {
	d := findChildNode(node, "description")
	return d != nil && strings.TrimSpace(d.value) != ""
}

// nodeHasSummary returns true if node has a child "summary" with non-empty value.
func nodeHasSummary(node *yamlNode) bool {
	s := findChildNode(node, "summary")
	return s != nil && strings.TrimSpace(s.value) != ""
}

// opMethodSet is the set of HTTP method names that mark an operation node.
var opMethodSet = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true,
	"delete": true, "head": true, "options": true, "trace": true,
}

// ---------------------------------------------------------------------------
// Collectors
// ---------------------------------------------------------------------------

// collectOpDocs returns error strings for operations missing 'summary' or 'description'.
func collectOpDocs(root *yamlNode) []string {
	var missing []string
	paths := findChildNode(root, "paths")
	if paths == nil {
		return nil
	}
	for _, pathNode := range paths.children {
		for _, opNode := range pathNode.children {
			if !opMethodSet[strings.ToLower(opNode.key)] {
				continue
			}
			loc := fmt.Sprintf("%s %s", strings.ToUpper(opNode.key), pathNode.key)
			if !nodeHasSummary(opNode) {
				missing = append(missing, loc+": missing 'summary'")
			}
			if !nodeHasDesc(opNode) {
				missing = append(missing, loc+": missing 'description'")
			}
		}
	}
	return missing
}

// collectParamDocs returns error strings for parameters missing 'description'.
func collectParamDocs(root *yamlNode) []string {
	var missing []string
	paths := findChildNode(root, "paths")
	if paths == nil {
		return nil
	}
	for _, pathNode := range paths.children {
		// Path-level parameters (shared across methods).
		if pathParams := findChildNode(pathNode, "parameters"); pathParams != nil {
			for _, p := range pathParams.children {
				name := "<unnamed>"
				if n := findChildNode(p, "name"); n != nil {
					name = n.value
				}
				if !nodeHasDesc(p) {
					missing = append(missing, fmt.Sprintf("path %s param '%s': missing 'description'", pathNode.key, name))
				}
			}
		}
		// Operation-level parameters.
		for _, opNode := range pathNode.children {
			if !opMethodSet[strings.ToLower(opNode.key)] {
				continue
			}
			opParams := findChildNode(opNode, "parameters")
			if opParams == nil {
				continue
			}
			for _, p := range opParams.children {
				name := "<unnamed>"
				if n := findChildNode(p, "name"); n != nil {
					name = n.value
				}
				if !nodeHasDesc(p) {
					missing = append(missing, fmt.Sprintf("%s %s param '%s': missing 'description'", strings.ToUpper(opNode.key), pathNode.key, name))
				}
			}
		}
	}
	return missing
}

// collectPropDocs returns error strings for schema properties missing 'description'.
func collectPropDocs(root *yamlNode) []string {
	var missing []string
	components := findChildNode(root, "components")
	if components == nil {
		return nil
	}
	schemas := findChildNode(components, "schemas")
	if schemas == nil {
		return nil
	}
	for _, schema := range schemas.children {
		walkSchemaProps(schema, schema.key, &missing)
	}
	return missing
}

// walkSchemaProps recurses through schema.properties to find missing descriptions.
func walkSchemaProps(schema *yamlNode, prefix string, missing *[]string) {
	props := findChildNode(schema, "properties")
	if props == nil {
		return
	}
	for _, prop := range props.children {
		loc := fmt.Sprintf("%s.%s", prefix, prop.key)
		if !nodeHasDesc(prop) {
			*missing = append(*missing, fmt.Sprintf("schema property %s: missing 'description'", loc))
		}
		// Recurse into nested inline properties.
		walkSchemaProps(prop, loc, missing)
		// Recurse into anyOf / oneOf / allOf sub-schemas that define properties.
		for _, kw := range []string{"anyOf", "oneOf", "allOf"} {
			kwNode := findChildNode(prop, kw)
			if kwNode == nil {
				continue
			}
			for i, sub := range kwNode.children {
				subLoc := fmt.Sprintf("%s[%s[%d]]", loc, kw, i)
				walkSchemaProps(sub, subLoc, missing)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Tests (Steps 2–5 of feature #67)
// ---------------------------------------------------------------------------

// TestOpenAPIDocs_OperationsSummary verifies every operation has a non-empty summary.
// Step 2 of feature #67.
func TestOpenAPIDocs_OperationsSummary(t *testing.T) {
	root := parseOpenAPISimple(t)
	missing := collectOpDocs(root)
	var summaryMissing []string
	for _, m := range missing {
		if strings.Contains(m, "missing 'summary'") {
			summaryMissing = append(summaryMissing, m)
		}
	}
	if len(summaryMissing) > 0 {
		t.Errorf("operations missing 'summary' (%d):\n  %s", len(summaryMissing), strings.Join(summaryMissing, "\n  "))
	}
}

// TestOpenAPIDocs_OperationsDescription verifies every operation has a non-empty description.
// Step 2 of feature #67.
func TestOpenAPIDocs_OperationsDescription(t *testing.T) {
	root := parseOpenAPISimple(t)
	missing := collectOpDocs(root)
	var descMissing []string
	for _, m := range missing {
		if strings.Contains(m, "missing 'description'") {
			descMissing = append(descMissing, m)
		}
	}
	if len(descMissing) > 0 {
		t.Errorf("operations missing 'description' (%d):\n  %s", len(descMissing), strings.Join(descMissing, "\n  "))
	}
}

// TestOpenAPIDocs_ParametersDescription verifies every parameter has a non-empty description.
// Step 3 of feature #67.
func TestOpenAPIDocs_ParametersDescription(t *testing.T) {
	root := parseOpenAPISimple(t)
	missing := collectParamDocs(root)
	if len(missing) > 0 {
		t.Errorf("parameters missing 'description' (%d):\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

// TestOpenAPIDocs_SchemaPropertiesDescription verifies every schema property has
// a non-empty description.
// Step 4 of feature #67.
func TestOpenAPIDocs_SchemaPropertiesDescription(t *testing.T) {
	root := parseOpenAPISimple(t)
	missing := collectPropDocs(root)
	if len(missing) > 0 {
		t.Errorf("schema properties missing 'description' (%d):\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

// TestOpenAPIDocs_PrintAndFailOnMissing is the canonical Step 5 test: it prints
// all undocumented items and fails (exit-1 semantics) if any are found.
// Step 5 of feature #67.
func TestOpenAPIDocs_PrintAndFailOnMissing(t *testing.T) {
	root := parseOpenAPISimple(t)

	opMissing := collectOpDocs(root)
	paramMissing := collectParamDocs(root)
	propMissing := collectPropDocs(root)

	var all []string
	all = append(all, opMissing...)
	all = append(all, paramMissing...)
	all = append(all, propMissing...)

	if len(all) > 0 {
		fmt.Printf("\n=== OpenAPI Documentation Gaps (%d items) ===\n", len(all))
		for _, item := range all {
			fmt.Printf("  MISSING: %s\n", item)
		}
		fmt.Printf("=== End of OpenAPI Documentation Gaps ===\n\n")
		t.Errorf("openapi.yaml has %d undocumented item(s) — see output above", len(all))
	}
}

// TestOpenAPIDocs_FullVerification combines all steps as sub-tests.
func TestOpenAPIDocs_FullVerification(t *testing.T) {
	t.Run("Step2_OperationsSummary", TestOpenAPIDocs_OperationsSummary)
	t.Run("Step2_OperationsDescription", TestOpenAPIDocs_OperationsDescription)
	t.Run("Step3_ParametersDescription", TestOpenAPIDocs_ParametersDescription)
	t.Run("Step4_SchemaPropertiesDescription", TestOpenAPIDocs_SchemaPropertiesDescription)
	t.Run("Step5_PrintAndFailOnMissing", TestOpenAPIDocs_PrintAndFailOnMissing)
}
