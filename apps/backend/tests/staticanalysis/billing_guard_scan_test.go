// billing_guard_scan_test.go holds the pure scanners behind the billing
// guards (billing_append_only_test.go). They take a base directory laid out
// like apps/backend, so the very same code runs on the real tree and on the
// fixtures under testdata/billing_guard, which is how the guards prove they
// can catch a violation.
package staticanalysis

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Rules are named after the spec points they enforce.
const (
	ruleAppendOnly   = "append-only (spec 42 §3 п.9, 41 §6.10)"
	ruleInsertOrigin = "insert-only-from-billing-package (spec 42 §3 п.9)"
	ruleMoneyInt     = "money-is-bigint (spec 41 §6.10)"
)

type billingViolation struct {
	File string // slash path relative to the scanned base
	Line int
	Rule string
	Text string
}

func (v billingViolation) String() string {
	return fmt.Sprintf("%s:%d: [%s] %s", v.File, v.Line, v.Rule, v.Text)
}

// billingAppendOnlyTables never change after the INSERT: a correction is a
// compensating row.
const billingAppendOnlyTables = `billable_events|charges|charge_reversals|credit_ledger|campaign_grants`

// billingWriteTables may be inserted into only by the billing package.
const billingWriteTables = `billable_events|charges|charge_reversals|charge_allocations|credit_lots|credit_ledger|campaign_grants`

var (
	billingMutation = regexp.MustCompile(`(?i)\b(UPDATE|DELETE\s+FROM|TRUNCATE(?:\s+TABLE)?)\s+(?:ONLY\s+)?(?:public\.)?(` + billingAppendOnlyTables + `)\b`)
	billingInsert   = regexp.MustCompile(`(?i)\bINSERT\s+INTO\s+(?:public\.)?(` + billingWriteTables + `)\b`)
	// billingInsertCall is a call of a gen wrapper that writes a billing table,
	// so a caller cannot dodge the SQL rule by calling the wrapper.
	billingInsertCall = regexp.MustCompile(`\.(?:Insert|Append)(?:BillableEvent|Charge|ChargeReversal|ChargeAllocation|CreditLot|CreditLedger|CampaignGrant)\w*\(`)
)

const goDownMarker = "-- +goose Down"

// billingSource is one scannable file with comments blanked (newlines kept,
// so line numbers stay true).
type billingSource struct {
	rel     string // slash path relative to base, ".txt" fixture suffix removed
	ext     string // ".go" or ".sql"
	text    string
	isTest  bool
	migrate bool // a migration: text was cut at "-- +goose Down"
}

// walkBillingSources lists every .go and .sql file under base/{cmd,internal,tests}
// except tests/staticanalysis/testdata. A fixture file may carry a trailing
// ".txt" (x.go.txt) so go vet, gofmt and the linters ignore it.
func walkBillingSources(base string) ([]billingSource, error) {
	var out []billingSource
	for _, dir := range []string{"cmd", "internal", "tests"} {
		root := filepath.Join(base, dir)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(base, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if d.Name() == ".gomodcache" || d.Name() == "node_modules" || rel == "tests/staticanalysis/testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			name := strings.TrimSuffix(rel, ".txt")
			ext := filepath.Ext(name)
			if ext != ".go" && ext != ".sql" {
				return nil
			}
			raw, rerr := os.ReadFile(path) // #nosec G304 -- repo-local test scan
			if rerr != nil {
				return rerr
			}
			src := billingSource{rel: name, ext: ext, isTest: strings.HasSuffix(name, "_test.go")}
			text := string(raw)
			if ext == ".sql" {
				if strings.HasPrefix(name, "internal/migrations/sql/") {
					src.migrate = true
					if i := strings.Index(text, goDownMarker); i >= 0 {
						text = text[:i]
					}
				}
				text = blankSQLComments(text)
			} else {
				text = blankGoComments(text)
			}
			src.text = text
			out = append(out, src)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// blankGoComments replaces every comment with spaces, keeping newlines.
func blankGoComments(src string) string {
	b := []byte(src)
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(b))
	var s scanner.Scanner
	s.Init(file, b, nil, scanner.ScanComments)
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok != token.COMMENT {
			continue
		}
		start := file.Offset(pos)
		end := len(b)
		if strings.HasPrefix(src[start:], "/*") {
			if i := strings.Index(src[start+2:], "*/"); i >= 0 {
				end = start + 2 + i + 2
			}
		} else if i := strings.IndexByte(src[start:], '\n'); i >= 0 {
			end = start + i
		}
		for k := start; k < end; k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
	}
	return string(b)
}

var sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// blankSQLComments blanks "--" line comments and block comments, keeping newlines.
func blankSQLComments(src string) string {
	blank := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == '\n' {
				return r
			}
			return ' '
		}, s)
	}
	src = sqlBlockComment.ReplaceAllStringFunc(src, blank)
	lines := strings.Split(src, "\n")
	for i, l := range lines {
		if j := strings.Index(l, "--"); j >= 0 {
			lines[i] = l[:j] + blank(l[j:])
		}
	}
	return strings.Join(lines, "\n")
}

