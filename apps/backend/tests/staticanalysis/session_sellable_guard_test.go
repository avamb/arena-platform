// session_sellable_guard_test.go keeps spec 63 §14.2 true: a session a
// partner box office sells (sessions.sales_mode = 'external', migration
// 0221) never takes a hold. The predicate is hcheckout.SessionSellable, and
// every hold path reaches it through the category gate:
//
//	CheckSessionSalesOpen -> SalesModeRefusal -> SessionSellable
//
// The guard has two halves:
//
//   - the gate chain itself, read from hcheckout/category_gate.go with
//     go/ast: every gate function still calls the next link;
//   - every production file that creates or grows a hold (calls
//     InsertReservation, InsertReservationSeat(s) or InsertReservationGAItem)
//     is on holdWriters and, unless exempted with a reason, calls a gate
//     function.
//
// A new hold writer is not forbidden, it just has to join holdWriters on
// purpose, which is the moment somebody reads this comment.
package staticanalysis

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// holdWriteCall matches a call of a query that creates or grows a hold.
var holdWriteCall = regexp.MustCompile(`\.(InsertReservation|InsertReservationSeat|InsertReservationSeats|InsertReservationGAItem)\(`)

// holdGateCall matches a call of a gate function that reaches SessionSellable.
var holdGateCall = regexp.MustCompile(`\b(CheckSessionSalesOpen|CheckCategorySellable|CheckCategoriesSellable|CheckGALinesSellable|CheckSeatCategoriesSellable|CheckSessionSellable|SessionSellable)\(`)

// holdWriters are the production files that create or grow a hold. An empty
// reason means the file must call a gate function itself; a reason exempts
// it and says why.
var holdWriters = map[string]string{
	// Hold entry points, all gated:
	// CreateSeatedHold / CreateGAHold (REST, widget and the Bil24 cart).
	filepath.Join("internal", "platform", "httpserver", "hcheckout", "hold_api.go"): "",
	// ExtendHold / ExtendHoldTx: growing a cart is a new hold of its places.
	filepath.Join("internal", "platform", "httpserver", "hcheckout", "hold_mutation.go"): "",
	// CreateSeatedHoldPartialTx / ExtendHoldPartialTx: the public hold at the
	// click (spec 67), partial mode.
	filepath.Join("internal", "platform", "httpserver", "hcheckout", "hold_partial.go"): "",
	// POST /v1/reservations (GA).
	filepath.Join("internal", "platform", "httpserver", "hcheckout", "reservations.go"): "",
	// POST /v1/sessions/{id}/seat-reservations.
	filepath.Join("internal", "platform", "httpserver", "hcheckout", "seat_reservations.go"): "",
	// Public widget checkout/start (seated, mixed and GA branches).
	filepath.Join("internal", "platform", "httpserver", "hfeed", "public_feed_checkout.go"): "",
	// Public checkout recover: a new hold for an expired cart.
	filepath.Join("internal", "platform", "httpserver", "hfeed", "public_checkout_recover.go"): "",
	// Staff and bot invitations (single and batch, via issueTarget): an
	// invitation is a ticket too, so an external session refuses it.
	filepath.Join("internal", "platform", "httpserver", "hinvitations", "issue.go"): "",

	// Exempt, with the reason:
	filepath.Join("internal", "platform", "httpserver", "hcheckout", "ga_units.go"):     "AllocateGAUnitsTx is a helper run inside a gated hold transaction",
	filepath.Join("internal", "platform", "httpserver", "hcheckout", "price_lock.go"):   "WriteReservationPriceLinesTx is a helper run inside a gated hold transaction",
	filepath.Join("internal", "platform", "httpserver", "hcheckout", "late_payment.go"): "re-acquires the places of an order already paid (decision 1 of plan 23: an order already placed is not gated)",
}

// gateChain is the chain every gate function must keep: function -> a
// function it must call.
var gateChain = map[string]string{
	"CheckSessionSalesOpen":       "SalesModeRefusal",
	"SalesModeRefusal":            "SessionSellable",
	"CheckSessionSellable":        "SalesModeRefusal",
	"CheckCategorySellable":       "CheckSessionSalesOpen",
	"CheckCategoriesSellable":     "CheckSessionSalesOpen",
	"CheckGALinesSellable":        "CheckSessionSalesOpen",
	"CheckSeatCategoriesSellable": "CheckCategoriesSellable",
}

func TestSessionSellableGuard_GateChain(t *testing.T) {
	path := filepath.Join(backendDir(t), "internal", "platform", "httpserver", "hcheckout", "category_gate.go")
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	calls := map[string]map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue
		}
		called := map[string]bool{}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				switch fun := c.Fun.(type) {
				case *ast.Ident:
					called[fun.Name] = true
				case *ast.SelectorExpr:
					called[fun.Sel.Name] = true
				}
			}
			return true
		})
		calls[fn.Name.Name] = called
	}
	if _, ok := calls["SessionSellable"]; !ok {
		t.Fatalf("hcheckout.SessionSellable is gone from category_gate.go")
	}
	for fn, next := range gateChain {
		called, ok := calls[fn]
		if !ok {
			t.Errorf("hcheckout.%s is gone from category_gate.go", fn)
			continue
		}
		if !called[next] {
			t.Errorf("hcheckout.%s no longer calls %s: an external session could take a hold", fn, next)
		}
	}
}

func TestSessionSellableGuard_EveryHoldWriterIsGated(t *testing.T) {
	backend := backendDir(t)
	var violations []string
	seen := map[string]bool{}
	for _, root := range []string{"internal", "cmd"} {
		_ = filepath.WalkDir(filepath.Join(backend, root), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(backend, path)
			// The wrappers themselves live in gen/; their call sites are guarded.
			if strings.Contains(rel, filepath.Join("adapters", "postgres", "gen")+string(filepath.Separator)) {
				return nil
			}
			src, rerr := os.ReadFile(path) // #nosec G304 — repo-local test scan
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			code := stripBlockAndLineComments(string(src))
			if !holdWriteCall.MatchString(code) {
				return nil
			}
			seen[rel] = true
			reason, listed := holdWriters[rel]
			switch {
			case !listed:
				violations = append(violations, rel+" creates or grows a hold but is not on holdWriters: gate it with hcheckout.CheckSessionSalesOpen (or a Check*Sellable) and list it")
			case reason == "" && !holdGateCall.MatchString(code):
				violations = append(violations, rel+" creates or grows a hold but calls no gate function that reaches hcheckout.SessionSellable")
			}
			return nil
		})
	}
	for rel := range holdWriters {
		if !seen[rel] {
			violations = append(violations, rel+" is on holdWriters but no longer creates or grows a hold: remove it")
		}
	}
	if len(violations) > 0 {
		t.Errorf("every hold path must reach hcheckout.SessionSellable (spec 63 §14.2):\n  %s",
			strings.Join(violations, "\n  "))
	}
}
