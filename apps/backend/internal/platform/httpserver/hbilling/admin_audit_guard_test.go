package hbilling

// admin_audit_guard_test.go — every writing handler of the superadmin screen
// "Price lists and rates" must write its audit row on the change's own
// transaction BEFORE the commit. The test reads the source: a Handle{Create,
// Revoke,Close,Delete}* method of AdminHandler that does not call auditTx, or
// calls it after tx.Commit, fails the build of the PR that added it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestAdminWriteHandlers_AuditBeforeCommit(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		n := e.Name()
		if !strings.HasPrefix(n, "admin") || strings.HasSuffix(n, "_test.go") || !strings.HasSuffix(n, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	checked := 0
	for _, file := range files {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Body == nil || !isWriteHandlerName(fd.Name.Name) {
				continue
			}
			checked++
			var auditPos, commitPos token.Pos
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "auditTx":
					if auditPos == token.NoPos {
						auditPos = call.Pos()
					}
				case "Commit":
					commitPos = call.Pos()
				}
				return true
			})
			switch {
			case auditPos == token.NoPos:
				t.Errorf("%s writes without calling auditTx", fd.Name.Name)
			case commitPos == token.NoPos:
				t.Errorf("%s never commits a transaction: the audit row and the change must share one", fd.Name.Name)
			case auditPos > commitPos:
				t.Errorf("%s writes its audit row after the commit", fd.Name.Name)
			}
		}
	}
	if checked < 6 {
		t.Errorf("only %d write handlers found, want at least 6: the guard lost its target", checked)
	}
}

func isWriteHandlerName(name string) bool {
	for _, p := range []string{"HandleCreate", "HandleRevoke", "HandleClose", "HandleDelete"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