func lineOf(text string, offset int) int {
	return strings.Count(text[:offset], "\n") + 1
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func isBillingPackageFile(rel string) bool {
	return strings.HasPrefix(rel, "internal/platform/billing/")
}

func isBillingGenFile(rel string) bool {
	return strings.HasPrefix(rel, "internal/adapters/postgres/gen/billing_") && strings.HasSuffix(rel, ".sql.go")
}

func isBillingQueryFile(rel string) bool {
	return strings.HasPrefix(rel, "internal/adapters/postgres/queries/billing_") && strings.HasSuffix(rel, ".sql")
}

// appendOnlyViolations finds UPDATE / DELETE FROM / TRUNCATE aimed at an
// append-only billing table. Allowed nowhere except: _test.go files (fixture
// clean-up), testdata, and the Down section of a migration. Not even the
// billing package may do it: a correction is a compensating row.
func appendOnlyViolations(srcs []billingSource) []billingViolation {
	var out []billingViolation
	for _, s := range srcs {
		if s.isTest {
			continue
		}
		for _, m := range billingMutation.FindAllStringIndex(s.text, -1) {
			out = append(out, billingViolation{
				File: s.rel, Line: lineOf(s.text, m[0]), Rule: ruleAppendOnly,
				Text: oneLine(s.text[m[0]:m[1]]) + " - billing tables are INSERT-only, correct with a compensating row",
			})
		}
	}
	return out
}

// insertOriginViolations finds an INSERT into a billing table outside the
// billing package (its migrations, query files and generated wrappers are the
// package's own SQL), and a call of a billing gen wrapper from anywhere
// outside the package.
func insertOriginViolations(srcs []billingSource) []billingViolation {
	var out []billingViolation
	for _, s := range srcs {
		if s.isTest || s.migrate {
			continue
		}
		sqlHome := isBillingPackageFile(s.rel) || isBillingGenFile(s.rel) || isBillingQueryFile(s.rel)
		if !sqlHome {
			for _, m := range billingInsert.FindAllStringIndex(s.text, -1) {
				out = append(out, billingViolation{
					File: s.rel, Line: lineOf(s.text, m[0]), Rule: ruleInsertOrigin,
					Text: oneLine(s.text[m[0]:m[1]]) + " - write billing tables only through internal/platform/billing",
				})
			}
		}
		if s.ext == ".go" && !isBillingPackageFile(s.rel) && !isBillingGenFile(s.rel) {
			for _, m := range billingInsertCall.FindAllStringIndex(s.text, -1) {
				out = append(out, billingViolation{
					File: s.rel, Line: lineOf(s.text, m[0]), Rule: ruleInsertOrigin,
					Text: "call " + oneLine(s.text[m[0]:m[1]]) + " outside internal/platform/billing",
				})
			}
		}
	}
	return out
}

// moneyFieldSuffixes name a money-carrying struct field. Basis points
// (…Bp / …BP) end in neither of these, they are plain integers too.
var moneyFieldSuffixes = []string{"Minor", "Amount", "Total", "Price", "Face", "Remaining", "Rate"}

func isMoneyFieldName(name string) bool {
	if strings.HasSuffix(name, "Bp") || strings.HasSuffix(name, "BP") {
		return false
	}
	for _, suf := range moneyFieldSuffixes {
		if strings.HasSuffix(name, suf) {
			return true
		}
	}
	return false
}

func typeName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + typeName(t.X)
	case *ast.ArrayType:
		return "[]" + typeName(t.Elt)
	case *ast.SelectorExpr:
		return typeName(t.X) + "." + t.Sel.Name
	}
	return ""
}

