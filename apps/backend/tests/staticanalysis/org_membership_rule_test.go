// org_membership_rule_test.go keeps ONE rule for "is this user a member of
// the organization" across every org-membership guard of httpserver: the
// guards decide with orgperm.HasMembershipRow, which also says that a row
// carrying only a platform role is not a membership (the permission checker
// ignores such rows too).
//
// Two halves:
//
//   - every function named actorIsMemberOfOrg* calls orgperm.HasMembershipRow
//     and never loops over the membership rows itself;
//   - no other production function reads ListMembershipsByUser and compares
//     a row's OrgID by hand, unless its file is on the allowlist below (code
//     that reads a role for display after the guard has decided).
package staticanalysis

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// membershipRowReaders may compare OrgID by hand: they read the role a
// member already holds, they do not decide membership.
var membershipRowReaders = map[string]bool{
	filepath.Join("orgperm", "orgperm.go"):        true, // the rule itself and Check
	filepath.Join("hiam", "org_me.go"):            true, // lists membership_roles after the guard
	filepath.Join("hbot", "invitations.go"):       true, // reports the role an invitation found
	filepath.Join("hbot", "invitation_manage.go"): true, // same, on resend
}

// minGuards is the number of guards known today (hiam, hcatalog, hapikeys,
// hbankaccounts, himports, hpayments, hseating, the server's own). Fewer
// means a guard was renamed and this test no longer sees it.
const minGuards = 8

func TestOrgMembershipRule_EveryGuardUsesTheSharedRule(t *testing.T) {
	root := filepath.Join(repoRoot(t), "apps", "backend", "internal", "platform", "httpserver")
	fset := token.NewFileSet()
	guards := 0
	var violations []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			body := string(src[fset.Position(fn.Body.Pos()).Offset:fset.Position(fn.Body.End()).Offset])
			readsRows := strings.Contains(body, "ListMembershipsByUser(")
			comparesByHand := strings.Contains(body, ".OrgID == ") && strings.Contains(body, "range ")
			if strings.HasPrefix(fn.Name.Name, "actorIsMemberOfOrg") {
				guards++
				if !strings.Contains(body, "orgperm.HasMembershipRow(") {
					violations = append(violations, rel+": "+fn.Name.Name+" does not decide with orgperm.HasMembershipRow")
				}
				if comparesByHand {
					violations = append(violations, rel+": "+fn.Name.Name+" compares membership rows by hand")
				}
				continue
			}
			if readsRows && comparesByHand && !membershipRowReaders[rel] {
				violations = append(violations, rel+": "+fn.Name.Name+" decides membership by hand, use orgperm.HasMembershipRow")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, v := range violations {
		t.Error(v)
	}
	if guards < minGuards {
		t.Errorf("found %d actorIsMemberOfOrg* guards, want at least %d: a guard was renamed or removed", guards, minGuards)
	}
}