func isFloatType(e ast.Expr) bool {
	n := strings.TrimLeft(typeName(e), "*[]")
	return n == "float32" || n == "float64"
}

// moneyViolations parses gen/billing_*.sql.go and platform/billing/**/*.go
// (not tests): no float type in a struct field, a parameter or a result, a
// money-named field is int64 or *int64, and a struct with one has a string
// Currency next to it.
func moneyViolations(srcs []billingSource) []billingViolation {
	var out []billingViolation
	for _, s := range srcs {
		if s.ext != ".go" || s.isTest || (!isBillingGenFile(s.rel) && !isBillingPackageFile(s.rel)) {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, s.rel, s.text, parser.SkipObjectResolution)
		if err != nil {
			out = append(out, billingViolation{File: s.rel, Line: 1, Rule: ruleMoneyInt, Text: "cannot parse: " + err.Error()})
			continue
		}
		line := func(p token.Pos) int { return fset.Position(p).Line }
		floatFields := func(fl *ast.FieldList, what, owner string) {
			if fl == nil {
				return
			}
			for _, f := range fl.List {
				if !isFloatType(f.Type) {
					continue
				}
				names := []string{"(unnamed)"}
				if len(f.Names) > 0 {
					names = names[:0]
					for _, n := range f.Names {
						names = append(names, n.Name)
					}
				}
				for _, n := range names {
					out = append(out, billingViolation{File: s.rel, Line: line(f.Pos()), Rule: ruleMoneyInt,
						Text: fmt.Sprintf("%s %s %s has a float type %s: money is int64 minor units", what, owner, n, typeName(f.Type))})
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				floatFields(x.Type.Params, "parameter of", x.Name.Name)
				floatFields(x.Type.Results, "result of", x.Name.Name)
			case *ast.FuncLit:
				floatFields(x.Type.Params, "parameter of", "func literal")
				floatFields(x.Type.Results, "result of", "func literal")
			case *ast.TypeSpec:
				st, ok := x.Type.(*ast.StructType)
				if !ok {
					return true
				}
				floatFields(st.Fields, "field of struct", x.Name.Name)
				hasMoney, hasCurrency := false, false
				for _, f := range st.Fields.List {
					for _, nm := range f.Names {
						if nm.Name == "Currency" {
							hasCurrency = typeName(f.Type) == "string" || typeName(f.Type) == "*string"
						}
						if !isMoneyFieldName(nm.Name) {
							continue
						}
						hasMoney = true
						if t := typeName(f.Type); t != "int64" && t != "*int64" && !isFloatType(f.Type) {
							out = append(out, billingViolation{File: s.rel, Line: line(f.Pos()), Rule: ruleMoneyInt,
								Text: fmt.Sprintf("struct %s field %s is %s, a money field must be int64 or *int64", x.Name.Name, nm.Name, t)})
						}
					}
				}
				if hasMoney && !hasCurrency {
					out = append(out, billingViolation{File: s.rel, Line: line(x.Pos()), Rule: ruleMoneyInt,
						Text: fmt.Sprintf("struct %s has money fields but no string Currency field next to them", x.Name.Name)})
				}
			}
			return true
		})
	}
	return out
}
